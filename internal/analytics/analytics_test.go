package analytics_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/analytics"
)

func TestQueryBounds(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, kind, query string
		valid             bool
	}{
		{"defaults", "summary", "", true},
		{"UTC hours", "traffic", "from=2026-11-01T00:00:00Z&to=2026-11-02T00:00:00Z&step=1h", true},
		{"dimension", "top", "dimension=host", true},
		{"injection", "top", "dimension=host%3BDROP+TABLE+instances", false},
		{"huge export", "connections", "limit=10000", false},
		{"deep page", "gaps", "offset=100001", false},
		{"duplicate", "traffic", "step=1h&step=1m", false},
		{"non UTC", "summary", "from=2026-11-05T00:00:00%2B08:00", false},
		{"unaligned", "summary", "from=2026-11-05T00:00:01Z", false},
		{"too many points", "traffic", "from=2026-11-01T00:00:00Z&step=1m", false},
		{"bad UUID", "summary", "instance_ids=arbitrary", false},
		{"unknown param", "summary", "target=http://evil", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			_, err = analytics.Parse(tt.kind, v, now)
			if (err == nil) != tt.valid {
				t.Fatal(err)
			}
			if err != nil && !errors.Is(err, analytics.ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
}

func TestBucketWindowUsesCompleteSourceBuckets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, step string
		from, to   int64
		wantFrom   int64
		wantTo     int64
	}{
		{name: "minute", step: "1m", from: 1, to: 179999, wantFrom: 60000, wantTo: 120000},
		{name: "no complete minute", step: "1m", from: 1, to: 59999, wantFrom: 60000, wantTo: 0},
		{name: "hour", step: "1h", from: 3600001, to: 10800001, wantFrom: 7200000, wantTo: 10800000},
		{name: "day reads hours", step: "1d", from: 3600001, to: 10800001, wantFrom: 7200000, wantTo: 10800000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := analytics.Query{Step: tc.step, From: tc.from, To: tc.to}
			from, to := query.BucketWindow()
			if from != tc.wantFrom || to != tc.wantTo {
				t.Fatalf("got [%d,%d), want [%d,%d)", from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}

type snapshotLoader func(context.Context, analytics.Query) (analytics.Snapshot, error)

func (load snapshotLoader) Load(ctx context.Context, query analytics.Query) (analytics.Snapshot, error) {
	return load(ctx, query)
}

func TestServiceLoadsSnapshotAndBuildsResult(t *testing.T) {
	t.Parallel()
	calls := 0
	service := analytics.Service{
		MaxInterval: 5 * time.Second,
		Repository: snapshotLoader(func(ctx context.Context, query analytics.Query) (analytics.Snapshot, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 5*time.Second {
				t.Fatal("history read has no bounded deadline")
			}
			if query.Kind != "summary" || query.From != 0 || query.To != 60000 || query.Step != "1m" {
				t.Fatalf("query was not normalized: %+v", query)
			}
			return analytics.Snapshot{
				Instances: []analytics.HistoryInstance{{ID: "one", Deleted: true}},
				Buckets: []analytics.TrafficBucket{{
					InstanceID: "one", Upload: 9, AttributedUpload: 3, ObservedMS: 60000,
				}},
			}, nil
		}),
	}
	values := url.Values{"from": {"1970-01-01T00:00:00Z"}, "to": {"1970-01-01T00:01:00Z"}}
	result, err := service.Query(t.Context(), "summary", values)
	if err != nil {
		t.Fatal(err)
	}
	totals := result.Data.(analytics.Totals)
	if calls != 1 || totals.Upload != 9 || *totals.UnattributedUpload != 6 {
		t.Fatalf("snapshot was not interpreted by the service: calls=%d, totals=%+v", calls, totals)
	}
	if result.Meta.HasGap || result.Meta.Coverage[0].SampleCoverage != 1 {
		t.Fatalf("full coverage was lost: %+v", result.Meta)
	}
}

func TestServiceQueryErrors(t *testing.T) {
	t.Parallel()
	storageErr := errors.New("snapshot unavailable")
	for _, tc := range []struct {
		name, query string
		loadErr     error
		cancel      bool
		want        error
		wantCalls   int
	}{
		{name: "invalid query", query: "unknown=value", want: analytics.ErrInvalid},
		{name: "storage failure", loadErr: storageErr, want: storageErr, wantCalls: 1},
		{name: "canceled read", cancel: true, want: context.Canceled, wantCalls: 1},
		{name: "missing history", query: "instance_ids=00000000-0000-4000-8000-000000000001",
			want: analytics.ErrNotFound, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			service := analytics.Service{
				Repository: snapshotLoader(func(ctx context.Context, _ analytics.Query) (analytics.Snapshot, error) {
					calls++
					if err := ctx.Err(); err != nil {
						return analytics.Snapshot{}, err
					}
					return analytics.Snapshot{}, tc.loadErr
				}),
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			values, err := url.ParseQuery(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Query(ctx, "summary", values)
			if !errors.Is(err, tc.want) || calls != tc.wantCalls {
				t.Fatalf("got err=%v, calls=%d; want err=%v, calls=%d", err, calls, tc.want, tc.wantCalls)
			}
		})
	}
}
