// Package pornhub 实现 Pornhub 站点适配器。
//
// ⚠️ 重要说明：本包中的 HTML 选择器基于站点已知结构编写，但**尚未在真实网络上验证**
// （开发环境无法访问该站点）。首次上线前请用 `myporn debug-parse` 命令抓取真实页面核对，
// 并参考 docs/NEEDS-VERIFICATION.md。所有解析都做了多策略回退，改选择器只需改 parse.go。
package pornhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
)

type Adapter struct {
	base   string
	client *http.Client
	ua     string
	cookie string
	lim    *rate.Limiter
}

// New 创建适配器。proxyURL 为空表示直连；cookie 可为空。
func New(baseURL, proxyURL, cookie string) (*Adapter, error) {
	if baseURL == "" {
		baseURL = "https://cn.pornhub.com"
	}
	hc, err := platform.NewHTTPClient(proxyURL, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		base:   strings.TrimSuffix(baseURL, "/"),
		client: hc,
		ua:     "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		cookie: cookie,
		// 一次解析要打好几个上游地址，1.5s/次的限速会让起播慢到 8 秒；
		// 单个用户浏览的量级远低于这个速率，放到 500ms/次（允许 2 个突发）即可。
		lim: rate.NewLimiter(rate.Every(500*time.Millisecond), 2),
	}, nil
}

// HTTPClient 暴露带 cookie jar 的客户端：HLS 签名地址与页面会话绑定，
// 用别的客户端（没有 sessid）去拉清单会被 CDN 判 410。
func (a *Adapter) HTTPClient() *http.Client { return a.client }

func (a *Adapter) Key() string     { return "pornhub" }
func (a *Adapter) Name() string    { return "Pornhub" }
func (a *Adapter) BaseURL() string { return a.base }

// pace 做礼貌限速：1 请求 / 1.5s，再叠加 0–400ms 抖动，降低被风控概率。
func (a *Adapter) pace(ctx context.Context) error {
	if err := a.lim.Wait(ctx); err != nil {
		return err
	}
	jitter := time.Duration(rand.Intn(400)) * time.Millisecond
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(jitter):
	}
	return nil
}

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
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", a.base+"/")
	if a.cookie != "" {
		req.Header.Set("Cookie", a.cookie)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("上游返回 %d (%s)", resp.StatusCode, u)
	}
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			if isEOF(err) {
				break
			}
			return nil, err
		}
		if len(buf) > 8<<20 {
			return nil, errors.New("页面超过 8MB，疑似异常")
		}
	}
	return buf, nil
}

var ErrRateLimited = errors.New("上游限流")

var rePerformerPath = regexp.MustCompile(`/(?:model|pornstar)/([A-Za-z0-9_\-]+)`)

// ParsePerformerURL 支持 https://cn.pornhub.com/model/mini-diva 及带 /videos 后缀的地址。
func (a *Adapter) ParsePerformerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("URL 为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if m := rePerformerPath.FindStringSubmatch(raw); len(m) == 2 {
		return m[1], nil
	}
	return "", fmt.Errorf("无法从 %q 识别 Pornhub 艺人标识", raw)
}

func (a *Adapter) GetPerformer(ctx context.Context, performerID string) (*model.Performer, error) {
	body, err := a.get(ctx, "/model/"+url.PathEscape(performerID), nil)
	if err != nil {
		return nil, err
	}
	p, err := parsePerformerPage(body)
	if err != nil {
		return nil, err
	}
	p.Platform = a.Key()
	p.PerformerID = performerID
	p.URL = a.base + "/model/" + performerID
	return p, nil
}

func (a *Adapter) ListPerformerVideos(ctx context.Context, performerID string, page int) (*platform.Page, error) {
	if page < 1 {
		page = 1
	}
	q := url.Values{}
	if page > 1 {
		q.Set("page", fmt.Sprint(page))
	}
	body, err := a.get(ctx, "/model/"+url.PathEscape(performerID)+"/videos", q)
	if err != nil {
		return nil, err
	}
	return parseVideoListPageFor(body, page, a.base, performerID)
}

