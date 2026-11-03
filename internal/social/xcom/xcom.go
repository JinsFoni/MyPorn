// Package xcom 抓 X（Twitter）博主的帖子照片。
//
// **不需要登录**，两条互补的公开路子：
//
//  1. 主页 HTML（拿来「发现」新帖，2026-11-03 加的）：GET https://x.com/{handle}
//     匿名就能拿到 200 + 约 140 KB，里头有一段 RSC 流（`$R[21].next(...)`），
//     带着 5 条帖子的完整对象（正文 / media_url_https / original_info / 尺寸）；
//     我们只从里面抠帖子 id，详情走 (2)。
//  2. 单帖：GET https://cdn.syndication.twimg.com/tweet-result?id={id}&lang=en&token={token}
//     官方嵌推 widget 用的那个接口，返回干净的 JSON（created_at / text / mediaDetails）。
//     token 是 id 算出来的：((id / 1e15) * π) 转 36 进制、去掉小数点与 0。**实测服务端只校验
//     「有没有这个参数」不校验值**（`token=abc123` 照样 200 + 完整 JSON），不带才是 `{}`。
//  3. 时间线 widget（老路子，负责「历史」）：GET https://syndication.twitter.com/srv/
//     timeline-profile/screen-name/{handle} → HTML 里的 __NEXT_DATA__ 就是 entries。
//
// 实测结论（docs/M5-SOCIAL.md §15）：
//   - 时间线 widget 一页固定 20 条，**没有任何翻页游标**，加 ?cursor= / ?max_id= / ?page= 反而会让响应
//     变成错误壳；而且它**会停在某个时间点不再前进**：@mysaaat 的 widget 到 2026-11-03 仍然只给到
//     2025-07-04 那 32 条（换来换 UA / 语言 / no-cache 都是同一份字节），可主页 HTML 里明摆着有
//     2025-10 和 2026-07 的帖子 —— 所以「最新帖」只能靠主页那条路发现，见 §15.10；
//   - 账号不存在/受保护时是 **HTTP 200 + hasResults:false**，不是 404，判空必须看这个字段；
//   - 时间线端点限流是 **按 IP** 的 30 次 / 15 分钟，429 之后必须停到 x-rate-limit-reset，别重试
//     （tweet-result 在另一个域名上，额度另算，实测连打 6 次没问题）；
//   - 原图后缀 `?name=orig` 可用，且 pbs.twimg.com 不校验 Referer。
package xcom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

const (
	baseURL = "https://syndication.twitter.com" // 时间线 widget（历史）
	cdnURL  = "https://cdn.syndication.twimg.com"
	webURL  = "https://x.com"
	// userAgent 用桌面 Chrome：x.com 对 Googlebot 直接 403（实测），对浏览器 UA 才给主页 HTML。
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	// maxBody 是响应体上限：一页 20 条带图 JSON 实测约 150 KB，主页 HTML 140 KB，8 MB 足够宽松。
	maxBody = 8 << 20
	// defaultCooldown 是没有 reset 头时的保守冷却。
	defaultCooldown = 15 * time.Minute
	// maxProfilePosts 是每轮从主页 HTML 里最多取几条帖子的详情：主页天生只给 5 条 id，
	// 这里留点余量；每条详情一次 tweet-result 请求，别让配额被一个号吃光。
	maxProfilePosts = 10
)

// Adapter 实现 social.Adapter。
type Adapter struct {
	client   *http.Client
	throttle *social.Throttle
	base     string // 时间线 widget，可替换，便于测试
	cdn      string // 单帖 tweet-result
	web      string // 主页 HTML（发现新帖 id）；空 = 这条路关掉（单测里只量时间线那条路）
	now      func() time.Time
	// retryWaits 是「连不上」之后的重试间隔（空 = 不重试，只给测试用）。
	// 为什么要有它：2026-11-03 22:21 实测三个账号在 45 秒内先后报
	// `dial tcp 198.18.7.255:443: connect: connection refused`（这台机器走 Clash TUN，
	// 出口节点抖了一下），同一分钟 curl 同一个域名是 200。而这种失败会把账号标红到
	// 下一次成功同步（默认间隔 6 小时），界面上一直挂着一条「请求 X 失败」。
	// 连接层抖动重试两次就够（1s + 3s），HTTP 状态码（429 / 403）一律不重试：
	// 那些是上游在说话，重试只会把额度烧得更快。
	retryWaits []time.Duration
}

