// Package igram 抓 Instagram 博主的帖子照片。
//
// **必须登录态**：实测（docs/M5-SOCIAL.md §15.7）免登录在这个站已经彻底没有路——
// web_profile_info 一律 401 require_login、usernameinfo 直接 429 spam、
// ?__a=1 返回空、主页 HTML 302 跳登录页。所以适配器要求用户先在设置里贴
// `sessionid` + `csrftoken` + `ds_user_id`（加密落库），拿不到就明确报 needs_auth，
// 绝不静默返回空列表。
//
// 两条请求：
//  1. 第一页（顺带把博主资料取回来）：
//     GET https://i.instagram.com/api/v1/users/web_profile_info/?username={handle}
//  2. 后续页：
//     GET https://i.instagram.com/api/v1/feed/user/{user_id}/?max_id={cursor}&count=12
//
// 已知限制：IG 的图片地址是**带签名的**，几小时后失效。抓过的图会进服务端磁盘缓存
// （之后一直可用），但从没被抓过的老图会在签名过期后取不到 —— 界面上表现为占位图。
package igram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

const (
	apiBase   = "https://i.instagram.com/api/v1"
	userAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
	// appID 是 Instagram 网页版自己的 app id（公开常量）。
	appID = "936619743392459"
	// pageSize 是 feed 接口一页的条数。
	pageSize = 12
	// maxBody 是响应体上限。
	maxBody = 8 << 20
	// cooldown 是撞到 429 后的冷却：IG 不给 reset 头，只能保守。
	cooldown = 15 * time.Minute
	// cookieKey 是设置表里存 Cookie 的键名。
	cookieKey = "social_cookie_instagram"
)

// Adapter 实现 social.Adapter。
type Adapter struct {
	client   *http.Client
	throttle *social.Throttle
	setting  social.SettingFunc
	base     string
	now      func() time.Time
}

func New(client *http.Client, setting social.SettingFunc) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	return &Adapter{
		client:   client,
		throttle: social.NewThrottle(3 * time.Second),
		setting:  setting,
		base:     apiBase,
		now:      time.Now,
	}
}

func (a *Adapter) Key() string  { return "instagram" }
func (a *Adapter) Name() string { return "Instagram" }

// NeedsCookie 为 true：这个站没有免登录路径。
func (a *Adapter) NeedsCookie() bool { return true }

func (a *Adapter) Throttle() *social.Throttle { return a.throttle }

// ---- Cookie ----

// Credentials 是用户在设置页填的登录态。
type Credentials struct {
	SessionID string `json:"sessionid"`
	CSRFToken string `json:"csrftoken"`
	UserID    string `json:"ds_user_id"`
	// Cookies 是「用户给了什么就带什么」的全量键值。
	//
	// 只带 sessionid/csrftoken/ds_user_id 是不够的：浏览器发出去的还有 mid / ig_did / datr / rur，
	// 少了这些，`web_profile_info` 有时会 401、`feed/user` 有时会直接回空列表。
	// 用户既然贴了一整份（Netscape 文件或整段 Cookie 头），就照原样带上。
	Cookies map[string]string `json:"cookies,omitempty"`
}

// ParseCredentials 解析设置里存的凭证；缺 sessionid 视为没配置。
//
// 三种写法都认（用户是直接从浏览器/工具里复制出来的，没道理逼他改成 JSON）：
//   - JSON：{"sessionid":"…","csrftoken":"…","ds_user_id":"…"}
//   - Cookie 头原文：sessionid=…; csrftoken=…; ds_user_id=…
//   - **Netscape cookie 文件**（浏览器扩展导出的 cookies.txt / curl 的 cookie jar）：
//     每行 7 个制表符分隔字段 `domain  flag  path  secure  expiry  name  value`，
//     HttpOnly 的那种行首会多一个 `#HttpOnly_` 前缀。
func ParseCredentials(raw string) (Credentials, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Credentials{}, false
	}
	var c Credentials
	switch {
	case strings.HasPrefix(raw, "{"):
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return Credentials{}, false
		}
	case looksNetscape(raw):
		c = parseNetscape(raw)
	default:
		c = parseCookieHeader(raw)
	}
	if strings.TrimSpace(c.SessionID) == "" {
		return Credentials{}, false
	}
	return c, true
}

