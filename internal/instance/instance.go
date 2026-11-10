package instance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
)

var (
	ErrNotFound = errors.New("instance not found")
	ErrDisabled = errors.New("instance disabled")
	ErrConflict = errors.New("instance name already exists")
	ErrInvalid  = errors.New("invalid instance")
)

type Instance struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ControllerURL   string `json:"-"`
	Secret          string `json:"-"`
	HasSecret       bool   `json:"has_secret"`
	GatewayURL      string `json:"gateway_url"`
	Enabled         bool   `json:"enabled"`
	CollectEnabled  bool   `json:"collect_enabled"`
	CollectorStatus string `json:"collector_status"`
	LastSeenAtMS    *int64 `json:"last_seen_at_ms"`
	CreatedAtMS     int64  `json:"created_at_ms"`
	UpdatedAtMS     int64  `json:"updated_at_ms"`
	Generation      int64  `json:"-"`
}

type Create struct {
	Name           string `json:"name"`
	ControllerURL  string `json:"controller_url"`
	Secret         string `json:"secret"`
	Enabled        *bool  `json:"enabled"`
	CollectEnabled *bool  `json:"collect_enabled"`
}

type Patch struct {
	Name           *string `json:"name"`
	ControllerURL  *string `json:"controller_url"`
	Secret         *string `json:"secret"`
	Enabled        *bool   `json:"enabled"`
	CollectEnabled *bool   `json:"collect_enabled"`
}

type Repository interface {
	List(context.Context) ([]Instance, error)
	ListRetained(context.Context) ([]Retained, error)
	Save(context.Context, Instance, string) error
	Delete(context.Context, string, string) error
}

// Retained 是已删除实例的历史目录，不包含连接地址和凭据。
type Retained struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DeletedAtMS int64  `json:"deleted_at_ms"`
}

func (s *Service) ListRetained(ctx context.Context) ([]Retained, error) {
	return s.repo.ListRetained(ctx)
}

type generation struct {
	instance Instance
	ctx      context.Context
	cancel   context.CancelFunc
	blocked  bool
}

type Service struct {
	repo   Repository
	policy mihomo.Policy
	root   context.Context
	// Serialize mutations across persistence and publication. Readers use only mu.
	writes sync.Mutex
	mu     sync.RWMutex
	items  map[string]generation
	events chan struct{}
}

func New(ctx context.Context, repo Repository, policy mihomo.Policy) (*Service, error) {
	items, err := repo.List(ctx)
	if err != nil {
		return nil, err
	}
	s := &Service{repo: repo, policy: policy, root: ctx, items: map[string]generation{}, events: make(chan struct{}, 1)}
	for _, item := range items {
		s.publish(item)
	}
	return s, nil
}

func (s *Service) publish(item Instance) {
	ctx, cancel := context.WithCancel(s.root)
	item.HasSecret = item.Secret != ""
	item.GatewayURL = "/mihomo/" + item.ID
	item.CollectorStatus = "disabled"
	if item.Enabled && item.CollectEnabled {
		item.CollectorStatus = "connecting"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.items[item.ID]; ok {
		old.cancel()
	}
	s.items[item.ID] = generation{instance: item, ctx: ctx, cancel: cancel}
	s.notify()
}

// Events coalesces registry changes; the manager reconciles the full current list.
func (s *Service) Events() <-chan struct{} { return s.events }

func (s *Service) notify() {
	select {
	case s.events <- struct{}{}:
	default:
	}
}

func (s *Service) List() []Instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Instance, 0, len(s.items))
	for _, g := range s.items {
		items = append(items, g.instance)
	}
	return items
}

func (s *Service) Get(id string) (Instance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.items[id]
	if !ok {
		return Instance{}, ErrNotFound
	}
	return g.instance, nil
}

// Acquire ties each request to a generation; mutations cancel established REST/WS too.
func (s *Service) Acquire(id string) (Instance, context.Context, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.items[id]
	if !ok {
		return Instance{}, nil, ErrNotFound
	}
	if !g.instance.Enabled || g.blocked {
		return Instance{}, nil, ErrDisabled
	}
	return g.instance, g.ctx, nil
}

