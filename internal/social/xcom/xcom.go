// Package xcom 抓 X（Twitter）博主的帖子照片。
//
// **不需要登录**：X 给第三方网站嵌时间线用的公开端点会直接吐一段 JSON 出来：
//
//	GET https://syndication.twitter.com/srv/timeline-profile/screen-name/{handle}
//	→ HTML 里的 <script id="__NEXT_DATA__"> 就是 props.pageProps.timeline.entries
//
// 实测结论（docs/M5-SOCIAL.md §15）：
//   - 一页固定 20 条，**没有任何翻页游标**，加 ?cursor= / ?max_id= / ?page= 反而会让响应变成错误壳
//     → 免登录路径 20 帖封顶，历史靠 6 小时一轮的增量慢慢累积；
//   - 账号不存在/受保护时是 **HTTP 200 + hasResults:false**，不是 404，判空必须看这个字段；
//   - 限流是 **按 IP** 的 30 次 / 15 分钟，429 之后必须停到 x-rate-limit-reset，别重试；
//   - 原图后缀 `?name=orig` 可用，且 pbs.twimg.com 不校验 Referer。
package xcom

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

const (
	baseURL   = "https://syndication.twitter.com"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	// maxBody 是响应体上限：一页 20 条带图 JSON 实测约 150 KB，8 MB 足够宽松。
	maxBody = 8 << 20
	// defaultCooldown 是没有 reset 头时的保守冷却。
	defaultCooldown = 15 * time.Minute
)

// Adapter 实现 social.Adapter。
type Adapter struct {
	client   *http.Client
	throttle *social.Throttle
	base     string // 可替换，便于测试
	now      func() time.Time
}

func New(client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	return &Adapter{
		client:   client,
		throttle: social.NewThrottle(1500 * time.Millisecond),
		base:     baseURL,
		now:      time.Now,
	}
}

func (a *Adapter) Key() string  { return "x" }
func (a *Adapter) Name() string { return "X" }

// NeedsCookie 为 false：这个站有免登录路径（代价是只能拿最近 20 帖）。
func (a *Adapter) NeedsCookie() bool { return false }

// Throttle 暴露给上层做「这个平台现在能不能打」的判断。
func (a *Adapter) Throttle() *social.Throttle { return a.throttle }

// ---- 主页链接 ----

var handleRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

// ParseProfileURL 接受主页链接、@handle 或裸 handle。
// 也容忍 /media、/with_replies 这类子页；状态链接（/xxx/status/123）不算主页。
func (a *Adapter) ParseProfileURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", social.NotFound("链接为空")
	}
	if strings.HasPrefix(raw, "@") {
		raw = raw[1:]
	}
	if handleRe.MatchString(raw) {
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
	switch host {
	case "x.com", "twitter.com", "mobile.twitter.com":
	default:
		return "", social.NotFound("这不是 X 的主页链接")
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return "", social.NotFound("链接里没有账号名")
	}
	if len(segs) > 1 && (segs[1] == "status" || segs[1] == "statuses") {
		return "", social.NotFound("这是单条帖子的链接，不是主页")
	}
	handle := social.NormalizeHandle(segs[0])
	if !handleRe.MatchString(handle) {
		return "", social.NotFound("账号名不像 X 的账号")
	}
	switch handle {
	case "i", "home", "explore", "search", "settings", "login", "logout", "intent", "share":
		return "", social.NotFound("这不是账号主页")
	}
	return handle, nil
}

// ---- 抓取 ----

type nextData struct {
	Props struct {
		PageProps struct {
			ContextProvider struct {
				HasResults bool `json:"hasResults"`
			} `json:"contextProvider"`
			Timeline struct {
				Entries []entry `json:"entries"`
			} `json:"timeline"`
		} `json:"pageProps"`
	} `json:"props"`
}

type entry struct {
	Type    string `json:"type"`
	Content struct {
		Tweet *tweet `json:"tweet"`
	} `json:"content"`
}

type tweet struct {
	IDStr            string `json:"id_str"`
	CreatedAt        string `json:"created_at"`
	FullText         string `json:"full_text"`
	Permalink        string `json:"permalink"`
	User             user   `json:"user"`
	ExtendedEntities struct {
		Media []media `json:"media"`
	} `json:"extended_entities"`
	Entities struct {
		Media []media `json:"media"`
	} `json:"entities"`
	RetweetedStatus *tweet `json:"retweeted_status"`
}

