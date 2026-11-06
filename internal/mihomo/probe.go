package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var versionValue = regexp.MustCompile(`^[A-Za-z0-9._+\-]{1,128}$`)

// Probe 只读取已注册 Controller 的 /version，不触发采集、配置修改或内核控制。
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
		return "", errors.New("controller unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("controller rejected probe")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return "", errors.New("invalid version response")
	}
	var value struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &value); err != nil || !versionValue.MatchString(value.Version) {
		return "", errors.New("invalid version response")
	}
	// 平台只返回严格版本字段，永不回显上游错误体、认证头或地址。
	if secret != "" && strings.Contains(value.Version, secret) {
		return "", errors.New("invalid version response")
	}
	return value.Version, nil
}
