// Package xhamster 实现 xHamster 站点适配器。
//
// 和 Pornhub 不一样，xHamster 的页面把数据直接塞在 `<script>window.initials = {…}</script>`
// 里（服务端渲染的初始状态），所以解析走 JSON 而不是 DOM 选择器，站点改 CSS 类名不受影响。
// 播放地址是密文，解密规则在 decrypt.go，注释里写了来龙去脉。
package xhamster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
)

const defaultBase = "https://xhamster.com"

// Adapter 实现 platform.Adapter。
type Adapter struct {
	base   string
	client *http.Client
	ua     string
	lim    *rate.Limiter
}

// New 创建适配器。proxyFor 返回空串表示直连（每次发请求现问，改设置即时生效）。
func New(baseURL string, proxyFor func() string) (*Adapter, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBase
	}
	hc, err := platform.NewDynamicHTTPClient(proxyFor, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		base:   strings.TrimSuffix(baseURL, "/"),
		client: hc,
		ua:     "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		// 一次播放要拉 1 个页面 + 2 份清单，1.5s/次会让起播慢到 4 秒开外；
		// 单人浏览远低于这个量级，500ms/次（允许 2 个突发）够礼貌也不会被限流。
		lim: rate.NewLimiter(rate.Every(500*time.Millisecond), 2),
	}, nil
}

// HTTPClient 暴露带 cookie jar 的客户端（清单地址可能与会话绑定）。
func (a *Adapter) HTTPClient() *http.Client { return a.client }

func (a *Adapter) Key() string     { return "xhamster" }
func (a *Adapter) Name() string    { return "xHamster" }
func (a *Adapter) BaseURL() string { return a.base }

// pace 做礼貌限速：1 请求 / 500ms + 0–300ms 抖动。
func (a *Adapter) pace(ctx context.Context) error {
	if err := a.lim.Wait(ctx); err != nil {
		return err
	}
	jitter := time.Duration(rand.Intn(300)) * time.Millisecond
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(jitter):
	}
	return nil
}

var ErrRateLimited = errors.New("上游限流")

// get 取页面（HTML 或清单都走这里）。
func (a *Adapter) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	if err := a.pace(ctx); err != nil {
		return nil, err
	}
	u := a.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", a.base+"/")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("上游返回 %d (%s)", resp.StatusCode, u)
	}
	body, err := readLimited(resp.Body, 8<<20)
	if err != nil {
		return nil, err
	}
	// 一整个页面 370–400 KB，回「几十字节」或干脆 0 字节不是页面，是链路被掐：
	// 实测并发抓取时 curl 报 `HTTP/2 framing layer` 错误、连接拿到空 body，
	// 而 Go 这边读到的是「成功但没内容」，于是 parseInitials 报「页面里没有 window.initials，
	// 可能被风控拦截或站点改版」—— 用户看到「平台解析失败」，其实重试一下就好。
	// 这类错误该按「可重试」上报（限流同款处理：不标红、下一轮接着来）。
	if len(body) < 2048 {
		return nil, fmt.Errorf("%w: 上游只回了 %d 字节的页面", platform.ErrRateLimited, len(body))
	}
	return body, nil
}

// getAbsolute 取绝对地址（清单在 CDN 上）。
func (a *Adapter) getAbsolute(ctx context.Context, raw string) ([]byte, error) {
	if err := a.pace(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.ua)
	req.Header.Set("Referer", a.base+"/")
	req.Header.Set("Accept", "*/*")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("上游返回 %d", resp.StatusCode)
	}
	return readLimited(resp.Body, 4<<20)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, err
		}
		if int64(len(buf)) > limit {
			return nil, errors.New("页面超过体积上限，疑似异常")
		}
	}
	return buf, nil
}

// errNotFound 包着公共的 platform.ErrNotFound：私有哨兵给本包判断用，
// 外面的任务（比如分页翻到末页）也能用 errors.Is 认出来。
var errNotFound = fmt.Errorf("%w（xHamster 页面不存在）", platform.ErrNotFound)

var rePerformerPath = regexp.MustCompile(`/(?:pornstars|creators)/([A-Za-z0-9_\-]+)`)

// ParsePerformerURL 支持 https://xhamster.com/pornstars/riley-reid 及
// /creators/<slug>（旧路径）、带 /videos 后缀的地址，也允许只贴 slug。
func (a *Adapter) ParsePerformerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("URL 为空")
	}
	if m := rePerformerPath.FindStringSubmatch(raw); m != nil {
		return m[1], nil
	}
	// 只贴了 slug（不含 ://）：认下来，方便在搜索框里直接敲名字。
	if !strings.Contains(raw, "://") && regexp.MustCompile(`^[A-Za-z0-9_\-]{2,64}$`).MatchString(raw) {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("URL 非法")
	}
	host := strings.ToLower(u.Hostname())
	if !strings.Contains(host, "xhamster") {
		return "", errors.New("不是 xHamster 的链接")
	}
	return "", errors.New("链接里没有艺人标识（应形如 /pornstars/<slug>）")
}

