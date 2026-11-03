// Instagram 网页版自己用的 GraphQL 端点（POST /api/graphql + doc_id 持久化查询）。
//
// 为什么改用这条：2026-11-04 实测旧的 `web_profile_info` 已经全线 429
// （带 Cookie、不带 Cookie、`i.` 与 `www.` 两个主机都一样，返回的是「找不到页面」的 HTML），
// 而网页版真正在用的 GraphQL 端点在同一天、同一个出口 IP、同一份 Cookie 下 6/6 全通：
// 一次 `PolarisProfilePostsQuery` 就能拿 33 条帖子（服务端上限，传 count=50 也只回 33），
// 155 条全量 5 次请求 9.2 s（含 4×1.2 s 人为间隔），一起把资料（`node.user.full_name` 等）带回来。
//
// 三段事实值得写死在注释里，都是踩过才知道的：
//
//  1. **必须带 `Referer` + `Sec-Fetch-*`**。少了它们，`POST /api/graphql` 不回 JSON，
//     而是 200 + 513 KB 的网页壳（`<!DOCTYPE html>`，里面一行数据都没有）。
//     实测：只带 content-type/x-ig-app-id/x-fb-lsd/x-csrftoken/x-fb-friendly-name → 网页壳；
//     补上 `Referer: https://www.instagram.com/{handle}/` 与 `Sec-Fetch-Site/Mode/Dest` → 正常 JSON。
//     所以「拿到 HTML」这个降级信号必须显式识别，不能当解析失败就完事。
//  2. **`lsd` 与 `fb_dtsg` 只能从页面 HTML 里现拿**（`["LSD",[],{"token":"…"}]` 与
//     `MRequestConfig.dtsg.token`），随会话与页面加载变化；`jazoest` 是 `"2" + fb_dtsg 的字符码求和`
//     （本机算出来 26448，与 IG 页面里自己写的 qm 参数逐位一致）。
//  3. **`variables` 里那 5 个 `__relay_internal__pv__…relayprovider` 布尔值是必填**：
//     少一个就回 `missing_required_variable_value`（code 1675012），而且文案长得像服务端抽风。
package igram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

const (
	// wwwBase 是网页版主机：GraphQL 端点与 HTML 都在这边（`i.instagram.com` 上同名路径是 404）。
	wwwBase = "https://www.instagram.com"
	// gqlDocID 是 `PolarisProfilePostsQuery` 的持久化查询 id。
	//
	// 这个 id 随 IG 发版会变（浏览器加载主页时在请求体里能直接读到）。变了的症状是
	// 回 `missing_required_variable_value` 或「查询不存在」，此时降级回 web_profile_info。
	gqlDocID = "29600383452901789"
	// gqlFriendlyName 要跟 doc_id 对得上，IG 会拿它做路由。
	gqlFriendlyName = "PolarisProfilePostsQuery"
	// gqlPageSize 是服务端一页的上限：实测传 50 也只回 33 个 edge，所以直接要 33。
	gqlPageSize = 33
	// gqlCursorPrefix 标记「这个断点是 GraphQL 游标」。
	//
	// 老断点（`{userID}|{nextMaxID}`）是 web_profile_info/feed 那条路留下的，
	// 库里已有的行必须还能接着跑，所以两种游标共存、按前缀分流。
	gqlCursorPrefix = "gql:"
	// tokenTTL 是 lsd/fb_dtsg 的缓存时长。它们是「页面加载级」的令牌，一次同步（≤7 个请求）
	// 复用同一对没问题，但隔一轮再用就可能已经被轮换掉，所以只在一次同步的窗口内缓存。
	tokenTTL = 10 * time.Minute
)

// gqlPV 是 `PolarisProfilePostsQuery` 要求的 relay provider 开关。
//
// 值照抄浏览器实际发出去的那一份（白名单，不是猜的）；缺任何一个都会 1675012。
var gqlPV = map[string]any{
	"__relay_internal__pv__PolarisMultiCaptionCarouselEnabledrelayprovider":  true,
	"__relay_internal__pv__PolarisShortDramaEnabledrelayprovider":            true,
	"__relay_internal__pv__PolarisSEOIsCrawlerrelayprovider":                 false,
	"__relay_internal__pv__PolarisThreadsStyleCommentsEnabledrelayprovider":  false,
	"__relay_internal__pv__PolarisReelsRecoDebugOverlayEnabledrelayprovider": false,
}

