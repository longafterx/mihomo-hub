package httpapi

import (
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/longafterx/mihomo-hub/internal/auth"
)

// 首次设置尚无 Session，使用与登录相同的同源校验、请求限流和容量限制。
func (a *api) allowAuthentication(c *gin.Context) bool {
	if !a.browser.SameOrigin(c.Request, false) {
		fail(c, http.StatusForbidden, "origin_rejected", "same-origin request required")
		return false
	}
	peer, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		peer = c.Request.RemoteAddr
	}
	if !a.loginLimit.Allow(peer) {
		fail(c, http.StatusTooManyRequests, "rate_limited", "too many login attempts")
		return false
	}
	return true
}

func (a *api) setupStatus(c *gin.Context) {
	required, err := a.Auth.SetupRequired(c.Request.Context())
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "auth_unavailable", "authentication unavailable")
		return
	}
	respond(c, http.StatusOK, gin.H{
		"setup_required": required, "min_password_bytes": auth.MinPasswordBytes,
		"max_password_bytes": auth.MaxPasswordBytes,
	})
}

func (a *api) setup(c *gin.Context) {
	if !a.allowAuthentication(c) {
		return
	}
	var input struct {
		Password     string `json:"password"`
		Confirmation string `json:"confirm_password"`
	}
	if !decode(c, &input, 8192) {
		return
	}
	token, session, err := a.Auth.Setup(c.Request.Context(), input.Password, input.Confirmation)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrAlreadyInitialized):
			fail(c, http.StatusConflict, "already_initialized", "password already set; please log in")
		case errors.Is(err, auth.ErrInvalidPassword):
			fail(c, http.StatusBadRequest, "invalid_password", err.Error())
		default:
			fail(c, http.StatusServiceUnavailable, "auth_unavailable", "password setup unavailable; retry later")
		}
		return
	}
	a.authenticated(c, token, session)
}

func (a *api) login(c *gin.Context) {
	if !a.allowAuthentication(c) {
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
		if errors.Is(err, auth.ErrSetupRequired) {
			fail(c, http.StatusConflict, "setup_required", "set your password before logging in")
			return
		}
		if auditErr := a.Audit(c.Request.Context(), "auth.login", "", "failure"); auditErr != nil {
			a.Logger.Error("login audit failed")
		}
		fail(c, status, code, "login failed")
		return
	}
	a.authenticated(c, token, session)
}

func (a *api) authenticated(c *gin.Context, token string, session *auth.Session) {
	if err := a.Audit(c.Request.Context(), "auth.login", "", "success"); err != nil {
		a.Auth.Logout(token)
		fail(c, http.StatusServiceUnavailable, "audit_unavailable", "login unavailable")
		return
	}
	// 认证成功后轮换浏览器中的旧会话，避免继续复用旧凭据。
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
