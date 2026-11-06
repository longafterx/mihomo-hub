package web_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/longafterx/mihomo-hub/internal/web"
)

func TestStatic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("original XD build"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, method, path string
		status             int
	}{
		{name: "index", method: "GET", path: "/", status: 200},
		{name: "missing asset", method: "GET", path: "/missing.js", status: 404},
		{name: "write denied", method: "POST", path: "/", status: 405},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			web.Static(dir).ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			if w.Code != tt.status {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
}
