//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/longafterx/mihomo-hub/internal/app"
	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/testutil/fakemihomo"
	"github.com/longafterx/mihomo-hub/tests/fixtures"
	"go.uber.org/goleak"
)

const password = "p0-test-only-password"

var passwordHash string

func TestMain(m *testing.M) {
	gin.SetMode(gin.ReleaseMode)
	var err error
	passwordHash, err = auth.HashPassword(password)
	if err != nil {
		panic(err)
	}
	goleak.VerifyTestMain(m)
}

type harness struct {
	t      *testing.T
	app    *app.App
	server *httptest.Server
	fakes  []*fakemihomo.Fake
	ids    []string
	cookie *http.Cookie
	csrf   string
	logs   *lockedBuffer
	config config.Config
}

func setup(t *testing.T) *harness {
	return setupConfig(t, nil)
}

type lockedBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func setupConfig(t *testing.T, customize func(*config.Config)) *harness {
	t.Helper()
	c := config.Default()
	c.Database.Path = filepath.Join(t.TempDir(), "hub.db")
	c.Security.MasterKeyFile = filepath.Join(t.TempDir(), "master.key")
	c.Gateway.MaxWSMessageBytes = 2048
	c.Gateway.MaxWebSockets = 16
	if customize != nil {
		customize(&c)
	}
	if err := storage.InitKey(c.Security.MasterKeyFile); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(t.Context(), c.Database.Path, c.Database.BusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	if err := (storage.Admins{DB: db}).Initialize(t.Context(), auth.Admin{Username: "legacy-admin-name", PasswordHash: passwordHash}); err != nil {
		t.Fatal(err)
	}
	// Gateway 协议夹具直接播种未采集记录，避免后台连接干扰流数量与透传断言。
	// 产品注册只能走 Create；后台采集测试另行通过 HTTP 注册并验证自动启动。
	aead, err := storage.LoadKey(c.Security.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	repo := storage.Instances{DB: db, AEAD: aead}
	fakes := []*fakemihomo.Fake{}
	ids := []string{}
	for i := range 2 {
		fake := fakemihomo.New(fmt.Sprintf("mock-%d", i), fmt.Sprintf("server-secret-%d-+$&", i))
		t.Cleanup(fake.Close)
		item := instance.Instance{ID: uuid.NewString(), Name: fake.Label, ControllerURL: fake.Server.URL,
			Secret: fake.Secret, Enabled: true, Generation: 1}
		if err := repo.Save(t.Context(), item, "fixture.create"); err != nil {
			t.Fatal(err)
		}
		fakes = append(fakes, fake)
		ids = append(ids, item.ID)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c.Server.PublicOrigin = "http://" + listener.Addr().String()
	h := &harness{t: t, ids: ids, fakes: fakes, logs: &lockedBuffer{}, config: c}
	logger := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.app, err = app.Open(t.Context(), c, logger)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	h.server = httptest.NewUnstartedServer(h.app.Handler)
	h.server.Listener = listener
	h.server.Config.ReadTimeout = 250 * time.Millisecond
	h.server.Config.WriteTimeout = 250 * time.Millisecond
	h.server.Start()
	t.Cleanup(func() {
		if err := h.app.Close(); err != nil {
			t.Error(err)
		}
		h.server.Close()
	})
	resp, data := h.request("POST", "/api/v1/auth/login", `{"password":"`+password+`"}`, false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, data)
	}
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
	if h.cookie == nil || h.csrf == "" {
		t.Fatal("missing cookie/CSRF")
	}
	return h
}

func (h *harness) request(method, path, body string, authenticated bool) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("Content-Type", "application/json")
	if authenticated {
		req.AddCookie(h.cookie)
		req.Header.Set("X-CSRF-Token", h.csrf)
	}
	return h.do(req)
}

func (h *harness) do(req *http.Request) (*http.Response, []byte) {
	h.t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp, data
}

func (h *harness) register(target, secret, name string) string {
	h.t.Helper()
	input, err := json.Marshal(map[string]any{"name": name, "controller_url": target, "secret": secret})
	if err != nil {
		h.t.Fatal(err)
	}
	resp, data := h.request("POST", "/api/v1/instances", string(input), true)
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("register: %d %s", resp.StatusCode, data)
	}
	if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte(target)) {
		h.t.Fatal("credential/target leaked")
	}
	var result struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		h.t.Fatal(err)
	}
	return result.Data.ID
}

