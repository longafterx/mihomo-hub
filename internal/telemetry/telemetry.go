package telemetry

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Connection struct {
	ID          string   `json:"id"`
	Upload      *int64   `json:"upload"`
	Download    *int64   `json:"download"`
	Start       string   `json:"start"`
	Chains      []string `json:"chains"`
	Rule        string   `json:"rule"`
	RulePayload string   `json:"rulePayload"`
	Metadata    struct {
		Host     string `json:"host"`
		Process  string `json:"process"`
		SourceIP string `json:"sourceIP"`
	} `json:"metadata"`
}

type Snapshot struct {
	Upload      *int64       `json:"uploadTotal"`
	Download    *int64       `json:"downloadTotal"`
	Connections []Connection `json:"connections"`
}

func Decode(data []byte, maxConnections int) (Snapshot, error) {
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return s, errors.New("invalid snapshot JSON")
	}
	validTotals := s.Upload != nil && s.Download != nil
	if !validTotals {
		return s, errors.New("missing cumulative totals")
	}
	if *s.Upload < 0 || *s.Download < 0 {
		return s, errors.New("negative cumulative totals")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return s, err
	}
	if _, ok := fields["connections"]; !ok {
		return s, errors.New("missing connections field")
	}
	if len(s.Connections) > maxConnections {
		return s, errors.New("connection cache limit exceeded")
	}
	seen := map[string]bool{}
	for _, c := range s.Connections {
		validCounters := c.Upload != nil && c.Download != nil
		if c.ID == "" || seen[c.ID] || !validCounters {
			return s, errors.New("invalid or duplicate connection")
		}
		if *c.Upload < 0 || *c.Download < 0 {
			return s, errors.New("negative connection counter")
		}
		seen[c.ID] = true
	}
	return s, nil
}

type Cursor struct {
	InstanceID string
	EpochID    string
	SampleID   string
	SourceID   string
	Upload     int64
	Download   int64
	At         int64
}

type Minute struct {
	Bucket, Upload, Download, AttributedUpload, AttributedDownload, ObservedMS int64
	Gap                                                                        bool
}
type Dimension struct {
	Bucket           int64
	Name, Value      string
	Upload, Download int64
}
type Record struct {
	LifecycleID, EpochID, ConnectionID, Start string
	FirstSeen, LastSeen                       int64
	ClosedAt                                  *int64
	CloseReason                               string
	Host, Proxy, Rule, Process, SourceIP      string
	Upload, Download                          int64
}
type Gap struct {
	ID, EpochID, Reason, Resolution    string
	From, To                           int64
	CandidateUpload, CandidateDownload *int64
}
type Batch struct {
	InstanceID        string
	Generation        int64
	Cursor            *Cursor
	EpochReason       string
	ClosePrevious     bool
	Minutes           []Minute
	Dimensions        []Dimension
	Records           []Record
	Gaps              []Gap
	Status, ErrorCode string
	At                int64
}

type tracked struct {
	conn   Connection
	record Record
}
type Engine struct {
	Cursor      Cursor
	connections map[string]tracked
	maxInterval int64
}

func New(cursor Cursor, maxInterval time.Duration) *Engine {
	return &Engine{Cursor: cursor, connections: map[string]tracked{}, maxInterval: maxInterval.Milliseconds()}
}

func checkedAdd(a, b int64) (int64, error) {
	if b > math.MaxInt64-a {
		return 0, errors.New("counter sum overflow")
	}
	return a + b, nil
}

