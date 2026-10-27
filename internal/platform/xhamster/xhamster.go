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

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
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
	return readLimited(resp.Body, 8<<20)
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

var errNotFound = errors.New("上游没有这个页面（404）")

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

// performerPath 返回艺人主页路径（第 1 页与第 N 页）。
func performerPath(slug string, page int) string {
	if page <= 1 {
		return "/pornstars/" + url.PathEscape(slug)
	}
	return fmt.Sprintf("/pornstars/%s/%d", url.PathEscape(slug), page)
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
	p.URL = a.base + performerPath(performerID, 1)
	return p, nil
}

// ListPerformerVideos 取一页影片。xHamster 的艺主页第 1 页是 /pornstars/<slug>，
// 第 2 页起是 /pornstars/<slug>/<n>，每页 46 条（perPage 字段也是 46）。
func (a *Adapter) ListPerformerVideos(ctx context.Context, performerID string, page int) (*platform.Page, error) {
	if page < 1 {
		page = 1
	}
	body, err := a.get(ctx, performerPath(performerID, page), nil)
	if err != nil {
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
		out.HasMore = len(out.Items) > 0
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

// plainMP4Variants 是明文的单档直链（144p/240p/480p/720p），只作兜底。
func plainMP4Variants(iv *initials) []model.Variant {
	if iv == nil || iv.DownloadComponent == nil {
		return nil
	}
	var out []model.Variant
	for quality, raw := range iv.DownloadComponent.Sources.MP4 {
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
