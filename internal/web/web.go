// Package web serves the authenticated dashboard and the public login shell.
package web

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

var hashedAsset = regexp.MustCompile(`(^|[.-])[A-Za-z0-9_-]{8,}\.(js|css|woff2?|ttf|svg|png)$`)

// Static 优先使用开发目录；发布构建直接从二进制内的只读文件系统提供 UI。
func Static(directory string) http.Handler {
	files := bundled
	if directory != "" {
		files = os.DirFS(directory)
	}
	return static(files)
}

func static(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if serveLogin(w, r) {
			return
		}
		// 使用退役 Worker 替换旧版 XD 的缓存 Worker，避免升级后一直加载旧 UI。
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			// Gin 的 NoRoute 初始为 404；静态成功响应必须显式覆盖。
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				w.Write([]byte(retireWorker))
			}
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		// 不做 SPA 路由回退。XD 使用 hash 路由，API/缺失资源必须明确返回 404。
		if !fs.ValidPath(name) || strings.Contains(name, `\`) || strings.HasPrefix(name, "api/") || strings.HasPrefix(name, "mihomo/") {
			http.NotFound(w, r)
			return
		}
		if files == nil {
			if name == "index.html" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					w.Write([]byte("MihomoHub backend development build. Run scripts/build-frontend.sh for the embedded dashboard.\n"))
				}
				return
			}
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(files, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(name, "_nuxt/") && hashedAsset.MatchString(path.Base(name)) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.FileServer(http.FS(files)).ServeHTTP(w, r)
	})
}

const retireWorker = `self.addEventListener('install',()=>self.skipWaiting());
self.addEventListener('activate',event=>event.waitUntil((async()=>{
 for(const key of await caches.keys()) if(key.startsWith('workbox-precache-')) await caches.delete(key);
 await self.registration.unregister();
 for(const client of await self.clients.matchAll({type:'window'})) await client.navigate(client.url);
})()));`
