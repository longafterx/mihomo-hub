package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/runtimeconfig"
)

func (a *api) runtimeConfig(c *gin.Context) {
	result, err := a.RuntimeConfig.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		a.runtimeConfigError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func (a *api) importRuntimeConfig(c *gin.Context) {
	if !a.runtimeWrites(c) {
		return
	}
	var input struct {
		YAML     string `json:"yaml"`
		Revision int64  `json:"revision"`
	}
	if !decode(c, &input, a.Config.Gateway.MaxBodyBytes) {
		return
	}
	result, err := a.RuntimeConfig.Import(c.Request.Context(), c.Param("id"), input.YAML, input.Revision)
	if err != nil {
		a.runtimeConfigError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func (a *api) applyDNS(c *gin.Context) {
	if !a.runtimeWrites(c) {
		return
	}
	var input struct {
		DNS      runtimeconfig.DNS `json:"dns"`
		Revision int64             `json:"revision"`
	}
	if !decode(c, &input, a.Config.Gateway.MaxBodyBytes) {
		return
	}
	timeout := time.Duration(a.Config.Gateway.MaintenanceTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	session := getSession(c)
	stop := context.AfterFunc(session.Context, cancel)
	defer stop()
	if session.Context.Err() != nil {
		cancel()
	}
	if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(timeout + 5*time.Second)); err != nil {
		a.Logger.Debug("runtime config response deadline unavailable")
	}
	if err := a.Audit(ctx, "runtime_config.apply", c.Param("id"), "attempt"); err != nil {
		fail(c, http.StatusServiceUnavailable, "audit_unavailable", "could not record config change")
		return
	}
	result, err := a.RuntimeConfig.ApplyDNS(ctx, c.Param("id"), input.DNS, input.Revision)
	if err != nil {
		a.runtimeConfigError(c, err)
		return
	}
	respond(c, http.StatusOK, result)
}

func (a *api) runtimeWrites(c *gin.Context) bool {
	if !a.Config.Gateway.AllowRuntimeWrites {
		fail(c, http.StatusForbidden, "runtime_writes_disabled", "runtime writes are disabled")
		return false
	}
	return true
}

func (a *api) runtimeConfigError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, runtimeconfig.ErrInvalid):
		fail(c, 400, "invalid_runtime_config", "provide a single complete YAML and valid DNS settings")
	case errors.Is(err, runtimeconfig.ErrMissing):
		fail(c, 409, "runtime_config_required", "import the full YAML for this controller first")
	case errors.Is(err, runtimeconfig.ErrConflict):
		fail(c, 409, "runtime_config_conflict", "config changed; refresh before saving")
	case errors.Is(err, runtimeconfig.ErrSaveAfterApply):
		fail(c, 503, "runtime_config_save_failed", "controller accepted config but Hub did not save it; refresh and import the effective config before retrying")
	case errors.Is(err, runtimeconfig.ErrUpstream):
		fail(c, 502, "runtime_config_unconfirmed", "controller did not confirm the config load; check its state before retrying")
	default:
		serviceError(c, err)
	}
}