func (a *Adapter) GetVideo(ctx context.Context, videoID string) (*model.Video, error) {
	body, err := a.get(ctx, "/view_video.php", url.Values{"viewkey": {videoID}})
	if err != nil {
		return nil, err
	}
	v, _, err := parseVideoPage(body)
	if err != nil {
		return nil, err
	}
	v.Platform = a.Key()
	v.PlatformVideoID = videoID
	v.URL = a.base + "/view_video.php?viewkey=" + url.QueryEscape(videoID)
	return v, nil
}

// ResolvePlayback 解析播放地址（含直链 mp4 与 HLS 清单地址），并顺手把详情页元数据带回去
// （标签、简介、时长这些只在详情页里有）。不逐个拉 master.m3u8 补码率——播放用不到码率。
func (a *Adapter) ResolvePlayback(ctx context.Context, videoID string) (*model.Video, []model.Variant, error) {
	body, err := a.get(ctx, "/view_video.php", url.Values{"viewkey": {videoID}})
	if err != nil {
		return nil, nil, err
	}
	v, defs, err := parseVideoPage(body)
	if err != nil {
		return nil, nil, err
	}
	if v != nil {
		v.Platform = a.Key()
		v.PlatformVideoID = videoID
		if v.URL == "" {
			v.URL = fmt.Sprintf("%s/view_video.php?viewkey=%s", a.base, videoID)
		}
	}
	variants, err := a.variantsFromDefinitions(ctx, defs, false)
	if err != nil {
		return v, nil, err
	}
	return v, variants, nil
}

func (a *Adapter) ResolveStreams(ctx context.Context, videoID string) ([]model.Variant, error) {
	body, err := a.get(ctx, "/view_video.php", url.Values{"viewkey": {videoID}})
	if err != nil {
		return nil, err
	}
	_, defs, err := parseVideoPage(body)
	if err != nil {
		return nil, err
	}
	variants, err := a.variantsFromDefinitions(ctx, defs, true)
	if err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		return nil, errors.New("未解析到任何可用流地址（页面结构可能已变更）")
	}
	model.SortVariantsDesc(variants)
	return variants, nil
}