func New(client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	return &Adapter{
		client:     client,
		throttle:   social.NewThrottle(1500 * time.Millisecond),
		base:       baseURL,
		cdn:        cdnURL,
		web:        webURL,
		now:        time.Now,
		retryWaits: []time.Duration{time.Second, 3 * time.Second},
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
	u := a.base + "/srv/timeline-profile/screen-name/" + url.PathEscape(handle)
	// 冷却只压这个宿主：widget 被限流时主页发现（另一个宿主）还得能跑。
	if err := a.throttle.WaitHost(ctx, hostOf(u)); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, social.Upstream("构造请求失败", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := a.do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, social.Upstream(transportMsg(err), err)
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
		a.throttle.CoolUntilHost(hostOf(u), until)
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

// ---- 主页 HTML 发现 + 单帖详情（2026-11-03 加）----
//
// 为什么加这条路：时间线 widget 会「停在某个时间点」。@mysaaat 的 widget 到 2026-11-03 仍然
// 只给到 2025-07-04 那 32 条，可主页 HTML 里明摆着有 2025-10-08 / 2026-07-02 的帖子，
// 用户在页面上看到的就是「关注了但墙上没有最新照片」。主页 HTML 匿名可取，所以新帖从这儿发现。

// profileStatusRe 从 media 的 expanded_url（https://x.com/{handle}/status/{id}/photo/1）里取帖子 id。
var profileStatusRe = regexp.MustCompile(`x\.com/[A-Za-z0-9_]+/status/(\d{15,})`)

// profileEditRe 从 edit_control.edit_tweet_ids 里取帖子 id（RSC 流里是 `\"edit_tweet_ids\":[\"123\"]`）。
var profileEditRe = regexp.MustCompile(`edit_tweet_ids\\?"\s*:\s*\[\s*\\?"(\d{15,})`)

// do 发请求，连接层失败按 `retryWaits` 重试。
//
// 只重试 `client.Do` 报错的那种失败（连不上、被对端切断、握手超时）——它们发生在
// 「请求根本没到 X」的阶段，重试不消耗 X 的额度，也不会被当成刷接口。
// 一旦拿到了响应（哪怕 429 / 403），立刻原样返回，交给上层按状态码分类。
func (a *Adapter) do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, err := a.client.Do(req)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
		if attempt >= len(a.retryWaits) {
			return nil, lastErr
		}
		t := time.NewTimer(a.retryWaits[attempt])
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, lastErr
		case <-t.C:
		}
	}
}

// transportMsg 给连接层失败换个能看懂的说法。
//
// `dial tcp 198.18.7.255:443: connect: connection refused` 里的那个地址是本机代理的
// fake-IP（这台机器走 Clash TUN），用户看到「198.18.x.x 拒绝连接」只会以为是我们的 bug；
// 说成「连不上 X」＋保留底层细节，才能让人知道该去看代理。
func transportMsg(err error) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) {
		return "连不上 X（本机网络或代理不通）"
	}
	return "请求 X 失败"
}

