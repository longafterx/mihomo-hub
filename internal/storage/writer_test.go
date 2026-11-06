package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/telemetry"
)

func TestWriterQueueBoundsAndShutdownFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var applied int
		w := storage.NewWriter(1, 2, time.Second, func(_ context.Context, b []telemetry.Batch) error { applied += len(b); return nil })
		ctx, cancel := context.WithCancel(t.Context())
		var producers sync.WaitGroup
		producers.Go(func() {
			if err := w.Submit(ctx, telemetry.Batch{}); !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		})
		synctest.Wait()
		if err := w.Submit(t.Context(), telemetry.Batch{}); !errors.Is(err, storage.ErrQueueFull) {
			t.Fatal("queue did not reject overflow")
		}
		cancel()
		producers.Wait()
		worker, cancelWorker := context.WithCancel(t.Context())
		var workers sync.WaitGroup
		workers.Go(func() { w.Run(worker) })
		synctest.Wait()
		cancelWorker()
		workers.Wait()
		_, dropped, failures := w.Stats()
		if applied != 1 || dropped != 1 || failures != 0 {
			t.Fatalf("apply=%d dropped=%d failures=%d", applied, dropped, failures)
		}
	})
}
