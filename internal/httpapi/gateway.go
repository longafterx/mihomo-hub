package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 原生 Mihomo 写操作先完成限流、同源校验和审计，再交给 Gateway 透传。
func (a *api) proxy(c *gin.Context) {
	session := getSession(c)
	if c.Request.Method != http.MethodGet {
		if !a.writeLimit.Allow(session.CSRF) {
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		if !a.browser.SameOrigin(c.Request, false) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if err := a.Audit(c.Request.Context(), "gateway."+c.Request.Method, c.Param("id"), "attempt"); err != nil {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
	}
	a.Gateway.Serve(c.Writer, c.Request, session)
}