// hostOf 取 URL 的 host，用作「按宿主冷却」的 key。解析不出来就原样返回。
func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// get 发一个 GET，返回响应体和状态码。限流/传输故障统一成 social.Error。
func (a *Adapter) get(ctx context.Context, u, accept string) ([]byte, int, error) {
	if err := a.throttle.WaitHost(ctx, hostOf(u)); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, social.Upstream("构造请求失败", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := a.do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, social.Upstream(transportMsg(err), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, 0, social.Upstream("读取 X 响应失败", err)
	}
	return body, resp.StatusCode, nil
}

// profileIDs 从主页 HTML 里抠帖子 id，新到旧，最多 maxProfilePosts 条。
//
// 实测（2026-11-03，@mysaaat）：x.com/{handle} 匿名 200 / 142 KB，里面有 5 条帖子
// （2025-10-08、2025-10-09、2025-10-20、2025-10-25、2026-07-02），而时间线 widget 一条都没给。
// Googlebot UA 直接 403，所以必须用桌面浏览器 UA。
func (a *Adapter) profileIDs(ctx context.Context, handle string) ([]string, error) {
	if a.web == "" {
		return nil, social.StaleParser("主页发现路径未启用")
	}
	body, status, err := a.get(ctx, a.web+"/"+url.PathEscape(handle), "text/html,application/xhtml+xml")
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		// 主页这条路的限流头没实测过，保守起见别在这儿下判断，交给上层换源。
		return nil, social.RateLimited("X 主页返回 429", a.now().Add(defaultCooldown))
	}
	if status != http.StatusOK {
		return nil, social.Upstream(fmt.Sprintf("X 主页返回 %d", status), nil)
	}
	seen := make(map[string]struct{}, 8)
	ids := make([]string, 0, 8)
	for _, re := range []*regexp.Regexp{profileStatusRe, profileEditRe} {
		for _, m := range re.FindAllSubmatch(body, -1) {
			id := string(m[1])
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		// 登录墙/风控页也会是 200，别当「这个号没帖」。
		return nil, social.StaleParser(fmt.Sprintf("@%s 的主页 HTML 里没有帖子 id（可能遇到登录墙或改版）", handle))
	}
	sort.Slice(ids, func(i, j int) bool { return idLess(ids[j], ids[i]) })
	if len(ids) > maxProfilePosts {
		ids = ids[:maxProfilePosts]
	}
	return ids, nil
}

// idLess 按雪花 id 的数值比大小（id 里编着时间戳，但全局唯一，只有同账号之间比才有意义）。
func idLess(a, b string) bool {
	na, ea := strconv.ParseInt(a, 10, 64)
	nb, eb := strconv.ParseInt(b, 10, 64)
	if ea != nil || eb != nil {
		return a < b
	}
	return na < nb
}

// tweetResult 是 cdn.syndication.twimg.com/tweet-result 的响应：字段比时间线那份少，
// 但 created_at 是 ISO（`2026-07-02T08:32:04.000Z`），图和尺寸在 mediaDetails 里。
type tweetResult struct {
	IDStr        string  `json:"id_str"`
	CreatedAt    string  `json:"created_at"`
	Text         string  `json:"text"`
	User         user    `json:"user"`
	MediaDetails []media `json:"mediaDetails"`
}

// toTweet 折回统一的 tweet 结构，后面的 toPost / collectPhotos 就不用管来源差异。
func (r *tweetResult) toTweet() *tweet {
	t := &tweet{IDStr: r.IDStr, CreatedAt: r.CreatedAt, FullText: r.Text, User: r.User}
	t.ExtendedEntities.Media = r.MediaDetails
	return t
}

// tweetByID 抓单帖详情。token 是官方 widget 那套算法，不带它只回 `{}`（实测 2 字节）。
func (a *Adapter) tweetByID(ctx context.Context, id string) (*tweet, error) {
	u := fmt.Sprintf("%s/tweet-result?id=%s&lang=en&token=%s",
		a.cdn, url.QueryEscape(id), url.QueryEscape(tweetToken(id)))
	body, status, err := a.get(ctx, u, "application/json")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, social.Upstream(fmt.Sprintf("X 单帖接口返回 %d", status), nil)
	}
	var r tweetResult
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, social.StaleParser("X 单帖 JSON 解析失败")
	}
	if r.IDStr == "" {
		return nil, social.NotFound(fmt.Sprintf("帖子 %s 读不到（可能已删除或需要登录）", id))
	}
	return r.toTweet(), nil
}

// tweetToken 复刻官方嵌推 widget 的 token：((id / 1e15) * π) 的 36 进制，去掉小数点和 0。
// 值不参与校验（实测 `token=abc123` 也返回完整 JSON，不带才是 `{}`），但保持和官方一致，
// 哪天上游真校验了也不用改。
func tweetToken(id string) string {
	n, err := strconv.ParseFloat(id, 64)
	if err != nil || n <= 0 {
		return ""
	}
	v := n / 1e15 * math.Pi
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var out strings.Builder
	out.WriteString(strconv.FormatInt(int64(v), 36))
	out.WriteByte('.')
	frac := v - math.Floor(v)
	for i := 0; i < 12 && frac > 0; i++ {
		frac *= 36
		d := int(frac)
		out.WriteByte(digits[d])
		frac -= float64(d)
	}
	// 官方实现就是「去掉小数点和所有 0」。
	return strings.NewReplacer(".", "", "0", "").Replace(out.String())
}

// ---- social.Adapter ----

