package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/hls"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
)

// mediaUserAgent 是抓流用的 UA（与平台适配器一致）。
const mediaUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// 允许通过 /api/stream 代理的域名后缀（平台自己的站点与 CDN）。
// 白名单防止这个接口被当成通用开放代理使用。
var streamHostSuffixes = []string{
	"pornhub.com", "phncdn.com",
	"xhcdn.com", "xhamster.com", "xhamster.desi", "xhwebsite.com",
	// xHamster 的 HLS 清单里只有开头的 init 段和前两个分片在 xhcdn.com 上，
	// 之后整条片子（实测 1080p AV1：319 段里有 317 段）改由 ip<数字>.ahcdn.com 分发。
	// 漏掉这个后缀，播放就在第 8 秒（两段 4 秒）之后每个分片都 400「非法的播放地址」。
	"ahcdn.com",
	"beeg.com", "beeg.porn",
	// B 站：单文件档（durl）走这里的代理直出。媒体 CDN 必须带 Referer（不带一律 403），
	// 而同一部片子的地址会在 PCDN（*.edge.mountaintoys.cn、*.mcdn.bilivideo.cn）和
	// 官方 upos-*（*.bilivideo.com）之间混着给。白名单只放 B 站自己的域名后缀，
	// 适配器那边会优先挑 upos-* 那条（实测 6.6 MB/s vs PCDN 0.9 MB/s）。
	"bilivideo.com", "bilivideo.cn",
	// krx18 的分片在 server7.stream-baby1.top（loadvid 的存储域名，Cloudflare 后面但
	// 不设挑战：实测不带 Referer、不带特指 UA 也是 200，还写着 access-control-allow-origin: *）。
	// 地址伪装成 .png（`…/hls/1/<uuid>-<时间戳>/AbCdEf12345678.png`），内容是 MPEG-TS，
	// 首字节 0x47 —— 按扩展名判断容器会全错。
	"stream-baby1.top",
	// YouTube 的 HLS（清单 + 分片）都在 googlevideo 上。地址是签名且绑定出口 IP 的，
	// 只有本进程能取——浏览器拿到的永远是 /api/yt/seg/... 这种站内地址。
	"googlevideo.com",
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
//
// 已经是站内地址（以 / 开头）的原样返回：krx18 的清单是站内现做的
// （见 platform.SitePlaylistProvider），再包一层 /api/stream 等于让代理去取它自己。
func streamURL(platformKey, raw string) string {
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}
	q := url.Values{}
	q.Set("u", raw)
	if platformKey != "" {
		q.Set("p", platformKey)
	}
	return "/api/stream?" + q.Encode()
}

// refMapper 把上游地址改写成站内地址。nil 表示用 /api/stream?u= 这套通用包装，
// YouTube 那边换成播放会话的 /api/yt/seg/{sid}/{n}（见 handlers_ytstream.go）。
type refMapper func(abs string) string

// handleStream 代理播放地址：透传 Range 以满足拖动进度条，
// 顺带补上平台 Referer/UA（Pornhub CDN 没有 Referer 直接 404）。
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("u"))
	s.proxyMedia(w, r, raw, r.URL.Query().Get("p"), nil)
}

