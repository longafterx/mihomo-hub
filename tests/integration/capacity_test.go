//go:build integration

package integration_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/storage"
)

// Opt-in capacity assessment, not a timing assertion in ordinary CI.
func TestCapacity10x1000(t *testing.T) {
	if os.Getenv("MIHOMOHUB_CAPACITY") != "1" {
		t.Skip("set MIHOMOHUB_CAPACITY=1 for the paced capacity assessment")
	}
	seconds := 11
	if raw := os.Getenv("MIHOMOHUB_CAPACITY_SECONDS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 11 || value > 600 {
			t.Fatal("capacity seconds must be 11..600")
		}
		seconds = value
	}
	mixed := os.Getenv("MIHOMOHUB_MIXED_LOAD") == "1"
	var maxQuery time.Duration
	h := setupConfig(t, func(c *config.Config) { c.Gateway.MaxWSMessageBytes = 16 << 20 })
	fakes := []*snapshotController{}
	ids := []string{}
	set := func(f *snapshotController, tick int) {
		conns := []map[string]any{}
		for j := range 1000 {
			conns = append(conns, map[string]any{"id": fmt.Sprintf("conn-%d", j), "upload": tick * 128, "download": tick * 256, "chains": []string{"node"}, "rule": "Match", "metadata": map[string]string{"host": fmt.Sprintf("host-%d.example", j), "process": "fixture", "sourceIP": "192.168.1.2"}})
		}
		data, err := json.Marshal(map[string]any{"uploadTotal": tick * 128000, "downloadTotal": tick * 256000, "connections": conns})
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.data = data
		f.mu.Unlock()
	}
	for i := range 10 {
		f := newControllerInterval(t, time.Second)
		set(f, 1)
		fakes = append(fakes, f)
		id := h.register(f.server.URL, "collector-fixture", fmt.Sprintf("capacity-%d", i))
		ids = append(ids, id)
	}
	eventually(t, "ten initial snapshots", func() bool {
		for _, id := range ids {
			if status(h, id) != "online" {
				return false
			}
		}
		return true
	})
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	var peakHeap uint64
	maxQueue := 0
	maxLag := int64(0)
	var maxWrite float64
	var final storage.WriterMetrics
	for tick := 2; tick <= seconds+1; tick++ {
		for _, f := range fakes {
			set(f, tick)
		}
		if mixed {
			// 写入同时执行有界历史查询，验证读池与 WAL 不会阻塞正常采集。
			end := time.Now().UTC().Truncate(time.Minute).Add(time.Minute)
			query := url.Values{"from": {end.Add(-time.Hour).Format(time.RFC3339)}, "to": {end.Format(time.RFC3339)}, "step": {"1m"}}
			for _, kind := range []string{"summary", "top", "connections"} {
				values := query.Encode()
				if kind == "top" {
					values += "&dimension=host&limit=20"
				}
				at := time.Now()
				response, _ := h.request("GET", "/api/v1/analytics/"+kind+"?"+values, "", true)
				requireStatus(t, response, 200)
				maxQuery = max(maxQuery, time.Since(at))
			}
		}
		deadline := started.Add(time.Duration(tick-1) * time.Second)
		for time.Now().Before(deadline) {
			_, data := h.request("GET", "/api/v1/system/status", "", true)
			var out struct {
				Data struct {
					Writer     storage.WriterMetrics `json:"writer"`
					Collectors map[string]struct {
						Lag int64 `json:"collector_lag_ms"`
					} `json:"collectors"`
				} `json:"data"`
			}
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			final = out.Data.Writer
			maxQueue = max(maxQueue, final.QueueLength)
			maxWrite = max(maxWrite, final.LastWriteMS)
			for _, c := range out.Data.Collectors {
				maxLag = max(maxLag, c.Lag)
			}
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			peakHeap = max(peakHeap, mem.HeapAlloc)
			time.Sleep(50 * time.Millisecond)
		}
	}
	var minSamples uint64 = ^uint64(0)
	for _, id := range ids {
		_, data := h.request("GET", "/api/v1/instances/"+id+"/status", "", true)
		var out struct {
			Data struct {
				Samples uint64 `json:"samples_committed"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		minSamples = min(minSamples, out.Data.Samples)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	info, err := os.Stat(h.config.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(h.config.Database.Path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("instances=10 connections_each=1000 mixed=%t max_query_ms=%d elapsed=%s min_samples=%d peak_heap_mib=%.1f allocations_mib=%.1f max_queue=%d dropped=%d failures=%d max_commit_ms=%.1f max_lag_ms=%d db_and_wal_mib=%.1f pool_wait_ms=%d", mixed, maxQuery.Milliseconds(), time.Since(started).Round(time.Millisecond), minSamples, float64(peakHeap)/(1<<20), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20), maxQueue, final.DroppedSamples, final.WriteFailures, maxWrite, maxLag, float64(info.Size()+wal.Size())/(1<<20), h.app.DB.Stats().WaitDuration.Milliseconds())
	if final.WriteFailures != 0 || final.DroppedSamples != 0 {
		t.Fatal("capacity run encountered failed or dropped writes")
	}
}
