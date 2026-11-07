package app_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/app"
	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/config"
)

func TestFirstRunSetupAndRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "hub.db")
	cfg.Security.MasterKeyFile = filepath.Join(dir, "secrets", "master.key")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.Open(t.Context(), cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if a != nil {
			a.Close()
		}
	})
	request := func(method, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, cfg.Server.PublicOrigin+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		a.Handler.ServeHTTP(w, r)
		return w
	}
	check := func(t *testing.T, w *httptest.ResponseRecorder, status int, content string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), content) {
			t.Fatalf("status=%d, want=%d: %s", w.Code, status, w.Body)
		}
	}
	check(t, request("GET", "/readyz", "", "", nil), 200, "ready")
	check(t, request("GET", "/api/v1/auth/setup", "", "", nil), 200, `"setup_required":true`)
	w := request("GET", "/", "", "", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("first visit did not redirect to password setup: %d %s", w.Code, w.Header())
	}
	check(t, request("GET", "/api/v1/instances", "", "", nil), 401, "unauthorized")
	check(t, request("GET", "/mihomo/missing/version", "", "", nil), 401, "")
	check(t, request("POST", "/api/v1/auth/login", `{"password":"12345678"}`, cfg.Server.PublicOrigin, nil), 409, "setup_required")
	valid := `{"password":"12345678","confirm_password":"12345678"}`
	for _, tc := range []struct {
		name, body, origin, code string
		status                   int
	}{
		{"mismatch", `{"password":"12345678","confirm_password":"87654321"}`, cfg.Server.PublicOrigin, "invalid_password", 400},
		{"short", `{"password":"123","confirm_password":"123"}`, cfg.Server.PublicOrigin, "invalid_password", 400},
		{"missing confirmation", `{"password":"12345678"}`, cfg.Server.PublicOrigin, "invalid_password", 400},
		{"unknown field", `{"password":"12345678","confirm_password":"12345678","username":"x"}`, cfg.Server.PublicOrigin, "invalid_json", 400},
		{"cross origin", valid, "https://outside.invalid", "origin_rejected", 403},
		{"missing origin", valid, "", "origin_rejected", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check(t, request("POST", "/api/v1/auth/setup", tc.body, tc.origin, nil), tc.status, tc.code)
			check(t, request("GET", "/api/v1/auth/setup", "", "", nil), 200, `"setup_required":true`)
		})
	}
	w = request("POST", "/api/v1/auth/setup", valid, cfg.Server.PublicOrigin, nil)
	check(t, w, 200, "csrf_token")
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe setup session cookie: %+v", cookies)
	}
	check(t, request("GET", "/api/v1/auth/me", "", "", cookies[0]), 200, "csrf_token")
	check(t, request("GET", "/api/v1/auth/setup", "", "", nil), 200, `"setup_required":false`)
	check(t, request("POST", "/api/v1/auth/setup", valid, cfg.Server.PublicOrigin, nil), 409, "already_initialized")
	var storedHash string
	if err := a.DB.QueryRow("SELECT password_hash FROM admin WHERE id=1").Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == "12345678" || !auth.VerifyPassword(storedHash, "12345678") {
		t.Fatal("password was not stored as a usable hash")
	}
	key, err := os.ReadFile(cfg.Security.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = app.Open(t.Context(), cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	check(t, request("GET", "/api/v1/auth/setup", "", "", nil), 200, `"setup_required":false`)
	check(t, request("GET", "/api/v1/auth/me", "", "", cookies[0]), 401, "unauthorized")
	check(t, request("POST", "/api/v1/auth/login", `{"password":"12345678"}`, cfg.Server.PublicOrigin, nil), 200, "csrf_token")
	after, err := os.ReadFile(cfg.Security.MasterKeyFile)
	if err != nil || !bytes.Equal(key, after) {
		t.Fatal("restart replaced the master key")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.Security.MasterKeyFile); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := app.Open(t.Context(), cfg, logger); err == nil {
		unexpected.Close()
		t.Fatal("existing database started with a replacement key")
	}
}

func TestConcurrentSetupRequestsHaveOneWinner(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	cfg.Database.Path = filepath.Join(dir, "hub.db")
	cfg.Security.MasterKeyFile = filepath.Join(dir, "master.key")
	a, err := app.Open(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	type result struct {
		password string
		response *httptest.ResponseRecorder
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for _, password := range []string{"first-browser", "second-browser"} {
		workers.Go(func() {
			<-start
			body := `{"password":"` + password + `","confirm_password":"` + password + `"}`
			r := httptest.NewRequest("POST", cfg.Server.PublicOrigin+"/api/v1/auth/setup", strings.NewReader(body))
			r.Header.Set("Origin", cfg.Server.PublicOrigin)
			w := httptest.NewRecorder()
			a.Handler.ServeHTTP(w, r)
			results <- result{password: password, response: w}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	var winner string
	for result := range results {
		switch result.response.Code {
		case http.StatusOK:
			if winner != "" || len(result.response.Result().Cookies()) != 1 {
				t.Fatal("concurrent setup produced more than one authenticated winner")
			}
			winner = result.password
		case http.StatusConflict:
			if len(result.response.Result().Cookies()) != 0 {
				t.Fatal("losing setup request created a session")
			}
		default:
			t.Fatalf("unexpected concurrent setup response: %d %s", result.response.Code, result.response.Body)
		}
	}
	var hash string
	if err := a.DB.QueryRow("SELECT password_hash FROM admin WHERE id=1").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if winner == "" || !auth.VerifyPassword(hash, winner) {
		t.Fatal("stored password does not match the sole successful request")
	}
}

func TestFirstRunSetupRateLimit(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	cfg.Database.Path = filepath.Join(dir, "hub.db")
	cfg.Security.MasterKeyFile = filepath.Join(dir, "master.key")
	a, err := app.Open(t.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for attempt := range 11 {
		r := httptest.NewRequest("POST", cfg.Server.PublicOrigin+"/api/v1/auth/setup", strings.NewReader(`{}`))
		r.Header.Set("Origin", cfg.Server.PublicOrigin)
		w := httptest.NewRecorder()
		a.Handler.ServeHTTP(w, r)
		want := http.StatusBadRequest
		if attempt == 10 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("attempt %d: got %d, want %d", attempt, w.Code, want)
		}
	}
}
