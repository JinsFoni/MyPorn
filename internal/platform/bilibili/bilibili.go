// Package bilibili 实现 B 站（bilibili）站点适配器。
//
// 这里说的「艺人」是 UP 主（mid），「影片」是 UP 主的投稿（bv 号）。
// 与另外三家（Pornhub / xHamster / beeg）最大的不同：B 站的数据全在 JSON 接口里，
// 没有页面可解析，但接口分两种脾气完全不同的：
//
//   - 资料、详情、播放地址（/x/web-interface/card、/x/web-interface/view、
//     /x/player/playurl）——随便调，几十次连发也没被拦过；
//   - 空间投稿列表（/x/space/arc/search）——**全站风控最严的一个**。浏览器能过是因为
//     它带 WBI 签名 + 一整套 dm_* 指纹参数 + 真实浏览器 TLS 指纹；服务端调 wbi 版本
//     一律 -352「风控校验失败」，非 wbi 版本能过但限流极紧。
//
// 2026-10 实测（无 Cookie，从大陆家宽直连）：
//
//	mid=946974（影视飓风，939 部投稿）
//	  - 第 1 次 /x/space/arc/search?ps=50&pn=1    → 0，正常返回
//	  - 隔 10s 再打 pn=2 / pn=3 / pn=1             → -799「请求过于频繁」→ HTTP 412「request was banned」
//	  - 停 55s 后单打 pn=2                        → 0，正常返回
//	  - 期间 /x/web-interface/view 与 /x/player/playurl 一直正常
//
// 补充一条更糟的：封禁是会滚动的。同一天里反复试探之后，space 接口对这个 IP 连续
// 封了一小时以上（每探一次都像在给它续期，间隔 35s/60s 一律 412 空响应），
// 而 App 接口全程正常。所以「探一下通不通」不能当健康检查用，日常增量就走 App 那条。
//
// 所以列表接口单独限速（1 次 / 20 秒），撞限流再退避重试两次
// （30s、60s），仍然不行就把 ErrRateLimited 抛给任务层——同步任务会记 LastError，
// 回填断点存在 subscriptions.backfill_page 里，下一轮从断点接着翻，不会白跑。
//
// 另一条路是 App 的 /x/v2/space/archive/cursor（带 appkey 签名）：它**完全不设防**
// （同一个 IP 在被 arc/search 封着的时候照样正常返回），但作为访客只能拿到第 1 页的 20 条
// ——offset / pn / page 传什么都忽略，next_cursor 只对登录用户有意义。
// 因此：第 1 页（增量同步天天走的就是这一页）优先用 App 接口，翻页回填才动 arc/search。
//
// 画质：未登录时 accept_quality/support_formats 照样把 4K、1080P 列出来，但
// dash.video 里只有 360P/480P、durl 只给到 720P——**能拿到什么以实际返回为准**，
// 接口声明的那份不能信。要 1080P 以上必须在设置里填 SESSDATA（见 cookie 相关注释）。
package bilibili

import (
	"context"
	"crypto/md5"
	"encoding/hex"
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

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
)

const (
	defaultBase = "https://www.bilibili.com"
	defaultAPI  = "https://api.bilibili.com"
	userAgent   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	// listPageSize 是空间投稿列表的单页条数。
	//
	// 取 20 而不是 web 接口允许的 50：第 1 页优先用 App 的 cursor 接口（见 appPage），
	// 那个接口**只肯给 20 条**（传 ps=30/50 实测还是 20）。两个来源的页大小必须一致，
	// 否则从第 2 页接着翻的 arc/search 会与 App 的第 1 页错位，中间那 30 条永远补不上。
	listPageSize = 20
	// appKey/appSecret 是 B 站安卓客户端公开的那对常量，用来给 App 接口算签名
	// （md5(排序后的参数串 + appsec)）。这不是破解什么，公开客户端里就摆着。
	appKey    = "1d8b6e7d45233436"
	appSecret = "560c52ccd288fed045859ed18bffd973"
	appBase   = "https://app.bilibili.com"
)

// ErrRateLimited 表示被 B 站风控/限流挡了：等一会儿再来，不是永久错误。
// 包在 platform.ErrRateLimited 外面，任务层不用认识 bilibili 这个包也能认出「限流」。
var ErrRateLimited = fmt.Errorf("%w：B 站限流了，等一会儿再同步", platform.ErrRateLimited)

