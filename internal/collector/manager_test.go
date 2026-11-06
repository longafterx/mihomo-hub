//go:build integration

package collector_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/collector"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/telemetry"
	"github.com/longafterx/mihomo-hub/internal/testutil/fakemihomo"
)

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("collector condition timed out")
}

func TestBoundedWriterPressureCreatesRecoveryGap(t *testing.T) {
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "hub.db"), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key := filepath.Join(t.TempDir(), "key")
	if err := storage.InitKey(key); err != nil {
		t.Fatal(err)
	}
	aead, err := storage.LoadKey(key)
	if err != nil {
		t.Fatal(err)
	}
	root, cancel := context.WithCancel(t.Context())
	defer cancel()
	registry, err := instance.New(root, &storage.Instances{DB: db, AEAD: aead}, mihomo.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	items := []instance.Instance{}
	for _, name := range []string{
		"a",
		"b",
		"c",
	} {
		fake := fakemihomo.New(name, "fixture-secret")
		defer fake.Close()
		enabled := true
		item, err := registry.Create(t.Context(), instance.Create{
			Name:           name,
			ControllerURL:  fake.Server.URL,
			Secret:         fake.Secret,
			CollectEnabled: &enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	store := storage.Telemetry{DB: db}
	var blocked atomic.Bool
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	writer := storage.NewWriter(1, 1, time.Millisecond, func(ctx context.Context, b []telemetry.Batch) error {
		if blocked.Load() {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return store.Apply(ctx, b)
	})
	writerCtx, stopWriter := context.WithCancel(context.WithoutCancel(root))
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); writer.Run(writerCtx) }()
	manager := collector.New(collector.Dependencies{
		Registry: registry,
		Store:    store,
		Writer:   writer,
		Policy:   mihomo.Policy{},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options: collector.Options{
			MaxInstances:    3,
			MaxConnections:  100,
			ReadLimit:       2048,
			SnapshotTimeout: time.Second,
			MaxInterval:     time.Second,
			ReconnectMin:    10 * time.Millisecond,
			ReconnectMax:    20 * time.Millisecond,
		},
	})
	managerDone := make(chan struct{})
	go func() { defer close(managerDone); manager.Run(root) }()
	defer func() { releaseOnce.Do(func() { close(release) }); cancel(); <-managerDone; stopWriter(); <-writerDone }()
	await(t, func() bool {
		for _, i := range items {
			if manager.Status(i.ID, i.Generation).SamplesCommitted < 2 {
				return false
			}
		}
		return true
	})
	_, droppedBefore, _ := writer.Stats()
	blocked.Store(true)
	await(t, func() bool { _, d, _ := writer.Stats(); return d > droppedBefore })
	blocked.Store(false)
	releaseOnce.Do(func() { close(release) })
	await(t, func() bool {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM traffic_gap_intervals WHERE reason='writer_backpressure'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	})
	var total int64
	if err := db.QueryRow("SELECT SUM(total_upload) FROM traffic_minute").Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatal("pressure created a spike", total)
	}
}