// looksNetscape 判断是不是 Netscape cookie 文件：有制表符，或有那句标准文件头。
func looksNetscape(raw string) bool {
	if strings.Contains(raw, "Netscape HTTP Cookie File") {
		return true
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_") {
			continue
		}
		if strings.Contains(line, "\t") {
			return true
		}
	}
	return false
}

// parseNetscape 读 Netscape cookie 文件；只保留 instagram.com 的 Cookie
// （用户往往直接把整份浏览器 Cookie 导出贴进来，里面全是别的站的）。
func parseNetscape(raw string) Credentials {
	c := Credentials{Cookies: map[string]string{}}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// `#HttpOnly_.instagram.com` 是有效数据行；其它 # 开头的是注释/文件头。
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain := strings.ToLower(strings.TrimSpace(fields[0]))
		if !strings.Contains(domain, "instagram.com") {
			continue
		}
		name := strings.TrimSpace(fields[5])
		value := strings.TrimSpace(fields[6])
		if name == "" {
			continue
		}
		c.Cookies[name] = value
	}
	c.applyCookies()
	return c
}

// parseCookieHeader 读 `a=1; b=2` 这种整段 Cookie 头。
func parseCookieHeader(raw string) Credentials {
	c := Credentials{Cookies: map[string]string{}}
	for _, part := range strings.Split(raw, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		name := strings.TrimSpace(k)
		if name == "" {
			continue
		}
		c.Cookies[name] = strings.TrimSpace(v)
	}
	c.applyCookies()
	return c
}

// applyCookies 把全量 Cookie 里的三项关键值同步到结构体字段上。
func (c *Credentials) applyCookies() {
	if v, ok := c.Cookies["sessionid"]; ok {
		c.SessionID = v
	}
	if v, ok := c.Cookies["csrftoken"]; ok {
		c.CSRFToken = v
	}
	if v, ok := c.Cookies["ds_user_id"]; ok {
		c.UserID = v
	}
}

// NormalizeCookie 把用户粘贴的内容规范化成要落库的 JSON；缺 sessionid 报错。
// 设置页只让用户粘一次，之后只显示「已配置/未配置」，不回显 Cookie 本身。
func NormalizeCookie(raw string) (string, error) {
	c, ok := ParseCredentials(raw)
	if !ok {
		return "", social.NeedsAuth("没找到 sessionid：请把 instagram.com 的整段 Cookie（或导出的 cookies.txt）贴进来")
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// HasCookie 报告当前是否配了登录态（给界面提示用）。
func (a *Adapter) HasCookie() bool {
	if a.setting == nil {
		return false
	}
	_, ok := ParseCredentials(a.setting(cookieKey))
	return ok
}

func (a *Adapter) cookieHeader() (string, error) {
	if a.setting == nil {
		return "", social.NeedsAuth("Instagram 需要登录态：请在设置 → 动态里粘贴 sessionid")
	}
	c, ok := ParseCredentials(a.setting(cookieKey))
	if !ok {
		return "", social.NeedsAuth("Instagram 需要登录态：请在设置 → 动态里粘贴 sessionid")
	}
	// 用户给了全量就照发全量（顺序无关），否则退回那三项。
	if len(c.Cookies) > 0 {
		names := make([]string, 0, len(c.Cookies))
		for name := range c.Cookies {
			names = append(names, name)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, name+"="+c.Cookies[name])
		}
		return strings.Join(parts, "; "), nil
	}
	parts := []string{"sessionid=" + c.SessionID}
	if c.CSRFToken != "" {
		parts = append(parts, "csrftoken="+c.CSRFToken)
	}
	if c.UserID != "" {
		parts = append(parts, "ds_user_id="+c.UserID)
	}
	return strings.Join(parts, "; "), nil
}

// ---- 主页链接 ----

// ParseProfileURL 接受 instagram.com/{handle} 与 @handle；/p/ 之类的帖子链接不算。
func (a *Adapter) ParseProfileURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", social.NotFound("链接为空")
	}
	if strings.HasPrefix(raw, "@") {
		return social.NormalizeHandle(strings.TrimPrefix(raw, "@")), nil
	}
	if !strings.Contains(raw, "://") && !strings.Contains(raw, "/") {
		return social.NormalizeHandle(raw), nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", social.NotFound("不是合法的链接")
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	if host != "instagram.com" {
		return "", social.NotFound("这不是 Instagram 的主页链接")
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return "", social.NotFound("链接里没有账号名")
	}
	switch segs[0] {
	case "p", "reel", "reels", "stories", "explore", "accounts", "direct", "tv":
		return "", social.NotFound("这是内容链接，不是账号主页")
	}
	return social.NormalizeHandle(segs[0]), nil
}