// performerPath 返回艺人影片列表的路径（第 1 页与第 N 页）。两个参数都是踩出来的：
//
// **1）必须带 `/full-length`**（站点的「完整影片」分类）。不带时 `/newest` 是「该艺人所有新片」，
// 会连 60–180 秒的付费预告一起抓：2026-11-04 实测 `hot-pearl` 的 `/newest` 第 1 页 46 条里
// 有 8 条 <180s（60 / 67 / 70 / 112s，标题带 `- Trailer`、`Private com -` 这类），
// 而 `/full-length/newest` 5 页 214 条最短 361s、一条预告都没有；`mysaaat` 这种每部都 ≥9min 的
// 艺人，两个地址给出**完全相同**的 42 条 id——所以这个过滤只削短片，不会漏长片，页数还从 12 降到 5。
// 注意 `full-length` 是站点的内容分类，不是我们算的时长门槛（riley-reid 那边最短 1274s、
// hot-pearl 最短 361s，门槛是上传时人工标的）。竖版短视频另有 `/shorts` 页（hot-pearl 上 234 条），不要。
//
// **2）必须带 `/newest`**：不带时站点给默认序（`sort: "rated"`），而增量同步是「遇到已入库影片即停」——
// 第 1 页最上面躺着一堆几年前的老片，一撞上就停，新上传的永远发现不了。2026-10 实测（`mysaaat`）：
// 默认页只有 38 条按评分排的，最新那 4 条单独挂在 `newestVideoSectionComponent` 里；
// 换 `/newest` 后 42 条、`sort: "added"`、按 `created` 严格倒序，最新那条落在第一位。
func performerPath(slug string, page int) string {
	if page <= 1 {
		return "/pornstars/" + url.PathEscape(slug) + "/full-length/newest"
	}
	return fmt.Sprintf("/pornstars/%s/full-length/newest/%d", url.PathEscape(slug), page)
}

// performerURLPath 是给人看/存下来的艺人主页地址（不带排序）。
func performerURLPath(slug string) string {
	return "/pornstars/" + url.PathEscape(slug)
}

// GetPerformer 抓艺人资料（名字、头像、影片数、订阅数、简介、资料行）。
func (a *Adapter) GetPerformer(ctx context.Context, performerID string) (*model.Performer, error) {
	body, err := a.get(ctx, performerPath(performerID, 1), nil)
	if err != nil {
		return nil, err
	}
	iv, err := parseInitials(body)
	if err != nil {
		return nil, err
	}
	p := performerFrom(iv, performerID)
	p.Platform = a.Key()
	p.PerformerID = performerID
	// 存干净的主页地址，不把「我抓列表时用的排序」写进用户资料里。
	p.URL = a.base + performerURLPath(performerID)
	return p, nil
}

// ListPerformerVideos 取一页影片：`/pornstars/<slug>/full-length/newest`，第 2 页起是
// `/full-length/newest/<n>`，每页 46 条（`perPage` 字段也是 46），按发布时间倒序。
// 为什么必须是这个路径（完整影片 + 最新排序）见 performerPath。
func (a *Adapter) ListPerformerVideos(ctx context.Context, performerID string, page int) (*platform.Page, error) {
	if page < 1 {
		page = 1
	}
	body, err := a.get(ctx, performerPath(performerID, page), nil)
	if err != nil {
		// `/full-length` 的页数比 `/newest` 少得多，站点对越界页是直接 404（riley-reid 的
		// `/full-length/newest/2` 实测 404）。这不算抓取失败，当「没有下一页」收尾，
		// 别让一次越界把整个艺人的同步标红。
		if page > 1 && errors.Is(err, errNotFound) {
			return &platform.Page{Page: page}, nil
		}
		return nil, err
	}
	iv, err := parseInitials(body)
	if err != nil {
		return nil, err
	}
	section := iv.videoSection()
	if section == nil {
		return &platform.Page{Page: page}, nil
	}
	out := &platform.Page{Page: page}
	for _, t := range section.items() {
		v := videoFromThumb(t, a.base)
		if v.PlatformVideoID == "" {
			continue
		}
		v.Platform = a.Key()
		out.Items = append(out.Items, *v)
	}
	if pc := iv.PaginationComponent; pc != nil && pc.LastPageNumber > 0 {
		out.TotalPages = pc.LastPageNumber
		out.HasMore = page < pc.LastPageNumber
	} else {
		// 站点不给分页组件 = 就这一页（`riley-reid` 的 `/full-length/newest` 正是如此：
		// 34 条、`paginationComponent: null`）。原先是 `len(items) > 0`，于是同步会去要第 2 页、
		// 拿回 404，日志里留一条「分页抓取中断」。
		out.HasMore = false
	}
	return out, nil
}

