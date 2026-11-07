package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/analytics"
	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/collector"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/gateway"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/runtimeconfig"
	"github.com/longafterx/mihomo-hub/internal/storage"
)

type Dependencies struct {
	Config           config.Config
	Auth             *auth.Service
	Instances        *instance.Service
	Gateway          *gateway.Gateway
	Logger           *slog.Logger
	Ready            func(context.Context) error
	Audit            func(context.Context, string, string, string) error
	Static           http.Handler
	Version          string
	Commit           string
	FrontendCommit   string
	BuildTime        string
	FrontendEmbedded bool
	Analytics        analytics.Service
	SystemStatus     func(context.Context) (any, error)
	Collector        *collector.Manager
	RuntimeConfig    *runtimeconfig.Service
}

type api struct {
	Dependencies
	browser    auth.Browser
	loginLimit *auth.Limiter
	writeLimit *auth.Limiter
}

func New(d Dependencies) *gin.Engine {
	a := &api{
		Dependencies: d, browser: auth.Browser{Origin: d.Config.Server.PublicOrigin},
		loginLimit: auth.NewLimiter(10, time.Minute), writeLimit: auth.NewLimiter(120, time.Minute),
	}
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		panic(err) // A nil proxy list is a programming invariant, not configuration.
	}
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.UseRawPath = true
	r.UnescapePathValues = false
	r.Use(a.accessLog())
	r.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		a.Logger.Error("request panic", "request_id", c.GetString("request_id"))
		fail(c, http.StatusInternalServerError, "internal_error", "internal server error")
	}))
	r.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok\n") })
	r.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := a.Ready(ctx); err != nil {
			c.String(http.StatusServiceUnavailable, "not ready\n")
			return
		}
		c.String(http.StatusOK, "ready\n")
	})
	r.POST("/api/v1/auth/login", a.login)
	platform := r.Group("/api/v1", a.requireSession)
	platform.GET("/auth/me", a.me)
	platform.POST("/auth/logout", a.csrf, a.logout)
	platform.GET("/meta", func(c *gin.Context) {
		respond(c, http.StatusOK, gin.H{
			"version": d.Version, "backend_commit": d.Commit, "api_version": "v1", "schema_version": storage.SchemaVersion,
			"frontend_commit": d.FrontendCommit, "frontend_embedded": d.FrontendEmbedded, "build_time": d.BuildTime,
		})
	})
	for _, kind := range []string{"summary", "traffic", "top", "connections", "gaps", "coverage"} {
		platform.GET("/analytics/"+kind, a.history(kind))
	}
	platform.GET("/system/status", a.systemStatus)
	platform.GET("/instances", a.list)
	platform.POST("/instances", a.csrf, a.create)
	platform.POST("/instances/test", a.csrf, a.testDraft)
	platform.GET("/instances/:id", a.get)
	platform.GET("/instances/:id/config", a.editConfig)
	platform.GET("/instances/:id/runtime-config", a.runtimeConfig)
	platform.PUT("/instances/:id/runtime-config", a.csrf, a.importRuntimeConfig)
	platform.PATCH("/instances/:id/runtime-config/dns", a.csrf, a.applyDNS)
	platform.GET("/instances/:id/status", a.status)
	platform.POST("/instances/:id/test", a.csrf, a.probe)
	platform.PATCH("/instances/:id", a.csrf, a.update)
	platform.DELETE("/instances/:id", a.csrf, a.remove)
	proxy := func(c *gin.Context) {
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
	r.Any("/mihomo/:id/*path", a.requireSession, proxy)
	r.Any("/mihomo/:id", a.requireSession, proxy)
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/mihomo") {
			if strings.HasPrefix(path, "/api/v1") {
				fail(c, http.StatusNotFound, "not_found", "route not found")
				return
			}
			c.Status(http.StatusNotFound)
			return
		}
		a.Static.ServeHTTP(c.Writer, c.Request)
	})
	return r
}

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
		// FullPath contains a route template, never a proxy name, query, Cookie or token.
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

func (a *api) login(c *gin.Context) {
	if !a.browser.SameOrigin(c.Request, false) {
		fail(c, http.StatusForbidden, "origin_rejected", "same-origin request required")
		return
	}
	peer, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		peer = c.Request.RemoteAddr
	}
	if !a.loginLimit.Allow(peer) {
		fail(c, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return
	}
	var input struct {
		Username string `json:"username"` // 兼容旧客户端字段，不参与单管理员密码验证。
		Password string `json:"password"`
	}
	if !decode(c, &input, 8192) {
		return
	}
	token, session, err := a.Auth.Login(c.Request.Context(), input.Password)
	if err != nil {
		status := http.StatusUnauthorized
		code := "invalid_credentials"
		if !errors.Is(err, auth.ErrCredentials) {
			status, code = http.StatusServiceUnavailable, "auth_unavailable"
		}
		if auditErr := a.Audit(c.Request.Context(), "auth.login", "", "failure"); auditErr != nil {
			a.Logger.Error("login audit failed")
		}
		fail(c, status, code, "login failed")
		return
	}
	if err := a.Audit(c.Request.Context(), "auth.login", "", "success"); err != nil {
		a.Auth.Logout(token)
		fail(c, http.StatusServiceUnavailable, "audit_unavailable", "login unavailable")
		return
	}
	// Rotate any prior session in this browser after successful authentication.
	if cookie, err := c.Request.Cookie(auth.CookieName); err == nil {
		a.Auth.Logout(cookie.Value)
	}
	http.SetCookie(c.Writer, a.browser.Cookie(token, int(time.Until(session.ExpiresAt).Seconds())))
	respond(c, http.StatusOK, sessionView(session))
}

