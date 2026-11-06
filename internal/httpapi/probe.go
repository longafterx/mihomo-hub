package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
)

func (a *api) testDraft(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	var input struct {
		InstanceID    string  `json:"instance_id"`
		ControllerURL *string `json:"controller_url"`
		Secret        *string `json:"secret"`
	}
	if !decode(c, &input, a.Config.Gateway.MaxBodyBytes) {
		return
	}
	var item instance.Instance
	if input.InstanceID != "" {
		var err error
		item, err = a.Instances.Get(input.InstanceID)
		if err != nil {
			serviceError(c, err)
			return
		}
	}
	if input.ControllerURL != nil {
		item.ControllerURL = *input.ControllerURL
	}
	if input.Secret != nil {
		item.Secret = *input.Secret
	}
	policy := mihomo.Policy{AllowPublic: a.Config.Gateway.AllowPublicControllers, AllowedPorts: a.Config.Gateway.AllowedPorts}
	invalidSecret := len(item.Secret) > 4096 || strings.ContainsAny(item.Secret, "\r\n")
	if invalidSecret {
		serviceError(c, instance.ErrInvalid)
		return
	}
	if err := policy.Validate(ctx, item.ControllerURL); err != nil {
		switch {
		case errors.Is(err, mihomo.ErrControllerDNS):
			fail(c, http.StatusBadGateway, "controller_dns_failed", "controller DNS lookup failed")
		case errors.Is(err, context.DeadlineExceeded):
			fail(c, http.StatusBadGateway, "controller_timeout", "controller request timed out")
		default:
			serviceError(c, instance.ErrInvalid)
		}
		return
	}
	if err := a.Audit(ctx, "instance.test_draft", item.ID, "attempt"); err != nil {
		fail(c, 503, "audit_unavailable", "probe unavailable")
		return
	}
	// 测试未保存的表单仅 GET /version，不创建实例或切换现有采集器。
	version, err := policy.Probe(ctx, item.ControllerURL, item.Secret)
	if err != nil {
		probeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"reachable": true, "version": version})
}

func (a *api) probe(c *gin.Context) {
	item, generation, err := a.Instances.Acquire(c.Param("id"))
	if err != nil {
		if errors.Is(err, instance.ErrDisabled) {
			fail(c, http.StatusForbidden, "instance_disabled", "instance disabled")
			return
		}
		serviceError(c, err)
		return
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	stop := context.AfterFunc(generation, cancel)
	defer stop()
	if err := a.Audit(ctx, "instance.test", item.ID, "attempt"); err != nil {
		fail(c, 503, "audit_unavailable", "probe unavailable")
		return
	}
	version, err := (mihomo.Policy{AllowPublic: a.Config.Gateway.AllowPublicControllers, AllowedPorts: a.Config.Gateway.AllowedPorts}).Probe(ctx, item.ControllerURL, item.Secret)
	if err != nil {
		probeError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"reachable": true, "version": version})
}

func probeError(c *gin.Context, err error) {
	// 只允许经过脱敏的固定原因进入 API；原始网络错误可能包含敏感 URL。
	var diagnostic *mihomo.ProbeError
	if errors.As(err, &diagnostic) {
		fail(c, http.StatusBadGateway, diagnostic.Code, diagnostic.Error())
		return
	}
	fail(c, http.StatusBadGateway, "controller_unavailable", "Controller version probe failed")
}
