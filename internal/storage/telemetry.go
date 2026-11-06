package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/longafterx/mihomo-hub/internal/storage/generated"
	"github.com/longafterx/mihomo-hub/internal/telemetry"
	"modernc.org/sqlite"
)

type Telemetry struct{ DB *sql.DB }

func (s Telemetry) LoadCursor(ctx context.Context, id string) (telemetry.Cursor, error) {
	r, err := generated.New(s.DB).GetCursor(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return telemetry.Cursor{InstanceID: id}, nil
	}
	if err != nil {
		return telemetry.Cursor{}, fmt.Errorf("load cursor: %w", err)
	}
	return telemetry.Cursor{InstanceID: id, EpochID: r.EpochID, SampleID: r.SampleID, SourceID: r.SourceID,
		Upload: r.UploadTotal, Download: r.DownloadTotal, At: r.AtMs}, nil
}

// Apply atomically commits each sample's cursor, lifecycle summaries and all
// aggregates. The current instance generation rejects stale queued samples.
func (s Telemetry) Apply(ctx context.Context, batches []telemetry.Batch) error {
	for attempt := range 3 {
		err := s.apply(ctx, batches)
		if err == nil {
			return nil
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&255 != 5 {
			return err
		}
		timer := time.NewTimer(time.Duration(10<<attempt) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return errors.New("sqlite busy retry limit reached")
}

func (s Telemetry) apply(ctx context.Context, batches []telemetry.Batch) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin telemetry: %w", err)
	}
	defer tx.Rollback()
	q := generated.New(tx)
	type hourKey struct {
		id string
		at int64
	}
	hours := map[hourKey]bool{}
	for _, b := range batches {
		generation, err := q.GetGeneration(ctx, b.InstanceID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if generation != b.Generation {
			continue
		}
		if b.Cursor != nil {
			old, err := q.GetCursor(ctx, b.InstanceID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && old.AtMs >= b.Cursor.At {
				continue
			}
			if b.EpochReason != "" {
				if err := q.InsertEpoch(ctx, generated.InsertEpochParams{InstanceID: b.InstanceID, EpochID: b.Cursor.EpochID,
					StartedAtMs: b.At, Reason: b.EpochReason}); err != nil {
					return err
				}
			}
			if b.ClosePrevious {
				if err := q.CloseObservedConnections(ctx, b.InstanceID); err != nil {
					return err
				}
			}
			for _, m := range b.Minutes {
				err := q.UpsertMinute(ctx, generated.UpsertMinuteParams{
					InstanceID:         b.InstanceID,
					BucketMs:           m.Bucket,
					TotalUpload:        m.Upload,
					TotalDownload:      m.Download,
					AttributedUpload:   m.AttributedUpload,
					AttributedDownload: m.AttributedDownload,
					ObservedMs:         m.ObservedMS,
					Gap:                boolInt(m.Gap),
				})
				if err != nil {
					return fmt.Errorf("write minute: %w", err)
				}
				hours[hourKey{id: b.InstanceID, at: m.Bucket / 3600000 * 3600000}] = true
			}
			for _, d := range b.Dimensions {
				err := q.UpsertDimensionMinute(ctx, generated.UpsertDimensionMinuteParams{
					InstanceID:    b.InstanceID,
					BucketMs:      d.Bucket,
					Dimension:     d.Name,
					Value:         d.Value,
					UploadBytes:   d.Upload,
					DownloadBytes: d.Download,
				})
				if err != nil {
					return fmt.Errorf("write dimensions: %w", err)
				}
			}
			for _, r := range b.Records {
				var opened *int64
				if t, err := time.Parse(time.RFC3339Nano, r.Start); err == nil {
					v := t.UnixMilli()
					opened = &v
				}
				err := q.UpsertConnection(ctx, generated.UpsertConnectionParams{
					InstanceID:    b.InstanceID,
					LifecycleID:   r.LifecycleID,
					EpochID:       r.EpochID,
					ConnectionID:  r.ConnectionID,
					OpenedAtMs:    nullableInt(opened),
					FirstSeenAtMs: r.FirstSeen,
					LastSeenAtMs:  r.LastSeen,
					ClosedAtMs:    nullableInt(r.ClosedAt),
					CloseReason:   r.CloseReason,
					Host:          r.Host,
					Proxy:         r.Proxy,
					Rule:          r.Rule,
					Process:       r.Process,
					SrcIp:         r.SourceIP,
					UploadBytes:   r.Upload,
					DownloadBytes: r.Download,
				})
				if err != nil {
					return fmt.Errorf("write lifecycle: %w", err)
				}
			}
			for _, gap := range b.Gaps {
				err := q.InsertGap(ctx, generated.InsertGapParams{
					ID:                     gap.ID,
					InstanceID:             b.InstanceID,
					EpochID:                gap.EpochID,
					FromMs:                 gap.From,
					ToMs:                   gap.To,
					Reason:                 gap.Reason,
					Resolution:             gap.Resolution,
					CandidateUploadBytes:   nullableInt(gap.CandidateUpload),
					CandidateDownloadBytes: nullableInt(gap.CandidateDownload),
				})
				if err != nil {
					return fmt.Errorf("write gap: %w", err)
				}
			}
			c := b.Cursor
			if err := q.SaveCursor(ctx, generated.SaveCursorParams{InstanceID: b.InstanceID, EpochID: c.EpochID, SampleID: c.SampleID,
				SourceID: c.SourceID, UploadTotal: c.Upload, DownloadTotal: c.Download, AtMs: c.At}); err != nil {
				return err
			}
		}
		if err := q.SaveCollectorState(ctx, generated.SaveCollectorStateParams{InstanceID: b.InstanceID, Generation: b.Generation,
			Status: b.Status, ErrorCode: b.ErrorCode, AtMs: b.At}); err != nil {
			return err
		}
	}
	for h := range hours {
		err := q.RecomputeHour(ctx, generated.RecomputeHourParams{
			InstanceID: h.id,
			BucketMs:   h.at,
			FromMs:     h.at,
			ToMs:       h.at + 3600000,
		})
		if err != nil {
			return fmt.Errorf("recompute hour: %w", err)
		}
		if err := q.DeleteDimensionHour(ctx, generated.DeleteDimensionHourParams{
			InstanceID: h.id,
			BucketMs:   h.at,
		}); err != nil {
			return err
		}
		err = q.RecomputeDimensionHour(ctx, generated.RecomputeDimensionHourParams{
			InstanceID: h.id,
			BucketMs:   h.at,
			FromMs:     h.at,
			ToMs:       h.at + 3600000,
		})
		if err != nil {
			return fmt.Errorf("recompute dimension hour: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit telemetry: %w", err)
	}
	return nil
}

type Retention struct{ ConnectionsDays, MinuteDays, HourlyDays int }

func (s Telemetry) Prune(ctx context.Context, now time.Time, r Retention) error {
	cuts := map[string]struct {
		column string
		days   int
	}{
		"connection_records":       {column: "closed_at_ms", days: r.ConnectionsDays},
		"traffic_minute":           {column: "bucket_ms", days: r.MinuteDays},
		"traffic_dimension_minute": {column: "bucket_ms", days: r.MinuteDays},
		"traffic_hour":             {column: "bucket_ms", days: r.HourlyDays},
		"traffic_dimension_hour":   {column: "bucket_ms", days: r.HourlyDays},
		"traffic_gap_intervals":    {column: "to_ms", days: r.HourlyDays},
	}
	for table, c := range cuts {
		// Identifiers are constants from the local map, never API input.
		query := fmt.Sprintf("DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s < ? LIMIT 1000)", table, table, c.column)
		// Yield the write connection between short deletions. Bound each pass
		// as well as the caller's deadline so retention cannot monopolize writes.
		for range 20 {
			result, err := s.DB.ExecContext(ctx, query, now.Add(-time.Duration(c.days)*24*time.Hour).UnixMilli())
			if err != nil {
				return fmt.Errorf("prune retention: %w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("count retention deletion: %w", err)
			}
			if affected < 1000 {
				break
			}
		}
	}
	return nil
}

// Backup uses SQLite's consistent VACUUM INTO snapshot, including committed WAL.
// The destination must be new; keys are backed up separately by the operator.
func (s Telemetry) Backup(ctx context.Context, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return errors.Join(fmt.Errorf("sqlite backup: %w", err), os.Remove(destination))
	}
	return nil
}

func nullableInt(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}
