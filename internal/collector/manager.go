package collector

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/telemetry"
	"golang.org/x/sync/errgroup"
)

type Status struct {
	Status            string `json:"status"`
	Generation        int64  `json:"-"`
	LastSeenAtMS      *int64 `json:"last_seen_at_ms"`
	LastConnectedAtMS *int64 `json:"last_connected_at_ms"`
	LastSnapshotAtMS  *int64 `json:"last_snapshot_at_ms"`
	LastErrorCode     string `json:"last_error_code"`
	ReconnectCount    uint64 `json:"reconnect_count"`
	SamplesCommitted  uint64 `json:"samples_committed"`
	DroppedSamples    uint64 `json:"dropped_samples"`
	CollectorLagMS    int64  `json:"collector_lag_ms"`
}

type Options struct {
	MaxInstances, MaxConnections                             int
	ReadLimit                                                int64
	SnapshotTimeout, MaxInterval, ReconnectMin, ReconnectMax time.Duration
}

type Manager struct {
	registry *instance.Service
	store    storage.Telemetry
	writer   *storage.Writer
	policy   mihomo.Policy
	options  Options
	logger   *slog.Logger
	mu       sync.RWMutex
	statuses map[string]Status
}

type Dependencies struct {
	Registry *instance.Service
	Store    storage.Telemetry
	Writer   *storage.Writer
	Policy   mihomo.Policy
	Options  Options
	Logger   *slog.Logger
}

func New(d Dependencies) *Manager {
	return &Manager{registry: d.Registry, store: d.Store, writer: d.Writer,
		policy: d.Policy, options: d.Options, logger: d.Logger, statuses: map[string]Status{}}
}

func (m *Manager) Status(id string, generation int64) Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.statuses[id]
	if !ok || s.Generation != generation {
		return Status{Status: "connecting", Generation: generation}
	}
	if s.LastSnapshotAtMS != nil {
		s.CollectorLagMS = max(s.CollectorLagMS, time.Now().UnixMilli()-*s.LastSnapshotAtMS)
	}
	return s
}

func (m *Manager) publish(id string, s Status) {
	m.mu.Lock()
	old := m.statuses[id]
	m.statuses[id] = s
	m.mu.Unlock()
	if old.Status != s.Status || old.LastErrorCode != s.LastErrorCode {
		m.logger.Info("collector state changed", "instance_id", id, "status", s.Status, "error_code", s.LastErrorCode)
	}
}

func (m *Manager) Run(ctx context.Context) {
	type running struct {
		generation int64
		cancel     context.CancelFunc
		done       chan struct{}
		ctx        context.Context
	}
	sessions := map[string]running{}
	group, root := errgroup.WithContext(ctx)
	reconcile := func() {
		items := m.registry.List()
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		wanted := map[string]instance.Instance{}
		for _, item := range items {
			if item.Enabled && item.CollectEnabled {
				wanted[item.ID] = item
			}
		}
		for id, session := range sessions {
			item, ok := wanted[id]
			finished := false
			select {
			case <-session.done:
				finished = true
			default:
			}
			if !ok || item.Generation != session.generation || finished || session.ctx.Err() != nil {
				session.cancel()
				<-session.done
				delete(sessions, id)
			}
		}
		for _, item := range items {
			id := item.ID
			if _, ok := wanted[id]; !ok {
				continue
			}
			if _, exists := sessions[id]; exists {
				continue
			}
			if len(sessions) >= m.options.MaxInstances {
				m.publish(id, Status{Generation: item.Generation, Status: "capacity_limited"})
				continue
			}
			current, generation, err := m.registry.Acquire(id)
			if err != nil || current.Generation != item.Generation {
				continue
			}
			workerCtx, cancel := context.WithCancel(root)
			stop := context.AfterFunc(generation, cancel)
			done := make(chan struct{})
			sessions[id] = running{generation: item.Generation, cancel: cancel, done: done, ctx: generation}
			group.Go(func() error {
				defer close(done)
				defer stop()
				defer cancel()
				m.runInstance(workerCtx, item)
				return nil
			})
		}
		m.mu.Lock()
		for id := range m.statuses {
			if _, exists := wanted[id]; !exists {
				delete(m.statuses, id)
			}
		}
		m.mu.Unlock()
	}
	reconcile()
	for {
		select {
		case <-root.Done():
			for _, session := range sessions {
				session.cancel()
			}
			if err := group.Wait(); err != nil {
				m.logger.Error("collector shutdown failed")
			}
			return
		case <-m.registry.Events():
			reconcile()
		}
	}
}