// gqlVars 拼出 variables。`data` 里那 4 个布尔值不是可选的：
// 只传 `{"count":N}` 时首页能出来，但带 `after` 翻页就回
// `A server error field_exception occured.`（实测 2026-11-04）——
// 浏览器自己发的是这 5 个键，照抄即可（探针连翻 5 页 155 条全通）。
func gqlVars(handle string, count int) map[string]any {
	vars := map[string]any{
		"data": map[string]any{
			"count":                             count,
			"include_reel_media_seen_timestamp": true,
			"include_relationship_info":         true,
			"latest_besties_reel_media":         true,
			"latest_reel_media":                 true,
		},
		"username": social.NormalizeHandle(handle),
	}
	for k, v := range gqlPV {
		vars[k] = v
	}
	return vars
}

// ---- 响应结构（只声明用得上的字段；一条帖子实测有 103 个字段） ----

type gqlResp struct {
	Data struct {
		Conn *struct {
			Edges []struct {
				Node gqlMedia `json:"node"`
			} `json:"edges"`
			PageInfo struct {
				HasNextPage bool   `json:"has_next_page"`
				EndCursor   string `json:"end_cursor"`
			} `json:"page_info"`
		} `json:"xdt_api__v1__feed__user_timeline_graphql_connection"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
	// 风控/参数错的另一种形状：`{"__ar":1,"error":1357054,"errorSummary":"…"}`。
	Error        int    `json:"error"`
	ErrorSummary string `json:"errorSummary"`
}

type gqlError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
	Summary string `json:"summary"`
}

// gqlMedia 是一条帖子（carousel 的子项复用同一个结构）。
type gqlMedia struct {
	Code    string `json:"code"`
	TakenAt int64  `json:"taken_at"`
	Caption *struct {
		Text string `json:"text"`
	} `json:"caption"`
	// VideoVersions 非空 = 视频/Reel。照片墙只收静态图，按原有约定跳过。
	VideoVersions []json.RawMessage `json:"video_versions"`
	ImageVersions struct {
		Candidates []candidate `json:"candidates"`
	} `json:"image_versions2"`
	CarouselMedia []gqlMedia `json:"carousel_media"`
	User          gqlUser    `json:"user"`
}

type gqlUser struct {
	Username      string `json:"username"`
	FullName      string `json:"full_name"`
	ProfilePicURL string `json:"profile_pic_url"`
	// HDProfilePicURLInfo 是原尺寸头像；没有时退回 150×150 的 profile_pic_url。
	HDProfilePicURLInfo struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"hd_profile_pic_url_info"`
}

// gqlPost 是一页帖子 + 下一段游标。
type gqlPost struct {
	Posts      []social.Post
	NextCursor string
}

// ---- 令牌（lsd / fb_dtsg） ----

// tokens 是本次同步窗口内复用的页面令牌。
type tokenCache struct {
	lsd  string
	dtsg string
	at   time.Time
}

// ensureTokens 拿到可用的 lsd/fb_dtsg；缓存过期或 force 时重新抓一次主页 HTML。
func (a *Adapter) ensureTokens(ctx context.Context, handle string, force bool) error {
	a.mu.Lock()
	fresh := a.tok.lsd != "" && a.tok.dtsg != "" && a.now().Sub(a.tok.at) < tokenTTL
	if !force && fresh {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	// 抓取不持锁：这一跳是网络请求，3 s 起步的间隔没必要连缓存锁一起占着。
	lsd, dtsg, err := a.fetchTokens(ctx, handle)
	if err != nil {
		a.mu.Lock()
		a.tok = tokenCache{}
		a.mu.Unlock()
		return err
	}
	a.mu.Lock()
	a.tok = tokenCache{lsd: lsd, dtsg: dtsg, at: a.now()}
	a.mu.Unlock()
	return nil
}

// fetchTokens 抓一次主页 HTML，抠出 LSD 与 fb_dtsg。
//
// 注意：这里不看 HTTP 状态码是否 200 就下结论——IG 的网页壳无论 200 还是 429 都长一样，
// 判定标准是「HTML 里有没有这两个令牌」。抠不到就是这条路今天不通，交给上层降级。
func (a *Adapter) fetchTokens(ctx context.Context, handle string) (string, string, error) {
	cookie, err := a.cookieHeader()
	if err != nil {
		return "", "", err
	}
	if err := a.throttle.Wait(ctx); err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.www+"/"+url.PathEscape(handle)+"/", nil)
	if err != nil {
		return "", "", social.Upstream("构造请求失败", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cookie", cookie)

	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", social.Upstream("请求 Instagram 主页失败", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", "", social.Upstream("读取 Instagram 主页失败", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// 主页被限流/拦截：这就是「今天这条 IP 不让抓」，不是解析问题。
		if isBlockPage(resp, body) {
			return "", "", social.Blocked("Instagram 拒绝了这个会话（主页返回风控拦截页）：" +
				"多半是 Cookie 与出口 IP 对不上。去设置 → 网络与代理里给 Instagram 配一个代理，或重新导出 cookies.txt")
		}
		until := a.now().Add(retryAfter(resp, cooldown))
		a.throttle.CoolUntil(until)
		return "", "", social.RateLimited("Instagram 限流", until)
	}
	// 主页这一跳就 401/403，说明登录态本身失效了，不是「抠不到令牌」——
	// 直接报 needs_auth，别再去试降级路径（那条路用的是同一个 Cookie）。
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", "", social.NeedsAuth("Instagram 登录态失效：请在设置 → 动态里换一个 sessionid")
	}
	html := string(body)
	lsd := matchToken(html, lsdPattern)
	dtsg := matchToken(html, dtsgPattern)
	if lsd == "" || dtsg == "" {
		return "", "", social.StaleParser("Instagram 主页里没找到 lsd/fb_dtsg 令牌（网页版改版或登录墙），无法走 GraphQL")
	}
	return lsd, dtsg, nil
}

// matchToken 用「锚点 + 截取」的方式取令牌，避免为一对字段引入正则依赖。
func matchToken(html, pattern string) string {
	i := strings.Index(html, pattern)
	if i < 0 {
		return ""
	}
	rest := html[i+len(pattern):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

const (
	// 页面里写作 `["LSD",[],{"token":"XXXX"},323]`。
	lsdPattern = `["LSD",[],{"token":"`
	// MRequestConfig 里写作 `{"dtsg":{"token":"XXXX","valid_for":86400,…}}`。
	dtsgPattern = `"dtsg":{"token":"`
)

// ---- GraphQL 请求 ----

// graphql 打一次 POST /api/graphql；variables 由调用方给。
//
// 回的是网页壳时（= 令牌已经和会话对不上，`lsd`/`fb_dtsg` 每次页面加载都会换）
// 换一对新令牌重试一次；两次都是网页壳才报 blocked —— 那种情况下再连打只会加深风控。
func (a *Adapter) graphql(ctx context.Context, handle string, variables map[string]any) (*gqlResp, error) {
	resp, err := a.graphqlOnce(ctx, handle, variables)
	if err == nil {
		return resp, nil
	}
	var deg *degradedError
	if !errors.As(err, &deg) {
		return nil, err
	}
	if err := a.ensureTokens(ctx, handle, true); err != nil {
		return nil, err
	}
	resp, err = a.graphqlOnce(ctx, handle, variables)
	if err == nil {
		return resp, nil
	}
	if errors.As(err, &deg) {
		return nil, social.Blocked("Instagram 把 GraphQL 请求降级成了网页壳（" + deg.detail +
			"）：令牌被轮换或这次请求被判成自动化。隔一会儿再试，或换出口 IP")
	}
	return nil, err
}

// degradedError 标记「拿到的是网页壳而不是 JSON」：换令牌重试一次，再不行才报错。
type degradedError struct{ detail string }

func (e *degradedError) Error() string {
	return "Instagram 把 GraphQL 请求降级成了网页壳：" + e.detail
}

func (a *Adapter) graphqlOnce(ctx context.Context, handle string, variables map[string]any) (*gqlResp, error) {
	if err := a.ensureTokens(ctx, handle, false); err != nil {
		return nil, err
	}
	a.mu.Lock()
	lsd, dtsg := a.tok.lsd, a.tok.dtsg
	a.mu.Unlock()

	cookie, err := a.cookieHeader()
	if err != nil {
		return nil, err
	}
	if err := a.throttle.Wait(ctx); err != nil {
		return nil, err
	}

	vars, err := json.Marshal(variables)
	if err != nil {
		return nil, social.Upstream("序列化查询参数失败", err)
	}
	form := url.Values{}
	form.Set("doc_id", gqlDocID)
	form.Set("variables", string(vars))
	form.Set("server_timestamps", "true")
	form.Set("fb_api_caller_class", "RelayModern")
	form.Set("fb_api_req_friendly_name", gqlFriendlyName)
	form.Set("lsd", lsd)
	form.Set("fb_dtsg", dtsg)
	form.Set("jazoest", jazoest(dtsg))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.www+"/api/graphql", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, social.Upstream("构造请求失败", err)
	}
	referer := a.www + "/" + url.PathEscape(handle) + "/"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("x-ig-app-id", appID)
	req.Header.Set("x-fb-lsd", lsd)
	req.Header.Set("x-fb-friendly-name", gqlFriendlyName)
	req.Header.Set("x-csrftoken", a.cookieValue(cookie, "csrftoken"))
	req.Header.Set("Origin", a.www)
	req.Header.Set("Referer", referer)
	// Referer 与这三行 Sec-Fetch-* 都是硬要求：缺了它们 IG 回 200 + 网页壳（见文件头 §1）。
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Cookie", cookie)

	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, social.Upstream("请求 Instagram GraphQL 失败", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, social.Upstream("读取 Instagram GraphQL 响应失败", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, social.NeedsAuth("Instagram 登录态失效：请在设置 → 动态里换一个 sessionid")
	case http.StatusTooManyRequests:
		if isBlockPage(resp, body) {
			return nil, social.Blocked("Instagram 拒绝了这个会话（返回的是风控拦截页，不是限流）：" +
				"多半是 Cookie 与当前出口 IP 对不上，或被判定为自动化。" +
				"去设置 → 网络与代理里给 Instagram 配一个代理，或重新登录后重新导出 cookies.txt")
		}
		until := a.now().Add(retryAfter(resp, cooldown))
		a.throttle.CoolUntil(until)
		return nil, social.RateLimited("Instagram 限流", until)
	case http.StatusNotFound:
		return nil, social.NotFound("Instagram 上没有这个账号")
	default:
		return nil, social.Upstream(fmt.Sprintf("Instagram 返回 %d", resp.StatusCode), nil)
	}

	// 正常响应可能带一段 AJAX 前缀 `for (;;);`。
	raw := strings.TrimPrefix(strings.TrimSpace(string(body)), "for (;;);")
	if looksHTML(resp, raw) {
		return nil, &degradedError{detail: fmt.Sprintf("%d 字节 HTML（不是 JSON）", len(raw))}
	}

	var out gqlResp
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, social.StaleParser("Instagram 的响应结构变了（GraphQL 回的不是 JSON）")
	}
	// IG 的 GraphQL 会「部分成功」：`data` 里整页数据齐全，`errors` 里却挂着一堆
	// `field_exception`——实测路径全是 `edges[i].node.location.profile_pic_url`
	// （被 geotag 的节点上，那个地点头像 IG 自己不返回），跟我们要的字段毫无关系。
	// 所以判定标准是「data 里有没有连接」，而不是「errors 是否为空」；
	// 它自己的前端也是这么做的（坏字段置空，整页照常渲染）。
	if out.Data.Conn == nil {
		if err := gqlErrorOf(&out, raw); err != nil {
			// 限流要顺手把冷却推到 throttle 上，否则下一轮同步立刻又打上去。
			if until, ok := social.ResetAtOf(err); ok {
				a.throttle.CoolUntil(until)
			}
			return nil, err
		}
	}
	return &out, nil
}

// gqlErrorOf 把 IG 的错误装进本仓库的错误分类里。
//
// 三种要分开：限流（等一会儿）、参数/协议变了（更新适配器）、风控（换 IP/换 Cookie）。
func gqlErrorOf(r *gqlResp, raw string) error {
	if r.Error != 0 {
		// 1357054 = "Your Request Couldn't Be Processed"，实测是「这个客户端的指纹不像浏览器」
		// （同一个 body 用 curl 打 3 次全是它，用 Go 的 net/http 打就通）。
		switch r.Error {
		case 1357054:
			return social.Blocked("Instagram 拒绝了这个请求（error 1357054）：客户端指纹被判定为自动化。" +
				"换出口 IP/代理再试，别连着重试")
		default:
			return social.Upstream(fmt.Sprintf("Instagram GraphQL 报错 %d：%s", r.Error, r.ErrorSummary), nil)
		}
	}
	if len(r.Errors) == 0 {
		return nil
	}
	e := r.Errors[0]
	switch {
	case e.Code == 1675004 || strings.Contains(strings.ToLower(e.Message), "rate limit"):
		// 实测文案就是 `{"message":"Rate limit exceeded","code":1675004}`：
		// 同一个请求带上登录 Cookie 就正常，所以它其实是「匿名额度用完了」。
		return social.RateLimited("Instagram 限流（未登录额度用完了？）", time.Now().Add(cooldown))
	case e.Code == 1675012 || strings.Contains(strings.ToLower(e.Message), "missing_required_variable_value"):
		return social.StaleParser("Instagram 的查询参数表变了（missing_required_variable_value）：" +
			"doc_id 或那 5 个 relay provider 开关要跟着网页版更新")
	default:
		msg := e.Message
		if e.Summary != "" {
			msg = e.Summary
		}
		// 带上原文片段：IG 的这类错（如 field_exception）光看 message 定位不了，
		// 得看第二个 error 里才有的 api_error_code / description。
		return social.Upstream(fmt.Sprintf("Instagram GraphQL 报错（code %d）：%s；原文 %s", e.Code, msg, clipJSON(raw, 320)), nil)
	}
}

// clipJSON 截一段响应原文进错误信息，免得排障时只剩一句「服务端错误」。
func clipJSON(body string, n int) string {
	s := strings.TrimSpace(body)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// jazoest 是 IG 的 CSRF sprinkle：`"2"` + fb_dtsg 的字符码之和。
// 实测与页面自己写的 qm 参数逐位一致（26448）。
func jazoest(dtsg string) string {
	sum := 0
	for _, r := range dtsg {
		sum += int(r)
	}
	return "2" + strconv.Itoa(sum)
}

// cookieValue 从 Cookie 头里取一个值（GraphQL 要 csrftoken 单独放头里）。
func (a *Adapter) cookieValue(header, name string) string {
	for _, part := range strings.Split(header, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.TrimSpace(k) == name {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// looksHTML 判断响应体是网页壳（降级）还是 JSON。
func looksHTML(resp *http.Response, body string) bool {
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); strings.Contains(ct, "text/html") {
		return true
	}
	head := strings.ToLower(strings.TrimSpace(body))
	if len(head) > 64 {
		head = head[:64]
	}
	return strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html")
}

// ---- 帖子列表 ----

// gqlPosts 抓一页；after 为空表示第一页。
func (a *Adapter) gqlPosts(ctx context.Context, handle, after string) (*gqlPost, error) {
	vars := gqlVars(handle, gqlPageSize)
	if after != "" {
		vars["after"] = after
	}
	resp, err := a.graphql(ctx, handle, vars)
	if err != nil {
		return nil, err
	}
	conn := resp.Data.Conn
	if conn == nil {
		return nil, social.StaleParser("Instagram 的 GraphQL 响应里没有帖子连接（doc_id 可能已更新）")
	}
	out := &gqlPost{}
	for _, e := range conn.Edges {
		if p, ok := postOfGQL(e.Node); ok {
			out.Posts = append(out.Posts, p)
		}
	}
	if conn.PageInfo.HasNextPage && conn.PageInfo.EndCursor != "" {
		out.NextCursor = gqlCursorPrefix + conn.PageInfo.EndCursor
	}
	return out, nil
}

// gqlProfile 用一页 count=1 的帖子把博主资料带回来。
//
// 实测 `PolarisProfilePageContentQuery` 只认数字 id（传 username 直接 1675012），
// 而 handle→id 又没有免费的入口（主页 HTML 里 0 处 pk），所以不如直接用帖子查询：
// `edges[].node.user` 里就有 `pk`/`full_name`/`profile_pic_url`/`hd_profile_pic_url_info`，
// 一次请求搞定资料，不用先解析 id 再查一次。
func (a *Adapter) gqlProfile(ctx context.Context, handle string) (*social.Account, error) {
	resp, err := a.graphql(ctx, handle, gqlVars(handle, 1))
	if err != nil {
		return nil, err
	}
	conn := resp.Data.Conn
	if conn == nil {
		return nil, social.StaleParser("Instagram 的 GraphQL 响应里没有帖子连接（doc_id 可能已更新）")
	}
	for _, e := range conn.Edges {
		if e.Node.User.Username != "" {
			return accountOfGQL(e.Node.User), nil
		}
	}
	// 连接在、但一条 edge 都没有：账号存在却没内容（新号/清空），资料拿不到但不该当错误。
	return nil, social.NotFound(fmt.Sprintf("Instagram 上 @%s 没有公开内容", social.NormalizeHandle(handle)))
}

// accountOfGQL 把 IG 的 user 对象转成统一账号结构。
func accountOfGQL(u gqlUser) *social.Account {
	avatar := u.HDProfilePicURLInfo.URL
	if avatar == "" {
		avatar = u.ProfilePicURL
	}
	handle := social.NormalizeHandle(u.Username)
	return &social.Account{
		Platform:    "instagram",
		Handle:      handle,
		DisplayName: strings.TrimSpace(u.FullName),
		AvatarURL:   avatar,
		ProfileURL:  wwwBase + "/" + handle + "/",
	}
}

// postOfGQL 把一条 GraphQL 帖子转成统一结构；carousel 的每个 child 都收，视频跳过。
func postOfGQL(n gqlMedia) (social.Post, bool) {
	if n.Code == "" {
		return social.Post{}, false
	}
	post := social.Post{
		PostID:  n.Code,
		URL:     wwwBase + "/p/" + n.Code + "/",
		Caption: captionOfGQL(n),
	}
	if n.TakenAt > 0 {
		post.PostedAt = time.Unix(n.TakenAt, 0).UTC()
	}
	children := n.CarouselMedia
	if len(children) == 0 {
		children = []gqlMedia{n}
	}
	seq := 0
	for _, c := range children {
		if len(c.VideoVersions) > 0 {
			continue // 视频/Reel：照片墙只收静态图
		}
		// candidates 由大到小给，取第一档带 url 的：实测单图帖第一档就是 3072×4096 原图。
		best, ok := pickBest(c.ImageVersions.Candidates)
		if !ok {
			continue
		}
		seq++
		post.Photos = append(post.Photos, social.Photo{URL: best.URL, Width: best.Width, Height: best.Height, Seq: seq})
	}
	return post, true
}

func captionOfGQL(n gqlMedia) string {
	if n.Caption != nil {
		return strings.TrimSpace(n.Caption.Text)
	}
	return ""
}

// pickBest 取一档可用尺寸（两条路径共用）。
func pickBest(cands []candidate) (candidate, bool) {
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
