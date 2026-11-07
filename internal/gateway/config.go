package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
)

// LoadConfig 仅供已鉴权的平台 DNS 服务应用完整配置；不向浏览器开放任意 payload 透传。
func (g *Gateway) LoadConfig(ctx context.Context, item instance.Instance, payload string) error {
	if !g.config.Gateway.AllowRuntimeWrites {
		return errors.New("runtime writes disabled")
	}
	policy := mihomo.Policy{AllowPublic: g.config.Gateway.AllowPublicControllers, AllowedPorts: g.config.Gateway.AllowedPorts}
	target, err := policy.Parse(item.ControllerURL)
	if err != nil {
		return errors.New("controller unavailable")
	}
	data, err := json.Marshal(map[string]string{"path": "", "payload": payload})
	if err != nil || int64(len(data)) > g.config.Gateway.MaxBodyBytes {
		return errors.New("full config exceeds request limit")
	}
	target.Path, target.RawQuery = "/configs", "force=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), bytes.NewReader(data))
	if err != nil {
		return errors.New("cannot create config load request")
	}
	req.Header.Set("Content-Type", "application/json")
	if item.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+item.Secret)
	}
	// RoundTrip 不跟随跳转，也不会因断线重试非幂等的配置加载。
	response, err := g.transport.RoundTrip(req)
	if err != nil {
		return errors.New("config load not confirmed")
	}
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, g.config.Gateway.MaxResponseBytes+1))
	if err != nil || response.StatusCode != http.StatusNoContent {
		return errors.New("config load not accepted")
	}
	return nil
}
