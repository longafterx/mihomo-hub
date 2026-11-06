package mihomo_test

import (
	"github.com/longafterx/mihomo-hub/internal/mihomo"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeLimitsAndSecretBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"version", `{"version":"v1.19.33"}`, 200, true},
		{"missing", `{}`, 200, false},
		{"oversized", strings.Repeat("x", 65537), 200, false},
		{"trailing", `{"version":"v1.19.33"}{}`, 200, false},
		{"upstream error", `secret-fixture`, 401, false},
		{"secret echo", `{"version":"secret-fixture"}`, 200, false},
		{"redirect", ``, 302, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/version" || r.Header.Get("Authorization") != "Bearer secret-fixture" {
					t.Error("invalid probe")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			version, err := (mihomo.Policy{}).Probe(t.Context(), server.URL, "secret-fixture")
			if (err == nil) != tc.ok {
				t.Fatal(version, err)
			}
			if tc.ok && version != "v1.19.33" {
				t.Fatal(version)
			}
		})
	}
}
