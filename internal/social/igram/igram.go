// Package igram 抓 Instagram 博主的帖子照片。
//
// **只走免登录**（2026-11-04 用户决定）：库里那把 Cookie 已经被 IG 风控
// （302 到 /accounts/update_risky_contactpoint，要验证邮箱/手机号），登录态这条路整条断了，
// 而免登录能拿到资料 + 最新 100 条、含原图与轮播全部子图。所以设置里不再有 Instagram Cookie
// 这一项，代码里也不再读它——少一个凭证槽位，就少一处「配了也不生效」的困惑。
//
// 走的还是那三步（实测数字在 anon.go 文件头，全部量过，不是猜的）：
//  1. GET https://www.instagram.com/{handle}/  —— **冒充 Googlebot UA**，拿真主页：
//     xig_user_by_igid_v2（资料）+ polaris_timeline_connection（首屏 12 条）+ LSD 令牌。
//     普通 Chrome UA 只会拿到 500 KB 登录壳，真号假号一个字节数。
//  2. POST https://www.instagram.com/api/graphql（doc 39451372751143943，
//     PolarisProfilePostsLoggedOutTabGridUIContentQuery，first 上限 100）—— 免登录网格。
//  3. 同上（doc 28459077533770840，PolarisLoggedOutDesktopWWWPostRootContentQuery，
//     variables 只有 {"media_id": pk}）—— 轮播帖的子图与精确 taken_at，只对轮播帖打。
//
// 天花板（说清楚，不假装）：这条网格查询**不能翻页**——after / cursor / max_id / end_cursor /
// offset / skip 全被忽略，回的都是第一页。所以免登录拿得到的是**最新 100 条**，
// 第 101 条往前的历史没有。这是 2026-11-04 拿「翻页能力」换「不再需要登录凭证」的取舍。
//
// 已知限制：IG 的图片地址是**带签名的**（`oe` 参数实测 105.5 小时 ≈ 4.4 天），过期就取不到。
// 抓过的图会进服务端磁盘缓存（之后一直可用），但从没被抓过的老图会在签名过期后取不到
// —— 界面上表现为占位图。
package igram

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

const (
	// wwwBase 是网页版主机：HTML 与 GraphQL 端点都在这边（`i.instagram.com` 上同名路径是 404）。
	wwwBase = "https://www.instagram.com"
	// appID 是 Instagram 网页版自己的 app id（公开常量），免登录查询也要发。
	appID = "936619743392459"
	// pageSize 是**我们**一页返回几条。免登录网格一次能拿 100 条，本地按这个大小切片，
	// 这样上层「一轮抓 5 页」的节奏不用改。
	pageSize = 33
	// maxBody 是响应体上限：免登录网格 first=100 实测 5.1 MB，8 MB 有余量。
	maxBody = 8 << 20
	// cooldown 是撞到 429 后的冷却：IG 不给 reset 头，只能保守。
	cooldown = 15 * time.Minute
)

// Adapter 实现 social.Adapter。
type Adapter struct {
	client   *http.Client
	throttle *social.Throttle
	// www 是网页版主机，测试里换成本地假站。
	www string
	now func() time.Time
	// anon 是免登录首屏缓存（资料 + 最新 ≤100 条节点），10 分钟。
	anon *anonCache
	// anonPosts 是免登录单帖详情的缓存（pk → 帖子），免得每轮重拉同一批轮播帖。
	anonPosts map[string]anonPostCache
	// mu 保护 anon / anonPosts。
	mu sync.Mutex
}

// anonPostCache 是单帖详情缓存的一条。
type anonPostCache struct {
	post social.Post
	at   time.Time
}

func New(client *http.Client) *Adapter {
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	return &Adapter{
		client:   client,
		throttle: social.NewThrottle(3 * time.Second),
		www:      wwwBase,
		now:      time.Now,
	}
}

func (a *Adapter) Key() string  { return "instagram" }
func (a *Adapter) Name() string { return "Instagram" }

// NeedsCookie 恒为 false：这条路径一个凭证都不用。
func (a *Adapter) NeedsCookie() bool { return false }

func (a *Adapter) Throttle() *social.Throttle { return a.throttle }

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

// ---- 抓取 ----

// GetProfile 抓博主资料：只读免登录页里的那段 xig_user_by_igid_v2，不解析任何帖子，
// 所以它是 4~5 秒级的（把 100 条轮播全解析一遍要两分多钟，见 anon.go 的 anonCache 注释）。
func (a *Adapter) GetProfile(ctx context.Context, handle string) (*social.Account, error) {
	return a.anonProfile(ctx, handle)
}

// ListPosts 抓一页帖子。
//
// 断点只有一种写法 `anon:{offset}`（本地切片序号）。库里可能还留着登录态时代的
// `gql:{end_cursor}` 与 `{userID}|{nextMaxID}`——那些游标现在没人能兑现，
// 遇到它们就从最新一页重来（已入库的帖子按 post_id 去重，不会重复上墙）。
func (a *Adapter) ListPosts(ctx context.Context, handle, cursor string) (*social.Page, error) {
	if strings.HasPrefix(cursor, anonCursorPrefix) {
		return a.anonCursorPage(ctx, handle, cursor)
	}
	return a.anonFirstPage(ctx, handle)
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

// candidate 是 IG 给的一档图片尺寸（免登录网格每条帖子 14 档，从大到小）。
type candidate struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// pickBest 取一档可用尺寸：一般按从大到小给，取第一个带 url 且宽 > 0 的。
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

// 编译期检查。
var _ social.Adapter = (*Adapter)(nil)
