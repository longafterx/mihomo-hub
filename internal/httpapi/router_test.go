package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/httpapi"
)

func TestProtectedRoutesRequireSession(t *testing.T) {
	t.Parallel()
	// 不提供业务依赖，确保缺失或伪造的会话在进入具体处理器前被拦截。
	handler := httpapi.New(httpapi.Dependencies{
		Config: config.Default(),
		Auth:   auth.New(t.Context(), nil, time.Hour),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for _, tc := range []struct {
		name, method, path string
	}{
		{name: "session", method: "GET", path: "/api/v1/auth/me"},
		{name: "logout", method: "POST", path: "/api/v1/auth/logout"},
		{name: "metadata", method: "GET", path: "/api/v1/meta"},
		{name: "diagnostics", method: "GET", path: "/api/v1/system/status"},
		{name: "summary", method: "GET", path: "/api/v1/analytics/summary"},
		{name: "traffic", method: "GET", path: "/api/v1/analytics/traffic"},
		{name: "top", method: "GET", path: "/api/v1/analytics/top?dimension=host"},
		{name: "connections", method: "GET", path: "/api/v1/analytics/connections"},
		{name: "gaps", method: "GET", path: "/api/v1/analytics/gaps"},
		{name: "coverage", method: "GET", path: "/api/v1/analytics/coverage"},
		{name: "instances", method: "GET", path: "/api/v1/instances"},
		{name: "retained", method: "GET", path: "/api/v1/instances/retained"},
		{name: "create", method: "POST", path: "/api/v1/instances"},
		{name: "test draft", method: "POST", path: "/api/v1/instances/test"},
		{name: "instance", method: "GET", path: "/api/v1/instances/example"},
		{name: "edit config", method: "GET", path: "/api/v1/instances/example/config"},
		{name: "runtime config", method: "GET", path: "/api/v1/instances/example/runtime-config"},
		{name: "import YAML", method: "PUT", path: "/api/v1/instances/example/runtime-config"},
		{name: "apply DNS", method: "PATCH", path: "/api/v1/instances/example/runtime-config/dns"},
		{name: "instance status", method: "GET", path: "/api/v1/instances/example/status"},
		{name: "probe", method: "POST", path: "/api/v1/instances/example/test"},
		{name: "update", method: "PATCH", path: "/api/v1/instances/example"},
		{name: "delete", method: "DELETE", path: "/api/v1/instances/example"},
		{name: "gateway root", method: "GET", path: "/mihomo/example"},
		{name: "gateway read", method: "GET", path: "/mihomo/example/version"},
		{name: "gateway write", method: "PATCH", path: "/mihomo/example/configs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, cookie := range []string{"", "forged-session"} {
				req := httptest.NewRequest(tc.method, tc.path, nil)
				if cookie != "" {
					req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, req)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("cookie=%q: got %d, want 401: %s", cookie, response.Code, response.Body)
				}
			}
		})
	}
}

func TestRouteFallbackSeparatesAPIAndFrontend(t *testing.T) {
	t.Parallel()
	handler := httpapi.New(httpapi.Dependencies{
		Config: config.Default(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Static: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Static", "true")
			w.WriteHeader(http.StatusOK)
		}),
	})
	for _, tc := range []struct {
		name, path string
		status     int
		static     bool
		jsonError  bool
	}{
		{name: "platform error", path: "/api/v1/missing", status: 404, jsonError: true},
		{name: "legacy API", path: "/api/control/info", status: 404},
		{name: "gateway namespace", path: "/mihomo", status: 404},
		{name: "trailing slash", path: "/api/v1/instances/", status: 404, jsonError: true},
		{name: "path case", path: "/api/v1/Instances", status: 404, jsonError: true},
		{name: "asset", path: "/assets/app.js", status: 200, static: true},
		{name: "frontend route", path: "/proxies", status: 200, static: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("got %d, want %d: %s", response.Code, tc.status, response.Body)
			}
			if got := response.Header().Get("X-Static") == "true"; got != tc.static {
				t.Fatalf("static fallback=%v, want %v", got, tc.static)
			}
			if tc.jsonError {
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Code != "not_found" {
					t.Fatalf("unexpected error: %+v", body.Error)
				}
			}
		})
	}
}
