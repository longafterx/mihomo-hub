package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/longafterx/mihomo-hub/internal/analytics"
	"github.com/longafterx/mihomo-hub/internal/storage/generated"
)

type Analytics struct{ DB *sql.DB }

// Load 在一个只读事务中读取流量、缺口和留存信息，保证它们来自同一个已提交快照。
// 返回前完成数据映射，事务外的统计计算不再访问数据库。
func (s Analytics) Load(ctx context.Context, p analytics.Query) (analytics.Snapshot, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return analytics.Snapshot{}, fmt.Errorf("begin analytics: %w", err)
	}
	defer tx.Rollback()
	snapshot, err := s.load(ctx, generated.New(tx), p)
	if err != nil {
		return analytics.Snapshot{}, fmt.Errorf("query analytics: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return analytics.Snapshot{}, fmt.Errorf("finish analytics: %w", err)
	}
	return snapshot, nil
}

func (s Analytics) load(ctx context.Context, q *generated.Queries, p analytics.Query) (analytics.Snapshot, error) {
	requested := p.IDs
	if requested == nil {
		requested = []string{}
	}
	raw, err := json.Marshal(requested)
	if err != nil {
		return analytics.Snapshot{}, err
	}
	items, err := q.AnalyticsInstances(ctx, string(raw))
	if err != nil {
		return analytics.Snapshot{}, err
	}
	ids := []string{}
	for _, i := range items {
		ids = append(ids, i.ID)
	}
	raw, err = json.Marshal(ids)
	if err != nil {
		return analytics.Snapshot{}, err
	}
	encoded := string(raw)
	buckets, observed, err := loadAnalyticsBuckets(
		ctx,
		q,
		p,
		encoded,
	)
	if err != nil {
		return analytics.Snapshot{}, err
	}
	snapshot := analytics.Snapshot{
		Instances: []analytics.HistoryInstance{}, Buckets: buckets, Observed: observed,
		Top: []analytics.Top{}, Connections: []analytics.Connection{}, Gaps: []analytics.Gap{},
	}
	for _, item := range items {
		retained, err := q.HistoryRange(ctx, item.ID)
		if err != nil {
			return analytics.Snapshot{}, err
		}
		gapCount, err := q.CountGaps(ctx, generated.CountGapsParams{
			InstanceID: item.ID, FromMs: p.From, ToMs: p.To,
		})
		if err != nil {
			return analytics.Snapshot{}, err
		}
		snapshot.Instances = append(snapshot.Instances, analytics.HistoryInstance{
			ID: item.ID, Name: item.Name, Deleted: item.DeletedAtMs.Valid, CreatedAt: item.CreatedAtMs,
			FirstRetainedMinute: optionalMS(retained.MinuteFrom),
			FirstRetainedHour:   optionalMS(retained.HourFrom),
			LastSnapshot:        optionalMS(retained.LastSnapshot),
			GapCount:            gapCount,
		})
	}
	switch p.Kind {
	case "top":
		if p.Step == "1m" {
			rows, err := q.TopMinute(ctx, generated.TopMinuteParams{
				Ids: encoded, FromMs: p.From, ToMs: p.To, Dimension: p.Dimension,
				PageLimit: int64(p.Limit), PageOffset: int64(p.Offset),
			})
			if err != nil {
				return analytics.Snapshot{}, err
			}
			for _, r := range rows {
				snapshot.Top = append(snapshot.Top, analytics.Top{
					Value: r.Value, Upload: r.UploadBytes, Download: r.DownloadBytes,
				})
			}
		} else {
			rows, err := q.TopHour(ctx, generated.TopHourParams{
				Ids: encoded, FromMs: p.From, ToMs: p.To, Dimension: p.Dimension,
				PageLimit: int64(p.Limit), PageOffset: int64(p.Offset),
			})
			if err != nil {
				return analytics.Snapshot{}, err
			}
			for _, r := range rows {
				snapshot.Top = append(snapshot.Top, analytics.Top{
					Value: r.Value, Upload: r.UploadBytes, Download: r.DownloadBytes,
				})
			}
		}
	case "connections":
		rows, err := q.HistoryConnections(ctx, generated.HistoryConnectionsParams{
			Ids: encoded, FromMs: p.From, ToMs: p.To,
			PageLimit: int64(p.Limit), PageOffset: int64(p.Offset),
		})
		if err != nil {
			return analytics.Snapshot{}, err
		}
		for _, r := range rows {
			snapshot.Connections = append(snapshot.Connections, analytics.Connection{
				InstanceID: r.InstanceID, LifecycleID: r.LifecycleID, EpochID: r.EpochID,
				ConnectionID: r.ConnectionID, OpenedAt: nullMS(r.OpenedAtMs),
				FirstSeen: r.FirstSeenAtMs, LastSeen: r.LastSeenAtMs,
				ClosedAt: nullMS(r.ClosedAtMs), CloseReason: r.CloseReason,
				Host: r.Host, Proxy: r.Proxy, Rule: r.Rule, Process: r.Process, SourceIP: r.SrcIp,
				Upload: r.UploadBytes, Download: r.DownloadBytes,
			})
		}
	case "gaps":
		// 只读取可能进入目标页的持久化前缀；合并持续缺口与最终分页由 analytics 完成。
		rows, err := q.HistoryGaps(ctx, generated.HistoryGapsParams{
			Ids: encoded, FromMs: p.From, ToMs: p.To,
			PageLimit: int64(p.Limit + p.Offset), PageOffset: 0,
		})
		if err != nil {
			return analytics.Snapshot{}, err
		}
		for _, r := range rows {
			end := r.ToMs
			snapshot.Gaps = append(snapshot.Gaps, analytics.Gap{
				ID: r.ID, InstanceID: r.InstanceID, EpochID: r.EpochID,
				From: r.FromMs, To: &end, Reason: r.Reason, Resolution: r.Resolution,
				CandidateUpload: nullMS(r.CandidateUploadBytes), CandidateDownload: nullMS(r.CandidateDownloadBytes),
			})
		}
	}
	return snapshot, nil
}

