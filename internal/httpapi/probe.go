package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
)

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
		fail(c, http.StatusBadGateway, "controller_unavailable", "Controller version probe failed")
		return
	}
	respond(c, http.StatusOK, gin.H{"reachable": true, "version": version})
}
