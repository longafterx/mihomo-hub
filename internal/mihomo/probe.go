package mihomo

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var versionValue = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,128}$`)

// DetectProbe 先完成不携带凭据的 TLS 握手，再读取版本；仅明确收到 HTTP 响应才尝试明文。
// 证书失败、握手中断和未知服务均不降级，避免把原本应由 HTTPS 保护的 Secret 发到明文链路。
func (p Policy) DetectProbe(ctx context.Context, target, secret string) (string, string, error) {
	u, err := p.Parse(target)
	if err != nil {
		return "", "", err
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	u.Host = net.JoinHostPort(u.Hostname(), port)
	u.Scheme = "https"
	if err := p.Validate(ctx, u.String()); err != nil {
		return "", "", err
	}
	transport := p.Transport()
	defer transport.CloseIdleConnections()
	conn, err := transport.DialTLSContext(ctx, "tcp", u.Host)
	if err == nil {
		conn.Close()
	} else {
		var record tls.RecordHeaderError
		if !errors.As(err, &record) || string(record.RecordHeader[:]) != "HTTP/" {
			return "", "", probeConnectionError(err)
		}
		u.Scheme = "http"
		if err := p.Validate(ctx, u.String()); err != nil {
			return "", "", err
		}
	}
	version, err := p.Probe(ctx, u.String(), secret)
	return version, u.String(), err
}

// Probe 只读取指定 Controller 的 /version，不触发采集、配置修改或内核控制。
func (p Policy) Probe(ctx context.Context, target, secret string) (string, error) {
	u, err := p.Parse(target)
	if err != nil {
		return "", err
	}
	u.Path = "/version"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := p.Transport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	if secret != "" {
		request.Header.Set("Authorization", "Bearer "+secret)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", probeConnectionError(err)
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return "", probeFailure("auth_failed", "controller authentication failed")
	case response.StatusCode >= 300 && response.StatusCode < 400:
		return "", probeFailure("redirected", "controller redirected the version request")
	case response.StatusCode != http.StatusOK:
		return "", probeFailure("http_error", "controller returned an unexpected HTTP status")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		return "", probeConnectionError(err)
	}
	if len(body) > 65536 {
		return "", probeFailure("invalid_response", "invalid version response")
	}
	var value struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &value); err != nil || !versionValue.MatchString(value.Version) {
		return "", probeFailure("invalid_response", "invalid version response")
	}
	// 平台只返回严格版本字段，永不回显上游错误体、认证头或地址。
	if secret != "" && strings.Contains(value.Version, secret) {
		return "", probeFailure("invalid_response", "invalid version response")
	}
	return value.Version, nil
}
