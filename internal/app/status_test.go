package app_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/app"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/storage"
)

func TestSystemStatusIncludesDatabaseFiles(t *testing.T) {
	t.Parallel()
	cfg := config.Default()
	dir := t.TempDir()
	cfg.Database.Path = filepath.Join(dir, "hub.db")
	cfg.Security.MasterKeyFile = filepath.Join(dir, "master.key")
	a, err := app.Open(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	req := httptest.NewRequest("POST", cfg.Server.PublicOrigin+"/api/v1/auth/setup", strings.NewReader(
		`{"password":"test-password","confirm_password":"test-password"}`,
	))
	req.Header.Set("Origin", cfg.Server.PublicOrigin)
	setup := httptest.NewRecorder()
	a.Handler.ServeHTTP(setup, req)
	if setup.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", setup.Code, setup.Body)
	}

	// 验证对外诊断结果包含主库和实际存在的 WAL/共享内存文件，不依赖回调内部结构。
	var expectedBytes int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(cfg.Database.Path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		expectedBytes += info.Size()
	}
	req = httptest.NewRequest("GET", cfg.Server.PublicOrigin+"/api/v1/system/status", nil)
	for _, cookie := range setup.Result().Cookies() {
		req.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	a.Handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status: %d %s", response.Code, response.Body)
	}
	var result struct {
		Data struct {
			Version       string                     `json:"version"`
			SchemaVersion int                        `json:"schema_version"`
			DatabaseBytes int64                      `json:"database_bytes"`
			Writer        storage.WriterMetrics      `json:"writer"`
			Collectors    map[string]json.RawMessage `json:"collectors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	status := result.Data
	if status.Version != app.Version || status.SchemaVersion != storage.SchemaVersion {
		t.Fatalf("unexpected versions: %+v", status)
	}
	if expectedBytes == 0 || status.DatabaseBytes != expectedBytes {
		t.Fatalf("database bytes=%d, want %d", status.DatabaseBytes, expectedBytes)
	}
	if status.Writer.QueueCapacity != cfg.Collector.QueueCapacity || status.Writer.WriteFailures != 0 {
		t.Fatalf("unexpected writer metrics: %+v", status.Writer)
	}
	if status.Collectors == nil || len(status.Collectors) != 0 {
		t.Fatalf("expected empty collector object: %s", response.Body)
	}
}
