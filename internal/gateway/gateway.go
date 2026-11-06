package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/longafterx/mihomo-hub/internal/auth"
	"github.com/longafterx/mihomo-hub/internal/config"
	"github.com/longafterx/mihomo-hub/internal/instance"
	"github.com/longafterx/mihomo-hub/internal/mihomo"
)

type Gateway struct {
	instances *instance.Service
	transport *http.Transport
	browser   auth.Browser
	config    config.Config
	logger    *slog.Logger
	slots     chan struct{}
}

func New(c config.Config, instances *instance.Service, logger *slog.Logger) *Gateway {
	policy := mihomo.Policy{AllowPublic: c.Gateway.AllowPublicControllers, AllowedPorts: c.Gateway.AllowedPorts}
	return &Gateway{
		instances: instances, transport: policy.Transport(), browser: auth.Browser{Origin: c.Server.PublicOrigin},
		config: c, logger: logger, slots: make(chan struct{}, c.Gateway.MaxWebSockets),
	}
}

func (g *Gateway) Close() { g.transport.CloseIdleConnections() }

// Serve is called only after platform Session validation. The Session context
// cancels live upgrades on logout, expiration and server shutdown.
func (g *Gateway) Serve(w http.ResponseWriter, r *http.Request, session *auth.Session) {
	id, escapedPath, err := splitPath(r.URL.EscapedPath())
	if err != nil {
		http.Error(w, "invalid gateway path", http.StatusBadRequest)
		return
	}
	item, generation, err := g.instances.Acquire(id)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, instance.ErrDisabled) {
			status = http.StatusForbidden
		}
		http.Error(w, "instance unavailable", status)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "invalid query", http.StatusBadRequest)
		return
	}
	for key := range query {
		if strings.EqualFold(key, "token") {
			http.Error(w, "use platform session authentication", http.StatusBadRequest)
			return
		}
	}
	ws := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if !allowed(r.Method, escapedPath, g.config.Gateway.AllowRuntimeWrites) {
		http.Error(w, "operation is not permitted", http.StatusForbidden)
		return
	}
	if ws && !streamPath(escapedPath) {
		http.Error(w, "websocket path is not permitted", http.StatusForbidden)
		return
	}
	write := r.Method != http.MethodGet && r.Method != http.MethodHead
	if (ws || write) && !g.browser.SameOrigin(r, ws) {
		http.Error(w, "same-origin request required", http.StatusForbidden)
		return
	}
	if ws {
		select {
		case g.slots <- struct{}{}:
			defer func() { <-g.slots }()
		default:
			http.Error(w, "websocket capacity exceeded", http.StatusServiceUnavailable)
			return
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stopGeneration := context.AfterFunc(generation, cancel)
	defer stopGeneration()
	stopSession := context.AfterFunc(session.Context, cancel)
	defer stopSession()
	if generation.Err() != nil || session.Context.Err() != nil {
		cancel()
	}
	if !ws {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, time.Duration(g.config.Gateway.RequestTimeoutSeconds)*time.Second)
		defer timeoutCancel()
	}
	r = r.WithContext(ctx)
	if !g.prepareBody(w, r, escapedPath) {
		return
	}
	policy := mihomo.Policy{
		AllowPublic: g.config.Gateway.AllowPublicControllers, AllowedPorts: g.config.Gateway.AllowedPorts,
	}
	target, err := policy.Parse(item.ControllerURL)
	if err != nil {
		http.Error(w, "controller unavailable", http.StatusBadGateway)
		return
	}
	proxy := &httputil.ReverseProxy{
		Transport: g.transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.Host = target.Host
			pr.Out.URL.RawPath = escapedPath
			pr.Out.URL.Path, _ = url.PathUnescape(escapedPath)
			// Rewrite normally sanitizes queries. This query has been validated above;
			// restore it byte-for-byte for duplicate keys, '+' and percent-escapes.
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Header = upstreamHeaders(pr.Out.Header, ws)
			pr.Out.Trailer = nil
			if item.Secret != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+item.Secret)
				if ws {
					separator := ""
					if pr.Out.URL.RawQuery != "" {
						separator = "&"
					}
					pr.Out.URL.RawQuery += separator + "token=" + url.QueryEscape(item.Secret)
				}
			}
		},
		ModifyResponse: func(response *http.Response) error {
			return g.filterResponse(response, ws)
		},
		// ReverseProxy's raw errors can contain URLs; only ErrorHandler emits logs.
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			// Transport errors may contain token URLs; deliberately log a stable code only.
			status := http.StatusBadGateway
			if errors.Is(req.Context().Err(), context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
			g.logger.Warn("gateway request failed", "instance_id", id, "error_code", "upstream_unavailable")
			http.Error(w, "controller request failed", status)
		},
	}
	if write {
		g.logger.Info("gateway runtime write", "instance_id", id, "method", r.Method)
	}
	proxy.ServeHTTP(&hijackWriter{ResponseWriter: w, ctx: ctx}, r)
}

