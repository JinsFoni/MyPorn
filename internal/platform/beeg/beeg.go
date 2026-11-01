package beeg

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
	"time"

	"golang.org/x/time/rate"

	"github.com/JinsFoni/MyPorn/internal/hls"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
)

// beeg.com 前端是个纯 SPA，页面本身只有一段 <div id="app">；
// 真正的数据全在公开接口 store.externulls.com 上，视频流在 video.beeg.com。
// 这个文件实现 Adapter（艺人、影片列表、详情、播放地址），头像复用 avatar.go 那套规则。
//
// 接口一览：
//
//	GET  {store}/tag?slug={slug}                     艺人资料（beeg 内部把人和厂牌统称 tag）
//	GET  {store}/tag/videos/{slug}?limit=48&offset=N 该艺人的影片列表（limit 最小 10）
//	GET  {store}/facts/file/{fileId}                 影片详情（标题、时长、发布日、标签）
//	GET  {store}/video/play_url/{fileId}             播放地址模板（纯文本，要拼上 CDN 域名）
//
// URL 形态：艺人 https://beeg.com/{TagSlug}，影片 https://beeg.com/{fileId}。

const (
	defaultBase = "https://beeg.com"
	// videoCDN 是播放清单所在的域名；play_url 只给相对路径。
	videoCDN = "https://video.beeg.com"

	// listPageSize 是列表每页条数。接口的 limit 最小值是 10，小于 10 会 400。
	listPageSize = 48
)

// reservedPaths 是 beeg 自己的路由，别把它们当成艺人 slug。
var reservedPaths = map[string]bool{
	"people": true, "creators": true, "account": true, "auth": true, "claim": true,
	"clipped": true, "contact": true, "contributor": true, "contributor-old": true,
	"copyright": true, "following": true, "ip-holder": true, "playground": true,
	"privacy": true, "reacted": true, "saved": true, "takedown": true, "terms": true,
	"search": true, "trending": true, "new": true, "hot": true, "live": true,
	"premium": true, "login": true, "signup": true, "sign-up": true, "tags": true,
	"about": true, "dmca": true, "2257": true, "sitemap": true, "pro": true,
	"notification": true, "settings": true, "messages": true, "support": true,
}

// Adapter 实现 platform.Adapter。
type Adapter struct {
	base     string
	storeAPI string
	thumbAPI string
	client   *http.Client
	lim      *rate.Limiter
}

// New 创建适配器。baseURL 是站点根地址（默认 https://beeg.com），proxyFor 返回空串表示直连（每次发请求现问，改设置即时生效）。
func New(baseURL string, proxyFor func() string) (*Adapter, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBase
	}
	hc, err := platform.NewDynamicHTTPClient(proxyFor, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		base:     strings.TrimSuffix(baseURL, "/"),
		storeAPI: storeAPI,
		thumbAPI: thumbBase,
		client:   hc,
		// 接口都在同一台机器上，别打太密：500ms 一次，允许 2 个突发。
		lim: rate.NewLimiter(rate.Every(500*time.Millisecond), 2),
	}, nil
}

func (a *Adapter) Key() string     { return "beeg" }
func (a *Adapter) Name() string    { return "beeg" }
func (a *Adapter) BaseURL() string { return a.base }

// HTTPClient 暴露客户端（代理配置与播放请求共用）。
func (a *Adapter) HTTPClient() *http.Client { return a.client }

var errNotFound = fmt.Errorf("%w（beeg 上没有这个条目）", platform.ErrNotFound)

// pace 做礼貌限速。
func (a *Adapter) pace(ctx context.Context) error {
	if err := a.lim.Wait(ctx); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(rand.Intn(250)) * time.Millisecond):
	}
	return nil
}

// getJSON 请求接口并把 JSON 解到 out。notFound 也当错误返回，调用方按需判断。
func (a *Adapter) getJSON(ctx context.Context, u string, out any) error {
	body, err := a.get(ctx, u)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析 beeg 接口响应失败: %w", err)
	}
	return nil
}