func (s *Service) validate(ctx context.Context, item Instance) error {
	validName := strings.TrimSpace(item.Name) != "" && len(item.Name) <= 128
	if !validName || len(item.Secret) > 4096 || strings.ContainsAny(item.Secret, "\r\n") {
		return ErrInvalid
	}
	if err := s.policy.Validate(ctx, item.ControllerURL); err != nil {
		return errors.Join(ErrInvalid, err)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, input Create) (Instance, error) {
	if rejectsManagement(input.Enabled, input.CollectEnabled) {
		return Instance{}, ErrInvalid
	}
	now := time.Now().UTC().UnixMilli()
	item := Instance{
		ID: uuid.NewString(), Name: input.Name, ControllerURL: input.ControllerURL,
		Secret: input.Secret, Enabled: true, CollectEnabled: true, CreatedAtMS: now, UpdatedAtMS: now, Generation: 1,
	}
	if err := s.validate(ctx, item); err != nil {
		return Instance{}, err
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	if err := s.repo.Save(ctx, item, "instance.create"); err != nil {
		return Instance{}, err
	}
	s.publish(item)
	return s.Get(item.ID)
}

func (s *Service) block(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.items[id]
	g.blocked = true
	g.cancel()
	s.items[id] = g
}

func (s *Service) Update(ctx context.Context, id string, patch Patch) (Instance, error) {
	if rejectsManagement(patch.Enabled, patch.CollectEnabled) {
		return Instance{}, ErrInvalid
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	previous, err := s.Get(id)
	if err != nil {
		return Instance{}, err
	}
	item := previous
	if patch.Name != nil {
		item.Name = *patch.Name
	}
	if patch.ControllerURL != nil {
		item.ControllerURL = *patch.ControllerURL
	}
	if patch.Secret != nil {
		item.Secret = *patch.Secret
	}
	item.Enabled, item.CollectEnabled = true, true
	// 离线实例仅改名时不依赖 DNS；修改连接参数才重新校验目标。
	if patch.ControllerURL != nil || patch.Secret != nil {
		if err := s.validate(ctx, item); err != nil {
			return Instance{}, err
		}
	}
	if strings.TrimSpace(item.Name) == "" || len(item.Name) > 128 {
		return Instance{}, ErrInvalid
	}
	connectionChanged := item.ControllerURL != previous.ControllerURL || item.Secret != previous.Secret
	item.UpdatedAtMS = time.Now().UTC().UnixMilli()
	if connectionChanged {
		item.Generation++
	}
	// 先提交再替换会话：失败保存不能打断原连接；新 generation 在数据库中隔离旧样本。
	if err := s.repo.Save(ctx, item, "instance.update"); err != nil {
		return Instance{}, err
	}
	if connectionChanged {
		s.publish(item)
	} else {
		// 名称等元数据不改变采集会话，也不重建浏览器正在使用的 REST/WS。
		s.mu.Lock()
		current := s.items[id]
		current.instance = item
		s.items[id] = current
		s.mu.Unlock()
		s.notify()
	}
	return s.Get(id)
}

func rejectsManagement(enabled, collect *bool) bool {
	// 接入 Hub 即启用管理和采集；兼容旧请求中的 true，但拒绝通过 API 关闭。
	return (enabled != nil && !*enabled) || (collect != nil && !*collect)
}

func (s *Service) Delete(ctx context.Context, id, history string) error {
	if history != "retain" && history != "purge" {
		return ErrInvalid
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	previous, err := s.Get(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) && history == "purge" {
			// 已删除身份仍可永久清理；是否存在由持久层在同一事务中检查。
			return s.repo.Delete(ctx, id, history)
		}
		return err
	}
	s.block(id)
	if err := s.repo.Delete(ctx, id, history); err != nil {
		s.publish(previous)
		return err
	}
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	s.notify()
	return nil
}