// proxyMedia 是流代理的本体：校验地址 → 带上头去取 → 清单就地改写、媒体原样透传。
// mapRef 决定改写后的地址长什么样（nil = /api/stream 的通用包装）。
//
// onStatus 是可选的「上游给了这个状态码」回调（只在 ≥400 时调），YouTube 分片那条路用它
// 当场作废播放会话：一条地址被拒之后剩下的分片只会重复撞墙，见 Manager.Drop。
func (s *Server) proxyMedia(w http.ResponseWriter, r *http.Request, raw, platformKey string, mapRef refMapper, onStatus ...func(int)) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !streamHostAllowed(u.Hostname()) {
		writeErr(w, http.StatusBadRequest, "bad_request", "非法的播放地址")
		return
	}

	// 播放大文件不能沿用 20s 的整体超时：这里用没有 Client.Timeout 的客户端，
	// 靠请求上下文取消来收尾（用户暂停/关页面即断开）。
	resp, err := s.openUpstream(r.Context(), u, platformKey, r.Header.Get("Range"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "取流失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		// 把上游给的错误正文也捞一句出来：googlevideo 的 403 正文里写着原因
		//（限流 / 签名失效 / IP 不符 / 需要人机验证），过去只记一个状态码，
		// 排查时只能靠猜。这里最多读 400 字节，够看第一行。
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		s.log.Warn("上游拒绝取流", "platform", platformKey, "status", resp.StatusCode,
			"host", u.Hostname(), "body", strings.TrimSpace(string(snippet)))
		for _, cb := range onStatus {
			cb(resp.StatusCode)
		}
		writeErr(w, http.StatusBadGateway, "resolve_failed", "上游返回 "+strconv.Itoa(resp.StatusCode))
		return
	}

	ct := resp.Header.Get("Content-Type")
	if isHLSPlaylist(u, ct) {
		s.serveRewrittenPlaylist(w, r, resp.Body, u, platformKey, mapRef)
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

// upstreamAttempts / upstreamRetryDelay 是取上游媒体对象的尝试次数与退避。
//
// 为什么要自己重试：YouTube 的 googlevideo 会**偶发**对连续分片请求回 403，
// 同一地址隔一秒再要就正常。实测一条 4K fancam（321 秒）播 40 秒里撞了 3 次，
// hls.js 自己也会重试（fragLoadingMaxRetry=6），但它每次重试要先把整个分片重新排队，
// 实测代价 3~9 秒，正好把缓冲吃穿——用户看到的就是「一顿一顿」。
// 在这一层重试只多花 150~400 毫秒，播放器那头完全无感。
const upstreamAttempts = 3

func upstreamRetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 150 * time.Millisecond
	}
	return 400 * time.Millisecond
}