func splitPath(raw string) (string, string, error) {
	parts := strings.SplitN(strings.TrimPrefix(raw, "/mihomo/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || strings.Contains(parts[0], "%") {
		return "", "", errors.New("invalid path")
	}
	path := "/" + parts[1]
	for _, segment := range strings.Split(parts[1], "/") {
		decoded, err := url.PathUnescape(segment)
		badDot := decoded == "." || decoded == ".."
		if err != nil || decoded == "" || badDot || strings.ContainsAny(decoded, "\\\r\n\x00") {
			return "", "", errors.New("invalid path segment")
		}
	}
	return parts[0], path, nil
}

func streamPath(path string) bool {
	return path == "/connections" || path == "/traffic" || path == "/memory" || path == "/logs"
}

func allowed(method, path string, writes bool) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if method == http.MethodGet {
		switch parts[0] {
		case "version", "configs", "connections", "traffic", "memory", "logs", "rules":
			return len(parts) == 1
		case "proxies":
			return len(parts) <= 2 || len(parts) == 3 && parts[2] == "delay"
		case "group":
			return len(parts) == 1 || len(parts) == 3 && parts[2] == "delay"
		case "providers":
			if len(parts) < 2 || (parts[1] != "proxies" && parts[1] != "rules") {
				return false
			}
			return len(parts) <= 3 || len(parts) <= 5 && parts[len(parts)-1] == "healthcheck"
		}
	}
	if !writes {
		return false
	}
	switch method {
	case http.MethodPut:
		return len(parts) == 2 && parts[0] == "proxies"
	case http.MethodDelete:
		return (parts[0] == "connections" && len(parts) <= 2) || (parts[0] == "proxies" && len(parts) == 2)
	case http.MethodPatch:
		return path == "/configs" || path == "/rules/disable"
	}
	return false
}

func upstreamHeaders(headers http.Header, ws bool) http.Header {
	clean := http.Header{}
	for key, values := range headers {
		canonical := http.CanonicalHeaderKey(key)
		ordinary := canonical == "Accept" || canonical == "Content-Type" || canonical == "Accept-Encoding"
		conditional := strings.HasPrefix(canonical, "If-") || canonical == "Range"
		upgradeHeader := canonical == "Upgrade" || canonical == "Connection"
		websocket := ws && (strings.HasPrefix(canonical, "Sec-Websocket-") || upgradeHeader)
		if ordinary || conditional || websocket {
			clean[key] = append([]string{}, values...)
		}
	}
	// No negotiated compression means limits can be enforced on actual message bytes.
	clean.Del("Sec-WebSocket-Extensions")
	return clean
}

func (g *Gateway) prepareBody(w http.ResponseWriter, r *http.Request, path string) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, g.config.Gateway.MaxBodyBytes))
	if err != nil {
		http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return false
	}
	if r.Method == http.MethodPatch && path == "/configs" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil || len(fields) == 0 {
			http.Error(w, "invalid runtime config patch", http.StatusBadRequest)
			return false
		}
		for key := range fields {
			switch key {
			case "mode", "log-level", "ipv6", "sniffing", "tcp-concurrent", "unified-delay", "find-process-mode":
			default:
				http.Error(w, "config field is not permitted in P0", http.StatusForbidden)
				return false
			}
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return true
}

func (g *Gateway) filterResponse(response *http.Response, ws bool) error {
	redirect := response.StatusCode >= 300 && response.StatusCode < 400 && response.StatusCode != http.StatusNotModified
	if redirect {
		return errors.New("upstream redirects are disabled")
	}
	for key := range response.Header {
		lower := strings.ToLower(key)
		forwarded := strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "access-control-")
		sensitive := lower == "set-cookie" || lower == "authorization" || lower == "www-authenticate"
		if forwarded || sensitive || lower == "location" {
			response.Header.Del(key)
		}
	}
	response.Header.Set("Cache-Control", "no-store")
	response.Header.Set("X-Content-Type-Options", "nosniff")
	response.Trailer = nil
	response.Header.Del("Trailer")
	if ws && response.StatusCode == http.StatusSwitchingProtocols {
		body, ok := response.Body.(io.ReadWriteCloser)
		if !ok {
			return errors.New("upstream websocket is not duplex")
		}
		response.Body = newGuardedSocket(body, uint64(g.config.Gateway.MaxWSMessageBytes))
		return nil
	}
	if response.StatusCode == http.StatusSwitchingProtocols {
		return errors.New("unsolicited protocol upgrade")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, g.config.Gateway.MaxResponseBytes+1))
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return errors.New("read upstream response failed")
	}
	if int64(len(body)) > g.config.Gateway.MaxResponseBytes {
		return errors.New("upstream response exceeds limit")
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return nil
}
