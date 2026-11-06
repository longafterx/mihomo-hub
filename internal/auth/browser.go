package auth

import (
	"crypto/subtle"
	"net/http"
	"net/url"
)

const CookieName = "mihomohub_session"

type Browser struct {
	Origin string
}

// SameOrigin is the equivalent CSRF protection used by unchanged XD runtime writes.
// Never derive the expected origin from untrusted forwarding headers.
func (b Browser) SameOrigin(r *http.Request, requireOrigin bool) bool {
	expected, err := url.Parse(b.Origin)
	if err != nil || r.Host != expected.Host {
		return false
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin == b.Origin
	}
	if requireOrigin {
		return false
	}
	referer, err := url.Parse(r.Referer())
	if err != nil || referer.User != nil {
		return false
	}
	return referer.Scheme == expected.Scheme && referer.Host == expected.Host
}

func ValidCSRF(session *Session, token string) bool {
	return subtle.ConstantTimeCompare([]byte(session.CSRF), []byte(token)) == 1
}

func (b Browser) Cookie(token string, maxAge int) *http.Cookie {
	u, _ := url.Parse(b.Origin)
	return &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: u.Scheme == "https",
	}
}