// variantsFromDefinitions 处理 flashvars.mediaDefinitions：
// format=hls 的 m3u8 会再拉一次 master playlist 展开成真实分辨率列表。
func (a *Adapter) variantsFromDefinitions(ctx context.Context, defs []mediaDefinition, needBandwidth bool) ([]model.Variant, error) {
	// 一次解析要打好几个上游地址（1 个 get_media + 若干 HLS master），串行做要 8 秒左右，
	// 起播因此很慢。这里并发跑，实测降到 2 秒出头。
	type result struct {
		idx      int
		variants []model.Variant
		bw       map[int]int
		err      error
	}

	jobs := make([]mediaDefinition, 0, len(defs))
	seen := map[string]bool{}
	for _, d := range defs {
		u := strings.TrimSpace(d.VideoURL)
		if u == "" || strings.HasPrefix(u, "data:") {
			continue
		}
		// 以 URL 形态为准：页面偶尔会把 m3u8 的 def 标成 mp4，
		// 按 Format 选路会把清单地址丢给 get_media，白白解析失败。
		isMP4 := strings.Contains(u, "get_media") || (!strings.Contains(u, ".m3u8") && d.Format == "mp4")
		isHLS := strings.Contains(u, ".m3u8") || (!isMP4 && d.Format == "hls")
		if !isMP4 && !isHLS && d.Height == 0 && parseQualityToHeight(d.QualityText()) == 0 {
			continue
		}
		if seen[u] {
			continue // 同一地址可能被多个 def 重复引用，别打两遍
		}
		seen[u] = true
		jobs = append(jobs, d)
	}

	results := make([]result, len(jobs))
	// 并发别开太大：同一 IP 同时打好几个清单地址时，CDN 会直接回 410。
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i, d := range jobs {
		wg.Add(1)
		go func(i int, d mediaDefinition) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			u := strings.TrimSpace(d.VideoURL)
			res := result{idx: i}
			switch {
			case strings.Contains(u, "get_media"):
				// mp4 组走 /video/get_media 接口，回的是直链 JSON 数组（含 1080p/4K）。
				vs, err := a.getMediaList(ctx, u)
				res.variants, res.err = vs, err
			case strings.Contains(u, ".m3u8") && !needBandwidth:
				// 快路径：清晰度已由 def 给出，不必为了码率再拉一次清单。
				h := d.Height
				if h == 0 {
					h = parseQualityToHeight(d.QualityText())
				}
				if h > 0 {
					res.variants = []model.Variant{{Height: h, Width: d.Width, URL: u}}
				}
			case strings.Contains(u, ".m3u8"):
				body, err := a.getAbsolute(ctx, u)
				if err != nil {
					// CDN 偶发 410/429：歇一下再试一次，成功就能拿到真实码率。
					time.Sleep(400 * time.Millisecond)
					body, err = a.getAbsolute(ctx, u)
				}
				if err != nil {
					res.err = err
					break
				}
				vs := ParseMasterPlaylist(string(body), u)
				res.bw = map[int]int{}
				for _, v := range vs {
					if v.Bandwidth > 0 {
						res.bw[v.Height] = v.Bandwidth
					}
				}
				res.variants = vs
			case d.Format == "mp4":
				vs, err := a.getMediaList(ctx, u)
				res.variants, res.err = vs, err
			default:
				h := d.Height
				if h == 0 {
					h = parseQualityToHeight(d.QualityText())
				}
				if h > 0 {
					res.variants = []model.Variant{{Height: h, Width: d.Width, URL: u}}
				}
			}
			results[i] = res
		}(i, d)
	}
	wg.Wait()

	var out []model.Variant
	var lastErr error
	// HLS 清单给出的码率是真实值：直链 mp4 的码率只能从文件名猜（偏大两三倍），
	// 同一清晰度两者都有时以 HLS 的为准，体积预估才准。
	hlsBW := map[int]int{}
	for _, res := range results {
		if res.err != nil {
			lastErr = res.err
			continue
		}
		for h, bw := range res.bw {
			hlsBW[h] = bw
		}
		out = append(out, res.variants...)
	}

	out = mergeVariants(out, hlsBW)
	if len(out) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

// mergeVariants 同一清晰度通常会同时出现 HLS 与直链 mp4 两条：
// 播放与下载都更喜欢直链（省一次分片拼接），但仍沿用 HLS 那条的码率信息。
func mergeVariants(in []model.Variant, hlsBW map[int]int) []model.Variant {
	type slot struct {
		v   model.Variant
		hls bool
	}
	order := make([]int, 0, len(in))
	byHeight := map[int]*slot{}
	for _, v := range in {
		if v.Height <= 0 {
			continue
		}
		isHLS := strings.Contains(v.URL, ".m3u8")
		cur, ok := byHeight[v.Height]
		if !ok {
			cp := v
			byHeight[v.Height] = &slot{v: cp, hls: isHLS}
			order = append(order, v.Height)
			continue
		}
		// 码率以 HLS 清单里的 BANDWIDTH 为准：实测与成品体积吻合（1080p ≈ 1.35 Mbps），
		// 而直链文件名里的 "1080P_4000K" 只是上限，用它预估体积会高估两三倍。
		if cur.v.Bandwidth == 0 && v.Bandwidth > 0 {
			cur.v.Bandwidth = v.Bandwidth
			// 码率是从直链文件名猜的这一点要一起带过来，否则体积预估会把它当真值。
			cur.v.ApproxBandwidth = v.ApproxBandwidth
		}
		if cur.v.Width == 0 && v.Width > 0 {
			cur.v.Width = v.Width
		}
		if cur.v.Codec == "" && v.Codec != "" {
			cur.v.Codec = v.Codec
		}
		if cur.hls && !isHLS {
			cur.v.URL = v.URL
			cur.hls = false
		}
	}
	out := make([]model.Variant, 0, len(order))
	for _, h := range order {
		v := byHeight[h].v
		if bw := hlsBW[h]; bw > 0 {
			v.Bandwidth = bw
			v.ApproxBandwidth = false
			if v.Width == 0 {
				v.Width = h * 16 / 9
			}
		}
		out = append(out, v)
	}
	return out
}

