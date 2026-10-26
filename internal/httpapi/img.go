package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mario/myporn/internal/platform"
)

// placeholderPNG 是 1x1 透明 PNG，图片代理失败时返回，避免前端出现破图。
var placeholderPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func (s *Server) platformClient(key string) *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.platformClientLocked(key)
}

// platformClientLocked 供已持锁的调用方使用（streamClient 会复制它）。
func (s *Server) platformClientLocked(key string) *http.Client {
	if c, ok := s.clients[key]; ok {
		return c
	}
	c, err := platform.NewHTTPClient(s.cfg.PlatformProxyFor(key), 20*time.Second)
	if err != nil {
		c, _ = platform.NewHTTPClient("", 20*time.Second)
	}
	s.clients[key] = c
	return c
}

// handleImage 代理远端图片/预览视频，并在本地磁盘缓存，规避 Referer 校验与 403。
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	platformKey := r.URL.Query().Get("p")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		w.Header().Set("Content-Type", "image/png")
		w.Write(placeholderPNG)
		return
	}
	// 前端要的宽度交给图床自己缩放（beeg 图床支持），缓存键也用缩放后的地址。
	u = withImageWidth(u, r.URL.Query().Get("w"))
	raw = u.String()

	sum := sha256.Sum256([]byte(platformKey + "|" + raw))
	hash := hex.EncodeToString(sum[:])
	cachePath := filepath.Join(s.cfg.DataDir, "imgcache", hash[:2], hash+".bin")

	if data, err := os.ReadFile(cachePath); err == nil && len(data) > 0 {
		serveCachedBytes(w, data)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil)
	if err != nil {
		w.Header().Set("Content-Type", "image/png")
		w.Write(placeholderPNG)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Referer", refererFor(u))
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")

	// 跨站头像（beeg 图床）要用 beeg 自己的代理配置，别跟着 Pornhub 的走。
	resp, err := s.platformClient(platform.ImageHostPlatform(raw, platformKey)).Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "image/png")
		w.Write(placeholderPNG)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		w.Header().Set("Content-Type", "image/png")
		w.Write(placeholderPNG)
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil || len(data) == 0 {
		w.Header().Set("Content-Type", "image/png")
		w.Write(placeholderPNG)
		return
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		tmp := cachePath + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err == nil {
			_ = os.Rename(tmp, cachePath)
		}
	}
	serveCachedBytes(w, data)
}

func serveCachedBytes(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// refererOverrides 按域名后缀指定 Referer：beeg 的图床反过来拒绝自家根域的
// Referer（https://externulls.com/ 会被 403），只认 beeg.com。
var refererOverrides = map[string]string{
	"externulls.com": "https://beeg.com/",
	"twimg.com":      "https://x.com/",
}

// withImageWidth 把前端的 w= 翻译成图床的服务端缩放参数。
// 目前只有 beeg 的照片图床（thumbs.externulls.com/photos/.../to.webp）支持 size_new；
// 其他图床不认识这个参数，原样返回。
func withImageWidth(u *url.URL, width string) *url.URL {
	w, err := strconv.Atoi(strings.TrimSpace(width))
	if err != nil || w <= 0 || w > 2048 {
		return u
	}
	host := strings.ToLower(u.Hostname())
	if host != "externulls.com" && !strings.HasSuffix(host, ".externulls.com") {
		return u
	}
	if !strings.Contains(u.Path, "/photos/") {
		return u
	}
	q := u.Query()
	if q.Get("size_new") != "" {
		return u
	}
	q.Set("size_new", fmt.Sprintf("%dx%d", w, w))
	out := *u
	out.RawQuery = q.Encode()
	return &out
}

func refererFor(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	for suffix, ref := range refererOverrides {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return ref
		}
	}
	// 图片通常在 CDN 上，但防盗链校验的是站点 Referer，这里用根域近似。
	parts := strings.Split(u.Host, ".")
	if len(parts) >= 2 {
		root := strings.Join(parts[len(parts)-2:], ".")
		return "https://" + root + "/"
	}
	return u.Scheme + "://" + u.Host + "/"
}
