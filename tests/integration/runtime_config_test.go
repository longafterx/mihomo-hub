//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/runtimeconfig"
	"go.yaml.in/yaml/v3"
)

const fullYAML = `mixed-port: 7890
mode: rule
secret: yaml-only-sensitive-value
proxies: []
proxy-groups: []
rules:
  - MATCH,DIRECT
dns:
  enable: true
  listen: 127.0.0.1:1053
  enhanced-mode: fake-ip
  nameserver: [1.1.1.1]
  nameserver-policy:
    example.com: 9.9.9.9
  use-hosts: true
`

func runtimeBody(t *testing.T, data any) string {
	t.Helper()
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func runtimeView(t *testing.T, body []byte) runtimeconfig.View {
	t.Helper()
	var envelope struct {
		Data runtimeconfig.View `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestRuntimeConfigImportAndDNS(t *testing.T) {
	h := setup(t)
	path := "/api/v1/instances/" + h.ids[0] + "/runtime-config"
	response, body := h.request("GET", path, "", true)
	requireStatus(t, response, 200)
	if runtimeView(t, body).Imported {
		t.Fatal("unexpected imported config")
	}
	response, body = h.request("PUT", path, runtimeBody(t, map[string]any{"yaml": fullYAML, "revision": 0}), true)
	requireStatus(t, response, 200)
	v := runtimeView(t, body)
	if !v.Imported || v.Revision != 1 || v.DNS.Nameserver[0] != "1.1.1.1" {
		t.Fatal(string(body))
	}
	if h.fakes[0].Requests.Load() != 0 {
		t.Fatal("import changed the controller")
	}
	if bytes.Contains(body, []byte("yaml-only-sensitive-value")) || bytes.Contains(body, []byte("MATCH,DIRECT")) {
		t.Fatal("full YAML leaked in response")
	}
	var ciphertext []byte
	if err := h.app.DB.QueryRow("SELECT yaml_ciphertext FROM runtime_configs WHERE instance_id=?", h.ids[0]).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("yaml-only-sensitive-value")) {
		t.Fatal("YAML stored in plaintext")
	}
	v.DNS.Nameserver = []string{"8.8.8.8"}
	response, body = h.request("PATCH", path+"/dns", runtimeBody(t, map[string]any{"dns": v.DNS, "revision": v.Revision}), true)
	requireStatus(t, response, 200)
	if runtimeView(t, body).Revision != 2 {
		t.Fatal(string(body))
	}
	observation := <-h.fakes[0].Observations
	var load struct{ Path, Payload string }
	if err := json.Unmarshal(observation.Body, &load); err != nil {
		t.Fatal(err)
	}
	if observation.Method != "PUT" || observation.URI != "/configs?force=true" || load.Path != "" {
		t.Fatal("not a native full config load")
	}
	var applied map[string]any
	if err := yaml.Unmarshal([]byte(load.Payload), &applied); err != nil {
		t.Fatal(err)
	}
	dns := applied["dns"].(map[string]any)
	if dns["nameserver"].([]any)[0] != "8.8.8.8" || dns["nameserver-policy"] == nil || applied["rules"] == nil || applied["mixed-port"] != 7890 {
		t.Fatal("DNS merge lost fields from imported YAML")
	}
	if h.fakes[1].Requests.Load() != 0 {
		t.Fatal("another instance received the config")
	}
	response, body = h.request("GET", path, "", true)
	requireStatus(t, response, 200)
	if runtimeView(t, body).DNS.Nameserver[0] != "8.8.8.8" {
		t.Fatal("successful DNS update not persisted")
	}
	response, _ = h.request("PATCH", path+"/dns", runtimeBody(t, map[string]any{"dns": v.DNS, "revision": 1}), true)
	requireStatus(t, response, 409)
	if len(h.fakes[0].Observations) != 0 {
		t.Fatal("stale DNS write reached controller")
	}
	if bytes.Contains([]byte(h.logs.String()), []byte("yaml-only-sensitive-value")) {
		t.Fatal("full YAML in logs")
	}
}

func TestRuntimeConfigRefusalsAndDeletion(t *testing.T) {
	h := setup(t)
	path := "/api/v1/instances/" + h.ids[0] + "/runtime-config"
	for _, tt := range []struct{ name, text string }{
		{name: "DNS fragment", text: "dns: {enable: true}"},
		{name: "multiple documents", text: fullYAML + "\n---\nrules: []"},
		{name: "duplicate keys", text: "rules: []\nrules: []"},
		{name: "invalid DNS shape", text: "rules: []\ndns: []"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response, _ := h.request("PUT", path, runtimeBody(t, map[string]any{"yaml": tt.text, "revision": 0}), true)
			requireStatus(t, response, 400)
		})
	}
	dns := runtimeconfig.DNS{Enable: true, EnhancedMode: "fake-ip", Nameserver: []string{"1.1.1.1"}}
	response, _ := h.request("PATCH", path+"/dns", runtimeBody(t, map[string]any{"dns": dns, "revision": 0}), true)
	requireStatus(t, response, 409)
	response, _ = h.request("PUT", path, runtimeBody(t, map[string]any{"yaml": fullYAML}), false)
	requireStatus(t, response, 401)
	request := httptest.NewRequest("PUT", path, nil)
	request.AddCookie(h.cookie)
	request.Header.Set("Origin", h.server.URL)
	recorder := httptest.NewRecorder()
	h.app.Handler.ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatal("import did not require CSRF token")
	}
	response, _ = h.request("PUT", path, runtimeBody(t, map[string]any{"yaml": fullYAML, "revision": 0}), true)
	requireStatus(t, response, 200)
	response, _ = h.request("PATCH", "/api/v1/instances/"+h.ids[0], `{"controller_url":"http://127.0.0.1:19090"}`, true)
	requireStatus(t, response, 200)
	response, body := h.request("GET", path, "", true)
	requireStatus(t, response, 200)
	if runtimeView(t, body).Imported {
		t.Fatal("old controller YAML carried over to new target")
	}
	response, _ = h.request("PATCH", path+"/dns", runtimeBody(t, map[string]any{"dns": dns, "revision": 1}), true)
	requireStatus(t, response, 409)
	response, _ = h.request("DELETE", "/api/v1/instances/"+h.ids[0]+"?history=retain", "", true)
	requireStatus(t, response, 200)
	var count int
	if err := h.app.DB.QueryRow("SELECT COUNT(*) FROM runtime_configs WHERE instance_id=?", h.ids[0]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retained instance kept YAML credentials: %d %v", count, err)
	}
}

func TestRuntimeConfigUpstreamFailure(t *testing.T) {
	h := setup(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"sensitive-upstream-detail"}`, 400)
	}))
	defer upstream.Close()
	id := h.register(upstream.URL, "fixture-secret", "DNS rejected")
	path := "/api/v1/instances/" + id + "/runtime-config"
	response, body := h.request("PUT", path, runtimeBody(t, map[string]any{"yaml": fullYAML}), true)
	requireStatus(t, response, 200)
	v := runtimeView(t, body)
	v.DNS.Nameserver = []string{"8.8.8.8"}
	response, body = h.request("PATCH", path+"/dns", runtimeBody(t, map[string]any{"dns": v.DNS, "revision": 1}), true)
	requireStatus(t, response, 502)
	if bytes.Contains(body, []byte("sensitive-upstream-detail")) {
		t.Fatal("native config error leaked")
	}
	response, body = h.request("GET", path, "", true)
	requireStatus(t, response, 200)
	v = runtimeView(t, body)
	if v.Revision != 1 || v.DNS.Nameserver[0] != "1.1.1.1" {
		t.Fatal("failed load replaced saved YAML")
	}
}

