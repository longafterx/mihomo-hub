package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/analytics"
)

func (a *api) history(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		result, err := a.Analytics.Query(c.Request.Context(), kind, c.Request.URL.Query())
		if err != nil {
			switch {
			case errors.Is(err, analytics.ErrInvalid):
				fail(c, 400, "invalid_query", err.Error())
			case errors.Is(err, analytics.ErrNotFound):
				fail(c, 404, "instance_not_found", "history instance not found")
			default:
				a.Logger.Error("analytics query failed", "request_id", c.GetString("request_id"), "error_code", "storage_error")
				fail(c, 503, "analytics_unavailable", "history query unavailable")
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": result.Data, "meta": result.Meta, "error": nil})
	}
}
func (a *api) systemStatus(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	result, err := a.SystemStatus(ctx)
	if err != nil {
		fail(c, 503, "status_unavailable", "diagnostics unavailable")
		return
	}
	respond(c, 200, result)
}