func loadAnalyticsBuckets(
	ctx context.Context,
	q *generated.Queries,
	p analytics.Query,
	ids string,
) ([]analytics.TrafficBucket, map[string]int64, error) {
	from, to := p.BucketWindow()
	buckets := []analytics.TrafficBucket{}
	observed := map[string]int64{}
	rows := []generated.TrafficMinute{}
	needsTotals := p.Kind == "summary" || p.Kind == "traffic"
	if needsTotals {
		if p.Step == "1m" {
			var err error
			rows, err = q.AnalyticsMinute(ctx, generated.AnalyticsMinuteParams{Ids: ids, FromMs: from, ToMs: to})
			if err != nil {
				return nil, nil, err
			}
		} else {
			hours, err := q.AnalyticsHour(ctx, generated.AnalyticsHourParams{Ids: ids, FromMs: from, ToMs: to})
			if err != nil {
				return nil, nil, err
			}
			for _, h := range hours {
				rows = append(rows, generated.TrafficMinute(h))
			}
		}
	} else if p.Step == "1m" {
		coverage, err := q.ObservedMinute(ctx, generated.ObservedMinuteParams{Ids: ids, FromMs: from, ToMs: to})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range coverage {
			observed[r.InstanceID] = r.ObservedMs
		}
	} else {
		coverage, err := q.ObservedHour(ctx, generated.ObservedHourParams{Ids: ids, FromMs: from, ToMs: to})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range coverage {
			observed[r.InstanceID] = r.ObservedMs
		}
	}
	for _, r := range rows {
		buckets = append(buckets, analytics.TrafficBucket{
			InstanceID: r.InstanceID, Bucket: r.BucketMs,
			Upload: r.TotalUpload, Download: r.TotalDownload,
			AttributedUpload: r.AttributedUpload, AttributedDownload: r.AttributedDownload,
			ObservedMS: r.ObservedMs, HasGap: r.Gap != 0,
		})
	}
	return buckets, observed, nil
}

func nullMS(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func optionalMS(v int64) *int64 {
	if v < 0 {
		return nil
	}
	return &v
}