// videoPath 返回影片页路径。ID 可以是「slug-xhHASH」也可以是「xhHASH」。
func videoPath(id string) string {
	return "/videos/" + strings.TrimPrefix(url.PathEscape(id), "/")
}

// GetVideo 抓影片详情（标题、时长、简介、标签、发布日）。
func (a *Adapter) GetVideo(ctx context.Context, videoID string) (*model.Video, error) {
	body, err := a.get(ctx, videoPath(videoID), nil)
	if err != nil {
		return nil, err
	}
	iv, err := parseInitials(body)
	if err != nil {
		return nil, err
	}
	v := videoFrom(iv, videoID, a.base)
	if v == nil {
		return nil, errors.New("页面里没有 videoModel，可能被风控拦截或站点改版")
	}
	v.Platform = a.Key()
	return v, nil
}

// ResolvePlayback 一次抓页面，既拿元数据又拿播放地址（播放路径最常用）。
func (a *Adapter) ResolvePlayback(ctx context.Context, videoID string) (*model.Video, []model.Variant, error) {
	body, err := a.get(ctx, videoPath(videoID), nil)
	if err != nil {
		return nil, nil, err
	}
	return a.playbackFrom(ctx, body, videoID)
}

// ResolvePlaybackPage 是 platform.PageResolver 的实现：拿「影片页地址」直接解析。
//
// 为什么需要这条：ID 统一成 URL 末尾那段（xhW3Ivw）之后，`/videos/xhW3Ivw`
// 会被 301 跳到带 slug 的规范地址。实测这一跳：301 响应本身 0.94/1.06/1.27 秒，
// 端到端解析 2.41s（带 slug）vs 2.59~2.77s（裸 ID）。而库里 `videos.url` 存的就是
// 带 slug 的规范地址——照它去取，每播一次省一跳，上游也少一次请求（xHamster 会限流）。
func (a *Adapter) ResolvePlaybackPage(ctx context.Context, pageURL string) (*model.Video, []model.Variant, error) {
	if !strings.Contains(pageURL, "/videos/") && !strings.Contains(pageURL, "/shorts/") {
		return nil, nil, fmt.Errorf("不是影片页地址: %s", pageURL)
	}
	body, err := a.getAbsolute(ctx, pageURL)
	if err != nil {
		return nil, nil, err
	}
	return a.playbackFrom(ctx, body, videoIDFromURL(pageURL))
}

// playbackFrom 是两条解析入口共用的尾巴：页面字节 → 元数据 + 全部清晰度。
func (a *Adapter) playbackFrom(ctx context.Context, body []byte, videoID string) (*model.Video, []model.Variant, error) {
	iv, err := parseInitials(body)
	if err != nil {
		return nil, nil, err
	}
	v := videoFrom(iv, videoID, a.base)
	if v != nil {
		v.Platform = a.Key()
	}
	variants, err := a.variantsFrom(ctx, iv)
	if err != nil {
		return v, nil, err
	}
	return v, variants, nil
}

// ResolveStreams 只解播放地址。
func (a *Adapter) ResolveStreams(ctx context.Context, videoID string) ([]model.Variant, error) {
	body, err := a.get(ctx, videoPath(videoID), nil)
	if err != nil {
		return nil, err
	}
	iv, err := parseInitials(body)
	if err != nil {
		return nil, err
	}
	variants, err := a.variantsFrom(ctx, iv)
	if err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		return nil, errors.New("未解析到任何可用流地址（可能是付费内容，或站点换了加密算法）")
	}
	return variants, nil
}

