// Package analytics 负责历史查询约束和统计口径，不依赖数据库或 HTTP 框架。
package analytics

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrInvalid = errors.New("invalid analytics query")
var ErrNotFound = errors.New("history instance not found")

type Query struct {
	Kind          string
	IDs           []string
	From, To      int64
	Step          string
	Dimension     string
	Limit, Offset int
}

// Repository 按查询条件读取一次一致的历史数据快照。
type Repository interface {
	Load(context.Context, Query) (Snapshot, error)
}
type Service struct {
	Repository  Repository
	MaxInterval time.Duration
}

type Result struct {
	Data any
	Meta Meta
}
type Meta struct {
	Source           string     `json:"source"`
	MetricDefinition string     `json:"metric_definition"`
	From             int64      `json:"from_ms"`
	To               int64      `json:"to_ms"`
	Step             string     `json:"step"`
	GapCount         int64      `json:"gap_count"`
	HasGap           bool       `json:"has_gap"`
	Coverage         []Coverage `json:"coverage"`
}
type Coverage struct {
	InstanceID          string  `json:"instance_id"`
	Name                string  `json:"name"`
	Deleted             bool    `json:"deleted"`
	ObservedMS          int64   `json:"observed_ms"`
	WindowMS            int64   `json:"window_ms"`
	SampleCoverage      float64 `json:"sample_coverage"`
	FirstRetainedMinute *int64  `json:"first_retained_minute_ms"`
	FirstRetainedHour   *int64  `json:"first_retained_hour_ms"`
	LastSnapshot        *int64  `json:"last_snapshot_at_ms"`
	HasGap              bool    `json:"has_gap"`
}
type Totals struct {
	Upload               int64  `json:"total_upload"`
	Download             int64  `json:"total_download"`
	AttributedUpload     int64  `json:"attributed_upload"`
	AttributedDownload   int64  `json:"attributed_download"`
	UnattributedUpload   *int64 `json:"unattributed_upload"`
	UnattributedDownload *int64 `json:"unattributed_download"`
	Consistent           bool   `json:"consistent"`
}
type Point struct {
	Bucket int64 `json:"bucket_ms"`
	Totals
	ObservedMS int64 `json:"observed_ms"`
	HasGap     bool  `json:"has_gap"`
}
type Top struct {
	Value    string `json:"value"`
	Upload   int64  `json:"upload_bytes"`
	Download int64  `json:"download_bytes"`
}
type Connection struct {
	InstanceID   string `json:"instance_id"`
	LifecycleID  string `json:"lifecycle_id"`
	EpochID      string `json:"epoch_id"`
	ConnectionID string `json:"connection_id"`
	OpenedAt     *int64 `json:"opened_at_ms"`
	FirstSeen    int64  `json:"first_seen_at_ms"`
	LastSeen     int64  `json:"last_seen_at_ms"`
	ClosedAt     *int64 `json:"closed_at_ms"`
	CloseReason  string `json:"close_reason"`
	Host         string `json:"host"`
	Proxy        string `json:"proxy"`
	Rule         string `json:"rule"`
	Process      string `json:"process"`
	SourceIP     string `json:"src_ip"`
	Upload       int64  `json:"lifetime_upload_bytes"`
	Download     int64  `json:"lifetime_download_bytes"`
}
type Gap struct {
	ID                string `json:"id"`
	InstanceID        string `json:"instance_id"`
	EpochID           string `json:"epoch_id"`
	From              int64  `json:"from_ms"`
	To                *int64 `json:"to_ms"`
	Reason            string `json:"reason"`
	Resolution        string `json:"resolution"`
	CandidateUpload   *int64 `json:"candidate_upload_bytes"`
	CandidateDownload *int64 `json:"candidate_download_bytes"`
}