func (m *Manager) runInstance(ctx context.Context, item instance.Instance) {
	st := Status{Status: "connecting", Generation: item.Generation}
	backoff := m.options.ReconnectMin
	boundary := "process_or_collector_restart"
	for ctx.Err() == nil {
		st.Status = "connecting"
		m.publish(item.ID, st)
		cursor, err := m.store.LoadCursor(ctx, item.ID)
		previousSnapshot := int64(0)
		if st.LastSnapshotAtMS != nil {
			previousSnapshot = *st.LastSnapshotAtMS
		}
		code := "storage_error"
		if err == nil {
			if cursor.At > 0 {
				v := cursor.At
				st.LastSeenAtMS = &v
				st.LastSnapshotAtMS = &v
			}
			engine := telemetry.New(cursor, m.options.MaxInterval)
			code = m.session(ctx, item, engine, boundary, &st)
		}
		if ctx.Err() != nil {
			return
		}
		if st.LastSnapshotAtMS != nil && *st.LastSnapshotAtMS > previousSnapshot {
			backoff = m.options.ReconnectMin
		}
		st.Status = "degraded"
		st.LastErrorCode = code
		st.ReconnectCount++
		m.publish(item.ID, st)
		state := telemetry.Batch{
			InstanceID: item.ID,
			Generation: item.Generation,
			Status:     "degraded",
			ErrorCode:  code,
			At:         time.Now().UnixMilli(),
		}
		if err := m.writer.Submit(ctx, state); err != nil {
			st.DroppedSamples++
			m.publish(item.ID, st)
		}
		boundary = code
		jitter := time.Duration(rand.Int64N(max(int64(backoff/4), 1)))
		timer := time.NewTimer(backoff + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, m.options.ReconnectMax)
	}
}

func (m *Manager) session(ctx context.Context, item instance.Instance, engine *telemetry.Engine, boundary string, st *Status) string {
	target, err := m.policy.Parse(item.ControllerURL)
	if err != nil {
		return "target_rejected"
	}
	target.Path = "/connections"
	q := url.Values{}
	if item.Secret != "" {
		q.Set("token", item.Secret)
	}
	target.RawQuery = q.Encode()
	headers := http.Header{}
	if item.Secret != "" {
		headers.Set("Authorization", "Bearer "+item.Secret)
	}
	transport := m.policy.Transport()
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	conn, _, err := websocket.Dial(dialCtx, target.String(), &websocket.DialOptions{
		HTTPClient: client,
		HTTPHeader: headers,
	})
	cancel()
	if err != nil {
		return "connect_failed"
	}
	defer conn.CloseNow()
	conn.SetReadLimit(m.options.ReadLimit)
	connected := time.Now().UnixMilli()
	st.LastConnectedAtMS = &connected
	for ctx.Err() == nil {
		readCtx, cancel := context.WithTimeout(ctx, m.options.SnapshotTimeout)
		typ, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return "stream_interrupted"
		}
		if typ != websocket.MessageText {
			return "invalid_snapshot_type"
		}
		at := time.Now().UTC().UnixMilli()
		snapshot, err := telemetry.Decode(data, m.options.MaxConnections)
		if err != nil {
			return "invalid_snapshot"
		}
		if at <= engine.Cursor.At {
			st.DroppedSamples++
			m.publish(item.ID, *st)
			continue
		}
		batch, next, err := engine.Plan(snapshot, at, telemetry.SourceID(item.ControllerURL), boundary)
		if err != nil {
			return "invalid_counters"
		}
		batch.Generation = item.Generation
		if err := m.writer.Submit(ctx, batch); err != nil {
			if errors.Is(err, storage.ErrQueueFull) {
				st.DroppedSamples++
				return "writer_backpressure"
			}
			return "storage_error"
		}
		engine = next
		st.SamplesCommitted++
		boundary = ""
		st.Status = "online"
		st.LastErrorCode = ""
		st.LastSeenAtMS = &at
		st.LastSnapshotAtMS = &at
		st.CollectorLagMS = time.Now().UnixMilli() - at
		m.publish(item.ID, *st)
	}
	return "canceled"
}
