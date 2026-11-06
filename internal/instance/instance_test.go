package instance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
)

type repository struct {
	items map[string]instance.Instance
	fail  bool
}

func (r *repository) List(context.Context) ([]instance.Instance, error) {
	items := []instance.Instance{}
	for _, item := range r.items {
		items = append(items, item)
	}
	return items, nil
}

func (r *repository) Save(_ context.Context, item instance.Instance, _ string) error {
	if r.fail {
		return errors.New("injected write failure")
	}
	r.items[item.ID] = item
	return nil
}

func (r *repository) Delete(_ context.Context, id, _ string) error {
	delete(r.items, id)
	return nil
}

func TestRegistryLifecycle(t *testing.T) {
	t.Parallel()
	repo := &repository{items: map[string]instance.Instance{}}
	s, err := instance.New(t.Context(), repo, mihomo.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.Create(t.Context(), instance.Create{Name: "first", ControllerURL: "http://127.0.0.1:9090", Secret: "hidden"})
	if err != nil {
		t.Fatal(err)
	}
	_, previous, err := s.Acquire(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := s.Update(t.Context(), item.ID, instance.Patch{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if previous.Err() == nil {
		t.Fatal("old generation still alive")
	}
	if _, _, err := s.Acquire(item.ID); !errors.Is(err, instance.ErrDisabled) {
		t.Fatalf("disabled acquire = %v", err)
	}
	enabled := true
	if _, err := s.Update(t.Context(), item.ID, instance.Patch{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	repo.fail = true
	if _, err := s.Update(t.Context(), item.ID, instance.Patch{Enabled: &disabled}); err == nil {
		t.Fatal("failed database write reported success")
	}
	if _, _, err := s.Acquire(item.ID); err != nil {
		t.Fatal("previous generation not recovered after write failure")
	}
	if err := s.Delete(t.Context(), item.ID, ""); !errors.Is(err, instance.ErrInvalid) {
		t.Fatal("missing retention choice accepted")
	}
	if err := s.Delete(t.Context(), item.ID, "retain"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Acquire(item.ID); !errors.Is(err, instance.ErrNotFound) {
		t.Fatal("deleted instance still routable")
	}
}
