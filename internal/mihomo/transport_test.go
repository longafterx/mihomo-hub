package mihomo_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/mihomo"
)

type resolver struct{ ips []netip.Addr }

func (r resolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.ips, nil
}

func TestControllerPolicy(t *testing.T) {
	t.Parallel()
	p := mihomo.Policy{}
	for _, tt := range []struct {
		name string
		url  string
		ok   bool
	}{
		{name: "loopback", url: "http://127.0.0.1:9090", ok: true},
		{name: "private", url: "http://192.168.1.1:9090", ok: true},
		{name: "tailscale", url: "http://100.111.37.112:9090", ok: true},
		{name: "ipv6 loopback", url: "https://[::1]:9090", ok: true},
		{name: "public", url: "https://8.8.8.8"},
		{name: "metadata", url: "http://169.254.169.254"},
		{name: "metadata ipv6", url: "http://[fd00:ec2::254]"},
		{name: "alibaba metadata", url: "http://100.100.100.200"},
		{name: "userinfo", url: "http://user:secret@127.0.0.1"},
		{name: "scheme", url: "file:///etc/passwd"},
		{name: "query", url: "http://127.0.0.1?target=http://8.8.8.8"},
		{name: "base path", url: "http://127.0.0.1/configs"},
		{name: "bad port", url: "http://127.0.0.1:0"},
		{name: "empty port", url: "http://127.0.0.1:"},
		{name: "ipv6 zone", url: "http://[fe80::1%25en0]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := p.Validate(t.Context(), tt.url); (err == nil) != tt.ok {
				t.Fatalf("validation = %v, want success %v", err, tt.ok)
			}
		})
	}
	p.AllowPublic = true
	if err := p.Validate(t.Context(), "https://8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	p.AllowedPorts = []int{9090}
	if err := p.Validate(t.Context(), "http://127.0.0.1:9091"); err == nil {
		t.Fatal("port policy bypassed")
	}
}

func TestDNSRebindingAtDial(t *testing.T) {
	t.Parallel()
	p := mihomo.Policy{Resolver: resolver{ips: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}}
	if err := p.Validate(t.Context(), "http://controller.test:9090"); err != nil {
		t.Fatal(err)
	}
	p.Resolver = resolver{ips: []netip.Addr{netip.MustParseAddr("169.254.169.254")}}
	transport := p.Transport()
	defer transport.CloseIdleConnections()
	if conn, err := transport.DialContext(t.Context(), "tcp", "controller.test:9090"); err == nil {
		conn.Close()
		t.Fatal("DNS rebinding bypassed dial-time policy")
	}
	p.Resolver = resolver{ips: []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("8.8.8.8")}}
	if err := p.Validate(t.Context(), "http://controller.test:9090"); err == nil {
		t.Fatal("mixed public/private DNS response accepted")
	}
}

func TestPublicOptInStillRejectsCleartextRebinding(t *testing.T) {
	t.Parallel()
	p := mihomo.Policy{AllowPublic: true, Resolver: resolver{ips: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}}
	transport := p.Transport()
	defer transport.CloseIdleConnections()
	if conn, err := transport.DialContext(t.Context(), "tcp", "controller.test:9090"); err == nil {
		conn.Close()
		t.Fatal("public HTTP dial accepted")
	}
}
func TestPolicyTLSVerifiesControllerCertificate(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer server.Close()
	transport := (mihomo.Policy{}).Transport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	if response, err := client.Get(server.URL); err == nil {
		response.Body.Close()
		t.Fatal("untrusted TLS certificate accepted")
	}
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
}