// ---- 请求 ----

func (a *Adapter) get(ctx context.Context, path string, dst any) error {
	cookie, err := a.cookieHeader()
	if err != nil {
		return err
	}
	if err := a.throttle.Wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path, nil)
	if err != nil {
		return social.Upstream("构造请求失败", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("x-ig-app-id", appID)
	req.Header.Set("X-IG-WWW-Claim", "0")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", "https://www.instagram.com/")
	req.Header.Set("Cookie", cookie)

	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return social.Upstream("请求 Instagram 失败", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return social.Upstream("读取 Instagram 响应失败", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return social.NeedsAuth("Instagram 登录态失效：请在设置 → 动态里换一个 sessionid")
	case http.StatusTooManyRequests:
		// 同样是 429，IG 有两种完全不同的意思，必须分开：
		//   - JSON `{"message":"Please wait a few minutes..."}`：真的是频率限制，等一会儿就好；
		//   - HTML 拦截页（标题 Page Not Found，页面里写着 wait a few minutes）：
		//     这是风控把整个会话/出口 IP 挡在门外了。等 15 分钟不会好，
		//     而且把平台整体锁 15 分钟只会让用户以为「站坏了」——所以只提示，不设冷却。
		if isBlockPage(resp, body) {
			return social.Blocked("Instagram 拒绝了这个会话（返回的是风控拦截页，不是限流）：" +
				"多半是 Cookie 与当前出口 IP 对不上，或被判定为自动化。" +
				"去设置 → 网络与代理里给 Instagram 配一个代理（保存即生效），" +
				"或重新登录后重新导出 cookies.txt 再试；等下去不会好")
		}
		until := a.now().Add(retryAfter(resp, cooldown))
		a.throttle.CoolUntil(until)
		return social.RateLimited("Instagram 限流", until)
	case http.StatusNotFound:
		return social.NotFound("Instagram 上没有这个账号")
	default:
		return social.Upstream(fmt.Sprintf("Instagram 返回 %d", resp.StatusCode), nil)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return social.StaleParser("Instagram 的响应结构变了")
	}
	return nil
}

// isBlockPage 判断这个 429 是不是风控拦截页而不是「正常限流」。
//
// 判据用响应体本身：真正的限流是 JSON，拦截页是 HTML（还带着 IG 的错误页模板）。
// 不看 Content-Type 以外的猜测，避免把 JSON 限流误判成拦截。
func isBlockPage(resp *http.Response, body []byte) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 256)])))
	if strings.Contains(ct, "json") || strings.HasPrefix(head, "{") {
		return false
	}
	return strings.Contains(ct, "html") || strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html")
}

// retryAfter 取 Retry-After（秒）当冷却时长；没有就用默认值。
func retryAfter(resp *http.Response, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return fallback
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 || secs > 3600 {
		return fallback
	}
	return time.Duration(secs) * time.Second
}

// ---- 响应结构 ----

