package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticCacheAndIsolation(t *testing.T) {
	t.Parallel()
	handler := static(fstest.MapFS{
		"index.html":              {Data: []byte("<html>hub</html>")},
		"_nuxt/entry.ABCDef12.js": {Data: []byte("export default 1")},
		"config.js":               {Data: []byte("window.config={}")},
	})
	for _, tc := range []struct {
		name, method, path string
		status             int
		immutable          bool
	}{
		{"html", "GET", "/", 200, false},
		{"head", "HEAD", "/", 200, false},
		{"hashed JS", "GET", "/_nuxt/entry.ABCDef12.js", 200, true},
		{"config", "GET", "/config.js", 200, false},
		{"retired worker", "GET", "/sw.js", 200, false},
		{"API never falls back", "GET", "/api/v1/missing", 404, false},
		{"missing asset", "GET", "/_nuxt/missing.js", 404, false},
		{"directory", "GET", "/_nuxt/", 404, false},
		{"traversal", "GET", "/../index.html", 404, false},
		{"encoded traversal", "GET", "/%2e%2e/index.html", 404, false},
		{"write", "POST", "/", 405, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status=%d, want=%d", w.Code, tc.status)
			}
			if strings.Contains(w.Header().Get("Cache-Control"), "immutable") != tc.immutable {
				t.Fatal(w.Header())
			}
			if tc.method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD returned body")
			}
			if tc.path == "/sw.js" && !strings.Contains(w.Body.String(), "unregister") {
				t.Fatal("old worker not retired")
			}
		})
	}
}
