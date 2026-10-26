package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// serveSPA 服务嵌入的前端静态资源，非静态路径回落到 index.html（History 路由）。
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if s.static == nil {
		http.Error(w, "前端资源未构建，请先执行 `pnpm --dir web build` 并重新编译", http.StatusOK)
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" || p == "." {
		p = "index.html"
	}
	if !strings.HasPrefix(p, "assets/") && !strings.Contains(path.Base(p), ".") {
		// 前端路由（如 /performers、/video/12）统一返回 index.html
		p = "index.html"
	}
	if _, err := fs.Stat(s.static, p); err != nil {
		p = "index.html"
	}
	data, err := fs.ReadFile(s.static, p)
	if err != nil {
		http.Error(w, "index.html 缺失", http.StatusInternalServerError)
		return
	}
	switch {
	case strings.HasSuffix(p, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasSuffix(p, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasSuffix(p, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasSuffix(p, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	case strings.HasSuffix(p, ".webmanifest"):
		w.Header().Set("Content-Type", "application/manifest+json")
	case strings.HasSuffix(p, ".woff2"):
		w.Header().Set("Content-Type", "font/woff2")
	}
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