func Parse(kind string, values url.Values, now time.Time) (Query, error) {
	q := Query{Kind: kind, Step: values.Get("step"), Dimension: values.Get("dimension"), Limit: 100}
	invalid := func(message string) (Query, error) { return Query{}, fmt.Errorf("%w: %s", ErrInvalid, message) }
	for key, v := range values {
		if len(v) != 1 {
			return invalid("duplicate query parameter")
		}
		switch key {
		case "instance_ids", "from", "to", "step", "dimension", "limit", "offset":
		default:
			return invalid("unknown query parameter")
		}
	}
	switch kind {
	case "summary", "traffic", "top", "connections", "gaps", "coverage":
	default:
		return invalid("unknown query")
	}
	if raw := values.Get("instance_ids"); raw != "" && raw != "all" {
		seen := map[string]bool{}
		for _, id := range strings.Split(raw, ",") {
			if _, err := uuid.Parse(id); err != nil || seen[id] {
				return invalid("instance_ids requires unique UUIDs")
			}
			seen[id] = true
			q.IDs = append(q.IDs, id)
		}
		if len(q.IDs) > 256 {
			return invalid("at most 256 instances")
		}
	}
	to := now.UTC().Truncate(time.Minute)
	from := to.Add(-24 * time.Hour)
	for key, dst := range map[string]*time.Time{"from": &from, "to": &to} {
		if raw := values.Get(key); raw != "" {
			t, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return invalid("times must be RFC3339 UTC")
			}
			_, offset := t.Zone()
			if offset != 0 {
				return invalid("times must use UTC")
			}
			*dst = t
		}
	}
	if !to.After(from) || from.UnixMilli() < 0 || to.Sub(from) > 366*24*time.Hour || to.After(now.Add(time.Minute)) {
		return invalid("window must be positive, at most 366 days, and not in the future")
	}
	q.From, q.To = from.UnixMilli(), to.UnixMilli()
	if q.Step == "" {
		q.Step = "1m"
		if to.Sub(from) > 24*time.Hour {
			q.Step = "1h"
		}
		if to.Sub(from) > 60*24*time.Hour {
			q.Step = "1d"
		}
	}
	duration := StepMS(q.Step)
	if duration == 0 {
		return invalid("step must be 1m, 1h or 1d")
	}
	if kind != "connections" && kind != "gaps" {
		alignment := q.sourceBucketMS()
		if q.From%alignment != 0 || q.To%alignment != 0 {
			return invalid("aggregate boundaries must align to UTC minutes (hours for 1h/1d)")
		}
		if (q.To-q.From+duration-1)/duration > 2000 {
			return invalid("at most 2000 points; choose a larger step")
		}
	}
	if kind == "top" {
		switch q.Dimension {
		case "host", "proxy", "rule", "process", "src_ip":
		default:
			return invalid("unsupported dimension")
		}
	}
	for key, dst := range map[string]*int{"limit": &q.Limit, "offset": &q.Offset} {
		if raw := values.Get(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return invalid("invalid pagination")
			}
			*dst = n
		}
	}
	if q.Limit < 1 || q.Limit > 500 || q.Offset < 0 || q.Offset > 100000 {
		return invalid("limit is 1..500 and offset is 0..100000")
	}
	if kind == "connections" && q.To-q.From > 31*24*time.Hour.Milliseconds() {
		return invalid("connection window is at most 31 days")
	}
	return q, nil
}
func StepMS(step string) int64 {
	switch step {
	case "1m":
		return 60000
	case "1h":
		return 3600000
	case "1d":
		return 86400000
	}
	return 0
}

// BucketWindow 只选择完整落在查询窗口内的留存桶，避免高估未对齐窗口的覆盖率。
// 日粒度从小时桶汇总，因此读取窗口仍按小时对齐。
func (q Query) BucketWindow() (int64, int64) {
	align := q.sourceBucketMS()
	return (q.From + align - 1) / align * align, q.To / align * align
}

func (q Query) sourceBucketMS() int64 {
	if q.Step == "1m" {
		return 60000
	}
	return 3600000
}

func (s Service) Query(ctx context.Context, kind string, v url.Values) (Result, error) {
	q, err := Parse(kind, v, time.Now())
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// 存储只返回一次一致快照；所有统计口径在事务外基于该快照计算，不再回读数据库。
	snapshot, err := s.Repository.Load(ctx, q)
	if err != nil {
		return Result{}, err
	}
	return BuildResult(
		q,
		snapshot,
		time.Now(),
		s.MaxInterval,
	)
}