// GetProfile 抓博主资料。X 的时间线里每条帖子都带完整的 user 对象，取第一条就够。
func (a *Adapter) GetProfile(ctx context.Context, handle string) (*social.Account, error) {
	nd, err := a.fetch(ctx, handle)
	if err != nil {
		// 时间线 widget 被限流时，主页那条路照样能拿到头像和名字——别让整轮同步白跑
		// （用户看到的只是「同步成功」但一直没动静，正是 §15.9 里那个假象）。
		if social.KindOf(err) == social.KindRateLimited {
			if acc, err2 := a.profileAccount(ctx, handle); err2 == nil {
				return acc, nil
			}
		}
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

// profileAccount 拿最新一条帖子的作者信息当资料（主页 HTML 本身不带头像字段）。
func (a *Adapter) profileAccount(ctx context.Context, handle string) (*social.Account, error) {
	ids, err := a.profileIDs(ctx, handle)
	if err != nil {
		return nil, err
	}
	t, err := a.tweetByID(ctx, ids[0])
	if err != nil {
		return nil, err
	}
	if t.User.Name == "" {
		return nil, social.StaleParser(fmt.Sprintf("@%s 的帖子作者信息是空的", handle))
	}
	return &social.Account{
		Platform:    a.Key(),
		Handle:      social.NormalizeHandle(handle),
		DisplayName: t.User.Name,
		AvatarURL:   avatarFrom(t.User.AvatarURL),
		ProfileURL:  "https://x.com/" + social.NormalizeHandle(handle),
	}, nil
}

// Discover 只跑「主页 HTML 发现 + 单帖详情」这一条便宜的路子，不碰时间线 widget。
//
// 存在的理由：主页 HTML 一页只有 5–8 个帖子 id，两轮之间发超过这个数就滑走了（§15.15），
// 而这条路每个账号只花 1 次主页请求 + 每条 1 次单帖请求，比完整同步便宜一半、也不占
// widget 那个 30 次/15 分钟的额度。所以它适合按更短的周期单独跑。
func (a *Adapter) Discover(ctx context.Context, handle string) (*social.Page, error) {
	page := &social.Page{Posts: []social.Post{}}
	profErr := a.discoverInto(ctx, page, handle)
	page.Posts = dedupePosts(page.Posts)
	if len(page.Posts) > 0 {
		return page, nil
	}
	if profErr != nil {
		return nil, profErr
	}
	return nil, social.StaleParser(fmt.Sprintf("@%s 的主页这一页没有解析出帖子", handle))
}

// discoverInto 把主页 HTML 里那几个 id 的详情追加进 page，返回第一个错误（不影响别的 id）。
func (a *Adapter) discoverInto(ctx context.Context, page *social.Page, handle string) error {
	if a.web == "" {
		return nil
	}
	ids, err := a.profileIDs(ctx, handle)
	if err != nil {
		return err
	}
	var firstErr error
	for _, id := range ids {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		t, err := a.tweetByID(ctx, id)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if p, ok := toPost(t); ok {
			page.Posts = append(page.Posts, p)
		}
	}
	return firstErr
}

// ListPosts 抓一页帖子。cursor 无意义（X 免登录路径没有翻页），传什么都忽略。
//
// 两个来源合并：主页 HTML 发现的最新帖（含时间线 widget 不返回的那些）+ 时间线 widget 的历史。
// 只要有一边拿到东西就算成功——另一边限流不该把已经抓到的帖子扔掉，6 小时后下一轮再补。
func (a *Adapter) ListPosts(ctx context.Context, handle, cursor string) (*social.Page, error) {
	page := &social.Page{Posts: []social.Post{}}

	profErr := a.discoverInto(ctx, page, handle)
	nd, tlErr := a.fetch(ctx, handle)
	if tlErr == nil {
		for _, e := range nd.Props.PageProps.Timeline.Entries {
			if e.Content.Tweet == nil {
				continue
			}
			if p, ok := toPost(e.Content.Tweet); ok {
				page.Posts = append(page.Posts, p)
			}
		}
	}

	page.Posts = dedupePosts(page.Posts)
	if len(page.Posts) > 0 {
		return page, nil
	}
	// 一条都没抓到：优先把限流抛上去（上层要据此整轮停手），其次才是解析/不存在。
	if tlErr != nil && social.KindOf(tlErr) == social.KindRateLimited {
		return nil, tlErr
	}
	if profErr != nil {
		return nil, profErr
	}
	if tlErr != nil {
		return nil, tlErr
	}
	// 两个来源都「成功但没有帖子」：不能当「同步成功、没有新帖」交差，那会把「账号不存在/设了保护」
	// 和「X 又改了页面结构」一起咽下去，界面只显示「同步成功」但墙上一年没动静——2026-11-03
	// 排查「动态里好像没有抓取最新的推特照片」时就差点被这条静默路径骗过去。
	if nd != nil && !nd.Props.PageProps.ContextProvider.HasResults {
		return nil, social.NotFound(fmt.Sprintf("X 上没有 @%s，或对方设了保护", handle))
	}
	return nil, social.StaleParser(fmt.Sprintf("@%s 的页面有结果但解析不出帖子（页面结构可能又变了）", handle))
}

// dedupePosts 按 post_id 去重（同一帖子两个来源都可能给到），再按发布时间倒序。
// 重复时留信息更全的那份：有正文、有图。
func dedupePosts(in []social.Post) []social.Post {
	idx := make(map[string]int, len(in))
	out := make([]social.Post, 0, len(in))
	for _, p := range in {
		if i, ok := idx[p.PostID]; ok {
			cur := out[i]
			if len(p.Photos) > len(cur.Photos) || (cur.Caption == "" && p.Caption != "") {
				out[i] = p
			}
			continue
		}
		idx[p.PostID] = len(out)
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].PostedAt.After(out[j].PostedAt) })
	return out
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
	s = strings.TrimSpace(s)
	if t, err := time.Parse(createdAtLayout, s); err == nil {
		return t
	}
	// 单帖接口给的是 ISO（`2026-07-02T08:32:04.000Z`），时间线给的是上面那种。
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

// 编译期检查。
var _ social.Adapter = (*Adapter)(nil)
