package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/longafterx/mihomo-hub/internal/telemetry"
)

var ErrQueueFull = errors.New("telemetry queue full")

type ApplyFunc func(context.Context, []telemetry.Batch) error
type job struct {
	batch  telemetry.Batch
	result chan error
}
type Writer struct {
	queue       chan job
	apply       ApplyFunc
	flush       time.Duration
	batchSize   int
	lastWriteNS atomic.Int64
	commits     atomic.Uint64
	dropped     atomic.Uint64
	failures    atomic.Uint64
}

func NewWriter(capacity, batchSize int, flush time.Duration, apply ApplyFunc) *Writer {
	return &Writer{queue: make(chan job, capacity), apply: apply, flush: flush, batchSize: batchSize}
}

func (w *Writer) Submit(ctx context.Context, b telemetry.Batch) error {
	j := job{batch: b, result: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	select {
	case w.queue <- j:
	default:
		w.dropped.Add(1)
		return ErrQueueFull
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-j.result:
		return err
	}
}

func (w *Writer) Stats() (int, uint64, uint64) {
	return len(w.queue), w.dropped.Load(), w.failures.Load()
}

func (w *Writer) Run(ctx context.Context) {
	ticker := time.NewTicker(w.flush)
	defer ticker.Stop()
	pending := make([]job, 0, w.batchSize)
	flush := func(parent context.Context) {
		if len(pending) == 0 {
			return
		}
		batches := make([]telemetry.Batch, 0, len(pending))
		for _, j := range pending {
			batches = append(batches, j.batch)
		}
		writeCtx, cancel := context.WithTimeout(parent, 10*time.Second)
		started := time.Now()
		err := w.apply(writeCtx, batches)
		w.lastWriteNS.Store(time.Since(started).Nanoseconds())
		if err == nil {
			w.commits.Add(1)
		}
		cancel()
		if err != nil {
			w.failures.Add(1)
		}
		for _, j := range pending {
			j.result <- err
		}
		pending = pending[:0]
	}
	for {
		select {
		case j := <-w.queue:
			pending = append(pending, j)
			if len(pending) >= w.batchSize {
				flush(ctx)
			}
		case <-ticker.C:
			flush(ctx)
		case <-ctx.Done():
			// Producers are stopped and joined by App before this cancellation.
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			for len(w.queue) > 0 {
				pending = append(pending, <-w.queue)
				if len(pending) >= w.batchSize {
					flush(shutdownCtx)
				}
			}
			flush(shutdownCtx)
			return
		}
	}
}

type WriterMetrics struct {
	QueueLength    int     `json:"queue_length"`
	QueueCapacity  int     `json:"queue_capacity"`
	DroppedSamples uint64  `json:"dropped_samples"`
	WriteFailures  uint64  `json:"write_failures"`
	Commits        uint64  `json:"commits"`
	LastWriteMS    float64 `json:"last_write_ms"`
}

func (w *Writer) Metrics() WriterMetrics {
	n, d, f := w.Stats()
	return WriterMetrics{
		QueueLength: n, QueueCapacity: cap(w.queue), DroppedSamples: d, WriteFailures: f,
		Commits: w.commits.Load(), LastWriteMS: float64(w.lastWriteNS.Load()) / 1e6,
	}
}