// retryBackoff 是撞限流后的退避表（第 n 次重试前等多久）。
// 抽成变量是为了测试能把它清零，不然一个用例要真睡 90 秒。
var retryBackoff = []time.Duration{30 * time.Second, 60 * time.Second}

// errNotFound 包着公共的 platform.ErrNotFound，外面用 errors.Is 能认出 404。
var errNotFound = fmt.Errorf("%w（B 站说没有这条）", platform.ErrNotFound)

// Adapter 实现 platform.Adapter（以及可选的 PlaybackResolver）。
type Adapter struct {
	base, api string
	// app 是 App 接口的域名（https://app.bilibili.com）。字段化是为了测试能指到 httptest。
	app    string
	client *http.Client
	// cookieFor 每次发请求现问一次：设置里改完 Cookie 立刻生效，不用重启。
	cookieFor func() string
	// lim 给资料/详情/播放地址这类不设防的接口用。
	lim *rate.Limiter
	// listLim 只给空间投稿列表用，见包注释里的实测数据。
	listLim *rate.Limiter
}

// New 创建适配器。proxyFor / cookieFor 都允许为 nil（空 = 直连 / 不带 Cookie）。
func New(baseURL string, proxyFor func() string, cookieFor func() string) (*Adapter, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBase
	}
	hc, err := platform.NewDynamicHTTPClient(proxyFor, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if cookieFor == nil {
		cookieFor = func() string { return "" }
	}
	return &Adapter{
		base:      strings.TrimSuffix(baseURL, "/"),
		api:       defaultAPI,
		app:       appBase,
		client:    hc,
		cookieFor: cookieFor,
		lim:       rate.NewLimiter(rate.Every(1200*time.Millisecond), 2),
		// 空间投稿列表：实测撞限流的阈值在「隔 10 秒打三次」附近，
		// 2 次 / 12 秒（允许 1 个突发）留了足够余量，回填一部千条投稿的 UP 主约 10 分钟。
		listLim: rate.NewLimiter(rate.Every(20*time.Second), 1),
	}, nil
}

// HTTPClient 暴露带 cookie jar 的客户端（媒体地址与页面同源校验时要用）。
func (a *Adapter) HTTPClient() *http.Client { return a.client }

func (a *Adapter) Key() string     { return "bilibili" }
func (a *Adapter) Name() string    { return "bilibili" }
func (a *Adapter) BaseURL() string { return a.base }

// ---- 请求层 ----

// apiResp 是所有 /x/... 接口共用的外壳。
type apiResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// call 取 /x/... 接口的 data 段并解到 out 里。撞限流会退避重试两次。
func (a *Adapter) call(ctx context.Context, path string, q url.Values, lim *rate.Limiter, referer string, out any) error {
	return a.callData(ctx, a.api+path, q, lim, referer, out)
}

// callData 与 call 一样，但第一个参数是完整地址（App 接口在另一个域名上）。
func (a *Adapter) callData(ctx context.Context, endpoint string, q url.Values, lim *rate.Limiter,
	referer string, out any) error {
	var r apiResp
	if err := a.callInto(ctx, endpoint, q, lim, referer, &r); err != nil {
		return err
	}
	if len(r.Data) == 0 || string(r.Data) == "null" {
		return fmt.Errorf("B 站接口没有返回数据（%s）", endpoint)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		return fmt.Errorf("解析 B 站接口数据失败（%s）: %w", endpoint, err)
	}
	return nil
}

// callInto 打一个接口并把整个响应体（不是 data 段）解到 out 里。
func (a *Adapter) callInto(ctx context.Context, endpoint string, q url.Values, lim *rate.Limiter,
	referer string, out any) error {
	body, err := a.fetch(ctx, endpoint, q, lim, referer)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("B 站接口返回的不是 JSON: %w", err)
	}
	return nil
}