// get 取原始响应体。接口偶发 429/5xx（同步任务与浏览撞在一起时会遇到），
// 所以失败一次再歇半秒重试一次——重试比把 502 抛给界面划算。
func (a *Adapter) get(ctx context.Context, u string) ([]byte, error) {
	body, err, retryable := a.getOnce(ctx, u)
	if err == nil || !retryable {
		return body, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(600 * time.Millisecond):
	}
	body, err, _ = a.getOnce(ctx, u)
	return body, err
}

// getOnce 发一次请求。retryable 表示这次失败值得重试（限流、5xx）。
func (a *Adapter) getOnce(ctx context.Context, u string) (body []byte, err error, retryable bool) {
	if err := a.pace(ctx); err != nil {
		return nil, err, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err, false
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", a.base+"/")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err, false
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNotFound, false
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("beeg 接口限流"), true
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("beeg 接口返回 %d", resp.StatusCode), true
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("beeg 接口返回 %d", resp.StatusCode), false
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return body, err, false
}

var (
	reTagPath = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.\-]{1,63}$`)
	reDigits  = regexp.MustCompile(`^[0-9]+$`)
)

// ParsePerformerURL 解析 https://beeg.com/MiniDiva 这类艺人主页地址。
// beeg 的艺人和影片共用一个路由（影片是纯数字 ID），所以纯数字地址按影片处理、直接拒。
func (a *Adapter) ParsePerformerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("URL 为空")
	}
	if !strings.Contains(raw, "://") {
		// 允许只贴 slug（搜索框直接敲名字时走这条路）。
		if reTagPath.MatchString(raw) && !reservedPaths[strings.ToLower(raw)] {
			return raw, nil
		}
		return "", errors.New("不是合法的 beeg 艺人标识")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("URL 非法")
	}
	host := strings.ToLower(u.Hostname())
	if !strings.Contains(host, "beeg.") {
		return "", errors.New("不是 beeg 的链接")
	}
	seg := strings.Trim(u.Path, "/")
	if seg == "" {
		return "", errors.New("链接里没有艺人标识（应形如 https://beeg.com/MiniDiva）")
	}
	if reDigits.MatchString(seg) {
		return "", errors.New("这是影片链接，不是艺人主页")
	}
	if !reTagPath.MatchString(seg) || reservedPaths[strings.ToLower(seg)] {
		return "", errors.New("无法识别的 beeg 链接")
	}
	return seg, nil
}

// GetPerformer 抓艺人资料：名字、头像、影片数、关注数、简介、外部链接。
func (a *Adapter) GetPerformer(ctx context.Context, performerID string) (*model.Performer, error) {
	slug := slugify(performerID)
	if slug == "" {
		return nil, fmt.Errorf("艺人标识无法使用: %q", performerID)
	}
	var t tagDetail
	if err := a.getJSON(ctx, a.storeAPI+"/tag?slug="+url.QueryEscape(slug)+"&get_original=true&with_relations=true", &t); err != nil {
		return nil, err
	}
	if t.Name == "" {
		return nil, fmt.Errorf("beeg 上没有艺人 %q", performerID)
	}
	p := &model.Performer{
		Platform:    a.Key(),
		PerformerID: firstNonEmpty(t.Slug, performerID),
		Name:        t.Name,
		Bio:         t.Caption(),
		URL:         a.base + "/" + firstNonEmpty(t.Slug, performerID),
		AvatarURL:   t.avatarURL(a.thumbAPI),
	}
	if t.VideosCount > 0 {
		n := t.VideosCount
		p.VideoCount = &n
	}
	if t.Followers > 0 {
		n := t.Followers
		p.SubscriberCount = &n
	}
	if t.Likes > 0 {
		p.Profile = append(p.Profile, model.ProfileField{Label: "点赞", Value: formatCount(t.Likes)})
	}
	if t.Followers > 0 {
		p.Profile = append(p.Profile, model.ProfileField{Label: "关注者", Value: formatCount(t.Followers)})
	}
	if ts, err := time.Parse(time.RFC3339Nano, t.Created); err == nil && !ts.IsZero() {
		p.Profile = append(p.Profile, model.ProfileField{Label: "加入时间", Value: ts.Format("2006-01-02")})
	}
	if t.Verified {
		p.Profile = append(p.Profile, model.ProfileField{Label: "认证", Value: "是"})
	}
	for _, l := range t.Links {
		label := strings.TrimSpace(l.Type.Name)
		u := strings.TrimSpace(l.URL)
		if label == "" || u == "" {
			continue
		}
		if !strings.Contains(u, "://") {
			u = "https://" + u
		}
		p.Social = append(p.Social, model.SocialLink{Label: label, URL: u})
	}
	return p, nil
}

// ListPerformerVideos 取一页影片。接口用 offset 翻页，一页 48 条。
func (a *Adapter) ListPerformerVideos(ctx context.Context, performerID string, page int) (*platform.Page, error) {
	if page < 1 {
		page = 1
	}
	slug := slugify(performerID)
	if slug == "" {
		return nil, fmt.Errorf("艺人标识无法使用: %q", performerID)
	}
	// 列表接口的路径参数用小写 slug（站点自己也是这么调的）。
	u := fmt.Sprintf("%s/tag/videos/%s?limit=%d&offset=%d",
		a.storeAPI, url.PathEscape(slug), listPageSize, (page-1)*listPageSize)
	var items []fileItem
	if err := a.getJSON(ctx, u, &items); err != nil {
		if errors.Is(err, errNotFound) {
			return &platform.Page{Page: page}, nil
		}
		return nil, err
	}
	out := &platform.Page{Page: page}
	for _, it := range items {
		v := it.toVideo(a.base, a.thumbAPI)
		if v == nil {
			continue
		}
		v.Platform = a.Key()
		out.Items = append(out.Items, *v)
	}
	out.HasMore = len(items) >= listPageSize
	return out, nil
}

// GetVideo 抓影片详情（标题、时长、发布日、观看数、标签、出演者）。
func (a *Adapter) GetVideo(ctx context.Context, videoID string) (*model.Video, error) {
	id := strings.TrimSpace(videoID)
	if !reDigits.MatchString(id) {
		return nil, errors.New("beeg 影片标识应为纯数字 ID")
	}
	var it fileItem
	if err := a.getJSON(ctx, a.storeAPI+"/facts/file/"+id, &it); err != nil {
		return nil, err
	}
	v := it.toVideo(a.base, a.thumbAPI)
	if v == nil {
		return nil, errors.New("beeg 影片详情为空（可能已下架）")
	}
	v.Platform = a.Key()
	return v, nil
}

// ResolvePlayback 一次拿元数据 + 播放地址。
func (a *Adapter) ResolvePlayback(ctx context.Context, videoID string) (*model.Video, []model.Variant, error) {
	v, err := a.GetVideo(ctx, videoID)
	if err != nil {
		return nil, nil, err
	}
	variants, err := a.resolveStreams(ctx, videoID)
	if err != nil {
		return v, nil, err
	}
	return v, variants, nil
}

// ResolveStreams 只解播放地址。
func (a *Adapter) ResolveStreams(ctx context.Context, videoID string) ([]model.Variant, error) {
	return a.resolveStreams(ctx, videoID)
}

// resolveStreams 先问 play_url 要「带签名的 master 清单模板」，再把模板拉下来展开成清晰度列表。
// play_url 返回的是纯文本相对路径（含 key/end 签名，几小时后过期），所以每次播放都得重新问一次。
func (a *Adapter) resolveStreams(ctx context.Context, videoID string) ([]model.Variant, error) {
	id := strings.TrimSpace(videoID)
	if !reDigits.MatchString(id) {
		return nil, errors.New("beeg 影片标识应为纯数字 ID")
	}
	raw, err := a.get(ctx, a.storeAPI+"/video/play_url/"+id)
	if err != nil {
		return nil, fmt.Errorf("取播放地址失败: %w", err)
	}
	path := strings.TrimSpace(string(raw))
	if path == "" {
		return nil, errors.New("beeg 没有返回播放地址（可能是付费内容）")
	}
	master := path
	if !strings.Contains(master, "://") {
		master = videoCDN + "/" + strings.TrimLeft(master, "/")
	}
	body, err := a.get(ctx, master)
	if err != nil {
		return nil, fmt.Errorf("取清单失败: %w", err)
	}
	variants := hls.ParseMasterPlaylist(string(body), master)
	if len(variants) == 0 {
		return nil, errors.New("清单里没有可用清晰度（站点结构可能已变更）")
	}
	model.SortVariantsDesc(variants)
	return variants, nil
}

// SearchPerformers 按名字找艺人。beeg 没有公开的名字搜索接口，
// 但 /tag?slug= 是按「名字压成 slug」精确查（大小写不敏感），所以走「名字 → slug → 探一下」：
// 查得到且名字对得上就返回，否则回空。搜索框里贴主页链接永远是最稳的路子。
func (a *Adapter) SearchPerformers(ctx context.Context, query string, page int) ([]*model.Performer, bool, error) {
	if page > 1 {
		return nil, false, nil
	}
	slug := slugify(query)
	if slug == "" {
		return nil, false, nil
	}
	var t tagDetail
	if err := a.getJSON(ctx, a.storeAPI+"/tag?slug="+url.QueryEscape(slug), &t); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if slugify(t.Name) != slug {
		return nil, false, nil
	}
	p := &model.Performer{
		Platform:    a.Key(),
		PerformerID: firstNonEmpty(t.Slug, slug),
		Name:        t.Name,
		URL:         a.base + "/" + firstNonEmpty(t.Slug, slug),
		AvatarURL:   t.avatarURL(a.thumbAPI),
	}
	if t.VideosCount > 0 {
		n := t.VideosCount
		p.VideoCount = &n
	}
	if t.Followers > 0 {
		n := t.Followers
		p.SubscriberCount = &n
	}
	return []*model.Performer{p}, false, nil
}

// ---- 接口数据结构 ----

// tagDetail 是 /tag 接口的返回（艺人或厂牌）。
type tagDetail struct {
	ID          int64  `json:"id"`
	Name        string `json:"tg_name"`
	Slug        string `json:"tg_slug"`
	Followers   int    `json:"tg_followers"`
	Likes       int    `json:"tg_likes"`
	VideosCount int    `json:"tg_videos_count"`
	Verified    bool   `json:"tg_verified"`
	Created     string `json:"tg_created"`
	Data        []struct {
		Column string `json:"td_column"`
		Value  string `json:"td_value"`
	} `json:"data"`
	Links  []linkEntry `json:"links"`
	Thumbs []struct {
		ID    int64 `json:"id"`
		Crops []struct {
			ID int64 `json:"id"`
		} `json:"crops"`
	} `json:"thumbs"`
}

// Caption 取简介（tg_caption 那一行）。
func (t tagDetail) Caption() string {
	for _, d := range t.Data {
		if d.Column == "tg_caption" && strings.TrimSpace(d.Value) != "" {
			return strings.TrimSpace(d.Value)
		}
	}
	return ""
}

// avatarURL 拼出头像地址（beeg 图床按照片 ID + 裁切 ID 取图）。
func (t tagDetail) avatarURL(thumbBase string) string {
	if len(t.Thumbs) == 0 || t.Thumbs[0].ID == 0 {
		return ""
	}
	src := fmt.Sprintf("%s/photos/%d/to.webp", thumbBase, t.Thumbs[0].ID)
	if len(t.Thumbs[0].Crops) > 0 && t.Thumbs[0].Crops[0].ID != 0 {
		src += fmt.Sprintf("?crop_id=%d", t.Thumbs[0].Crops[0].ID)
	}
	return src
}

// linkEntry 是艺人主页上挂的外部链接（OnlyFans / Twitter / Instagram…）。
type linkEntry struct {
	URL  string `json:"lk_url"`
	Text string `json:"lk_text_link"`
	Type struct {
		Name string `json:"lt_name"`
	} `json:"type"`
}

// fileItem 是列表和详情共用的影片结构：file 是文件本体，fc_facts 是统计，tags 是标签/出演者。
type fileItem struct {
	File struct {
		ID       int64  `json:"id"`
		Duration int    `json:"fl_duration"`
		Height   int    `json:"fl_height"`
		Width    int    `json:"fl_width"`
		Codec    string `json:"fl_min_codec"`
		Data     []struct {
			Column string `json:"cd_column"`
			Value  string `json:"cd_value"`
		} `json:"data"`
	} `json:"file"`
	Facts []struct {
		Created string  `json:"fc_created"`
		Views   int     `json:"fc_st_views"`
		Thumbs  []int64 `json:"fc_thumbs"`
	} `json:"fc_facts"`
	Tags []struct {
		Name    string `json:"tg_name"`
		Slug    string `json:"tg_slug"`
		IsOwner bool   `json:"is_owner"`
		IsHuman bool   `json:"is_person"`
	} `json:"tags"`
}

// toVideo 把接口结构转成 model.Video。
func (f fileItem) toVideo(base, thumbBase string) *model.Video {
	if f.File.ID == 0 {
		return nil
	}
	id := strconv.FormatInt(f.File.ID, 10)
	v := &model.Video{
		PlatformVideoID: id,
		Title:           f.title(),
		URL:             base + "/" + id,
		DurationSec:     f.File.Duration,
	}
	if len(f.Facts) > 0 {
		if f.Facts[0].Views > 0 {
			n := f.Facts[0].Views
			v.ViewCount = &n
		}
		if ts, err := time.Parse(time.RFC3339Nano, f.Facts[0].Created); err == nil && !ts.IsZero() {
			v.PublishedAt = &ts
		}
		// fc_thumbs[0] 为 0 是正常值（站点自己就用 /videos/{id}/0.webp 当首帧），只要数组非空。
		if len(f.Facts[0].Thumbs) > 0 {
			v.ThumbURL = fmt.Sprintf("%s/videos/%d/%d.webp?size=480x270",
				thumbBase, f.File.ID, f.Facts[0].Thumbs[0])
		}
	}
	for _, t := range f.Tags {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		if t.IsHuman {
			v.Performers = append(v.Performers, model.PerformerRef{
				Name: name,
				URL:  base + "/" + t.Slug,
			})
			continue
		}
		// 上传者/厂牌不算标签，其余（题材、画质）算。
		if t.IsOwner {
			continue
		}
		v.Tags = append(v.Tags, cleanTagName(name))
	}
	return v
}

// cleanTagName 去掉 beeg 标签里的花括号：站点把 `Big {Tits}` 渲染成「Big 🍒Tits」，
// 花括号只是给前端标出「这个词有图标」。留着括号会让标签库里出现「Big {Tits}」
// 这种带符号的怪标签，和「Big Tits」重复；所以只去括号、词本身保留。
var reTagDecoration = regexp.MustCompile(`[{}]`)

func cleanTagName(name string) string {
	out := reTagDecoration.ReplaceAllString(name, "")
	return strings.Join(strings.Fields(out), " ")
}

// title 取标题（sf_name 那一行）。
func (f fileItem) title() string {
	for _, d := range f.File.Data {
		if d.Column == "sf_name" && strings.TrimSpace(d.Value) != "" {
			return strings.TrimSpace(d.Value)
		}
	}
	if len(f.File.Data) > 0 {
		return strings.TrimSpace(f.File.Data[0].Value)
	}
	return ""
}

// formatCount 把数字压成 12.3K / 4.5M 这种紧凑写法（资料行空间有限）。
func formatCount(n int) string {
	switch {
	case n >= 1_000_000:
		return trimZero(float64(n)/1_000_000) + "M"
	case n >= 1_000:
		return trimZero(float64(n)/1_000) + "K"
	default:
		return strconv.Itoa(n)
	}
}

func trimZero(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}

func firstNonEmpty(vals ...string) string {
	for _, s := range vals {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// 编译期检查：Adapter 满足 platform.Adapter 与 platform.PlaybackResolver。
var (
	_ platform.Adapter          = (*Adapter)(nil)
	_ platform.PlaybackResolver = (*Adapter)(nil)
)