type user struct {
	IDStr       string `json:"id_str"`
	Name        string `json:"name"`
	ScreenName  string `json:"screen_name"`
	AvatarURL   string `json:"profile_image_url_https"`
	Description string `json:"description"`
}

type media struct {
	Type          string `json:"type"`
	MediaURLHTTPS string `json:"media_url_https"`
	OriginalInfo  struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"original_info"`
	ExpandedURL string `json:"expanded_url"`
}

var nextDataRe = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__"[^>]*>(.*?)</script>`)

// fetch 抓一页时间线并解析成 nextData。
func (a *Adapter) fetch(ctx context.Context, handle string) (*nextData, error) {
	if err := a.throttle.Wait(ctx); err != nil {
		return nil, err
	}
	u := a.base + "/srv/timeline-profile/screen-name/" + url.PathEscape(handle)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, social.Upstream("构造请求失败", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, social.Upstream("请求 X 失败", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, social.Upstream("读取 X 响应失败", err)
	}

	// 限流：把 reset 时间记下来，冷却到那一刻（实测是 15 分钟窗口）。
	//
	// remaining=0 但 reset 已经过去，是「缓存里那份 429 头」——窗口早翻篇了，
	// 这时候把请求判死没有任何好处，继续用响应体就行。真正要紧张的只有两种：
	// 429，以及 remaining=0 且 reset 还在未来。
	remaining := resp.Header.Get("x-rate-limit-remaining")
	var until time.Time
	if resp.StatusCode == http.StatusTooManyRequests {
		until = a.resetAt(resp.Header.Get("x-rate-limit-reset"))
	} else if remaining == "0" {
		if t := a.resetAt(resp.Header.Get("x-rate-limit-reset")); t.After(a.now()) {
			until = t
		}
	}
	if !until.IsZero() {
		a.throttle.CoolUntil(until)
		return nil, social.RateLimited(
			fmt.Sprintf("X 限流（%d 次/窗口已用尽），冷却到 %s",
				limitOf(resp.Header.Get("x-rate-limit-limit")), until.Format("15:04")), until)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, social.Upstream(fmt.Sprintf("X 返回 %d", resp.StatusCode), nil)
	}

	m := nextDataRe.FindSubmatch(body)
	if m == nil {
		// 200 但没有 JSON：多半是风控页或端点改了，别静默当「没有帖子」。
		return nil, social.StaleParser("X 的页面里没有 __NEXT_DATA__（可能被风控或端点已改）")
	}
	var nd nextData
	if err := json.Unmarshal(m[1], &nd); err != nil {
		return nil, social.StaleParser("X 的时间线 JSON 结构变了")
	}
	return &nd, nil
}

func limitOf(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 30
	}
	return n
}

// resetAt 把 x-rate-limit-reset（epoch 秒）换成时间点。
func (a *Adapter) resetAt(v string) time.Time {
	if sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && sec > 0 {
		// 多给 5 秒，避免卡在窗口边界上又吃一次 429。
		return time.Unix(sec, 0).Add(5 * time.Second)
	}
	return a.now().Add(defaultCooldown)
}

// ---- social.Adapter ----

// GetProfile 抓博主资料。X 的时间线里每条帖子都带完整的 user 对象，取第一条就够。
func (a *Adapter) GetProfile(ctx context.Context, handle string) (*social.Account, error) {
	nd, err := a.fetch(ctx, handle)
	if err != nil {
		return nil, err
	}
	for _, e := range nd.Props.PageProps.Timeline.Entries {
		if e.Content.Tweet == nil {
			continue
		}
		u := e.Content.Tweet.User
		return &social.Account{
			Platform:    a.Key(),
			Handle:      social.NormalizeHandle(handle),
			DisplayName: u.Name,
			AvatarURL:   avatarFrom(u.AvatarURL),
			ProfileURL:  "https://x.com/" + social.NormalizeHandle(handle),
		}, nil
	}
	if !nd.Props.PageProps.ContextProvider.HasResults {
		return nil, social.NotFound(fmt.Sprintf("X 上没有 @%s，或对方设了保护", handle))
	}
	// 有结果但一条都解析不出来：账号存在，但拿不到资料（只发过转推/被限流的空壳页面）。
	return nil, social.StaleParser(fmt.Sprintf("@%s 的页面没有可取的用户信息", handle))
}

