package mihomo_test

import (
	"context"
	"crypto/x509"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeExplainsFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"authentication", "do-not-echo-credentials", "controller authentication failed", 401},
		{"forbidden", "do-not-echo-credentials", "controller authentication failed", 403},
		{"wrong service", "private upstream error", "controller returned an unexpected HTTP status", 404},
		{"redirect", "private upstream error", "controller redirected the version request", 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer server.Close()
			_, err := (mihomo.Policy{}).Probe(t.Context(), server.URL, "")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("want safe reason %q, got %v", tc.want, err)
			}
		})
	}
	t.Run("connection refused", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		server.Close()
		_, err := (mihomo.Policy{}).Probe(t.Context(), server.URL, "")
		if err == nil || err.Error() != "controller connection refused" {
			t.Fatalf("want connection refused, got %v", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
		defer cancel()
		_, err := (mihomo.Policy{}).Probe(ctx, "http://127.0.0.1:9090", "")
		if err == nil || err.Error() != "controller request timed out" {
			t.Fatalf("want timeout, got %v", err)
		}
	})
	t.Run("TLS certificate", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"version":"v1.19.33"}`))
		}))
		defer server.Close()
		_, err := (mihomo.Policy{}).Probe(t.Context(), server.URL, "")
		if err == nil || err.Error() != "controller TLS verification failed" {
			t.Fatalf("want TLS diagnostic, got %v", err)
		}
	})
}

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

func TestDetectProbeSelectsProtocolWithoutDowngradingTLS(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"http", "trusted TLS", "untrusted TLS", "wrong secret"} {
		t.Run(name, func(t *testing.T) {
			var authorized atomic.Int64
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					authorized.Add(1)
				}
				if r.Header.Get("Authorization") != "Bearer fixture-secret" {
					w.WriteHeader(401)
					return
				}
				w.Write([]byte(`{"version":"v1.19.33"}`))
			})
			var server *httptest.Server
			policy := mihomo.Policy{}
			if name == "http" || name == "wrong secret" {
				server = httptest.NewServer(handler)
			} else {
				server = httptest.NewTLSServer(handler)
				if name == "trusted TLS" {
					policy.TLSRootCAs = x509.NewCertPool()
					policy.TLSRootCAs.AddCert(server.Certificate())
				}
			}
			defer server.Close()
			secret := "fixture-secret"
			if name == "wrong secret" {
				secret = "wrong"
			}
			version, target, err := policy.DetectProbe(t.Context(), strings.Replace(server.URL, "https://", "http://", 1), secret)
			if name == "untrusted TLS" {
				if err == nil || authorized.Load() != 0 {
					t.Fatal("invalid certificate downgraded or exposed credentials", err)
				}
				return
			}
			if name == "wrong secret" {
				if err == nil {
					t.Fatal("authentication accepted")
				}
				return
			}
			if err != nil || target != server.URL || version != "v1.19.33" || authorized.Load() != 1 {
				t.Fatal(version, target, err, authorized.Load())
			}
		})
	}
}