func (h *harness) ws(id, path, origin string, authenticated bool) (*websocket.Conn, *http.Response, error) {
	headers := http.Header{"Origin": []string{origin}}
	if authenticated {
		headers.Set("Cookie", h.cookie.String())
	}
	ctx, cancel := context.WithTimeout(h.t.Context(), 3*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/mihomo/"+id+path,
		&websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{"mihomo-test"}},
	)
}

func requireStatus(t *testing.T, response *http.Response, want int) {
	t.Helper()
	if response.StatusCode != want {
		t.Fatalf("status = %d, want %d", response.StatusCode, want)
	}
}

func waitInactive(t *testing.T, f *fakemihomo.Fake) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for f.Active.Load() != 0 {
		select {
		case <-ctx.Done():
			t.Fatalf("leaked upstream streams: %d", f.Active.Load())
		case <-ticker.C:
		}
	}
}

func TestRegistryAndRESTContract(t *testing.T) {
	h := setup(t)
	for i, id := range h.ids {
		for _, path := range []string{"version", "configs", "proxies", "connections", "rules"} {
			t.Run(fmt.Sprintf("instance-%d/%s", i, path), func(t *testing.T) {
				resp, data := h.request("GET", "/mihomo/"+id+"/"+path, "", true)
				requireStatus(t, resp, http.StatusOK)
				want, err := fixtures.Files.ReadFile(path + ".json")
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data, want) {
					t.Fatalf("payload changed: %s", data)
				}
				if resp.Header.Get("X-Mock-Instance") != h.fakes[i].Label {
					t.Fatal("instances crossed")
				}
			})
		}
	}
	path := "/proxies/%E4%B8%AD%E6%96%87%2F%25%20name?x=a%2Fb&x=two+words&keep=%2f"
	body := `{"name":"DIRECT"}`
	req, err := http.NewRequest("PUT", h.server.URL+"/mihomo/"+h.ids[0]+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(h.cookie)
	req.Header.Set("Origin", h.server.URL)
	req.Header.Set("Authorization", "Bearer browser-forgery")
	req.Header.Set("Forwarded", "host=metadata")
	req.Header.Set("X-Forwarded-Host", "metadata")
	req.Header.Set("X-Forwarded-For", "169.254.169.254")
	resp, _ := h.do(req)
	requireStatus(t, resp, http.StatusNoContent)
	var found bool
	for len(h.fakes[0].Observations) > 0 {
		observation := <-h.fakes[0].Observations
		if observation.Method != "PUT" {
			continue
		}
		found = true
		if observation.URI != path || string(observation.Body) != body {
			t.Fatalf("path/query/body changed: %+v", observation)
		}
		if observation.Header.Get("Authorization") != "Bearer "+h.fakes[0].Secret {
			t.Fatal("wrong upstream secret")
		}
		for _, key := range []string{"Cookie", "Forwarded", "X-Forwarded-Host", "X-Forwarded-For", "Origin", "X-CSRF-Token"} {
			if observation.Header.Get(key) != "" {
				t.Fatalf("forwarded unsafe %s", key)
			}
		}
	}
	if !found {
		t.Fatal("missing upstream write")
	}
	resp, _ = h.request("GET", "/mihomo/"+h.ids[0]+"/providers/proxies/test/healthcheck", "", true)
	requireStatus(t, resp, http.StatusNoContent)
	resp, data := h.request("GET", "/api/v1/instances", "", true)
	requireStatus(t, resp, http.StatusOK)
	for _, f := range h.fakes {
		if bytes.Contains(data, []byte(f.Secret)) {
			t.Fatal("secret leaked in list")
		}
	}
	resp, _ = h.request("PATCH", "/api/v1/instances/"+h.ids[0], `{"enabled":false,"name":"renamed"}`, true)
	requireStatus(t, resp, http.StatusBadRequest)
	resp, _ = h.request("PATCH", "/api/v1/instances/"+h.ids[0], `{"name":"renamed"}`, true)
	requireStatus(t, resp, http.StatusOK)
	resp, _ = h.request("GET", "/mihomo/"+h.ids[0]+"/version", "", true)
	requireStatus(t, resp, http.StatusOK)
	resp, _ = h.request("GET", "/mihomo/"+h.ids[1]+"/version", "", true)
	requireStatus(t, resp, http.StatusOK)
	for i, history := range []string{"retain", "purge"} {
		resp, _ = h.request("DELETE", "/api/v1/instances/"+h.ids[i]+"?history="+history, "", true)
		requireStatus(t, resp, http.StatusOK)
		resp, _ = h.request("GET", "/mihomo/"+h.ids[i]+"/version", "", true)
		requireStatus(t, resp, http.StatusNotFound)
	}
	var count int
	if err := h.app.DB.QueryRow("SELECT COUNT(*) FROM instances WHERE deleted_at_ms IS NOT NULL").Scan(&count); err != nil || count != 1 {
		t.Fatalf("retain tombstone: count=%d err=%v", count, err)
	}
}

