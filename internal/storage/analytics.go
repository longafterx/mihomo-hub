package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/longafterx/mihomo-hub/internal/analytics"
	"github.com/longafterx/mihomo-hub/internal/storage/generated"
)

type Analytics struct {
	DB          *sql.DB
	MaxInterval time.Duration
}

func nullMS(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
func sumInto(dst *int64, n int64) error {
	if n < 0 || *dst > math.MaxInt64-n {
		return errors.New("analytics counter overflow")
	}
	*dst += n
	return nil
}
func finishTotals(t *analytics.Totals) {
	t.Consistent = t.AttributedUpload <= t.Upload && t.AttributedDownload <= t.Download
	if t.AttributedUpload <= t.Upload {
		n := t.Upload - t.AttributedUpload
		t.UnattributedUpload = &n
	}
	if t.AttributedDownload <= t.Download {
		n := t.Download - t.AttributedDownload
		t.UnattributedDownload = &n
	}
}
func addTotals(dst *analytics.Totals, r generated.TrafficMinute) error {
	for _, v := range []struct {
		dst *int64
		n   int64
	}{
		{&dst.Upload, r.TotalUpload},
		{&dst.Download, r.TotalDownload},
		{&dst.AttributedUpload, r.AttributedUpload},
		{&dst.AttributedDownload, r.AttributedDownload},
	} {
		if err := sumInto(v.dst, v.n); err != nil {
			return err
		}
	}
	return nil
}

// Query uses a read-only transaction so metrics, gaps and cursor coverage share
// one committed SQLite snapshot even while the writer continues ingesting.
func (s Analytics) Query(ctx context.Context, p analytics.Query) (analytics.Result, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return analytics.Result{}, fmt.Errorf("begin analytics: %w", err)
	}
	defer tx.Rollback()
	result, err := s.query(ctx, generated.New(tx), p)
	if err != nil {
		return analytics.Result{}, fmt.Errorf("query analytics: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return analytics.Result{}, fmt.Errorf("finish analytics: %w", err)
	}
	return result, nil
}
func (s Analytics) query(ctx context.Context, q *generated.Queries, p analytics.Query) (analytics.Result, error) {
	requested := p.IDs
	if requested == nil {
		requested = []string{}
	}
	raw, err := json.Marshal(requested)
	if err != nil {
		return analytics.Result{}, err
	}
	items, err := q.AnalyticsInstances(ctx, string(raw))
	if err != nil {
		return analytics.Result{}, err
	}
	if len(requested) > 0 && len(items) != len(requested) {
		return analytics.Result{}, analytics.ErrNotFound
	}
	// 256 仅限制客户端显式选择的 ID 数量，不能限制全局汇总中的历史身份。
	if len(requested) > 256 {
		return analytics.Result{}, fmt.Errorf("%w: select at most 256 historical instances", analytics.ErrInvalid)
	}
	ids := []string{}
	for _, i := range items {
		ids = append(ids, i.ID)
	}
	raw, err = json.Marshal(ids)
	if err != nil {
		return analytics.Result{}, err
	}
	encoded := string(raw)
	from, to := p.From, p.To
	// Unaligned connection/gap windows receive a conservative coverage lower bound:
	// only complete retained buckets wholly inside the requested interval count.
	align := int64(60000)
	if p.Step != "1m" {
		align = 3600000
	}
	coverageFrom := (from + align - 1) / align * align
	coverageTo := to / align * align
	rows := []generated.TrafficMinute{}
	observed := map[string]int64{}
	needsTotals := p.Kind == "summary" || p.Kind == "traffic"
	if needsTotals {
		if p.Step == "1m" {
			rows, err = q.AnalyticsMinute(ctx, generated.AnalyticsMinuteParams{
				Ids:    encoded,
				FromMs: coverageFrom,
				ToMs:   coverageTo,
			})
		} else {
			hours, queryErr := q.AnalyticsHour(ctx, generated.AnalyticsHourParams{
				Ids:    encoded,
				FromMs: coverageFrom,
				ToMs:   coverageTo,
			})
			err = queryErr
			for _, h := range hours {
				rows = append(rows, generated.TrafficMinute(h))
			}
		}
	} else if p.Step == "1m" {
		coverage, queryErr := q.ObservedMinute(ctx, generated.ObservedMinuteParams{
			Ids:    encoded,
			FromMs: coverageFrom,
			ToMs:   coverageTo,
		})
		err = queryErr
		for _, r := range coverage {
			observed[r.InstanceID] = r.ObservedMs
		}
	} else {
		coverage, queryErr := q.ObservedHour(ctx, generated.ObservedHourParams{
			Ids:    encoded,
			FromMs: coverageFrom,
			ToMs:   coverageTo,
		})
		err = queryErr
		for _, r := range coverage {
			observed[r.InstanceID] = r.ObservedMs
		}
	}
	if err != nil {
		return analytics.Result{}, err
	}
	points := map[int64]*analytics.Point{}
	summary := analytics.Totals{}
	for _, r := range rows {
		if err := sumIntoPtr(observed, r.InstanceID, r.ObservedMs); err != nil {
			return analytics.Result{}, err
		}
		if err := addTotals(&summary, r); err != nil {
			return analytics.Result{}, err
		}
		bucket := r.BucketMs / analytics.StepMS(p.Step) * analytics.StepMS(p.Step)
		point := points[bucket]
		if point == nil {
			point = &analytics.Point{Bucket: bucket}
			points[bucket] = point
		}
		if err := addTotals(&point.Totals, r); err != nil {
			return analytics.Result{}, err
		}
		if err := sumInto(&point.ObservedMS, r.ObservedMs); err != nil {
			return analytics.Result{}, err
		}
		point.HasGap = point.HasGap || r.Gap != 0
	}
	finishTotals(&summary)
	meta := analytics.Meta{
		Source:           "mihomo_connections_ws",
		MetricDefinition: "observed_counter_deltas; connection attribution is sampled; recovery candidates excluded; coverage is time, not accuracy",
		From:             from,
		To:               to,
		Step:             p.Step,
		Coverage:         []analytics.Coverage{},
	}
	ongoing := []analytics.Gap{}
	now := time.Now().UnixMilli()
	for _, item := range items {
		retained, err := q.HistoryRange(ctx, item.ID)
		if err != nil {
			return analytics.Result{}, err
		}
		gapCount, err := q.CountGaps(ctx, generated.CountGapsParams{InstanceID: item.ID, FromMs: from, ToMs: to})
		if err != nil {
			return analytics.Result{}, err
		}
		meta.GapCount += gapCount
		c := analytics.Coverage{
			InstanceID:          item.ID,
			Name:                item.Name,
			Deleted:             item.DeletedAtMs.Valid,
			ObservedMS:          observed[item.ID],
			WindowMS:            to - from,
			FirstRetainedMinute: optionalMS(retained.MinuteFrom),
			FirstRetainedHour:   optionalMS(retained.HourFrom),
			LastSnapshot:        optionalMS(retained.LastSnapshot),
		}
		c.SampleCoverage = float64(c.ObservedMS) / float64(c.WindowMS)
		c.HasGap = gapCount > 0 || c.ObservedMS < c.WindowMS
		meta.HasGap = meta.HasGap || c.HasGap
		meta.Coverage = append(meta.Coverage, c)
		if item.Enabled != 0 && item.CollectEnabled != 0 && !item.DeletedAtMs.Valid {
			start := item.CreatedAtMs
			if retained.LastSnapshot >= 0 {
				start = retained.LastSnapshot
			}
			if now-start > s.MaxInterval.Milliseconds() && start < to && now >= from {
				ongoing = append(ongoing, analytics.Gap{
					ID:         "ongoing/" + item.ID,
					InstanceID: item.ID,
					From:       start,
					Reason:     "awaiting_snapshot",
					Resolution: "unknown",
				})
			}
		}
	}
	meta.GapCount += int64(len(ongoing))
	result := analytics.Result{Meta: meta}
	switch p.Kind {
	case "summary":
		result.Data = summary
	case "coverage":
		result.Data = meta.Coverage
	case "traffic":
		out := []analytics.Point{}
		step := analytics.StepMS(p.Step)
		for bucket := from / step * step; bucket < to; bucket += step {
			point := points[bucket]
			if point == nil {
				point = &analytics.Point{Bucket: bucket}
			}
			finishTotals(&point.Totals)
			expected := (min(bucket+step, to) - max(bucket, from)) * int64(len(items))
			point.HasGap = point.HasGap || point.ObservedMS < expected || !point.Consistent
			out = append(out, *point)
		}
		result.Data = out
	case "top":
		out := []analytics.Top{}
		if p.Step == "1m" {
			rs, err := q.TopMinute(ctx, generated.TopMinuteParams{
				Ids:        encoded,
				FromMs:     from,
				ToMs:       to,
				Dimension:  p.Dimension,
				PageLimit:  int64(p.Limit),
				PageOffset: int64(p.Offset),
			})
			if err != nil {
				return result, err
			}
			for _, r := range rs {
				out = append(out, analytics.Top{Value: r.Value, Upload: r.UploadBytes, Download: r.DownloadBytes})
			}
		} else {
			rs, err := q.TopHour(ctx, generated.TopHourParams{
				Ids:        encoded,
				FromMs:     from,
				ToMs:       to,
				Dimension:  p.Dimension,
				PageLimit:  int64(p.Limit),
				PageOffset: int64(p.Offset),
			})
			if err != nil {
				return result, err
			}
			for _, r := range rs {
				out = append(out, analytics.Top{Value: r.Value, Upload: r.UploadBytes, Download: r.DownloadBytes})
			}
		}
		result.Data = out
	case "connections":
		rs, err := q.HistoryConnections(ctx, generated.HistoryConnectionsParams{
			Ids:        encoded,
			FromMs:     from,
			ToMs:       to,
			PageLimit:  int64(p.Limit),
			PageOffset: int64(p.Offset),
		})
		if err != nil {
			return result, err
		}
		out := []analytics.Connection{}
		for _, r := range rs {
			out = append(out, analytics.Connection{
				InstanceID:   r.InstanceID,
				LifecycleID:  r.LifecycleID,
				EpochID:      r.EpochID,
				ConnectionID: r.ConnectionID,
				OpenedAt:     nullMS(r.OpenedAtMs),
				FirstSeen:    r.FirstSeenAtMs,
				LastSeen:     r.LastSeenAtMs,
				ClosedAt:     nullMS(r.ClosedAtMs),
				CloseReason:  r.CloseReason,
				Host:         r.Host,
				Proxy:        r.Proxy,
				Rule:         r.Rule,
				Process:      r.Process,
				SourceIP:     r.SrcIp,
				Upload:       r.UploadBytes,
				Download:     r.DownloadBytes,
			})
		}
		result.Data = out
		result.Meta.MetricDefinition = "connection lifecycle cumulative counters, not bytes within the query window; selected by observation overlap"
	case "gaps":
		// Merge bounded persisted records with current open intervals before paging.
		rs, err := q.HistoryGaps(ctx, generated.HistoryGapsParams{
			Ids:        encoded,
			FromMs:     from,
			ToMs:       to,
			PageLimit:  int64(p.Limit + p.Offset),
			PageOffset: 0,
		})
		if err != nil {
			return result, err
		}
		out := ongoing
		for _, r := range rs {
			end := r.ToMs
			out = append(out, analytics.Gap{
				ID:                r.ID,
				InstanceID:        r.InstanceID,
				EpochID:           r.EpochID,
				From:              r.FromMs,
				To:                &end,
				Reason:            r.Reason,
				Resolution:        r.Resolution,
				CandidateUpload:   nullMS(r.CandidateUploadBytes),
				CandidateDownload: nullMS(r.CandidateDownloadBytes),
			})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].From == out[j].From {
				return out[i].ID < out[j].ID
			}
			return out[i].From > out[j].From
		})
		start := min(p.Offset, len(out))
		out = out[start:min(start+p.Limit, len(out))]
		result.Data = out
	}
	result.Meta.HasGap = result.Meta.HasGap || !summary.Consistent
	return result, nil
}
func sumIntoPtr(m map[string]int64, key string, n int64) error {
	v := m[key]
	if err := sumInto(&v, n); err != nil {
		return err
	}
	m[key] = v
	return nil
}

func optionalMS(v int64) *int64 {
	if v < 0 {
		return nil
	}
	return &v
}