// Plan never mutates the committed engine. Publish the returned engine only after
// SQLite acknowledges the batch; a failed write therefore cannot advance memory.
func (e *Engine) Plan(s Snapshot, at int64, source, boundary string) (Batch, *Engine, error) {
	if s.Upload == nil || s.Download == nil || *s.Upload < 0 || *s.Download < 0 {
		return Batch{}, nil, errors.New("invalid counters")
	}
	for _, c := range s.Connections {
		if c.Upload == nil || c.Download == nil || *c.Upload < 0 || *c.Download < 0 {
			return Batch{}, nil, errors.New("invalid connection counters")
		}
	}
	if at <= e.Cursor.At {
		return Batch{}, nil, errors.New("out of order snapshot")
	}
	b := Batch{InstanceID: e.Cursor.InstanceID, Status: "online", At: at,
		Minutes: []Minute{}, Dimensions: []Dimension{}, Records: []Record{}, Gaps: []Gap{}}
	c := Cursor{InstanceID: e.Cursor.InstanceID, EpochID: e.Cursor.EpochID, SampleID: uuid.NewString(), SourceID: source,
		Upload: *s.Upload, Download: *s.Download, At: at}
	first := e.Cursor.At == 0
	changed := !first && e.Cursor.SourceID != source
	reset := !first && (c.Upload < e.Cursor.Upload || c.Download < e.Cursor.Download)
	if first || changed || reset {
		c.EpochID = uuid.NewString()
		b.EpochReason = "first_seen"
		if changed {
			b.EpochReason = "controller_changed"
		}
		if reset {
			b.EpochReason = "counter_reset"
		}
	}
	interval := at - e.Cursor.At
	if interval > e.maxInterval && !first && boundary == "" {
		boundary = "sampling_gap"
	}
	baseline := first || changed || reset || boundary != ""
	b.ClosePrevious = baseline
	next := New(c, time.Duration(e.maxInterval)*time.Millisecond)
	minute := Minute{Bucket: at / 60000 * 60000}
	if baseline {
		minute.Gap = true
		reason := boundary
		if b.EpochReason != "" {
			reason = b.EpochReason
		}
		gap := Gap{ID: uuid.NewString(), EpochID: c.EpochID, From: at, To: at, Reason: reason, Resolution: "unknown"}
		if !first {
			gap.From = e.Cursor.At
		}
		if !first && !changed && !reset {
			u, d := c.Upload-e.Cursor.Upload, c.Download-e.Cursor.Download
			gap.CandidateUpload = &u
			gap.CandidateDownload = &d
			gap.Resolution = "uncertain_epoch_and_time"
		}
		b.Gaps = append(b.Gaps, gap)
	} else {
		minute.Upload = c.Upload - e.Cursor.Upload
		minute.Download = c.Download - e.Cursor.Download
	}
	// Coalesce shared dimensions before SQLite: many connections share the
	// same node/rule/process. This reduces per-row execution and driver scheduling.
	type dimensionKey struct{ name, value string }
	dimensions := map[dimensionKey]int{}
	for _, connection := range s.Connections {
		old, exists := e.connections[connection.ID]
		same := exists && old.conn.Start == connection.Start
		if same {
			same = *connection.Upload >= old.record.Upload && *connection.Download >= old.record.Download
		}
		r := old.record
		if baseline || !same {
			if exists && !baseline {
				closed := old.record
				closed.ClosedAt = &at
				closed.CloseReason = "counter_reset_or_id_reuse"
				b.Records = append(b.Records, closed)
			}
			r = Record{
				LifecycleID:  uuid.NewString(),
				EpochID:      c.EpochID,
				ConnectionID: connection.ID,
				Start:        connection.Start,
				FirstSeen:    at,
			}
		}
		u, d := int64(0), int64(0)
		if !baseline {
			u, d = *connection.Upload, *connection.Download
			if same {
				u -= old.record.Upload
				d -= old.record.Download
			}
		}
		var err error
		minute.AttributedUpload, err = checkedAdd(minute.AttributedUpload, u)
		if err != nil {
			return Batch{}, nil, err
		}
		minute.AttributedDownload, err = checkedAdd(minute.AttributedDownload, d)
		if err != nil {
			return Batch{}, nil, err
		}
		r.LastSeen = at
		r.Upload = *connection.Upload
		r.Download = *connection.Download
		// Dimensions are fixed at first observation, preventing later metadata changes
		// from moving already counted bytes between buckets or counting them twice.
		if r.Host == "" {
			r.Host = value(connection.Metadata.Host)
			r.Process = value(connection.Metadata.Process)
			r.SourceIP = value(connection.Metadata.SourceIP)
			r.Rule = value(connection.Rule + ":" + connection.RulePayload)
			r.Proxy = "(unknown)"
			if len(connection.Chains) > 0 {
				r.Proxy = value(connection.Chains[0])
			}
		}
		if u != 0 || d != 0 {
			for name, v := range map[string]string{
				"host":    r.Host,
				"proxy":   r.Proxy,
				"rule":    r.Rule,
				"process": r.Process,
				"src_ip":  r.SourceIP,
			} {
				key := dimensionKey{name, v}
				if index, ok := dimensions[key]; ok {
					b.Dimensions[index].Upload += u
					b.Dimensions[index].Download += d
				} else {
					dimensions[key] = len(b.Dimensions)
					b.Dimensions = append(b.Dimensions, Dimension{Bucket: minute.Bucket, Name: name, Value: v, Upload: u, Download: d})
				}
			}
		}
		b.Records = append(b.Records, r)
		next.connections[connection.ID] = tracked{conn: connection, record: r}
	}
	if !baseline {
		for id, old := range e.connections {
			if _, exists := next.connections[id]; !exists {
				r := old.record
				r.ClosedAt = &at
				r.CloseReason = "disappeared"
				b.Records = append(b.Records, r)
			}
		}
		for from := e.Cursor.At; from < at; {
			bucket := from / 60000 * 60000
			end := min(at, bucket+60000)
			if bucket == minute.Bucket {
				minute.ObservedMS += end - from
			} else {
				b.Minutes = append(b.Minutes, Minute{Bucket: bucket, ObservedMS: end - from})
			}
			from = end
		}
	}
	if minute.AttributedUpload > minute.Upload || minute.AttributedDownload > minute.Download {
		minute.Gap = true
		b.Gaps = append(b.Gaps, Gap{ID: uuid.NewString(), EpochID: c.EpochID, From: e.Cursor.At, To: at,
			Reason: "attribution_exceeds_total", Resolution: "inconsistent_sample"})
	}
	b.Minutes = append(b.Minutes, minute)
	b.Cursor = &c
	return b, next, nil
}

func value(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unknown)"
	}
	return s
}

func SourceID(url string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(url))) }
