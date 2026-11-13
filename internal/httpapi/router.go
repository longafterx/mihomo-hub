package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
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

// New 装配 HTTP 路由与公共中间件；请求处理按职责放在同包文件中。
func New(d Dependencies) *gin.Engine {
	a := &api{
		Dependencies: d, browser: auth.Browser{Origin: d.Config.Server.PublicOrigin},
		loginLimit: auth.NewLimiter(10, time.Minute), writeLimit: auth.NewLimiter(120, time.Minute),
	}
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		panic(err) // 空代理列表是固定的程序约束，与用户配置无关。
	}
	// 保留原始路径与转义，避免框架重定向或解码改变 Mihomo 代理请求。
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
	r.GET("/api/v1/auth/setup", a.setupStatus)
	r.POST("/api/v1/auth/setup", a.setup)
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
	platform.GET("/instances/retained", a.listRetained)
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
	r.Any("/mihomo/:id/*path", a.requireSession, a.proxy)
	r.Any("/mihomo/:id", a.requireSession, a.proxy)
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
		if path == "/" || path == "/index.html" {
			required, err := a.Auth.SetupRequired(c.Request.Context())
			if err != nil {
				fail(c, http.StatusServiceUnavailable, "auth_unavailable", "authentication unavailable")
				return
			}
			if required {
				c.Redirect(http.StatusSeeOther, "/login")
				return
			}
		}
		a.Static.ServeHTTP(c.Writer, c.Request)
	})
	return r
}
