//go:build integration

package integration_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/longafterx/mihomo-hub/internal/config"
)

func TestGatewayMaintenanceContract(t *testing.T) {
	h := setup(t)
	cases := []struct{ name, method, path, body string }{
		{name: "reload", method: "PUT", path: "/configs?force=true", body: `{"path":"","payload":""}`},
		{name: "restart", method: "POST", path: "/restart"},
		{name: "proxy provider", method: "PUT", path: "/providers/proxies/%E4%B8%AD%E6%96%87%2Fprovider?keep=a%2fb"},
		{name: "rule provider", method: "PUT", path: "/providers/rules/%E8%A7%84%E5%88%99"},
		{name: "dns cache", method: "POST", path: "/cache/dns/flush"},
		{name: "fakeip cache", method: "POST", path: "/cache/fakeip/flush"},
		{name: "geo", method: "POST", path: "/configs/geo"},
		{name: "ports", method: "PATCH", path: "/configs", body: `{"port":7891,"mixed-port":7890,"socks-port":7892,"redir-port":7893,"tproxy-port":7894}`},
		{name: "lan", method: "PATCH", path: "/configs", body: `{"allow-lan":true}`},
		{name: "interface", method: "PATCH", path: "/configs", body: `{"interface-name":"eth0"}`},
		{name: "tun", method: "PATCH", path: "/configs", body: `{"tun":{"enable":true,"stack":"mixed","device":"utun-test"}}`},
	}
	for i, id := range h.ids {
		for _, tt := range cases {
			t.Run(fmt.Sprintf("instance-%d/%s", i, tt.name), func(t *testing.T) {
				before := h.fakes[1-i].Requests.Load()
				response, _ := h.request(tt.method, "/mihomo/"+id+tt.path, tt.body, true)
				want := http.StatusNoContent
				if tt.name == "restart" {
					want = http.StatusOK
				}
				requireStatus(t, response, want)
				observation := <-h.fakes[i].Observations
				if observation.Method != tt.method || observation.URI != tt.path || string(observation.Body) != tt.body {
					t.Fatalf("request changed: %+v", observation)
				}
				if observation.Header.Get("Authorization") != "Bearer "+h.fakes[i].Secret {
					t.Fatal("incorrect controller credential")
				}
				if h.fakes[1-i].Requests.Load() != before {
					t.Fatal("write reached another instance")
				}
			})
		}
	}
	response, body := h.request("GET", "/mihomo/"+h.ids[0]+"/dns/query?name=example.com&type=A", "", true)
	requireStatus(t, response, http.StatusOK)
	if !strings.Contains(string(body), "192.0.2.1") {
		t.Fatal("DNS result was not forwarded")
	}
}

func TestGatewayMaintenanceRefusals(t *testing.T) {
	h := setup(t)
	for _, tt := range []struct {
		name, method, path, body string
		status                   int
	}{
		{name: "upgrade", method: "POST", path: "/upgrade", status: 403},
		{name: "upgrade UI", method: "POST", path: "/upgrade/ui", status: 403},
		{name: "arbitrary file", method: "PUT", path: "/configs", body: `{"path":"/etc/other.yaml"}`, status: 403},
		{name: "inline replacement", method: "PUT", path: "/configs", body: `{"payload":"rules: []"}`, status: 403},
		{name: "invalid reload", method: "PUT", path: "/configs", body: `[]`, status: 400},
		{name: "extra reload", method: "PUT", path: "/configs", body: `{} {}`, status: 400},
		{name: "unknown reload field", method: "PUT", path: "/configs", body: `{"url":"https://example.com"}`, status: 400},
		{name: "restart arguments", method: "POST", path: "/restart", body: `{"path":"/tmp/other"}`, status: 400},
		{name: "unsupported dns patch", method: "PATCH", path: "/configs", body: `{"dns":{"nameserver":["1.1.1.1"]}}`, status: 403},
		{name: "controller binding", method: "PATCH", path: "/configs", body: `{"external-controller":"0.0.0.0:9090"}`, status: 403},
		{name: "negative port", method: "PATCH", path: "/configs", body: `{"mixed-port":-1}`, status: 400},
		{name: "overflow port", method: "PATCH", path: "/configs", body: `{"port":65536}`, status: 400},
		{name: "noninteger port", method: "PATCH", path: "/configs", body: `{"port":1.5}`, status: 400},
		{name: "null port", method: "PATCH", path: "/configs", body: `{"port":null}`, status: 400},
		{name: "invalid lan", method: "PATCH", path: "/configs", body: `{"allow-lan":"yes"}`, status: 400},
		{name: "empty tun", method: "PATCH", path: "/configs", body: `{"tun":{}}`, status: 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := h.fakes[0].Requests.Load()
			response, _ := h.request(tt.method, "/mihomo/"+h.ids[0]+tt.path, tt.body, true)
			requireStatus(t, response, tt.status)
			if h.fakes[0].Requests.Load() != before {
				t.Fatal("rejected request reached upstream")
			}
		})
	}
	for _, methodPath := range [][2]string{{"POST", "/restart"}, {"PUT", "/configs"}, {"PUT", "/providers/rules/test"}} {
		response, _ := h.request(methodPath[0], "/mihomo/"+h.ids[0]+methodPath[1], "{}", false)
		requireStatus(t, response, 401)
		request, err := http.NewRequest(methodPath[0], h.server.URL+"/mihomo/"+h.ids[0]+methodPath[1], strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(h.cookie)
		request.Header.Set("Origin", "https://evil.invalid")
		response, _ = h.do(request)
		requireStatus(t, response, 403)
	}
}

func TestGatewayMaintenanceReadOnlyPolicy(t *testing.T) {
	h := setupConfig(t, func(c *config.Config) { c.Gateway.AllowRuntimeWrites = false })
	for _, operation := range [][3]string{
		{"POST", "/restart", ""}, {"POST", "/configs/geo", ""},
		{"POST", "/cache/dns/flush", ""}, {"POST", "/cache/fakeip/flush", ""},
		{"PUT", "/configs", "{}"}, {"PUT", "/providers/proxies/test", ""},
		{"PUT", "/providers/rules/test", ""}, {"PATCH", "/configs", `{"allow-lan":true}`},
	} {
		response, _ := h.request(operation[0], "/mihomo/"+h.ids[0]+operation[1], operation[2], true)
		requireStatus(t, response, 403)
	}
	if h.fakes[0].Requests.Load() != 0 {
		t.Fatal("read-only writes reached upstream")
	}
}

func TestGatewayMaintenanceTimeout(t *testing.T) {
	h := setupConfig(t, func(c *config.Config) {
		c.Gateway.RequestTimeoutSeconds = 1
		c.Gateway.MaintenanceTimeoutSeconds = 3
	})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/connections" {
			w.WriteHeader(404)
			return
		}
		select {
		case <-time.After(1200 * time.Millisecond):
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	id := h.register(upstream.URL, "fixture-timeout-secret", "slow maintenance")
	response, _ := h.request("POST", "/mihomo/"+id+"/configs/geo", "", true)
	requireStatus(t, response, http.StatusNoContent)
	response, _ = h.request("GET", "/mihomo/"+id+"/version", "", true)
	requireStatus(t, response, http.StatusGatewayTimeout)
}
