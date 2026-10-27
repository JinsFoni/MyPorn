// Package imgcache 把远端图片抓到本地磁盘缓存，并负责隐私模式要的「小糊图」。
//
// 单独成包有两个原因：
//  1. 图床有 Referer / UA / 签名校验，浏览器直连会被 403，必须由服务端代抓并缓存；
//     这个逻辑除了 HTTP 代理要用，**同步任务也要用**——pornhub 的封面地址带签名，
//     有效期只有一天，必须趁同步时地址还新鲜就把它抓下来落盘。
//  2. 隐私模式的模糊不能靠 CSS 滤镜（一张图一个合成层 + 一遍高斯模糊，滚动直接掉帧），
//     要的是「服务器缩好、糊好、发一张 1KB 的小图」，浏览器只管拉伸。
//
// 缓存键是 sha256(平台 | 上游地址[|s=宽度])，落在 <DataDir>/imgcache/xx/<hash>.bin。
package imgcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mario/myporn/internal/platform"
)

// ErrBadURL 表示传进来的不是可代理的 http(s) 地址。
var ErrBadURL = errors.New("图片地址不合法")

// 两个 Accept：正常显示随便什么格式都行（浏览器都认，avif 最省），
// 但隐私模式的小图要在本地解码，只能用 Go 解得了的格式。
const (
	passthroughAccept = "image/avif,image/webp,image/*,*/*;q=0.8"
	decodableAccept   = "image/webp,image/png,image/jpeg,image/*;q=0.8"
)

// smallPipeline 是「小糊图」的生成流水线版本，进缓存键。
// 换过编码（JPEG → PNG）或改过缩放/模糊算法就得 +1，否则旧的丑图还会被端出来。
const smallPipeline = 3

// Service 是图片缓存的入口。构造一次，全局共用（进程内不会写坏同一份缓存：
// 写入是 tmp + rename 的原子替换）。
type Service struct {
	dir      string
	proxyFor func(platformKey string) string
	ffmpeg   string
	log      *slog.Logger

	mu      sync.Mutex
	clients map[string]*http.Client

	// maxBytes 是磁盘缓存上限（0 = 不限），见 evict.go。
	maxBytes int64
}

// New 构造图片缓存。proxyFor 给出各平台出网用的代理配置（可为 nil）；ffmpeg 为可执行文件路径。
func New(dir string, proxyFor func(platformKey string) string, ffmpeg string, log *slog.Logger) *Service {
	return &Service{dir: dir, proxyFor: proxyFor, ffmpeg: ffmpeg, log: log,
		clients: map[string]*http.Client{}}
}

// Bytes 取一张图（原图）。带 width 时先把显示宽度翻译成图床的缩放参数。
func (s *Service) Bytes(ctx context.Context, platformKey, raw, width string) ([]byte, bool) {
	upstream, err := normalize(raw, width)
	if err != nil {
		return nil, false
	}
	path := s.path(platformKey, upstream)
	if data, ok := readCache(path); ok {
		touch(path)
		return data, true
	}
	data, ok := s.fetch(ctx, upstream, platformKey, passthroughAccept)
	if !ok {
		return nil, false
	}
	writeCache(path, data)
	s.maybeSweep()
	return data, true
}

// Small 取一张「小糊图」：缩到 smallWidth 宽、糊一遍、压成 JPEG（0.5–2KB）。
// 隐私模式专用；失败时调用方应当发空白占位，绝不能退回原图。
func (s *Service) Small(ctx context.Context, platformKey, raw string, smallWidth int) ([]byte, error) {
	// 小图不要 w：糊图跟显示宽度无关，带上 w 只会让同一张图按每种宽度各缓存一份。
	upstream, err := normalize(raw, "")
	if err != nil {
		return nil, err
	}
	smallPath := s.path(platformKey, fmt.Sprintf("%s|s=%d|v=%d", upstream, smallWidth, smallPipeline))
	if data, ok := readCache(smallPath); ok {
		touch(smallPath)
		return data, nil
	}
	// 手上有原图就先在本地缩，省一次上游请求。
	if src, ok := readCache(s.path(platformKey, upstream)); ok {
		if small, err := s.blurAny(ctx, src, smallWidth); err == nil {
			writeCache(smallPath, small)
			s.maybeSweep()
			return small, nil
		}
		// 解不出来——缓存里的这份多半是早先按 `Accept: image/avif` 抓下来的 AVIF，
		// Go 解不了。落到下面按「能解的格式」重抓一份，只给这张小图用。
	}
	src, ok := s.fetch(ctx, upstream, platformKey, decodableAccept)
	if !ok {
		return nil, fmt.Errorf("小图抓取失败: %s", upstream)
	}
	small, err := s.blurAny(ctx, src, smallWidth)
	if err != nil {
		return nil, err
	}
	writeCache(smallPath, small)
	s.maybeSweep()
	return small, nil
}

// Cached 报告这张图的原图是否已经在本地缓存里（封面预热/修复用）。
func (s *Service) Cached(platformKey, raw string) bool {
	upstream, err := normalize(raw, "")
	if err != nil {
		return false
	}
	_, ok := readCache(s.path(platformKey, upstream))
	return ok
}

// Warm 尽力把一张图抓进缓存（已经有就什么都不做），失败只返回 false。
func (s *Service) Warm(ctx context.Context, platformKey, raw string) bool {
	_, ok := s.Bytes(ctx, platformKey, raw, "")
	return ok
}

// normalize 校验地址并把 w= 翻译成图床的缩放参数，返回最终的上游地址。
func normalize(raw, width string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", ErrBadURL
	}
	return withImageWidth(u, width).String(), nil
}

func (s *Service) path(platformKey, key string) string {
	sum := sha256.Sum256([]byte(platformKey + "|" + key))
	hash := hex.EncodeToString(sum[:])
	return filepath.Join(s.dir, "imgcache", hash[:2], hash+".bin")
}

// client 取某个平台出网用的客户端。跨站图床（beeg 的 externulls、X 的 twimg）
// 用它自己平台的代理配置，不能跟着影片所属平台走。
func (s *Service) client(platformKey string) *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[platformKey]; ok {
		return c
	}
	proxy := ""
	if s.proxyFor != nil {
		proxy = s.proxyFor(platformKey)
	}
	c, err := platform.NewHTTPClient(proxy, 20*time.Second)
	if err != nil {
		c, _ = platform.NewHTTPClient("", 20*time.Second)
	}
	s.clients[platformKey] = c
	return c
}

// fetch 带着站点 Referer / UA 把上游图片抓下来（失败返回 ok=false）。
func (s *Service) fetch(ctx context.Context, raw, platformKey, accept string) ([]byte, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Referer", RefererFor(u))
	req.Header.Set("Accept", accept)

	resp, err := s.client(platform.ImageHostPlatform(raw, platformKey)).Do(req)
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

// refererOverrides 按域名后缀指定 Referer：beeg 的图床反过来拒绝自家根域的
// Referer（https://externulls.com/ 会被 403），只认 beeg.com。
var refererOverrides = map[string]string{
	"externulls.com": "https://beeg.com/",
	"twimg.com":      "https://x.com/",
	// Instagram 的图都在 CDN 上，防盗链认的是站点来源（M5 动态照片墙）。
	"cdninstagram.com": "https://www.instagram.com/",
	"fbcdn.net":        "https://www.instagram.com/",
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

// RefererFor 给出抓这张图该带的 Referer（CDN 只校验站点来源）。
func RefererFor(u *url.URL) string {
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
