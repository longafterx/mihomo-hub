package auth_test

import (
	"net/http/httptest"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/auth"
)

func TestSameOrigin(t *testing.T) {
	t.Parallel()
	b := auth.Browser{Origin: "https://hub.example"}
	for _, tt := range []struct {
		name    string
		origin  string
		referer string
		ws      bool
		ok      bool
	}{
		{name: "same origin write", origin: b.Origin, ok: true},
		{name: "same origin websocket", origin: b.Origin, ws: true, ok: true},
		{name: "cross origin", origin: "https://evil.example"},
		{name: "null origin", origin: "null"},
		{name: "missing origin"},
		{name: "referer fallback", referer: b.Origin + "/#/proxies", ok: true},
		{name: "websocket requires origin", referer: b.Origin, ws: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("PUT", b.Origin+"/mihomo/id/proxies/group", nil)
			r.Header.Set("Origin", tt.origin)
			r.Header.Set("Referer", tt.referer)
			if b.SameOrigin(r, tt.ws) != tt.ok {
				t.Fatal("unexpected origin decision")
			}
		})
	}
	cookie := b.Cookie("opaque", 3600)
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" {
		t.Fatal("unsafe session cookie")
	}
}