// retryableStatus 是「同一地址重试大概率能过」的上游状态码。
// 403 在 googlevideo 上是限流信号（不是签名失效，失效会 410），所以也算。
func retryableStatus(code int) bool {
	switch code {
	case http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// openUpstream 取上游媒体的响应：网络错误与可重试状态码都再试几次。
//
// 只在**还没往浏览器写任何字节之前**重试，所以对播放器是完全透明的：
// 它看到的要么是一次成功响应，要么是最终失败。
func (s *Server) openUpstream(ctx context.Context, u *url.URL, platformKey, rangeHdr string) (*http.Response, error) {
	client := s.streamClientFor(platformKey)
	var lastErr error
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, matchOriginalURL(u), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", mediaUserAgent)
		req.Header.Set("Referer", platformReferer(s, platformKey, u))
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}

		resp, err := client.Do(req)
		switch {
		case err != nil:
			lastErr = err
		case retryableStatus(resp.StatusCode) && attempt < upstreamAttempts:
			// 把这一小段错误响应的正文丢掉再关，连接才能干净地回到池子里。
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			lastErr = fmt.Errorf("上游返回 %d", resp.StatusCode)
		default:
			return resp, nil
		}
		if attempt >= upstreamAttempts {
			return nil, lastErr
		}
		select {
		case <-time.After(upstreamRetryDelay(attempt)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// maxWarmObject 是单个预热对象的大小上限：HLS 分片几百 KB 到几十 MB。
//
// 原来定 16 MiB 是按「分片只有几 MB」估的，YouTube 4K 直接把这个假设打穿：
// 实测 2160p VP9 60fps（itag 628，28220 kbps，分段 5~7 秒）每段 21~23 MB。
// 超限那一版是「按上限截断后照旧入缓存」，于是**第一段视频分片永远是残缺的 16 MiB**
// （16777216 字节，第二次请求 5 毫秒就原样再发一遍）。hls.js 把残缺的 fMP4 塞进
// SourceBuffer 得到 bufferAppendError，重试又命中同一份截断字节，循环不休：
// 实测 40 秒里 28 次 waiting、currentTime 卡在 0、缓冲区区间为空。
// 所以口径改成「超过上限就整条不缓存」——宁可少一次预热，也不能把半段字节当整段发出去。
const maxWarmObject = 64 << 20

// fetchRawMedia 取一个媒体对象的原文（清单也不改写）：给「起播预热」用。
//
// 为什么存原文而不是存改写后的清单：预热发生在拿到播放会话之前（点开弹窗那一刻），
// 那时还没有发号器；原文与地址无关，之后谁拿到号都能现场改写。
func (s *Server) fetchRawMedia(ctx context.Context, raw, platformKey string) (string, []byte, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !streamHostAllowed(u.Hostname()) {
		return "", nil, fmt.Errorf("非法的播放地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, matchOriginalURL(u), nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("User-Agent", mediaUserAgent)
	req.Header.Set("Referer", platformReferer(s, platformKey, u))
	req.Header.Set("Accept", "*/*")
	resp, err := s.streamClientFor(platformKey).Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", nil, fmt.Errorf("上游返回 %d", resp.StatusCode)
	}
	// 多读 1 个字节用来判断「是不是超限」：宁可整条不缓存，也不截断后冒充完整对象
	// （4K 分片 22 MB 被截成 16 MiB 就是上面注释里那个 bufferAppendError 的来历）。
	data, err := readCapped(resp.Body, maxWarmObject)
	if err != nil {
		return "", nil, err
	}
	return resp.Header.Get("Content-Type"), data, nil
}

// readCapped 读完一个媒体对象，超过 limit 就整体报错。
//
// 单独抽出来是为了能测：这里是「截断 → 播放器 append 失败 → 无限重试」这类
// 隐蔽故障的唯一入口，必须有一个小的、能断言「要么完整、要么报错」的单元。
func readCapped(body io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("对象超过上限 %d 字节", limit)
	}
	return data, nil
}

// serveRewrittenPlaylist 把 m3u8 里的分片/子清单地址改写成代理地址。
func (s *Server) serveRewrittenPlaylist(w http.ResponseWriter, r *http.Request, body io.Reader,
	playlistURL *url.URL, platformKey string, mapRef refMapper) {
	// 一小时的片子分片表就有上千行，YouTube 的能到接近 1MB，留足余量。
	data, err := io.ReadAll(io.LimitReader(body, 16<<20))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "读取清单失败")
		return
	}
	out := rewritePlaylist(data, playlistURL, platformKey, mapRef)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// rewritePlaylist 处理相对地址与 URI="..." 属性。
func rewritePlaylist(data []byte, base *url.URL, platformKey string, mapRef refMapper) []byte {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[i] = rewriteURIAttrs(line, base, platformKey, mapRef)
			continue
		}
		abs := resolveRef(base, trimmed)
		lines[i] = mapStreamURL(platformKey, abs.String(), mapRef)
	}
	return []byte(strings.Join(lines, "\n"))
}

func rewriteURIAttrs(line string, base *url.URL, platformKey string, mapRef refMapper) string {
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
		mapped := mapStreamURL(platformKey, abs.String(), mapRef)
		line = line[:idx] + marker + mapped + `"` + line[start+end+1:]
		next := idx + len(marker) + len(mapped) + 1
		more := strings.Index(line[next:], marker)
		if more < 0 {
			break
		}
		idx = next + more
	}
	return line
}

// mapStreamURL 是改写规则的唯一分叉点：要么走通用 /api/stream，要么交给调用方给的映射。
func mapStreamURL(platformKey, abs string, mapRef refMapper) string {
	if mapRef != nil {
		return mapRef(abs)
	}
	return streamURL(platformKey, abs)
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
	return imgcache.RefererFor(u)
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
