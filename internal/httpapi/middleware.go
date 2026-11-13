package httpapi

import (
	"crypto/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/auth"
)

func (a *api) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		id := rand.Text()
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		if !strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
			if err := http.NewResponseController(c.Writer).SetWriteDeadline(start.Add(35 * time.Second)); err != nil {
				a.Logger.Debug("response deadline unavailable")
			}
		}
		c.Next()
		// 日志只记录路由模板，避免代理名称、查询参数、Cookie 或令牌泄露。
		a.Logger.Info("http request", "request_id", id, "method", c.Request.Method,
			"route", c.FullPath(), "status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

func (a *api) requireSession(c *gin.Context) {
	cookie, err := c.Request.Cookie(auth.CookieName)
	if err != nil {
		a.unauthorized(c)
		return
	}
	session, ok := a.Auth.Lookup(cookie.Value)
	if !ok {
		a.unauthorized(c)
		return
	}
	c.Set("session", session)
	c.Next()
}

func (a *api) unauthorized(c *gin.Context) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/v1/") {
		fail(c, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	c.AbortWithStatus(http.StatusUnauthorized)
}

// 平台写操作必须同时通过同源和 CSRF 校验，再进入业务处理器。
func (a *api) csrf(c *gin.Context) {
	if !a.browser.SameOrigin(c.Request, false) || !auth.ValidCSRF(getSession(c), c.GetHeader("X-CSRF-Token")) {
		fail(c, http.StatusForbidden, "csrf_failed", "same-origin request and CSRF token required")
		return
	}
	if !a.writeLimit.Allow(getSession(c).CSRF) {
		fail(c, http.StatusTooManyRequests, "rate_limited", "too many changes; retry later")
		return
	}
	c.Next()
}

func getSession(c *gin.Context) *auth.Session {
	return c.MustGet("session").(*auth.Session)
}
