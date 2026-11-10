package mihomo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// ErrControllerDNS 区分主机解析失败与地址策略拒绝，不携带原始 DNS 错误内容。
var ErrControllerDNS = errors.New("controller DNS lookup failed")

type Policy struct {
	AllowPublic  bool
	AllowedPorts []int
	Resolver     Resolver
	TLSRootCAs   *x509.CertPool
}

func (p Policy) Parse(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid controller URL")
	}
	validScheme := u.Scheme == "http" || u.Scheme == "https"
	hasExtras := u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != ""
	validPath := u.Path == "" || u.Path == "/"
	if !validScheme || hasExtras || !validPath || u.Opaque != "" {
		return nil, errors.New("controller URL must be an http(s) root URL without credentials, query or fragment")
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "%\\ \t\r\n") {
		return nil, errors.New("invalid controller host")
	}
	for _, ch := range host {
		if ch > 127 {
			return nil, errors.New("controller DNS name must use ASCII/punycode")
		}
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("invalid controller port")
	}
	if len(p.AllowedPorts) > 0 {
		found := false
		for _, allowed := range p.AllowedPorts {
			found = found || n == allowed
		}
		if !found {
			return nil, errors.New("controller port is not allowed")
		}
	}
	// Reject malformed authorities, including unbracketed IPv6 and empty ports.
	expectedHost := host
	if strings.Contains(host, ":") {
		expectedHost = "[" + host + "]"
	}
	if u.Port() != "" {
		expectedHost += ":" + u.Port()
	}
	if u.Host != expectedHost {
		return nil, errors.New("malformed controller authority")
	}
	u.Path = ""
	u.RawPath = ""
	return u, nil
}

func (p Policy) Validate(ctx context.Context, raw string) error {
	u, err := p.Parse(raw)
	if err != nil {
		return err
	}
	ips, err := p.resolve(ctx, u.Hostname())
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		for _, ip := range ips {
			if !(Policy{}).AllowedIP(ip) {
				return errors.New("public controllers require HTTPS")
			}
		}
	}
	return nil
}

func (p Policy) AllowedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	// Link-local/metadata, unspecified, multicast and reserved destinations stay denied,
	// even when public controllers are explicitly enabled.
	denied := []string{"0.0.0.0/8", "169.254.0.0/16", "240.0.0.0/4", "fd00:ec2::254/128", "100.100.100.200/32"}
	for _, prefix := range denied {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		return true
	}
	return p.AllowPublic && ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast()
}

func (p Policy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	addresses := []netip.Addr{}
	if ip, err := netip.ParseAddr(host); err == nil {
		addresses = append(addresses, ip)
	} else {
		resolver := p.Resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		ips, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrControllerDNS
		}
		addresses = ips
	}
	if len(addresses) == 0 {
		return nil, errors.New("controller has no resolved address")
	}
	for _, ip := range addresses {
		if !p.AllowedIP(ip) {
			return nil, errors.New("controller address is prohibited by network policy")
		}
	}
	return addresses, nil
}

// Transport 在每次实际拨号重新验证 DNS；公网目标必须使用 TLS，不能只依赖注册时校验。
func (p Policy) Transport() *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	dial := func(ctx context.Context, network, address string, encrypted bool) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid dial address")
		}
		ips, err := p.resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		// 即使管理员开启公网 Controller，也禁止 DNS 重绑定把 HTTP 内网目标变为公网明文目标。
		if !encrypted {
			for _, ip := range ips {
				if !(Policy{}).AllowedIP(ip) {
					return nil, errors.New("public controllers require HTTPS")
				}
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("dial controller: %w", lastErr)
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: p.TLSRootCAs},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dial(ctx, network, address, false)
		},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout: 60 * time.Second, MaxIdleConns: 64, MaxIdleConnsPerHost: 4,
		MaxConnsPerHost: 32, MaxResponseHeaderBytes: 64 << 10, DisableCompression: true,
	}
	transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address, true)
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			conn.Close()
			return nil, err
		}
		config := &tls.Config{MinVersion: tls.VersionTLS12}
		if transport.TLSClientConfig != nil {
			config = transport.TLSClientConfig.Clone()
		}
		if config.ServerName == "" {
			config.ServerName = host
		}
		tlsConn := tls.Client(conn, config)
		handshake, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := tlsConn.HandshakeContext(handshake); err != nil {
			conn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	return transport
}
