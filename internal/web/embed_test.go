//go:build frontend

package web

import (
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRealEmbeddedDashboard(t *testing.T) {
	if !Embedded {
		t.Fatal("frontend not embedded")
	}
	w := httptest.NewRecorder()
	Static("").ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "__NUXT") {
		t.Fatal("embedded Nuxt shell missing")
	}
	entries, err := fs.ReadDir(bundled, "_nuxt")
	if err != nil || len(entries) == 0 {
		t.Fatal("_nuxt excluded from embed", err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".js") {
			continue
		}
		asset := httptest.NewRecorder()
		Static("").ServeHTTP(asset, httptest.NewRequest("GET", "/_nuxt/"+entry.Name(), nil))
		if asset.Code != 200 || !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
			t.Fatal(asset.Code, asset.Header())
		}
		if !strings.Contains(asset.Header().Get("Content-Type"), "javascript") {
			t.Fatal(asset.Header())
		}
		return
	}
	t.Fatal("embedded JS missing")
}