func TestRuntimeConfigReadOnly(t *testing.T) {
	h := setupConfig(t, func(c *config.Config) { c.Gateway.AllowRuntimeWrites = false })
	response, _ := h.request("PUT", "/api/v1/instances/"+h.ids[0]+"/runtime-config", runtimeBody(t, map[string]any{"yaml": fullYAML}), true)
	requireStatus(t, response, 403)
}

func TestRuntimeConfigConcurrentRevisions(t *testing.T) {
	h := setup(t)
	path := "/api/v1/instances/" + h.ids[0] + "/runtime-config"
	response, body := h.request("PUT", path, runtimeBody(t, map[string]any{"yaml": fullYAML}), true)
	requireStatus(t, response, 200)
	v := runtimeView(t, body)
	payload := runtimeBody(t, map[string]any{"dns": v.DNS, "revision": v.Revision})
	statuses := make(chan int, 2)
	var done sync.WaitGroup
	for range 2 {
		done.Go(func() {
			response, _ := h.request("PATCH", path+"/dns", payload, true)
			statuses <- response.StatusCode
		})
	}
	done.Wait()
	first, second := <-statuses, <-statuses
	if !((first == 200 && second == 409) || (first == 409 && second == 200)) {
		t.Fatalf("revision conflict not enforced: %d %d", first, second)
	}
	if len(h.fakes[0].Observations) != 1 {
		t.Fatal("concurrent stale config was applied twice")
	}
}

func TestRuntimeConfigImportAuditRollback(t *testing.T) {
	h := setup(t)
	if _, err := h.app.DB.Exec("DROP TABLE audit_events"); err != nil {
		t.Fatal(err)
	}
	response, _ := h.request("PUT", "/api/v1/instances/"+h.ids[0]+"/runtime-config", runtimeBody(t, map[string]any{"yaml": fullYAML}), true)
	requireStatus(t, response, 500)
	var count int
	if err := h.app.DB.QueryRow("SELECT COUNT(*) FROM runtime_configs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("config committed without audit: %d %v", count, err)
	}
}
