package telemetry_test

import (
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/telemetry"
)

func ptr(n int64) *int64 { return &n }
func snapshot(u, d, cu, cd int64) telemetry.Snapshot {
	c := telemetry.Connection{ID: "shared-id", Upload: ptr(cu), Download: ptr(cd), Start: "2026-11-06T00:00:00Z", Chains: []string{"DIRECT"}}
	c.Metadata.Host = "example.test"
	c.Metadata.Process = "curl"
	c.Metadata.SourceIP = "192.168.1.2"
	c.Rule = "Match"
	return telemetry.Snapshot{Upload: ptr(u), Download: ptr(d), Connections: []telemetry.Connection{c}}
}

func TestDecodeRejectsMissingAndInvalidCounters(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, json string
		ok         bool
	}{
		{name: "null empty list", json: `{"uploadTotal":0,"downloadTotal":0,"connections":null}`, ok: true},
		{name: "missing total", json: `{"downloadTotal":0,"connections":[]}`},
		{name: "missing connections", json: `{"uploadTotal":0,"downloadTotal":0}`},
		{name: "negative", json: `{"uploadTotal":-1,"downloadTotal":0,"connections":[]}`},
		{name: "counter overflow", json: `{"uploadTotal":9223372036854775808,"downloadTotal":0,"connections":[]}`},
		{name: "missing connection counter", json: `{"uploadTotal":0,"downloadTotal":0,"connections":[{"id":"x","upload":1}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := telemetry.Decode([]byte(tt.json), 100)
			if (err == nil) != tt.ok {
				t.Fatalf("decode=%v", err)
			}
		})
	}
}

func TestDeltaBaselineDuplicatesAndShortConnections(t *testing.T) {
	t.Parallel()
	e := telemetry.New(telemetry.Cursor{InstanceID: "a"}, 5*time.Second)
	b, next, err := e.Plan(snapshot(100, 200, 10, 20), 1000, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Cursor.At != 0 || b.Minutes[0].Upload != 0 || b.Minutes[0].AttributedUpload != 0 {
		t.Fatal("baseline advanced or counted historical bytes")
	}
	e = next
	b, next, err = e.Plan(snapshot(140, 260, 20, 40), 2000, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	m := b.Minutes[len(b.Minutes)-1]
	if m.Upload != 40 || m.Download != 60 || m.AttributedUpload != 10 || m.AttributedDownload != 20 {
		t.Fatalf("delta=%+v", m)
	}
	if len(b.Dimensions) != 5 {
		t.Fatal("dimensions missing")
	}
	e = next
	if _, _, err := e.Plan(snapshot(140, 260, 20, 40), 2000, "source", ""); err == nil {
		t.Fatal("same-time duplicate accepted")
	}
	b, next, err = e.Plan(snapshot(140, 260, 20, 40), 3000, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Minutes[0].Upload != 0 || b.Minutes[0].AttributedUpload != 0 {
		t.Fatal("duplicate counters double counted")
	}
	s := snapshot(200, 300, 0, 0)
	s.Connections = []telemetry.Connection{}
	b, _, err = next.Plan(s, 4000, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Minutes[0].Upload != 60 || b.Minutes[0].AttributedUpload != 0 || len(b.Records) != 1 || b.Records[0].ClosedAt == nil {
		t.Fatal("short-connection total or lifecycle semantics incorrect")
	}
}

func TestResetRecoveryAndEpochIsolation(t *testing.T) {
	t.Parallel()
	first := telemetry.New(telemetry.Cursor{InstanceID: "a"}, 5*time.Second)
	_, e, err := first.Plan(snapshot(100, 200, 10, 20), 1000, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name                string
		u, d, at            int64
		source, boundary    string
		newEpoch, candidate bool
	}{
		{name: "counter reset", u: 50, d: 100, at: 2000, source: "source", newEpoch: true},
		{name: "reconnect", u: 200, d: 300, at: 2000, source: "source", boundary: "reconnect", candidate: true},
		{name: "three minute gap", u: 200, d: 300, at: 181000, source: "source", candidate: true},
		{name: "controller replacement", u: 200, d: 300, at: 2000, source: "other", newEpoch: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, next, err := e.Plan(snapshot(tt.u, tt.d, 100, 100), tt.at, tt.source, tt.boundary)
			if err != nil {
				t.Fatal(err)
			}
			if (next.Cursor.EpochID != e.Cursor.EpochID) != tt.newEpoch {
				t.Fatal("generation confused with kernel epoch")
			}
			if len(b.Gaps) != 1 || (b.Gaps[0].CandidateUpload != nil) != tt.candidate {
				t.Fatalf("gap=%+v", b.Gaps)
			}
			for _, m := range b.Minutes {
				if m.Upload != 0 || m.AttributedUpload != 0 {
					t.Fatal("recovery manufactured a minute spike")
				}
			}
		})
	}
	restored := telemetry.New(e.Cursor, 5*time.Second)
	b, _, err := restored.Plan(snapshot(300, 400, 100, 100), 4000, "source", "go_restart")
	if err != nil {
		t.Fatal(err)
	}
	if *b.Gaps[0].CandidateUpload != 200 || b.Minutes[0].Upload != 0 {
		t.Fatal("persistent cursor recovery lost or counted uncertain bytes")
	}
}

func TestConnectionReuseCoverageAndConsistency(t *testing.T) {
	t.Parallel()
	e := telemetry.New(telemetry.Cursor{InstanceID: "a"}, 5*time.Second)
	_, e, err := e.Plan(snapshot(100, 200, 10, 20), 59500, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	b, next, err := e.Plan(snapshot(101, 201, 100, 100), 60500, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Minutes) != 2 || b.Minutes[0].ObservedMS != 500 || b.Minutes[1].ObservedMS != 500 {
		t.Fatal("UTC bucket boundary lost coverage")
	}
	if !b.Minutes[1].Gap || b.Gaps[0].Reason != "attribution_exceeds_total" {
		t.Fatal("inconsistent attribution silently clipped")
	}
	b, _, err = next.Plan(snapshot(110, 210, 1, 1), 61500, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Records) != 2 || b.Records[0].LifecycleID == b.Records[1].LifecycleID {
		t.Fatal("reused connection ID overwrote a lifecycle")
	}
}

func TestSharedDimensionsCountEachConnectionExactlyOnce(t *testing.T) {
	t.Parallel()
	e := telemetry.New(telemetry.Cursor{InstanceID: "one"}, 5*time.Second)
	s := snapshot(100, 200, 10, 20)
	other := s.Connections[0]
	other.ID = "other"
	s.Connections = append(s.Connections, other)
	_, e, err := e.Plan(s, 1000, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	s = snapshot(140, 280, 20, 40)
	other = s.Connections[0]
	other.ID = "other"
	s.Connections = append(s.Connections, other)
	b, _, err := e.Plan(s, 2000, "source", "")
	if err != nil {
		t.Fatal(err)
	}
	totals := map[string]int64{}
	for _, d := range b.Dimensions {
		totals[d.Name] += d.Upload
	}
	for _, name := range []string{"host", "proxy", "rule", "process", "src_ip"} {
		if totals[name] != 20 {
			t.Fatal(name, totals)
		}
	}
}