func TestAuthenticationAndPermissionBoundary(t *testing.T) {
	h := setup(t)
	for _, path := range []string{"/api/v1/instances", "/mihomo/" + h.ids[0] + "/version"} {
		resp, _ := h.request("GET", path, "", false)
		requireStatus(t, resp, http.StatusUnauthorized)
	}
	for _, tt := range []struct {
		name, method, path, body string
		status                   int
	}{
		{name: "unknown instance", method: "GET", path: "/mihomo/unknown/version", status: 404},
		{name: "dangerous restart", method: "POST", path: "/mihomo/" + h.ids[0] + "/restart", status: 403},
		{name: "dangerous upgrade", method: "POST", path: "/mihomo/" + h.ids[0] + "/upgrade", status: 403},
		{name: "config replacement", method: "PUT", path: "/mihomo/" + h.ids[0] + "/configs", body: `{"payload":"danger"}`, status: 403},
		{name: "secret patch", method: "PATCH", path: "/mihomo/" + h.ids[0] + "/configs", body: `{"secret":"hijack"}`, status: 403},
		{name: "browser token", method: "GET", path: "/mihomo/" + h.ids[0] + "/version?token=forged", status: 400},
		{name: "path traversal", method: "GET", path: "/mihomo/" + h.ids[0] + "/proxies/%2e%2e/restart", status: 400},
		{name: "invalid URL", method: "POST", path: "/api/v1/instances", body: `{"name":"evil","controller_url":"http://169.254.169.254"}`, status: 400},
		{name: "public URL", method: "POST", path: "/api/v1/instances", body: `{"name":"evil","controller_url":"https://8.8.8.8"}`, status: 400},
		{name: "retention required", method: "DELETE", path: "/api/v1/instances/" + h.ids[0], status: 400},
		{name: "no agent", method: "GET", path: "/api/control/info", status: 404},
		{name: "collector status", method: "GET", path: "/api/v1/instances/" + h.ids[0] + "/status", status: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := h.fakes[0].Requests.Load() + h.fakes[1].Requests.Load()
			resp, _ := h.request(tt.method, tt.path, tt.body, true)
			requireStatus(t, resp, tt.status)
			if after := h.fakes[0].Requests.Load() + h.fakes[1].Requests.Load(); after != before {
				t.Fatal("rejected request reached upstream")
			}
		})
	}
	for _, path := range []string{"/api/v1/instances/" + h.ids[0], "/mihomo/" + h.ids[0] + "/configs"} {
		req, err := http.NewRequest("PATCH", h.server.URL+path, strings.NewReader(`{"mode":"rule"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(h.cookie)
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("X-CSRF-Token", h.csrf)
		resp, _ := h.do(req)
		requireStatus(t, resp, http.StatusForbidden)
	}
	for _, tt := range []struct {
		name, origin  string
		authenticated bool
		status        int
	}{
		{name: "anonymous", origin: h.server.URL, status: 401},
		{name: "cross origin", origin: "https://evil.example", authenticated: true, status: 403},
		{name: "missing origin", authenticated: true, status: 403},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn, resp, err := h.ws(h.ids[0], "/connections", tt.origin, tt.authenticated)
			if conn != nil {
				conn.CloseNow()
				t.Fatal("unauthorized websocket opened")
			}
			if err == nil || resp == nil {
				t.Fatal("expected HTTP refusal")
			}
			requireStatus(t, resp, tt.status)
		})
	}
}

func TestWebSocketStreamsAndClose(t *testing.T) {
	h := setup(t)
	for _, id := range h.ids {
		for _, path := range []string{"/connections", "/traffic", "/memory", "/logs?level=info&keep=a%2fb"} {
			conn, _, err := h.ws(id, path, h.server.URL, true)
			if err != nil {
				t.Fatal(err)
			}
			if conn.Subprotocol() != "mihomo-test" {
				t.Fatal("subprotocol lost")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			for range 4 {
				typ, data, err := conn.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if typ != websocket.MessageText || !json.Valid(data) {
					t.Fatalf("frame corrupted: %d %s", typ, data)
				}
			}
			pingDone := make(chan error, 1)
			go func() { pingDone <- conn.Ping(ctx) }()
			pingComplete := false
			for !pingComplete {
				if _, _, err := conn.Read(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-pingDone:
					if err != nil {
						t.Fatal(err)
					}
					pingComplete = true
				default:
				}
			}
			binary := []byte{0, 1, 0xff, 0x80}
			if err := conn.Write(ctx, websocket.MessageBinary, binary); err != nil {
				t.Fatal(err)
			}
			for {
				typ, data, err := conn.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if typ == websocket.MessageBinary {
					if !bytes.Equal(data, binary) {
						t.Fatal("binary frame altered")
					}
					break
				}
			}
			if err := conn.Close(websocket.StatusNormalClosure, "browser done"); err != nil {
				t.Fatal(err)
			}
			cancel()
		}
	}
	for _, f := range h.fakes {
		waitInactive(t, f)
	}
	for _, trigger := range []string{"close-normal", "close-abrupt"} {
		conn, _, err := h.ws(h.ids[0], "/connections", h.server.URL, true)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		if err := conn.Write(ctx, websocket.MessageText, []byte(trigger)); err != nil {
			t.Fatal(err)
		}
		for {
			_, _, err = conn.Read(ctx)
			if err != nil {
				break
			}
		}
		if trigger == "close-normal" && websocket.CloseStatus(err) != websocket.StatusNormalClosure {
			t.Fatalf("close frame lost: %v", err)
		}
		conn.CloseNow()
		cancel()
		waitInactive(t, h.fakes[0])
	}
}

func TestActiveStreamRevocation(t *testing.T) {
	for _, action := range []string{"rename", "delete", "credentials", "logout", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			h := setup(t)
			conn, _, err := h.ws(h.ids[0], "/logs", h.server.URL, true)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if _, _, err := conn.Read(ctx); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "rename":
				resp, _ := h.request("PATCH", "/api/v1/instances/"+h.ids[0], `{"name":"renamed"}`, true)
				requireStatus(t, resp, 200)
			case "delete":
				resp, _ := h.request("DELETE", "/api/v1/instances/"+h.ids[0]+"?history=purge", "", true)
				requireStatus(t, resp, 200)
			case "credentials":
				resp, _ := h.request("PATCH", "/api/v1/instances/"+h.ids[0], `{"secret":"rotated"}`, true)
				requireStatus(t, resp, 200)
			case "logout":
				resp, _ := h.request("POST", "/api/v1/auth/logout", "", true)
				requireStatus(t, resp, 200)
			case "shutdown":
				if err := h.app.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					break
				}
			}
			if ctx.Err() != nil {
				t.Fatal("revocation did not close browser websocket promptly")
			}
			if action == "rename" {
				// 改名后的新代际会自动采集；先移出 Hub，再检查所有上游流已收敛。
				resp, _ := h.request("DELETE", "/api/v1/instances/"+h.ids[0]+"?history=retain", "", true)
				requireStatus(t, resp, 200)
			}
			waitInactive(t, h.fakes[0])
		})
	}
}

func TestUpstreamStatusRedirectAndLimits(t *testing.T) {
	h := setup(t)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/connections" {
			// 仅统计被测 Gateway 请求，不把注册后自动启动的采集请求算作重试。
			http.Error(w, "not a collector fixture", http.StatusNotFound)
			return
		}
		calls.Add(1)
		switch r.URL.Query().Get("case") {
		case "redirect":
			w.Header().Set("Location", "http://169.254.169.254/?token=do-not-leak")
			w.WriteHeader(302)
		case "big":
			w.Write(bytes.Repeat([]byte("x"), int(h.config.Gateway.MaxResponseBytes)+1))
		case "timeout":
			<-r.Context().Done()
		default:
			w.Header().Set("Set-Cookie", "upstream-secret=do-not-leak")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(418)
			w.Write([]byte{0, 255, 'x'})
		}
	}))
	defer upstream.Close()
	id := h.register(upstream.URL, "status-test-secret", "status-test")
	resp, data := h.request("GET", "/mihomo/"+id+"/version", "", true)
	requireStatus(t, resp, 418)
	if !bytes.Equal(data, []byte{0, 255, 'x'}) || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatal("status/body/type changed")
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("upstream content can execute in platform origin")
	}
	if resp.Header.Get("Set-Cookie") != "" {
		t.Fatal("upstream cookie forwarded")
	}
	for _, testCase := range []string{"redirect", "big"} {
		resp, data = h.request("GET", "/mihomo/"+id+"/version?case="+testCase, "", true)
		requireStatus(t, resp, 502)
		if bytes.Contains(data, []byte("do-not-leak")) || resp.Header.Get("Location") != "" {
			t.Fatal("redirect target leaked")
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected retries or followed redirect: %d", calls.Load())
	}
	resp, _ = h.request("PUT", "/mihomo/"+h.ids[0]+"/proxies/test", strings.Repeat("x", int(h.config.Gateway.MaxBodyBytes)+1), true)
	requireStatus(t, resp, 413)
	conn, _, err := h.ws(h.ids[0], "/traffic", h.server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, bytes.Repeat([]byte("x"), 4096)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			break
		}
	}
	if ctx.Err() != nil {
		t.Fatal("oversize websocket was not closed")
	}
	waitInactive(t, h.fakes[0])
}

func TestPersistenceAndReadiness(t *testing.T) {
	h := setup(t)
	resp, _ := h.request("GET", "/readyz", "", false)
	requireStatus(t, resp, 200)
	for _, f := range h.fakes {
		waitInactive(t, f)
	}
	aead, err := storage.LoadKey(h.config.Security.MasterKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	items, err := (&storage.Instances{DB: h.app.DB, AEAD: aead}).List(t.Context())
	if err != nil || len(items) != 2 {
		t.Fatalf("persisted registry: %d %v", len(items), err)
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(h.config.Database.Path + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		for _, f := range h.fakes {
			if bytes.Contains(data, []byte(f.Secret)) {
				t.Fatal("plaintext secret in SQLite/WAL")
			}
		}
	}
	if err := h.app.Close(); err != nil {
		t.Fatal(err)
	}
	resp, _ = h.request("GET", "/readyz", "", false)
	requireStatus(t, resp, 503)
	resp, _ = h.request("GET", "/healthz", "", false)
	requireStatus(t, resp, 200)
	reopened, err := app.Open(t.Context(), h.config, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	req := httptest.NewRequest("GET", "/api/v1/instances", nil)
	req.AddCookie(h.cookie)
	recorder := httptest.NewRecorder()
	reopened.Handler.ServeHTTP(recorder, req)
	if recorder.Code != 401 {
		t.Fatal("sessions should expire on restart")
	}
	items, err = (&storage.Instances{DB: reopened.DB, AEAD: aead}).List(t.Context())
	if err != nil || len(items) != 2 || items[0].Secret == "" {
		t.Fatal("registry/secret not restored")
	}
}

func TestTimeoutCancellationAndWritePolicy(t *testing.T) {
	h := setupConfig(t, func(c *config.Config) { c.Gateway.RequestTimeoutSeconds = 1; c.Gateway.AllowRuntimeWrites = false })
	canceled := make(chan struct{}, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer upstream.Close()
	id := h.register(upstream.URL, "timeout-secret", "timeout")
	resp, _ := h.request("GET", "/mihomo/"+id+"/version", "", true)
	requireStatus(t, resp, 504)
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout did not cancel upstream")
	}
	req, err := http.NewRequest("GET", h.server.URL+"/mihomo/"+id+"/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	req = req.WithContext(ctx)
	req.AddCookie(h.cookie)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	cancel()
	<-done
	resp, _ = h.request("PUT", "/mihomo/"+h.ids[0]+"/proxies/test", `{"name":"DIRECT"}`, true)
	requireStatus(t, resp, 403)
}

func TestCSRFLoginLimitAndRedaction(t *testing.T) {
	h := setup(t)
	req, err := http.NewRequest("PATCH", h.server.URL+"/api/v1/instances/"+h.ids[0], strings.NewReader(`{"enabled":false}`))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(h.cookie)
	req.Header.Set("Origin", h.server.URL)
	resp, _ := h.do(req)
	requireStatus(t, resp, 403)
	// An incorrect Controller credential remains a native upstream 401.
	resp, _ = h.request("PATCH", "/api/v1/instances/"+h.ids[0], `{"secret":"intentionally-wrong-secret"}`, true)
	requireStatus(t, resp, 200)
	resp, _ = h.request("GET", "/mihomo/"+h.ids[0]+"/version?private=never-log-this", "", true)
	requireStatus(t, resp, 401)
	for range 10 {
		resp, _ = h.request("POST", "/api/v1/auth/login", `{"password":"not-the-password"}`, false)
	}
	requireStatus(t, resp, 429)
	logs := h.logs.String()
	for _, sensitive := range []string{password, h.cookie.Value, "intentionally-wrong-secret", "never-log-this", h.fakes[0].Secret, h.fakes[1].Secret} {
		if strings.Contains(logs, sensitive) {
			t.Fatalf("sensitive value in logs")
		}
	}
	if !strings.Contains(logs, "request_id") || !strings.Contains(logs, "duration_ms") {
		t.Fatal("structured request context missing")
	}
}

func TestWebSocketCapacity(t *testing.T) {
	h := setupConfig(t, func(c *config.Config) { c.Gateway.MaxWebSockets = 1 })
	first, _, err := h.ws(h.ids[0], "/traffic", h.server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer first.CloseNow()
	second, resp, err := h.ws(h.ids[1], "/memory", h.server.URL, true)
	if second != nil {
		second.CloseNow()
		t.Fatal("websocket capacity not enforced")
	}
	if err == nil || resp == nil {
		t.Fatal("expected bounded capacity refusal")
	}
	requireStatus(t, resp, 503)
}

func TestInstanceTransactionRollback(t *testing.T) {
	h := setup(t)
	if _, err := h.app.DB.Exec("DROP TABLE audit_events"); err != nil {
		t.Fatal(err)
	}
	resp, _ := h.request("POST", "/api/v1/instances", `{"name":"must-rollback","controller_url":"http://127.0.0.1:9090"}`, true)
	requireStatus(t, resp, 500)
	var count int
	if err := h.app.DB.QueryRow("SELECT COUNT(*) FROM instances").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("instance write committed without its audit event")
	}
	resp, data := h.request("GET", "/api/v1/instances", "", true)
	requireStatus(t, resp, 200)
	if bytes.Contains(data, []byte("must-rollback")) {
		t.Fatal("uncommitted instance published in registry")
	}
}

func TestLoginResourcesAndStaticBoundary(t *testing.T) {
	h := setup(t)
	for _, path := range []string{"/login", "/login.js", "/login.css", "/sw.js", "/"} {
		resp, data := h.request("GET", path, "", false)
		requireStatus(t, resp, 200)
		if path == "/login" && (bytes.Contains(data, []byte(`name="username"`)) || !bytes.Contains(data, []byte(`name="password"`)) || !bytes.Contains(data, []byte(`method="post"`))) {
			t.Fatal("login must not fall back to GET credentials")
		}
	}
	for _, path := range []string{"/missing.js", "/api/control/info", "/api/v1/does-not-exist", "/mihomo/does-not-exist/version"} {
		resp, data := h.request("GET", path, "", true)
		requireStatus(t, resp, 404)
		if bytes.Contains(data, []byte("<!doctype html>")) {
			t.Fatal("API/static error fell back to HTML")
		}
	}
}

func TestRegisteredControllerProbe(t *testing.T) {
	h := setup(t)
	path := "/api/v1/instances/" + h.ids[0] + "/test"
	response, _ := h.request("POST", path, "", false)
	requireStatus(t, response, 401)
	response, body := h.request("POST", path, "", true)
	requireStatus(t, response, 200)
	if !bytes.Contains(body, []byte(`"reachable":true`)) || bytes.Contains(body, []byte(h.fakes[0].Secret)) {
		t.Fatal(string(body))
	}
	request, _ := http.NewRequest("POST", h.server.URL+path, nil)
	request.AddCookie(h.cookie)
	request.Header.Set("Origin", "https://evil.invalid")
	response, _ = h.do(request)
	requireStatus(t, response, 403)
	response, _ = h.request("DELETE", "/api/v1/instances/"+h.ids[0]+"?history=retain", "", true)
	requireStatus(t, response, 200)
	response, _ = h.request("POST", path, "", true)
	requireStatus(t, response, 404)
}

func TestPasswordOnlyLogin(t *testing.T) {
	h := setup(t)
	for _, tt := range []struct {
		name, body string
		status     int
	}{
		{name: "password only", body: `{"password":"` + password + `"}`, status: 200},
		{name: "legacy username ignored", body: `{"username":"unused-name","password":"` + password + `"}`, status: 200},
		{name: "wrong password", body: `{"password":"wrong"}`, status: 401},
		{name: "missing password", body: `{}`, status: 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := h.request("POST", "/api/v1/auth/login", tt.body, false)
			requireStatus(t, resp, tt.status)
			if bytes.Contains(body, []byte(`"username"`)) {
				t.Fatal("login exposes unused username")
			}
		})
	}
	resp, body := h.request("GET", "/api/v1/auth/me", "", true)
	requireStatus(t, resp, 200)
	if bytes.Contains(body, []byte(`"username"`)) || !bytes.Contains(body, []byte(`"csrf_token"`)) {
		t.Fatal("unexpected session response")
	}
}

func TestInstancesAlwaysManaged(t *testing.T) {
	h := setup(t)
	id := h.register(h.fakes[0].Server.URL, h.fakes[0].Secret, "managed")
	snapshot := func() instance.Instance {
		var item instance.Instance
		err := h.app.DB.QueryRow(
			"SELECT name,enabled,collect_enabled,generation FROM instances WHERE id=?", id,
		).Scan(&item.Name, &item.Enabled, &item.CollectEnabled, &item.Generation)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	current := snapshot()
	if !current.Enabled || !current.CollectEnabled {
		t.Fatal("create did not enable management and collection")
	}
	for _, field := range []string{"enabled", "collect_enabled"} {
		t.Run(field, func(t *testing.T) {
			resp, _ := h.request(
				"POST",
				"/api/v1/instances",
				`{"name":"rejected","controller_url":"http://127.0.0.1:9090","`+field+`":false}`,
				true,
			)
			requireStatus(t, resp, 400)
			resp, _ = h.request(
				"PATCH",
				"/api/v1/instances/"+id,
				`{"name":"must-not-change","`+field+`":false}`,
				true,
			)
			requireStatus(t, resp, 400)
			got := snapshot()
			unchanged := got.Generation == current.Generation && got.Name == current.Name
			managed := got.Enabled && got.CollectEnabled
			if !unchanged || !managed {
				t.Fatal("rejected patch changed instance")
			}
		})
	}
	var count int
	if err := h.app.DB.QueryRow("SELECT COUNT(*) FROM instances WHERE name='rejected'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected creation persisted: count=%d err=%v", count, err)
	}
}
