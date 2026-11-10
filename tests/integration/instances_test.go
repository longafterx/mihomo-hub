//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/storage"
	"github.com/longafterx/mihomo-hub/internal/testutil/fakemihomo"
)

func TestRetainedHistoryCanBePurgedLater(t *testing.T) {
	h := setup(t)
	id, active := h.ids[0], h.ids[1]
	resp, _ := h.request("GET", "/api/v1/instances/retained", "", false)
	requireStatus(t, resp, 401)
	resp, _ = h.request("DELETE", "/api/v1/instances/"+id+"?history=retain", "", true)
	requireStatus(t, resp, 200)
	replacement := h.register(h.fakes[0].Server.URL, h.fakes[0].Secret, h.fakes[0].Label)
	resp, data := h.request("GET", "/api/v1/instances/retained", "", true)
	requireStatus(t, resp, 200)
	if !bytes.Contains(data, []byte(id)) || bytes.Contains(data, []byte(active)) || bytes.Contains(data, []byte("secret")) {
		t.Fatal("invalid retained catalog", string(data))
	}
	resp, _ = h.request("DELETE", "/api/v1/instances/"+id+"?history=purge", "", false)
	requireStatus(t, resp, 401)
	// 永久清理与审计同事务，审计失败不能把保留历史提前删除。
	_, err := h.app.DB.Exec(`CREATE TRIGGER reject_purge_audit BEFORE INSERT ON audit_events
        WHEN NEW.action='instance.delete.purge' BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ = h.request("DELETE", "/api/v1/instances/"+id+"?history=purge", "", true)
	requireStatus(t, resp, 500)
	var preserved int
	if err := h.app.DB.QueryRow("SELECT count(*) FROM instances WHERE id=?", id).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatal("failed purge lost retained identity", preserved, err)
	}
	if _, err := h.app.DB.Exec("DROP TRIGGER reject_purge_audit"); err != nil {
		t.Fatal(err)
	}
	resp, _ = h.request("DELETE", "/api/v1/instances/"+id+"?history=purge", "", true)
	requireStatus(t, resp, 200)
	var remaining int
	if err := h.app.DB.QueryRow("SELECT count(*) FROM instances WHERE id=?", id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("retained identity not purged", remaining, err)
	}
	resp, _ = h.request("GET", "/api/v1/instances/"+active, "", true)
	requireStatus(t, resp, 200)
	resp, _ = h.request("GET", "/api/v1/instances/"+replacement, "", true)
	requireStatus(t, resp, 200)
	resp, _ = h.request("DELETE", "/api/v1/instances/"+id+"?history=purge", "", true)
	requireStatus(t, resp, 404)
}

func TestDraftProbeFailureCodes(t *testing.T) {
	h := setup(t)
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
	}{
		{"no listener", 0, "", "controller_connection_refused"},
		{"authentication", 401, "private response body", "controller_auth_failed"},
		{"wrong service", 404, "private response body", "controller_http_error"},
		{"invalid JSON", 200, "private response body", "controller_invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer server.Close()
			if tc.status == 0 {
				server.Close()
			}
			body, err := json.Marshal(map[string]string{"controller_url": server.URL})
			if err != nil {
				t.Fatal(err)
			}
			resp, data := h.request("POST", "/api/v1/instances/test", string(body), true)
			requireStatus(t, resp, http.StatusBadGateway)
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != tc.code {
				t.Fatalf("want %s, got %s", tc.code, data)
			}
			if bytes.Contains(data, []byte("private response body")) {
				t.Fatal("upstream response leaked")
			}
		})
	}
}

func TestInstanceEditorConfigAndDraftProbe(t *testing.T) {
	h := setup(t)
	configPath := "/api/v1/instances/" + h.ids[0] + "/config"
	resp, _ := h.request("GET", configPath, "", false)
	requireStatus(t, resp, http.StatusUnauthorized)
	resp, data := h.request("GET", configPath, "", true)
	requireStatus(t, resp, http.StatusOK)
	if !bytes.Contains(data, []byte(h.fakes[0].Server.URL)) || bytes.Contains(data, []byte(h.fakes[0].Secret)) {
		t.Fatal("editor config missing URL or leaking Secret")
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("editor config is cacheable")
	}
	reader, err := storage.OpenReader(t.Context(), h.config.Database.Path, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	snapshot := func() string {
		t.Helper()
		var state string
		if err := reader.QueryRow(`SELECT group_concat(id || ':' || generation || ':' || hex(secret_ciphertext), ',') FROM (SELECT * FROM instances ORDER BY id)`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	for _, test := range []struct {
		name   string
		body   any
		status int
	}{
		{"stored secret", map[string]any{"instance_id": h.ids[0]}, http.StatusOK},
		{"wrong secret", map[string]any{"instance_id": h.ids[0], "secret": "wrong"}, http.StatusBadGateway},
		{"clear secret", map[string]any{"instance_id": h.ids[0], "secret": ""}, http.StatusBadGateway},
		{"invalid URL", map[string]any{"controller_url": "file:///etc/passwd"}, http.StatusBadRequest},
		{"metadata", map[string]any{"controller_url": "http://169.254.169.254"}, http.StatusBadRequest},
		{"invalid secret", map[string]any{"controller_url": h.fakes[0].Server.URL, "secret": "bad\nvalue"}, http.StatusBadRequest},
		{"missing instance", map[string]any{"instance_id": "missing"}, http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(test.body)
			if err != nil {
				t.Fatal(err)
			}
			resp, data := h.request("POST", "/api/v1/instances/test", string(body), true)
			requireStatus(t, resp, test.status)
			if bytes.Contains(data, []byte(h.fakes[0].Secret)) {
				t.Fatal("probe leaked secret")
			}
		})
	}
	fake := fakemihomo.New("draft-no-secret", "")
	defer fake.Close()
	body, _ := json.Marshal(map[string]string{"controller_url": fake.Server.URL, "secret": ""})
	resp, _ = h.request("POST", "/api/v1/instances/test", string(body), true)
	requireStatus(t, resp, http.StatusOK)
	resp, _ = h.request("POST", "/api/v1/instances/test", string(body), false)
	requireStatus(t, resp, http.StatusUnauthorized)
	for _, test := range []struct{ origin, csrf string }{
		{h.server.URL, ""},
		{"https://outside.invalid", h.csrf},
	} {
		req, err := http.NewRequest("POST", h.server.URL+"/api/v1/instances/test", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(h.cookie)
		req.Header.Set("Origin", test.origin)
		req.Header.Set("X-CSRF-Token", test.csrf)
		resp, _ := h.do(req)
		requireStatus(t, resp, http.StatusForbidden)
	}
	if snapshot() != before {
		t.Fatal("draft test created or changed registered instances")
	}
}

func TestRecreateRetainedInstanceWithoutSecret(t *testing.T) {
	h := setup(t)
	fake := fakemihomo.New("no-secret", "")
	defer fake.Close()
	body, err := json.Marshal(instance.Create{Name: "reusable", ControllerURL: fake.Server.URL})
	if err != nil {
		t.Fatal(err)
	}
	create := func() instance.Instance {
		t.Helper()
		resp, data := h.request("POST", "/api/v1/instances", string(body), true)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create without secret: %d %s", resp.StatusCode, data)
		}
		var result struct {
			Data instance.Instance `json:"data"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if result.Data.HasSecret {
			t.Fatal("empty secret reported as configured")
		}
		return result.Data
	}
	first := create()
	resp, data := h.request("GET", "/mihomo/"+first.ID+"/version", "", true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-secret gateway: %d %s", resp.StatusCode, data)
	}
	resp, _ = h.request("DELETE", "/api/v1/instances/"+first.ID+"?history=retain", "", true)
	requireStatus(t, resp, http.StatusOK)
	second := create()
	if first.ID == second.ID {
		t.Fatal("recreated instance reused historical identity")
	}
	resp, _ = h.request("GET", "/api/v1/instances/"+first.ID, "", true)
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = h.request("GET", "/api/v1/instances/"+first.ID+"/config", "", true)
	requireStatus(t, resp, http.StatusNotFound)
	resp, _ = h.request("POST", "/api/v1/instances", string(body), true)
	requireStatus(t, resp, http.StatusConflict)
}

func TestAutomaticDraftProbeReturnsResolvedURL(t *testing.T) {
	h := setup(t)
	body, err := json.Marshal(map[string]any{"controller_url": h.fakes[0].Server.URL, "secret": h.fakes[0].Secret, "auto_detect": true})
	if err != nil {
		t.Fatal(err)
	}
	resp, data := h.request("POST", "/api/v1/instances/test", string(body), true)
	requireStatus(t, resp, 200)
	var result struct {
		Data struct {
			URL string `json:"controller_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Data.URL != h.fakes[0].Server.URL {
		t.Fatal("protocol not resolved", string(data), err)
	}
	if bytes.Contains(data, []byte(h.fakes[0].Secret)) {
		t.Fatal("secret leaked")
	}
}
