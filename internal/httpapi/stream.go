package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mario/myporn/internal/hls"
)

// mediaUserAgent 是抓流用的 UA（与平台适配器一致）。
const mediaUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// 允许通过 /api/stream 代理的域名后缀（平台自己的站点与 CDN）。
// 白名单防止这个接口被当成通用开放代理使用。
var streamHostSuffixes = []string{
	"pornhub.com", "phncdn.com",
	"xhcdn.com", "xhamster.com", "xhamster.desi", "xhwebsite.com",
	"beeg.com", "beeg.porn",
}

func streamHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, suffix := range streamHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// streamURL 把上游地址包成同源代理地址，浏览器才能带上 Referer 拿到分片。
func streamURL(platformKey, raw string) string {
	if raw == "" {
		return ""
	}
	q := url.Values{}
	q.Set("u", raw)
	if platformKey != "" {
		q.Set("p", platformKey)
	}
	return "/api/stream?" + q.Encode()
}

// handleStream 代理播放地址：透传 Range 以满足拖动进度条，
// 顺带补上平台 Referer/UA（Pornhub CDN 没有 Referer 直接 404）。
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	platformKey := r.URL.Query().Get("p")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !streamHostAllowed(u.Hostname()) {
		writeErr(w, http.StatusBadRequest, "bad_request", "非法的播放地址")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, matchOriginalURL(u), nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "非法的播放地址")
		return
	}
	req.Header.Set("User-Agent", mediaUserAgent)
	req.Header.Set("Referer", platformReferer(s, platformKey, u))
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}

	// 播放大文件不能沿用 20s 的整体超时：这里用没有 Client.Timeout 的客户端，
	// 靠请求上下文取消来收尾（用户暂停/关页面即断开）。
	client := s.streamClientFor(platformKey)
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "取流失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "上游返回 "+strconv.Itoa(resp.StatusCode))
		return
	}

	ct := resp.Header.Get("Content-Type")
	if isHLSPlaylist(u, ct) {
		s.serveRewrittenPlaylist(w, r, resp.Body, u, platformKey)
		return
	}

	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// serveRewrittenPlaylist 把 m3u8 里的分片/子清单地址改写成代理地址。
func (s *Server) serveRewrittenPlaylist(w http.ResponseWriter, r *http.Request, body io.Reader, playlistURL *url.URL, platformKey string) {
	data, err := io.ReadAll(io.LimitReader(body, 4<<20))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "读取清单失败")
		return
	}
	out := rewritePlaylist(data, playlistURL, platformKey)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// rewritePlaylist 处理相对地址与 URI="..." 属性。
func rewritePlaylist(data []byte, base *url.URL, platformKey string) []byte {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[i] = rewriteURIAttrs(line, base, platformKey)
			continue
		}
		abs := resolveRef(base, trimmed)
		lines[i] = streamURL(platformKey, abs.String())
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteURIAttrs(line string, base *url.URL, platformKey string) string {
	const marker = `URI="`
	idx := strings.Index(line, marker)
	for idx >= 0 {
		start := idx + len(marker)
		end := strings.Index(line[start:], `"`)
		if end < 0 {
			break
		}
		ref := line[start : start+end]
		abs := resolveRef(base, ref)
		line = line[:idx] + marker + streamURL(platformKey, abs.String()) + `"` + line[start+end+1:]
		next := idx + len(marker) + len(streamURL(platformKey, abs.String())) + 1
		more := strings.Index(line[next:], marker)
		if more < 0 {
			break
		}
		idx = next + more
	}
	return line
}

// resolveRef 复用 hls.ResolveURL：签名参数要跟着相对地址一起带过去，
// 否则改写后的分片地址会被 CDN 判 410。
func resolveRef(base *url.URL, ref string) *url.URL {
	out, err := url.Parse(hls.ResolveURL(base.String(), ref))
	if err != nil || out == nil {
		return base
	}
	return out
}

func isHLSPlaylist(u *url.URL, contentType string) bool {
	if strings.Contains(strings.ToLower(contentType), "mpegurl") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

// platformReferer 用平台站点根地址做 Referer（CDN 只校验站点来源）。
func platformReferer(s *Server, platformKey string, u *url.URL) string {
	if platformKey != "" {
		if ad, ok := s.reg.Get(platformKey); ok {
			if base := ad.BaseURL(); base != "" {
				return strings.TrimSuffix(base, "/") + "/"
			}
		}
	}
	return refererFor(u)
}

// matchOriginalURL 还原查询串里的原始地址（保留签名参数）。
func matchOriginalURL(u *url.URL) string {
	return u.String()
}

// streamClientFor 优先用适配器自带客户端（带 cookie jar）：HLS 签名绑定页面会话，
// 没有 sessid 的客户端会被 CDN 判 410；拿不到再退回通用客户端。
func (s *Server) streamClientFor(platformKey string) *http.Client {
	type clientExposer interface{ HTTPClient() *http.Client }
	if ad, ok := s.reg.Get(platformKey); ok {
		if ce, isExposer := ad.(clientExposer); isExposer {
			if base := ce.HTTPClient(); base != nil {
				cp := *base
				cp.Timeout = 0
				return &cp
			}
		}
	}
	return s.streamClient(platformKey)
}

// streamClient 与 platformClient 共用同一份 Transport/代理配置，但去掉整体超时。
func (s *Server) streamClient(platformKey string) *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "stream:" + platformKey
	if c, ok := s.clients[key]; ok {
		return c
	}
	base := s.platformClientLocked(platformKey)
	copied := *base
	copied.Timeout = 0
	s.clients[key] = &copied
	return &copied
}