// ListPosts 抓一页帖子。cursor 无意义（X 免登录路径没有翻页），传什么都忽略。
func (a *Adapter) ListPosts(ctx context.Context, handle, cursor string) (*social.Page, error) {
	nd, err := a.fetch(ctx, handle)
	if err != nil {
		return nil, err
	}
	page := &social.Page{Posts: []social.Post{}}
	for _, e := range nd.Props.PageProps.Timeline.Entries {
		if e.Content.Tweet == nil {
			continue
		}
		if p, ok := toPost(e.Content.Tweet); ok {
			page.Posts = append(page.Posts, p)
		}
	}
	return page, nil
}

// toPost 把一条 tweet 转成统一的帖子；转推取原帖（照片属于原帖）。
func toPost(t *tweet) (social.Post, bool) {
	src := t
	if t.RetweetedStatus != nil {
		src = t.RetweetedStatus
	}
	if src.IDStr == "" {
		return social.Post{}, false
	}
	photos := collectPhotos(src)
	post := social.Post{
		PostID:   src.IDStr,
		URL:      postURL(src),
		Caption:  cleanCaption(src.FullText),
		PostedAt: parseTime(src.CreatedAt),
		Photos:   photos,
	}
	return post, true
}

// tcoRe 是 X 的短链（正文里指回附件或链接卡片的那些）。
var tcoRe = regexp.MustCompile(`https?://t\.co/\w+`)

// cleanCaption 把正文清成「能直接给人看/直接落盘」的样子：
//
//  1. `full_text` 里的 `&amp;` / `&#39;` 之类的 HTML 实体要解开（X 的 JSON 里就是转义过的）；
//  2. 末尾那条 `https://t.co/…` 指向的就是附件本身，图片我们已经抓了，留着只是噪音
//     （所有 t.co 短链都去掉：它们的展开地址本来也不在正文里）。
func cleanCaption(s string) string {
	s = html.UnescapeString(s)
	s = tcoRe.ReplaceAllString(s, "")
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line) // 去掉短链后常留下行尾空格
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	for strings.Contains(out, "\n\n\n") { // 段落之间最多留一个空行
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return out
}

// collectPhotos 只收静态图；video / animated_gif 跳过（它们的 media_url_https 其实是封面，
// 以后要做「视频只显示封面」时不用改结构）。
func collectPhotos(t *tweet) []social.Photo {
	med := t.ExtendedEntities.Media
	if len(med) == 0 {
		med = t.Entities.Media
	}
	out := make([]social.Photo, 0, len(med))
	seq := 0
	for _, m := range med {
		if m.Type != "photo" || m.MediaURLHTTPS == "" {
			continue
		}
		seq++
		out = append(out, social.Photo{
			URL:    originalURL(m.MediaURLHTTPS),
			Width:  m.OriginalInfo.Width,
			Height: m.OriginalInfo.Height,
			Seq:    seq,
		})
	}
	return out
}

// originalURL 把 pbs.twimg.com 的图换成原图：`…/abc.jpg?name=orig`（实测 62 KB → 299 KB）。
func originalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Set("name", "orig")
	u.RawQuery = q.Encode()
	return u.String()
}

// postURL 把 permalink（`/NASA/status/123`，相对路径）补成完整地址。
func postURL(t *tweet) string {
	if strings.HasPrefix(t.Permalink, "http") {
		return t.Permalink
	}
	if t.Permalink != "" {
		return "https://x.com" + t.Permalink
	}
	handle := social.NormalizeHandle(t.User.ScreenName)
	if handle == "" || t.IDStr == "" {
		return ""
	}
	return "https://x.com/" + handle + "/status/" + t.IDStr
}

// avatarFrom 把 `…_normal.jpg` 换成 `…_400x400.jpg`（和 platform/xcom 借头像时同一套规则）。
var avatarSizeRe = regexp.MustCompile(`_(?:normal|mini|bigger|reasonably_small|\d+x\d+)(\.\w+)$`)

func avatarFrom(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	u.Path = avatarSizeRe.ReplaceAllString(u.Path, "_400x400$1")
	return u.String()
}

// createdAtLayout 是 X 的时间格式：`Fri Oct 23 23:37:56 +0000 2026`。
const createdAtLayout = "Mon Jan 02 15:04:05 -0700 2006"

func parseTime(s string) time.Time {
	t, err := time.Parse(createdAtLayout, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// 编译期检查。
var _ social.Adapter = (*Adapter)(nil)