type node struct {
	ID          string `json:"id"`
	Shortcode   string `json:"shortcode"`
	Code        string `json:"code"` // feed 接口用 code，网页接口用 shortcode
	DisplayURL  string `json:"display_url"`
	IsVideo     bool   `json:"is_video"`
	MediaType   int    `json:"media_type"` // feed 接口：1 图 / 2 视频 / 8 carousel
	TakenAt     int64  `json:"taken_at_timestamp"`
	TakenAtUnix int64  `json:"taken_at"` // feed 接口字段名不同
	Dimensions  struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"dimensions"`
	ImageVersions struct {
		Candidates []candidate `json:"candidates"`
	} `json:"image_versions2"`
	Caption struct {
		Text string `json:"text"`
	} `json:"caption"`
	EdgeMediaToCaption struct {
		Edges []struct {
			Node struct {
				Text string `json:"text"`
			} `json:"node"`
		} `json:"edges"`
	} `json:"edge_media_to_caption"`
	EdgeSidecarToChildren *struct {
		Edges []struct {
			Node node `json:"node"`
		} `json:"edges"`
	} `json:"edge_sidecar_to_children"`
	CarouselMedia []node `json:"carousel_media"`
}

type profileResp struct {
	Data struct {
		User struct {
			ID                       string `json:"id"`
			Username                 string `json:"username"`
			FullName                 string `json:"full_name"`
			ProfilePicURL            string `json:"profile_pic_url"`
			ProfilePicURLHD          string `json:"profile_pic_url_hd"`
			EdgeOwnerToTimelineMedia struct {
				Count int `json:"count"`
				Edges []struct {
					Node node `json:"node"`
				} `json:"edges"`
				PageInfo struct {
					HasNextPage bool   `json:"has_next_page"`
					EndCursor   string `json:"end_cursor"`
				} `json:"page_info"`
			} `json:"edge_owner_to_timeline_media"`
		} `json:"user"`
	} `json:"data"`
	RequireLogin bool   `json:"require_login"`
	Message      string `json:"message"`
	Status       string `json:"status"`
}

type feedResp struct {
	Items        []node `json:"items"`
	NextMaxID    string `json:"next_max_id"`
	MoreAvail    bool   `json:"more_available"`
	Status       string `json:"status"`
	RequireLogin bool   `json:"require_login"`
	Message      string `json:"message"`
}

// ---- social.Adapter ----

// GetProfile 抓博主资料。
func (a *Adapter) GetProfile(ctx context.Context, handle string) (*social.Account, error) {
	var pr profileResp
	if err := a.get(ctx, "/users/web_profile_info/?username="+url.QueryEscape(handle), &pr); err != nil {
		return nil, err
	}
	if pr.RequireLogin {
		return nil, social.NeedsAuth("Instagram 要求登录态（Cookie 可能已失效）")
	}
	u := pr.Data.User
	if u.Username == "" {
		return nil, social.NotFound(fmt.Sprintf("Instagram 上没有 @%s", handle))
	}
	avatar := u.ProfilePicURLHD
	if avatar == "" {
		avatar = u.ProfilePicURL
	}
	return &social.Account{
		Platform:    a.Key(),
		Handle:      social.NormalizeHandle(u.Username),
		DisplayName: strings.TrimSpace(u.FullName),
		AvatarURL:   avatar,
		ProfileURL:  "https://www.instagram.com/" + social.NormalizeHandle(u.Username) + "/",
	}, nil
}

// ListPosts 抓一页帖子。cursor 形如 `{userID}|{nextMaxID}`，空表示第一页。
func (a *Adapter) ListPosts(ctx context.Context, handle, cursor string) (*social.Page, error) {
	if cursor == "" {
		// 第一页：顺带拿到 user id 与翻页游标。
		var pr profileResp
		if err := a.get(ctx, "/users/web_profile_info/?username="+url.QueryEscape(handle), &pr); err != nil {
			return nil, err
		}
		if pr.RequireLogin {
			return nil, social.NeedsAuth("Instagram 要求登录态（Cookie 可能已失效）")
		}
		u := pr.Data.User
		if u.Username == "" {
			return nil, social.NotFound(fmt.Sprintf("Instagram 上没有 @%s", handle))
		}
		media := u.EdgeOwnerToTimelineMedia
		page := &social.Page{Posts: make([]social.Post, 0, len(media.Edges))}
		for _, e := range media.Edges {
			if p, ok := toPost(e.Node); ok {
				page.Posts = append(page.Posts, p)
			}
		}
		if media.PageInfo.HasNextPage && media.PageInfo.EndCursor != "" && u.ID != "" {
			page.NextCursor = u.ID + "|" + media.PageInfo.EndCursor
		}
		return page, nil
	}

	userID, maxID, ok := strings.Cut(cursor, "|")
	if !ok || userID == "" {
		return nil, social.StaleParser("Instagram 的翻页游标坏了")
	}
	var fr feedResp
	path := fmt.Sprintf("/feed/user/%s/?max_id=%s&count=%d", url.PathEscape(userID), url.QueryEscape(maxID), pageSize)
	if err := a.get(ctx, path, &fr); err != nil {
		// 第二页起失败不该把已经存好的整页数据回滚：交给上层按「本页失败」处理。
		return nil, err
	}
	if fr.RequireLogin {
		return nil, social.NeedsAuth("Instagram 要求登录态（Cookie 可能已失效）")
	}
	page := &social.Page{Posts: make([]social.Post, 0, len(fr.Items))}
	for _, it := range fr.Items {
		if p, ok := toPost(it); ok {
			page.Posts = append(page.Posts, p)
		}
	}
	if fr.MoreAvail && fr.NextMaxID != "" {
		page.NextCursor = userID + "|" + fr.NextMaxID
	}
	return page, nil
}

// toPost 把一条帖子转成统一结构；carousel 的每个 child 都收。
func toPost(n node) (social.Post, bool) {
	shortcode := n.Shortcode
	if shortcode == "" {
		shortcode = n.Code
	}
	if shortcode == "" {
		return social.Post{}, false
	}
	post := social.Post{
		PostID:   shortcode,
		URL:      "https://www.instagram.com/p/" + shortcode + "/",
		Caption:  caption(n),
		PostedAt: postedAt(n),
	}
	// carousel：网页接口给 edge_sidecar_to_children，feed 接口给 carousel_media。
	children := []node{}
	if n.EdgeSidecarToChildren != nil {
		for _, e := range n.EdgeSidecarToChildren.Edges {
			children = append(children, e.Node)
		}
	}
	if len(children) == 0 {
		children = n.CarouselMedia
	}

	seq := 0
	add := func(child node) {
		if child.IsVideo || child.MediaType == 2 {
			return // 视频不收（照片墙只放静态图）
		}
		u, w, h := imageOf(child)
		if u == "" {
			return
		}
		seq++
		post.Photos = append(post.Photos, social.Photo{URL: u, Width: w, Height: h, Seq: seq})
	}
	if len(children) > 0 {
		for _, c := range children {
			add(c)
		}
	} else {
		add(n)
	}
	return post, true
}

// imageOf 取图片地址与尺寸（网页接口用 display_url/dimensions，feed 接口用 image_versions2）。
func imageOf(n node) (string, int, int) {
	if n.DisplayURL != "" {
		w, h := n.Dimensions.Width, n.Dimensions.Height
		if w == 0 || h == 0 {
			if c, ok := bestCandidates(n); ok {
				w, h = c.Width, c.Height
			}
		}
		return n.DisplayURL, w, h
	}
	if c, ok := bestCandidates(n); ok {
		return c.URL, c.Width, c.Height
	}
	return "", 0, 0
}

// candidate 是 feed 接口里的一档图片尺寸。
type candidate struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// bestCandidates 取一档可用尺寸：一般按从大到小给，取第一个带 url 且宽 > 0 的。
func bestCandidates(n node) (candidate, bool) {
	cands := n.ImageVersions.Candidates
	for _, c := range cands {
		if c.URL != "" && c.Width > 0 {
			return c, true
		}
	}
	for _, c := range cands {
		if c.URL != "" {
			return c, true
		}
	}
	return candidate{}, false
}

func caption(n node) string {
	if n.Caption.Text != "" {
		return strings.TrimSpace(n.Caption.Text)
	}
	if len(n.EdgeMediaToCaption.Edges) > 0 {
		return strings.TrimSpace(n.EdgeMediaToCaption.Edges[0].Node.Text)
	}
	return ""
}

func postedAt(n node) time.Time {
	sec := n.TakenAt
	if sec == 0 {
		sec = n.TakenAtUnix
	}
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

// 编译期检查。
var _ social.Adapter = (*Adapter)(nil)
