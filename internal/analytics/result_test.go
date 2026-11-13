package analytics_test

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/analytics"
)

func TestBuildResultAggregatesTraffic(t *testing.T) {
	t.Parallel()
	candidate := int64(1000)
	snapshot := analytics.Snapshot{
		Instances: []analytics.HistoryInstance{
			{ID: "one", Name: "first", Deleted: true, GapCount: 1},
			{ID: "two", Name: "second", Deleted: true},
		},
		Buckets: []analytics.TrafficBucket{
			{InstanceID: "one", Bucket: 0, Upload: 10, Download: 20,
				AttributedUpload: 4, AttributedDownload: 8, ObservedMS: 60000},
			{InstanceID: "two", Bucket: 0, Upload: 20, Download: 40,
				AttributedUpload: 8, AttributedDownload: 16, ObservedMS: 60000},
			{InstanceID: "one", Bucket: 60000, Upload: 30, Download: 60,
				AttributedUpload: 12, AttributedDownload: 24, ObservedMS: 30000, HasGap: true},
		},
		Observed: map[string]int64{"one": 0},
		Gaps:     []analytics.Gap{{ID: "recovery", CandidateUpload: &candidate}},
	}
	query := analytics.Query{Kind: "summary", From: 0, To: 180000, Step: "1m"}
	result, err := analytics.BuildResult(
		query,
		snapshot,
		time.UnixMilli(180000),
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	totals := result.Data.(analytics.Totals)
	if totals.Upload != 60 || totals.Download != 120 || !totals.Consistent {
		t.Fatalf("unexpected totals: %+v", totals)
	}
	if totals.AttributedUpload != 24 || totals.AttributedDownload != 48 {
		t.Fatalf("unexpected attribution: %+v", totals)
	}
	if *totals.UnattributedUpload != 36 || *totals.UnattributedDownload != 72 {
		t.Fatalf("unexpected unattributed counters: %+v", totals)
	}
	if result.Meta.GapCount != 1 || !result.Meta.HasGap || len(result.Meta.Coverage) != 2 {
		t.Fatalf("unexpected metadata: %+v", result.Meta)
	}
	coverage := result.Meta.Coverage[0]
	if coverage.ObservedMS != 90000 || coverage.SampleCoverage != 0.5 || !coverage.Deleted {
		t.Fatalf("unexpected coverage: %+v", coverage)
	}
	query.Kind = "traffic"
	result, err = analytics.BuildResult(
		query,
		snapshot,
		time.UnixMilli(180000),
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	points := result.Data.([]analytics.Point)
	if len(points) != 3 {
		t.Fatalf("expected three minute buckets: %+v", points)
	}
	if points[0].Upload != 30 || points[0].ObservedMS != 120000 || points[0].HasGap {
		t.Fatalf("complete bucket changed: %+v", points[0])
	}
	if points[1].Upload != 30 || points[1].ObservedMS != 30000 || !points[1].HasGap {
		t.Fatalf("partial bucket changed: %+v", points[1])
	}
	if points[2].Bucket != 120000 || points[2].Upload != 0 || !points[2].HasGap || !points[2].Consistent {
		t.Fatalf("missing bucket was not filled with a gap: %+v", points[2])
	}
	if snapshot.Observed["one"] != 0 || snapshot.Buckets[0].Upload != 10 {
		t.Fatal("building results mutated the input snapshot")
	}
}

func TestBuildResultGroupsHoursIntoDays(t *testing.T) {
	t.Parallel()
	query := analytics.Query{Kind: "traffic", From: 0, To: 172800000, Step: "1d"}
	snapshot := analytics.Snapshot{
		Instances: []analytics.HistoryInstance{{ID: "one", Deleted: true}},
		Buckets: []analytics.TrafficBucket{
			{InstanceID: "one", Bucket: 0, Upload: 3, ObservedMS: 3600000},
			{InstanceID: "one", Bucket: 3600000, Upload: 5, ObservedMS: 3600000},
			{InstanceID: "one", Bucket: 86400000, Upload: 7, ObservedMS: 3600000},
		},
	}
	result, err := analytics.BuildResult(
		query,
		snapshot,
		time.UnixMilli(172800000),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	points := result.Data.([]analytics.Point)
	if len(points) != 2 || points[0].Bucket != 0 || points[1].Bucket != 86400000 {
		t.Fatalf("unexpected day boundaries: %+v", points)
	}
	if points[0].Upload != 8 || points[0].ObservedMS != 7200000 || points[1].Upload != 7 {
		t.Fatalf("hourly counters were not grouped into days: %+v", points)
	}
}

func TestBuildResultOngoingGaps(t *testing.T) {
	t.Parallel()
	ms := func(v int64) *int64 { return &v }
	for _, tc := range []struct {
		name     string
		instance analytics.HistoryInstance
		from, to int64
		wantGap  bool
		wantFrom int64
	}{
		{name: "exact timeout", instance: analytics.HistoryInstance{LastSnapshot: ms(95000)}, to: 120000},
		{name: "stale snapshot", instance: analytics.HistoryInstance{LastSnapshot: ms(94999)},
			to: 120000, wantGap: true, wantFrom: 94999},
		{name: "never sampled", instance: analytics.HistoryInstance{CreatedAt: 80000},
			to: 120000, wantGap: true, wantFrom: 80000},
		{name: "new instance", instance: analytics.HistoryInstance{CreatedAt: 99000}, to: 120000},
		{name: "retained deletion", instance: analytics.HistoryInstance{Deleted: true}, to: 120000},
		{name: "after window", instance: analytics.HistoryInstance{LastSnapshot: ms(80000)}, to: 60000},
		{name: "future window", instance: analytics.HistoryInstance{CreatedAt: 80000}, from: 120000, to: 180000},
		{name: "zero snapshot is known", instance: analytics.HistoryInstance{CreatedAt: 99000, LastSnapshot: ms(0)},
			to: 120000, wantGap: true, wantFrom: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.instance.ID = "one"
			result, err := analytics.BuildResult(
				analytics.Query{Kind: "gaps", From: tc.from, To: tc.to, Step: "1m", Limit: 10},
				analytics.Snapshot{Instances: []analytics.HistoryInstance{tc.instance}},
				time.UnixMilli(100000),
				5*time.Second,
			)
			if err != nil {
				t.Fatal(err)
			}
			gaps := result.Data.([]analytics.Gap)
			if (len(gaps) == 1) != tc.wantGap || int64(len(gaps)) != result.Meta.GapCount {
				t.Fatalf("unexpected ongoing gaps: %+v, count=%d", gaps, result.Meta.GapCount)
			}
			if tc.wantGap {
				gap := gaps[0]
				if gap.ID != "ongoing/one" || gap.From != tc.wantFrom || gap.To != nil {
					t.Fatalf("unexpected open interval: %+v", gap)
				}
				if gap.Reason != "awaiting_snapshot" || gap.Resolution != "unknown" {
					t.Fatalf("unexpected gap semantics: %+v", gap)
				}
			}
		})
	}
}

func TestBuildResultMergesGapsBeforePaging(t *testing.T) {
	t.Parallel()
	last := int64(40000)
	snapshot := analytics.Snapshot{
		Instances: []analytics.HistoryInstance{{ID: "one", LastSnapshot: &last, GapCount: 4}},
		Gaps: []analytics.Gap{
			{ID: "z", From: 40000}, {ID: "a", From: 40000},
			{ID: "latest", From: 50000}, {ID: "old", From: 10000},
		},
	}
	original := append([]analytics.Gap{}, snapshot.Gaps...)
	result, err := analytics.BuildResult(
		analytics.Query{Kind: "gaps", To: 120000, Step: "1m", Offset: 1, Limit: 2},
		snapshot,
		time.UnixMilli(100000),
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	gaps := result.Data.([]analytics.Gap)
	if len(gaps) != 2 || gaps[0].ID != "a" || gaps[1].ID != "ongoing/one" || result.Meta.GapCount != 5 {
		t.Fatalf("merged page is incorrect: %+v, count=%d", gaps, result.Meta.GapCount)
	}
	if !reflect.DeepEqual(snapshot.Gaps, original) {
		t.Fatal("sorting results changed the snapshot's gap order")
	}
}

func TestBuildResultRejectsInvalidCounters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		rows []analytics.TrafficBucket
	}{
		{name: "upload overflow", rows: []analytics.TrafficBucket{{Upload: math.MaxInt64}, {Upload: 1}}},
		{name: "download overflow", rows: []analytics.TrafficBucket{{Download: math.MaxInt64}, {Download: 1}}},
		{name: "attribution overflow", rows: []analytics.TrafficBucket{
			{AttributedUpload: math.MaxInt64}, {AttributedUpload: 1},
		}},
		{name: "observation overflow", rows: []analytics.TrafficBucket{{ObservedMS: math.MaxInt64}, {ObservedMS: 1}}},
		{name: "negative counter", rows: []analytics.TrafficBucket{{Upload: -1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := analytics.BuildResult(
				analytics.Query{Kind: "summary", To: 60000, Step: "1m"},
				analytics.Snapshot{Buckets: tc.rows},
				time.UnixMilli(60000),
				time.Second,
			)
			if err == nil || !strings.Contains(err.Error(), "counter overflow") {
				t.Fatalf("invalid counters were accepted: %v", err)
			}
		})
	}
}

func TestBuildResultMarksInconsistentAttribution(t *testing.T) {
	t.Parallel()
	result, err := analytics.BuildResult(
		analytics.Query{Kind: "summary", To: 60000, Step: "1m"},
		analytics.Snapshot{Buckets: []analytics.TrafficBucket{{Upload: 1, AttributedUpload: 2}}},
		time.UnixMilli(60000),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	totals := result.Data.(analytics.Totals)
	if totals.Consistent || totals.UnattributedUpload != nil || !result.Meta.HasGap {
		t.Fatalf("inconsistent attribution was hidden: %+v", result)
	}
}

func TestBuildResultKeepsConnectionCountersCumulative(t *testing.T) {
	t.Parallel()
	result, err := analytics.BuildResult(
		analytics.Query{Kind: "connections", From: 60000, To: 120000, Step: "1m"},
		analytics.Snapshot{Connections: []analytics.Connection{{Upload: 1000, FirstSeen: 1000, LastSeen: 90000}}},
		time.UnixMilli(120000),
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Data.([]analytics.Connection)[0].Upload != 1000 {
		t.Fatal("connection lifetime counters were changed into window counters")
	}
	if !strings.Contains(result.Meta.MetricDefinition, "connection lifecycle cumulative counters") {
		t.Fatal("connection counter definition was lost")
	}
}