// getMediaList 请求 /video/get_media 拿一批直链 mp4（比 HLS 更适合 -c copy 封装）。
// 该接口依赖页面种下的 cookie，且只对部分账号/地区返 4K，拿不到时静默跳过。
func (a *Adapter) getMediaList(ctx context.Context, raw string) ([]model.Variant, error) {
	body, err := a.getMediaJSON(ctx, raw)
	if err != nil {
		return nil, err
	}
	var defs []mediaDefinition
	if err := json.Unmarshal(body, &defs); err != nil {
		return nil, nil
	}
	out := make([]model.Variant, 0, len(defs))
	for _, d := range defs {
		u := strings.TrimSpace(d.VideoURL)
		if u == "" {
			continue
		}
		h := d.Height
		if h == 0 {
			h = parseQualityToHeight(d.QualityText())
		}
		if h == 0 {
			continue
		}
		bw := bitrateFromQuality(d.QualityText())
		approx := false
		if bw == 0 {
			bw = bitrateFromURL(u)
			approx = bw > 0
		}
		w := d.Width
		if w == 0 {
			w = h * 16 / 9
		}
		out = append(out, model.Variant{
			Height:          h,
			Width:           w,
			Bandwidth:       bw,
			Codec:           "avc1",
			URL:             u,
			ApproxBandwidth: approx,
		})
	}
	return out, nil
}

var reBitrateInURL = regexp.MustCompile(`_(\d+)K_`)

// bitrateFromURL 从 CDN 路径里的 "1080P_4000K_" 抠码率（bps）。
// get_media 返回的条目 quality 字段是空数组，只能这样反推。
func bitrateFromURL(u string) int {
	m := reBitrateInURL.FindStringSubmatch(u)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n * 1000
}

// bitrateFromQuality 从 "1080P_4000K" 这类标识里抠出码率（bps）。
func bitrateFromQuality(q string) int {
	i := strings.LastIndex(q, "_")
	if i < 0 || i+1 >= len(q) {
		return 0
	}
	s := strings.TrimSuffix(strings.ToUpper(q[i+1:]), "K")
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n * 1000
}

func (a *Adapter) getMediaJSON(ctx context.Context, raw string) ([]byte, error) {
	if err := a.pace(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Referer", a.base+"/")
	req.Header.Set("Origin", a.base)
	req.Header.Set("platform", "PC")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("get_media 返回 %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

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
	if a.cookie != "" {
		req.Header.Set("Cookie", a.cookie)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("拉取 m3u8 失败: %d", resp.StatusCode)
	}
	buf := make([]byte, 0, 16*1024)
	tmp := make([]byte, 16*1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			if isEOF(err) {
				break
			}
			return nil, err
		}
		if len(buf) > 2<<20 {
			break
		}
	}
	return buf, nil
}

func (a *Adapter) SearchPerformers(ctx context.Context, query string, page int) ([]*model.Performer, bool, error) {
	if page < 1 {
		page = 1
	}
	q := url.Values{"search": {query}}
	if page > 1 {
		q.Set("page", fmt.Sprint(page))
	}
	// 依次尝试已知的搜索入口，任一返回结果即采用。
	paths := []string{"/model/search", "/pornstars"}
	var lastErr error
	for _, p := range paths {
		body, err := a.get(ctx, p, q)
		if err != nil {
			lastErr = err
			continue
		}
		items, hasMore := parseSearchResults(body, a.base)
		if len(items) > 0 {
			for _, it := range items {
				it.Platform = a.Key()
			}
			return items, hasMore, nil
		}
	}
	if lastErr != nil {
		return nil, false, lastErr
	}
	return nil, false, nil
}