// variantsFrom 解出所有清晰度。
//
// 正常路径：sources.standard.{av1,h264} 里 quality=auto 的那条密文解出 master 清单模板，
// 两份清单各拉一次 → 合并成「最高分辨率、同分辨率优先 AV1」的列表。
// 兜底路径：解密失败（站点换算法）时退回 downloadDropdownComponent 里那批明文直链，
// 只有 144p–720p，但能看比看不了强。
func (a *Adapter) variantsFrom(ctx context.Context, iv *initials) ([]model.Variant, error) {
	var out []model.Variant
	var lastErr error

	for codec, master := range masterURLs(iv) {
		body, err := a.getAbsolute(ctx, master)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, parseMaster(string(body), master, codec)...)
	}

	if len(out) == 0 {
		// 没有 quality=auto：VR 影片就是这样（见 progressiveVariants）
		out = append(out, a.progressiveVariants(iv)...)
	}
	if len(out) == 0 {
		if plain := plainMP4Variants(iv); len(plain) > 0 {
			out = append(out, plain...)
		}
	}
	if len(out) == 0 {
		if lastErr != nil {
			return nil, fmt.Errorf("解析播放地址失败: %w", lastErr)
		}
		return nil, errDecrypt
	}
	model.SortVariantsDesc(out)
	return out, nil
}

// progressiveVariants 是「没有 quality=auto」时的取法：把 sources.standard 里**逐档明文 MP4**
// 直接当清晰度用。
//
// 为什么需要它：VR 影片的 sources 结构和普通片完全不同——standard 下只有 h264 一个编码、
// 列出的是 960p / 1440p / 1920p 三条**投影档**（每条还带 framePack=LR、angle=180），
// 没有 quality=auto，也没有 sources.hls；downloadDropdownComponent 又是空数组。
// 我们只认 quality=auto，于是整部片「一条清晰度都取不到」，报出去的却是「播放地址解密失败」——
// 而 2026-11 实测这几条密文**是能解开的**，解出来就是
// `https://video-h.xhcdn.com/key=…/029/353/596/ws1920p.h264.mp4` 这样的直链
// （302 到 ip*.ahcdn.com，206 video/mp4，能直接播）。
// 所以这里逐条解密，档位名直接当高度用（`1920p` → 1920），主机照 hostMap 换。
func (a *Adapter) progressiveVariants(iv *initials) []model.Variant {
	if iv == nil {
		return nil
	}
	ps := iv.PlayerSettings
	if ps == nil {
		ps = iv.PlayerSettings2
	}
	if ps == nil {
		return nil
	}
	var out []model.Variant
	seen := map[string]bool{}
	for codec, entries := range ps.standardSources() {
		for _, e := range entries {
			quality := strings.TrimSpace(e.Quality)
			if quality == "" || strings.EqualFold(quality, "auto") {
				continue
			}
			h := heightOf(quality)
			if h <= 0 {
				continue
			}
			plain, err := decryptSource(e.URL)
			if err != nil || plain == "" {
				continue
			}
			key := fmt.Sprintf("%d|%s", h, codec)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, model.Variant{
				Height: h,
				Codec:  strings.ToLower(codec),
				URL:    applyHostOverride(plain, iv.HostMap, "standard."+codec+"."+quality+".prime"),
			})
		}
	}
	return out
}

// plainMP4Variants 是明文的单档直链（144p/240p/480p/720p），只作兜底。
func plainMP4Variants(iv *initials) []model.Variant {
	if iv == nil {
		return nil
	}
	var out []model.Variant
	for quality, raw := range iv.DownloadComponent.mp4Sources() {
		h := heightOf(quality)
		if h <= 0 || raw == "" {
			continue
		}
		out = append(out, model.Variant{Height: h, URL: raw})
	}
	return out
}

// heightOf 把 "720p" / "1080p" 这样的档位名换成像素高度。
func heightOf(quality string) int {
	q := strings.TrimSpace(strings.ToLower(quality))
	q = strings.TrimSuffix(q, "p")
	n := 0
	for _, r := range q {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// slugOf 把艺人名压成 xHamster 的 slug（小写、非字母数字换连字符、去重连字符）。
func slugOf(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// SearchPerformers 找艺人。xHamster 的搜索建议接口由 Cloudflare 挡在浏览器后面
// （要 x-csrf-token，服务端调用一律 403），所以这里按「名字 → slug」直接探页面：
// 命中就用，没命中就回空。搜索框里贴主页链接永远是最稳的路子。
func (a *Adapter) SearchPerformers(ctx context.Context, query string, page int) ([]*model.Performer, bool, error) {
	if page > 1 {
		return nil, false, nil
	}
	slug := slugOf(query)
	if slug == "" {
		return nil, false, nil
	}
	body, err := a.get(ctx, performerPath(slug, 1), nil)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	iv, err := parseInitials(body)
	if err != nil {
		return nil, false, err
	}
	p := performerFrom(iv, slug)
	// 探的是 slug，回来的名字必须对得上，否则宁可返回空。
	if slugOf(p.Name) != slug {
		return nil, false, nil
	}
	p.Platform = a.Key()
	p.PerformerID = slug
	p.URL = a.base + performerPath(slug, 1)
	return []*model.Performer{p}, false, nil
}