func (a *api) me(c *gin.Context) { respond(c, http.StatusOK, sessionView(getSession(c))) }

func sessionView(s *auth.Session) gin.H {
	return gin.H{"csrf_token": s.CSRF, "expires_at_ms": s.ExpiresAt.UTC().UnixMilli()}
}

func (a *api) logout(c *gin.Context) {
	cookie, err := c.Request.Cookie(auth.CookieName)
	if err != nil {
		a.unauthorized(c)
		return
	}
	a.Auth.Logout(cookie.Value)
	http.SetCookie(c.Writer, a.browser.Cookie("", -1))
	if err := a.Audit(c.Request.Context(), "auth.logout", "", "success"); err != nil {
		a.Logger.Error("logout audit failed")
	}
	respond(c, http.StatusOK, gin.H{"logged_out": true})
}

func (a *api) list(c *gin.Context) {
	items := a.Instances.List()
	for i := range items {
		a.withStatus(&items[i])
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	respond(c, http.StatusOK, items)
}

func (a *api) get(c *gin.Context) {
	item, err := a.Instances.Get(c.Param("id"))
	if err != nil {
		serviceError(c, err)
		return
	}
	a.withStatus(&item)
	respond(c, http.StatusOK, item)
}

func (a *api) editConfig(c *gin.Context) {
	item, err := a.Instances.Get(c.Param("id"))
	if err != nil {
		serviceError(c, err)
		return
	}
	// 管理员编辑时按需读取地址；列表、运行 Endpoint 与响应中均不含 Secret。
	respond(c, http.StatusOK, gin.H{"controller_url": item.ControllerURL})
}

func (a *api) withStatus(item *instance.Instance) {
	item.CollectorStatus = "disabled"
	if item.Enabled && item.CollectEnabled {
		s := a.Collector.Status(item.ID, item.Generation)
		item.CollectorStatus = s.Status
		item.LastSeenAtMS = s.LastSeenAtMS
	}
}

func (a *api) status(c *gin.Context) {
	item, err := a.Instances.Get(c.Param("id"))
	if err != nil {
		serviceError(c, err)
		return
	}
	if !item.Enabled || !item.CollectEnabled {
		respond(c, 200, collector.Status{Status: "disabled"})
		return
	}
	respond(c, 200, a.Collector.Status(item.ID, item.Generation))
}

func (a *api) create(c *gin.Context) {
	var input instance.Create
	if !decode(c, &input, a.Config.Gateway.MaxBodyBytes) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	item, err := a.Instances.Create(ctx, input)
	if err != nil {
		serviceError(c, err)
		return
	}
	a.withStatus(&item)
	respond(c, http.StatusCreated, item)
}

func (a *api) update(c *gin.Context) {
	var input instance.Patch
	if !decode(c, &input, a.Config.Gateway.MaxBodyBytes) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	item, err := a.Instances.Update(ctx, c.Param("id"), input)
	if err != nil {
		serviceError(c, err)
		return
	}
	a.withStatus(&item)
	respond(c, http.StatusOK, item)
}

func (a *api) remove(c *gin.Context) {
	if err := a.Instances.Delete(c.Request.Context(), c.Param("id"), c.Query("history")); err != nil {
		serviceError(c, err)
		return
	}
	respond(c, http.StatusOK, gin.H{"deleted": true, "history": c.Query("history")})
}

func decode(c *gin.Context, dst any, limit int64) bool {
	d := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		fail(c, http.StatusBadRequest, "invalid_json", "invalid JSON body or unknown field")
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		fail(c, http.StatusBadRequest, "invalid_json", "request must contain exactly one JSON object")
		return false
	}
	return true
}

func serviceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, instance.ErrNotFound):
		fail(c, http.StatusNotFound, "instance_not_found", "instance not found")
	case errors.Is(err, instance.ErrConflict):
		fail(c, http.StatusConflict, "instance_conflict", "instance name already exists")
	case errors.Is(err, instance.ErrInvalid):
		fail(c, http.StatusBadRequest, "invalid_instance", "invalid name, secret, controller URL or history choice")
	default:
		fail(c, http.StatusInternalServerError, "storage_error", "could not persist instance change")
	}
}

func respond(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"data": data, "meta": gin.H{}, "error": nil})
}

func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"data": nil, "meta": gin.H{}, "error": gin.H{"code": code, "message": message},
	})
}