// fetch 拼好 query 打一次接口（带限流重试）。
func (a *Adapter) fetch(ctx context.Context, endpoint string, q url.Values, lim *rate.Limiter, referer string) ([]byte, error) {
	u := endpoint
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var lastErr error
	for attempt := 0; attempt <= len(retryBackoff); attempt++ {
		if attempt > 0 {
			wait := retryBackoff[attempt-1]
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		body, err := a.fetchURL(ctx, u, lim, referer)
		if err == nil {
			return body, nil
		}
		if !errors.Is(err, ErrRateLimited) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// fetchURL 是真正落到网络上的那一层，负责限速、请求头、以及把 B 站的错误码翻成我们的哨兵。
func (a *Adapter) fetchURL(ctx context.Context, u string, lim *rate.Limiter, referer string) ([]byte, error) {
	if lim != nil {
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
		// 0–300ms 抖动：所有实例都精确到毫秒会显得很机器。
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(rand.Intn(300)) * time.Millisecond):
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	// 带 Cookie 的请求必须同时给 Origin，否则空间接口会按「无来源」处理。
	if c := strings.TrimSpace(a.cookieFor()); c != "" {
		req.Header.Set("Cookie", c)
		if strings.Contains(referer, "space.bilibili.com") {
			req.Header.Set("Origin", "https://space.bilibili.com")
		}
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPreconditionFailed, // 412 = 被风控封了
		resp.StatusCode == http.StatusTooManyRequests, // 429
		resp.StatusCode == http.StatusNotAcceptable:   // 406：同一套风控的另一种脸
		return nil, ErrRateLimited
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNotFound
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("上游返回 %d（%s）", resp.StatusCode, u)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	// 业务错误码：-799 频繁、-412 封禁、-352 风控校验（服务端指纹过不了 wbi）、
	// -509 超频。这几个都当「等一会儿」处理；其余照原样报出来。
	var r apiResp
	if err := json.Unmarshal(body, &r); err == nil {
		switch r.Code {
		case 0:
		case -799, -412, -509, -352:
			return nil, fmt.Errorf("%w（B 站回 %d：%s）", ErrRateLimited, r.Code, r.Message)
		case -404:
			return nil, errNotFound
		case -101: // 账号未登录：某些接口（如空间列表的第 2 页起）需要登录
			return nil, fmt.Errorf("B 站要求登录后才能取这份数据（%s）：到设置里填 SESSDATA", r.Message)
		default:
			return nil, fmt.Errorf("B 站接口返回 %d：%s", r.Code, r.Message)
		}
	}
	return body, nil
}

// ---- URL 解析 ----

var (
	reMid     = regexp.MustCompile(`^\d{1,20}$`)
	reSpaceID = regexp.MustCompile(`/(?:space|space\.bilibili\.com)?/?(\d{1,20})(?:/|$|\?)`)
)

// ParsePerformerURL 从 UP 主主页链接里取 mid。支持：
//
//	https://space.bilibili.com/946974
//	https://space.bilibili.com/946974/video?spm_id_from=333.999
//	https://m.bilibili.com/space/946974
//	946974（只贴 mid）
//
// 不认单条视频链接：那属于「影片」，不是订阅对象；贴上来会明确报错而不是猜。
func (a *Adapter) ParsePerformerURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("URL 为空")
	}
	if reMid.MatchString(s) {
		return s, nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("URL 非法")
	}
	host := strings.ToLower(u.Hostname())
	if !strings.Contains(host, "bilibili.com") {
		return "", errors.New("不是 B 站（bilibili.com）的链接")
	}
	// /space/<mid> 与 space.bilibili.com/<mid> 两种写法：统一从路径里抠第一串数字。
	if m := reSpaceID.FindStringSubmatch(u.Path); m != nil {
		return m[1], nil
	}
	if m := regexp.MustCompile(`^\d{1,20}$`).FindString(strings.Trim(u.Path, "/")); m != "" {
		return m, nil
	}
	// 视频链接：明确说清楚，别让用户以为是网络问题。
	if strings.Contains(u.Path, "/video/") {
		return "", errors.New("这是单条视频的链接，订阅要贴 UP 主主页（space.bilibili.com/<mid>）")
	}
	return "", errors.New("链接里没有 UP 主 mid（应形如 space.bilibili.com/946974）")
}

// ---- 艺人（UP 主）资料 ----

type cardData struct {
	Card struct {
		Mid       string `json:"mid"`
		Name      string `json:"name"`
		Face      string `json:"face"`
		Sign      string `json:"sign"`
		Sex       string `json:"sex"`
		LevelInfo struct {
			CurrentLevel int `json:"current_level"`
		} `json:"level_info"`
	} `json:"card"`
	// 粉丝数有两个来源：card.follower（老字段）和 card.card.fans，实测两者一致，取前者。
	Follower     int `json:"follower"`
	ArchiveCount int `json:"archive_count"`
}

// GetPerformer 抓 UP 主资料：名字、头像、签名（当简介用）、粉丝数、投稿数。
func (a *Adapter) GetPerformer(ctx context.Context, performerID string) (*model.Performer, error) {
	mid := strings.TrimSpace(performerID)
	if !reMid.MatchString(mid) {
		return nil, fmt.Errorf("UP 主 mid 不合法：%q", performerID)
	}
	var d cardData
	if err := a.call(ctx, "/x/web-interface/card", url.Values{"mid": {mid}}, a.lim, a.base+"/space/"+mid, &d); err != nil {
		return nil, err
	}
	if strings.TrimSpace(d.Card.Name) == "" {
		return nil, errNotFound
	}
	p := &model.Performer{
		Platform:    a.Key(),
		PerformerID: mid,
		Name:        d.Card.Name,
		URL:         a.base + "/space/" + mid,
		AvatarURL:   d.Card.Face,
		Bio:         strings.TrimSpace(d.Card.Sign),
	}
	if d.ArchiveCount > 0 {
		n := d.ArchiveCount
		p.VideoCount = &n
	}
	if d.Follower > 0 {
		n := d.Follower
		p.SubscriberCount = &n
	}
	// 资料行：B 站没有三围这类东西，能给的只有性别和等级。
	if s := strings.TrimSpace(d.Card.Sex); s != "" {
		p.Profile = append(p.Profile, model.ProfileField{Label: "性别", Value: s})
	}
	if lvl := d.Card.LevelInfo.CurrentLevel; lvl > 0 {
		p.Profile = append(p.Profile, model.ProfileField{Label: "等级", Value: "L" + strconv.Itoa(lvl)})
	}
	return p, nil
}

// ---- 投稿列表 ----

type spaceList struct {
	Page struct {
		PN    int `json:"pn"`
		PS    int `json:"ps"`
		Count int `json:"count"`
	} `json:"page"`
	List struct {
		Vlist []struct {
			BVID        string `json:"bvid"`
			Title       string `json:"title"`
			Pic         string `json:"pic"`
			Length      string `json:"length"`
			Created     int64  `json:"created"`
			Play        int64  `json:"play"`
			Description string `json:"description"`
			TName       string `json:"tname"`
			Author      string `json:"author"`
			Mid         int64  `json:"mid"`
		} `json:"vlist"`
	} `json:"list"`
}

// appArchive 是 App 版列表里的一条投稿（字段比 web 版少，但够建卡）。
type appArchive struct {
	BVID     string `json:"bvid"`
	Title    string `json:"title"`
	Cover    string `json:"cover"`
	Duration int    `json:"duration"` // 秒
	CTime    int64  `json:"ctime"`
	Play     int64  `json:"play"`
	TName    string `json:"tname"`
	Goto     string `json:"goto"` // av = 普通投稿；别的（pgc 番剧、直播回放）不认
}

type appCursorData struct {
	Count   int          `json:"count"`
	HasNext bool         `json:"has_next"`
	Item    []appArchive `json:"item"`
}

// appPage 用安卓客户端的 cursor 接口取最新的一页（20 条，且只能取第 1 页）。
//
// 这个接口是整套方案的关键：同样的 IP、同样的时刻，arc/search 回 -799「请求过于频繁」
// 甚至 HTTP 412 封禁，它照样正常返回（2026-10 实测）。所以日常增量同步永远打得动——
// 每 6 小时一次、每次一页 20 条，对 UP 主更新频率来说绰绰有余。
func (a *Adapter) appPage(ctx context.Context, mid string) (*appCursorData, error) {
	q := url.Values{
		"vmid":     {mid},
		"ps":       {strconv.Itoa(listPageSize)},
		"order":    {"pubdate"},
		"mobi_app": {"android"},
		"build":    {"7000000"},
		"appkey":   {appKey},
		"ts":       {strconv.FormatInt(time.Now().Unix(), 10)},
	}
	// 签名 = md5(按 key 排序的 "k=v&k=v" + appSecret)，先算再加进 query。
	sum := md5.Sum([]byte(q.Encode() + appSecret))
	q.Set("sign", hex.EncodeToString(sum[:]))

	var d appCursorData
	if err := a.callData(ctx, a.app+"/x/v2/space/archive/cursor", q, a.lim,
		a.base+"/space/"+mid+"/video", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// pageFromApp 把 App 版列表翻成通用分页结构。
func (a *Adapter) pageFromApp(d *appCursorData) *platform.Page {
	out := &platform.Page{Page: 1}
	for _, it := range d.Item {
		if strings.TrimSpace(it.BVID) == "" {
			continue
		}
		// goto 不是 av 的是番剧/直播回放这类，播放地址拿不到，别塞进影片墙。
		if it.Goto != "" && it.Goto != "av" {
			continue
		}
		v := &model.Video{
			Platform:        a.Key(),
			PlatformVideoID: it.BVID,
			Title:           it.Title,
			URL:             a.base + "/video/" + it.BVID,
			ThumbURL:        thumbURL(it.Cover),
			DurationSec:     it.Duration,
		}
		if it.CTime > 0 {
			t := time.Unix(it.CTime, 0)
			v.PublishedAt = &t
		}
		if it.Play > 0 {
			n := int(it.Play)
			v.ViewCount = &n
		}
		if t := strings.TrimSpace(it.TName); t != "" {
			v.Tags = []string{t}
		}
		out.Items = append(out.Items, *v)
	}
	if d.Count > 0 {
		total := (d.Count + listPageSize - 1) / listPageSize
		out.TotalPages = total
		out.HasMore = total > 1
	}
	return out
}

// ListPerformerVideos 取一页投稿，按发布时间倒序（order=pubdate）。
//
// 第 1 页优先打 App 接口（不受风控），翻页只能打 web 的 arc/search（受限流）。
// 两个来源页大小都是 20，所以「第 1 页 20 条 + 第 2 页起 20 条一页」正好接得上。
func (a *Adapter) ListPerformerVideos(ctx context.Context, performerID string, page int) (*platform.Page, error) {
	if page < 1 {
		page = 1
	}
	if page == 1 {
		if d, err := a.appPage(ctx, performerID); err == nil && len(d.Item) > 0 {
			if out := a.pageFromApp(d); len(out.Items) > 0 {
				return out, nil
			}
		}
		// App 接口不可用（改版、签名失效、需要登录）时退回 web 列表，下面的重试与退避照旧。
	}
	return a.spacePage(ctx, performerID, page)
}

// spacePage 是 web 版投稿列表（/x/space/arc/search），翻页回填走这条。
func (a *Adapter) spacePage(ctx context.Context, performerID string, page int) (*platform.Page, error) {
	q := url.Values{
		"mid":           {performerID},
		"ps":            {strconv.Itoa(listPageSize)},
		"pn":            {strconv.Itoa(page)},
		"order":         {"pubdate"},
		"platform":      {"web"},
		"web_location":  {"1550101"},
		"order_avoided": {"true"},
		"keyword":       {""},
		"tid":           {"0"},
		"special_type":  {""},
		"index":         {"0"},
	}
	var d spaceList
	referer := a.base + "/space/" + performerID + "/video"
	if err := a.call(ctx, "/x/space/arc/search", q, a.listLim, referer, &d); err != nil {
		return nil, err
	}
	out := &platform.Page{Page: page}
	for _, it := range d.List.Vlist {
		// 合集/课程这类条目没有 bvid，跳过（它们不是单条视频）。
		if strings.TrimSpace(it.BVID) == "" {
			continue
		}
		v := &model.Video{
			Platform:        a.Key(),
			PlatformVideoID: it.BVID,
			Title:           it.Title,
			URL:             a.base + "/video/" + it.BVID,
			ThumbURL:        thumbURL(it.Pic),
			DurationSec:     parseLength(it.Length),
			Description:     it.Description,
		}
		if it.Created > 0 {
			t := time.Unix(it.Created, 0)
			v.PublishedAt = &t
		}
		if it.Play > 0 {
			n := int(it.Play)
			v.ViewCount = &n
		}
		out.Items = append(out.Items, *v)
	}
	if d.Page.PS > 0 && d.Page.Count > 0 {
		total := (d.Page.Count + d.Page.PS - 1) / d.Page.PS
		out.TotalPages = total
		out.HasMore = page < total
	} else {
		out.HasMore = len(out.Items) > 0
	}
	return out, nil
}

// thumbURL 把封面地址换成缩略图：B 站图床支持在地址后面加 @<宽>w_<高>h_1c.webp
// 让服务端现缩（实测 1920×1080 原图 335KB → 960w 67KB → 480w 24KB）。
func thumbURL(pic string) string {
	s := strings.TrimSpace(pic)
	if s == "" {
		return ""
	}
	s = strings.Replace(s, "http://", "https://", 1)
	if strings.Contains(s, "@") {
		return s
	}
	return s + "@960w_540h_1c.webp"
}

// parseLength 把 "24:52" / "1:02:05" 换成秒。
func parseLength(s string) int {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) == 0 {
		return 0
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

// ---- 影片详情 ----

type viewData struct {
	BVID     string `json:"bvid"`
	AID      int64  `json:"aid"`
	Title    string `json:"title"`
	Pic      string `json:"pic"`
	Desc     string `json:"desc"`
	PubDate  int64  `json:"pubdate"`
	Duration int    `json:"duration"`
	TName    string `json:"tname"`
	Owner    struct {
		Mid  int64  `json:"mid"`
		Name string `json:"name"`
		Face string `json:"face"`
	} `json:"owner"`
	Stat struct {
		View int64 `json:"view"`
	} `json:"stat"`
	Pages []struct {
		CID      int64  `json:"cid"`
		Page     int    `json:"page"`
		Part     string `json:"part"`
		Duration int    `json:"duration"`
	} `json:"pages"`
}

// firstCID 是 P1 的 cid：分 P 视频默认播第 1 P（更细的「某一 P」不在本期范围内）。
func (v *viewData) firstCID() int64 {
	if len(v.Pages) == 0 {
		return 0
	}
	return v.Pages[0].CID
}

// view 拉影片详情（含 cid）。
func (a *Adapter) view(ctx context.Context, bvid string) (*viewData, error) {
	var d viewData
	if err := a.call(ctx, "/x/web-interface/view", url.Values{"bvid": {bvid}}, a.lim,
		a.base+"/video/"+bvid, &d); err != nil {
		return nil, err
	}
	if d.BVID == "" {
		return nil, errNotFound
	}
	return &d, nil
}

// video 把详情接口的字段翻成 model.Video。
func (a *Adapter) video(d *viewData) *model.Video {
	v := &model.Video{
		Platform:        a.Key(),
		PlatformVideoID: d.BVID,
		Title:           d.Title,
		URL:             a.base + "/video/" + d.BVID,
		ThumbURL:        thumbURL(d.Pic),
		DurationSec:     d.Duration,
		Description:     strings.TrimSpace(d.Desc),
	}
	if d.PubDate > 0 {
		t := time.Unix(d.PubDate, 0)
		v.PublishedAt = &t
	}
	if d.Stat.View > 0 {
		n := int(d.Stat.View)
		v.ViewCount = &n
	}
	// 分区名当标签：B 站投稿只有一个分区，但卡片上有个标签位，聊胜于无。
	if t := strings.TrimSpace(d.TName); t != "" {
		v.Tags = []string{t}
	}
	if d.Owner.Name != "" {
		v.Performers = []model.PerformerRef{{
			Name: d.Owner.Name,
			URL:  a.base + "/space/" + strconv.FormatInt(d.Owner.Mid, 10),
		}}
	}
	return v
}

// GetVideo 只取元数据。
func (a *Adapter) GetVideo(ctx context.Context, videoID string) (*model.Video, error) {
	d, err := a.view(ctx, videoID)
	if err != nil {
		return nil, err
	}
	return a.video(d), nil
}

// SearchPerformers 找 UP 主。
//
// 搜索接口（/x/web-interface/wbi/search/type）同样是 WBI + 指纹保护的，
// 服务端调必然 -352；与其做一个永远 500 的搜索框，不如直接说清楚怎么用。
func (a *Adapter) SearchPerformers(ctx context.Context, query string, page int) ([]*model.Performer, bool, error) {
	return nil, false, errors.New("B 站不支持按名字搜索：请直接贴 UP 主主页链接（space.bilibili.com/<mid>）")
}
