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
	Save(context.Context, Instance, string) error
	Delete(context.Context, string, string) error
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
	now := time.Now().UTC().UnixMilli()
	item := Instance{
		ID: uuid.NewString(), Name: input.Name, ControllerURL: input.ControllerURL,
		Secret: input.Secret, Enabled: true, CreatedAtMS: now, UpdatedAtMS: now, Generation: 1,
	}
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	if input.CollectEnabled != nil {
		item.CollectEnabled = *input.CollectEnabled
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
	if patch.Enabled != nil {
		item.Enabled = *patch.Enabled
	}
	if patch.CollectEnabled != nil {
		item.CollectEnabled = *patch.CollectEnabled
	}
	// Disabling or renaming an offline controller must not require a working DNS lookup.
	if patch.ControllerURL != nil || patch.Secret != nil {
		if err := s.validate(ctx, item); err != nil {
			return Instance{}, err
		}
	}
	if strings.TrimSpace(item.Name) == "" || len(item.Name) > 128 {
		return Instance{}, ErrInvalid
	}
	s.block(id)
	item.UpdatedAtMS = time.Now().UTC().UnixMilli()
	item.Generation++
	if err := s.repo.Save(ctx, item, "instance.update"); err != nil {
		s.publish(previous)
		return Instance{}, err
	}
	s.publish(item)
	return s.Get(id)
}

func (s *Service) Delete(ctx context.Context, id, history string) error {
	if history != "retain" && history != "purge" {
		return ErrInvalid
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	previous, err := s.Get(id)
	if err != nil {
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
