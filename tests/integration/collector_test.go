//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/longafterx/mihomo-hub/internal/app"
	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/config"
)

type snapshotController struct {
	server  *httptest.Server
	mu      sync.Mutex
	data    []byte
	offline atomic.Bool
	active  atomic.Int64
	starts  atomic.Uint64
}

func newController(t *testing.T) *snapshotController {
	return newControllerInterval(t, 20*time.Millisecond)
}
func newControllerInterval(t *testing.T, interval time.Duration) *snapshotController {
	t.Helper()
	f := &snapshotController{}
	f.set(100, 50)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" || r.Header.Get("Authorization") != "Bearer collector-fixture" || r.URL.Query().Get("token") != "collector-fixture" {
			http.Error(w, "rejected", 401)
			return
		}
		if f.offline.Load() {
			http.Error(w, "offline", 503)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		f.starts.Add(1)
		f.active.Add(1)
		defer f.active.Add(-1)
		ctx := conn.CloseRead(r.Context())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if f.offline.Load() {
				return
			}
			f.mu.Lock()
			data := bytes.Clone(f.data)
			f.mu.Unlock()
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *snapshotController) set(total, connection int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = []byte(fmt.Sprintf(`{"uploadTotal":%d,"downloadTotal":%d,"connections":[{"id":"same-id","upload":%d,"download":%d,"start":"2026-11-01T00:00:00Z","chains":["fixture-node"],"rule":"Match","metadata":{"host":"fixture.example","process":"fixture","sourceIP":"192.168.1.2"}}]}`, total, total*2, connection, connection*2))
}
func eventually(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out: " + description)
}
func counter(t *testing.T, h *harness, id string) int64 {
	t.Helper()
	var n int64
	if err := h.app.DB.QueryRow("SELECT COALESCE(SUM(total_upload),0) FROM traffic_minute WHERE instance_id=?", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func status(h *harness, id string) string {
	_, data := h.request("GET", "/api/v1/instances/"+id+"/status", "", true)
	var v struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		h.t.Fatal(err)
	}
	return v.Data.Status
}

func TestCollectorsIndependentRecoveryAndReset(t *testing.T) {
	h := setupConfig(t, func(c *config.Config) {
		c.Collector.FlushIntervalMS = 5
		c.Collector.ReconnectMinMS = 10
		c.Collector.ReconnectMaxMS = 50
	})
	a, b := newController(t), newController(t)
	ids := []string{h.register(a.server.URL, "collector-fixture", "collector-a"), h.register(b.server.URL, "collector-fixture", "collector-b")}
	eventually(t, "two background collectors online", func() bool { return status(h, ids[0]) == "online" && status(h, ids[1]) == "online" })
	// No browser or browser WebSocket is created anywhere in this test.
	a.set(120, 60)
	b.set(140, 70)
	eventually(t, "isolated deltas", func() bool { return counter(t, h, ids[0]) == 20 && counter(t, h, ids[1]) == 40 })
	a.offline.Store(true)
	eventually(t, "first collector disconnected", func() bool { return a.active.Load() == 0 && status(h, ids[0]) == "degraded" })
	b.set(180, 90)
	eventually(t, "second collector unaffected", func() bool { return counter(t, h, ids[1]) == 80 })
	a.set(1000, 500)
	a.offline.Store(false)
	eventually(t, "recovered collector online", func() bool { return status(h, ids[0]) == "online" })
	if got := counter(t, h, ids[0]); got != 20 {
		t.Fatal("recovery spike", got)
	}
	var candidate int64
	if err := h.app.DB.QueryRow("SELECT COALESCE(SUM(candidate_upload_bytes),0) FROM traffic_gap_intervals WHERE instance_id=?", ids[0]).Scan(&candidate); err != nil || candidate != 880 {
		t.Fatal("recovery candidate", candidate, err)
	}
	a.set(1010, 505)
	eventually(t, "post-recovery delta", func() bool { return counter(t, h, ids[0]) == 30 })
	a.set(1, 0)
	eventually(t, "counter reset epoch", func() bool {
		var n int
		if err := h.app.DB.QueryRow("SELECT COUNT(*) FROM instance_epochs WHERE instance_id=?", ids[0]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 2
	})
	if got := counter(t, h, ids[0]); got != 30 {
		t.Fatal("reset spike", got)
	}
	resp, _ := h.request("DELETE", "/api/v1/instances/"+ids[0]+"?history=retain", "", true)
	requireStatus(t, resp, 200)
	eventually(t, "removed worker closed", func() bool { return a.active.Load() == 0 })
	resp, _ = h.request("GET", "/api/v1/instances/"+ids[0]+"/status", "", true)
	requireStatus(t, resp, 404)
	if got := counter(t, h, ids[0]); got != 30 {
		t.Fatalf("retained history = %d", got)
	}
	resp, _ = h.request("DELETE", "/api/v1/instances/"+ids[1]+"?history=retain", "", true)
	requireStatus(t, resp, 200)
	eventually(t, "deleted worker closed", func() bool { return b.active.Load() == 0 })
}

func TestCollectorProcessRestartAndURLGeneration(t *testing.T) {
	h := setupConfig(t, func(c *config.Config) {
		c.Collector.FlushIntervalMS = 5
		c.Collector.ReconnectMinMS = 10
		c.Collector.ReconnectMaxMS = 50
	})
	a, b := newController(t), newController(t)
	id := h.register(a.server.URL, "collector-fixture", "restart-test")
	eventually(t, "baseline", func() bool { return status(h, id) == "online" })
	a.set(120, 60)
	eventually(t, "delta", func() bool { return counter(t, h, id) == 20 })
	h.server.Close()
	if err := h.app.Close(); err != nil {
		t.Fatal(err)
	}
	a.set(900, 450)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.config.Server.PublicOrigin = "http://" + listener.Addr().String()
	h.app, err = app.Open(t.Context(), h.config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	h.server = httptest.NewUnstartedServer(h.app.Handler)
	h.server.Listener = listener
	h.server.Start()
	eventually(t, "restart baseline restored", func() bool {
		var total int64
		err := h.app.DB.QueryRow("SELECT upload_total FROM collector_cursor WHERE instance_id=?", id).Scan(&total)
		return err == nil && total == 900
	})
	if got := counter(t, h, id); got != 20 {
		t.Fatal("process restart spike", got)
	}
	resp, data := h.request("POST", "/api/v1/auth/login", `{"password":"`+password+`"}`, false)
	requireStatus(t, resp, 200)
	for _, cookie := range resp.Cookies() {
		if cookie.Name == auth.CookieName {
			h.cookie = cookie
		}
	}
	var login struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &login); err != nil {
		t.Fatal(err)
	}
	h.csrf = login.Data.CSRF
	body, err := json.Marshal(map[string]string{"controller_url": b.server.URL})
	if err != nil {
		t.Fatal(err)
	}
	resp, _ = h.request("PATCH", "/api/v1/instances/"+id, string(body), true)
	requireStatus(t, resp, 200)
	eventually(t, "new URL generation online", func() bool { return a.active.Load() == 0 && b.active.Load() == 1 && status(h, id) == "online" })
	if got := counter(t, h, id); got != 20 {
		t.Fatal("controller change spike", got)
	}
	previousStarts := b.starts.Load()
	// 元数据保存失败不能打断已经在线的采集器，也不能把正常流量变成恢复候选。
	resp, _ = h.request("PATCH", "/api/v1/instances/"+id, `{"name":"mock-0"}`, true)
	requireStatus(t, resp, 409)
	b.set(120, 60)
	eventually(t, "worker survives failed update", func() bool {
		return status(h, id) == "online" && counter(t, h, id) == 40
	})
	if b.starts.Load() != previousStarts {
		t.Fatal("failed metadata update restarted collector")
	}
	resp, _ = h.request("PATCH", "/api/v1/instances/"+id, `{"name":"renamed-collector"}`, true)
	requireStatus(t, resp, 200)
	b.set(140, 70)
	eventually(t, "rename preserves traffic delta", func() bool { return counter(t, h, id) == 60 })
	if b.starts.Load() != previousStarts {
		t.Fatal("rename restarted collector")
	}
	resp, _ = h.request("PATCH", "/api/v1/instances/"+id, `{"secret":"invalid-fixture"}`, true)
	requireStatus(t, resp, 200)
	eventually(t, "secret rotation closes old socket", func() bool { return b.active.Load() == 0 && status(h, id) == "degraded" })
	resp, _ = h.request("PATCH", "/api/v1/instances/"+id, `{"secret":"collector-fixture"}`, true)
	requireStatus(t, resp, 200)
	eventually(t, "valid rotated secret reconnects", func() bool { return b.active.Load() == 1 && status(h, id) == "online" })
}

func TestAnalyticsAuthValidationAndCollectorDiagnostics(t *testing.T) {
	h := setup(t)
	for _, kind := range []string{"summary", "traffic", "top?dimension=host", "connections", "gaps", "coverage"} {
		path := "/api/v1/analytics/" + kind
		resp, _ := h.request("GET", path, "", false)
		requireStatus(t, resp, 401)
		resp, data := h.request("GET", path, "", true)
		requireStatus(t, resp, 200)
		if !bytes.Contains(data, []byte(`"metric_definition"`)) || !bytes.Contains(data, []byte(`"has_gap"`)) {
			t.Fatal("missing statistics semantics")
		}
	}
	for _, query := range []string{"dimension=host%3BDROP+TABLE+instances", "dimension=host&limit=10000", "dimension=host&step=1s", "dimension=host&instance_ids=missing"} {
		resp, _ := h.request("GET", "/api/v1/analytics/top?"+query, "", true)
		requireStatus(t, resp, 400)
	}
	resp, _ := h.request("GET", "/api/v1/system/status", "", false)
	requireStatus(t, resp, 401)
	resp, data := h.request("GET", "/api/v1/system/status", "", true)
	requireStatus(t, resp, 200)
	if !bytes.Contains(data, []byte(`"queue_capacity"`)) || !bytes.Contains(data, []byte(`"write_failures"`)) {
		t.Fatal("missing writer diagnostics")
	}

}

func TestCollectorRejectsInvalidAndOversizedSnapshots(t *testing.T) {
	for _, tt := range []struct{ name, body, code string }{
		{"missing total", `{"downloadTotal":1,"connections":[]}`, "invalid_snapshot"},
		{"oversized", `{"padding":"` + strings.Repeat("x", 3000) + `"}`, "stream_interrupted"},
		{"connection cap", `{"uploadTotal":10,"downloadTotal":10,"connections":[{"id":"a","upload":1,"download":1},{"id":"b","upload":1,"download":1}]}`, "invalid_snapshot"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := setupConfig(t, func(c *config.Config) {
				c.Collector.MaxConnections = 1
				c.Collector.FlushIntervalMS = 5
				c.Collector.ReconnectMinMS = 50
				c.Collector.ReconnectMaxMS = 100
			})
			f := newController(t)
			f.mu.Lock()
			f.data = []byte(tt.body)
			f.mu.Unlock()
			id := h.register(f.server.URL, "collector-fixture", tt.name)
			eventually(t, "invalid protocol rejected", func() bool {
				_, data := h.request("GET", "/api/v1/instances/"+id+"/status", "", true)
				var v struct {
					Data struct {
						Code string `json:"last_error_code"`
					} `json:"data"`
				}
				if err := json.Unmarshal(data, &v); err != nil {
					t.Fatal(err)
				}
				return v.Data.Code == tt.code
			})
			if counter(t, h, id) != 0 {
				t.Fatal("invalid snapshot counted")
			}
			f.set(100, 50)
			eventually(t, "valid snapshot recovers", func() bool { return status(h, id) == "online" })
		})
	}
}

func TestCollectorStorageFailureReportsRecoveryGap(t *testing.T) {
	h := setupConfig(t, func(c *config.Config) {
		c.Collector.FlushIntervalMS = 5
		c.Collector.ReconnectMinMS = 100
		c.Collector.ReconnectMaxMS = 100
	})
	f := newController(t)
	id := h.register(f.server.URL, "collector-fixture", "write-failure")
	eventually(t, "baseline online", func() bool { return status(h, id) == "online" })
	if _, err := h.app.DB.Exec(`CREATE TRIGGER fail_telemetry BEFORE UPDATE ON collector_cursor BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	f.set(1000, 500)
	eventually(t, "write failure observable", func() bool {
		_, data := h.request("GET", "/api/v1/system/status", "", true)
		var v struct {
			Data struct {
				Writer struct {
					Failures int `json:"write_failures"`
				} `json:"writer"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		return v.Data.Writer.Failures > 0
	})
	if _, err := h.app.DB.Exec("DROP TRIGGER fail_telemetry"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "storage recovery gap persisted", func() bool {
		var n int
		if err := h.app.DB.QueryRow("SELECT COUNT(*) FROM traffic_gap_intervals WHERE instance_id=? AND reason='storage_error'", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	})
	if n := counter(t, h, id); n != 0 {
		t.Fatal("failed write recovery spike", n)
	}
}
