package httpapi

import (
	"context"
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
//
// 两个可选参数：
//   - w=<px>  显示宽度，交给图床自己缩放（只有 beeg 的图床认，见 withImageWidth）
//   - s=<px>  隐私模式要的小糊图：在本地缩到 s 像素宽、糊一遍再发（见 imgblur.go）
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	platformKey := r.URL.Query().Get("p")
	smallWidth := smallWidthParam(r.URL.Query().Get("s"))
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		servePlaceholder(w)
		return
	}
	// 小图不要 w：糊图跟显示宽度无关，带上 w 只会让同一张图按每种宽度各缓存一份。
	if smallWidth == 0 {
		u = withImageWidth(u, r.URL.Query().Get("w"))
	}
	raw = u.String()

	// 原图缓存键沿用老口径（平台 + 上游地址），别因为加了个参数就把已有的缓存全作废。
	origPath := s.imgCachePath(platformKey, raw)
	if smallWidth > 0 {
		// 小图单独一份缓存，别把原图顶掉——关掉隐私模式还要用。
		smallPath := s.imgCachePath(platformKey, fmt.Sprintf("%s|s=%d", raw, smallWidth))
		if data, ok := readCache(smallPath); ok {
			serveCachedBytes(w, data)
			return
		}
		// 手上有原图就先在本地缩，省一次上游请求。
		if src, ok := readCache(origPath); ok {
			if small, err := s.blurredSmallAny(r.Context(), src, smallWidth); err == nil {
				writeCache(smallPath, small)
				serveCachedBytes(w, small)
				return
			}
			// 解不出来——缓存里的这份多半是早先按 `Accept: image/avif` 抓下来的 AVIF，
			// Go 解不了。落到下面按「能解的格式」重抓一份，只给这张小图用。
		}
		src, ok := s.fetchImage(r.Context(), raw, platformKey, decodableAccept)
		if !ok {
			s.log.Warn("隐私模式小图抓取失败，发空白占位", "url", raw)
			servePlaceholder(w)
			return
		}
		small, err := s.blurredSmallAny(r.Context(), src, smallWidth)
		if err != nil {
			// 还是解不出来就发空白，绝不把原图漏出去——隐私模式的失败方式只能是「看不见」。
			s.log.Warn("隐私模式小图解码失败，发空白占位", "url", raw, "err", err)
			servePlaceholder(w)
			return
		}
		writeCache(smallPath, small)
		serveCachedBytes(w, small)
		return
	}

	if data, ok := readCache(origPath); ok {
		serveCachedBytes(w, data)
		return
	}
	data, ok := s.fetchImage(r.Context(), raw, platformKey, passthroughAccept)
	if !ok {
		servePlaceholder(w)
		return
	}
	writeCache(origPath, data)
	serveCachedBytes(w, data)
}

// smallWidthParam 解析 s= 参数：只收 8–256，别的都当没写。
func smallWidthParam(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 8 || n > 256 {
		return 0
	}
	return n
}

func (s *Server) imgCachePath(platformKey, key string) string {
	sum := sha256.Sum256([]byte(platformKey + "|" + key))
	hash := hex.EncodeToString(sum[:])
	return filepath.Join(s.cfg.DataDir, "imgcache", hash[:2], hash+".bin")
}

func readCache(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

func writeCache(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// 两个 Accept：正常显示随便什么格式都行（浏览器都认，avif 最省），
// 但隐私模式的小图要在本地解码，只能用 Go 解得了的格式。
const (
	passthroughAccept = "image/avif,image/webp,image/*,*/*;q=0.8"
	decodableAccept   = "image/webp,image/png,image/jpeg,image/*;q=0.8"
)

// fetchImage 带着站点 Referer / UA 把上游图片抓下来（失败返回 ok=false）。
func (s *Server) fetchImage(ctx context.Context, raw, platformKey, accept string) ([]byte, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Referer", refererFor(u))
	req.Header.Set("Accept", accept)

	// 跨站头像（beeg 图床）要用 beeg 自己的代理配置，别跟着 Pornhub 的走。
	resp, err := s.platformClient(platform.ImageHostPlatform(raw, platformKey)).Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

func servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	w.Write(placeholderPNG)
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
