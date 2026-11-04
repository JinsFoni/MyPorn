// Instagram 的免登录抓取：冒充搜索引擎爬虫，拿 IG 给 SEO 用的那份公开页面。
//
// 2026-11-04 起这是**唯一**的路径（用户要求：设置里去掉 Instagram Cookie，以后一律免登录）。
// 之前它只是登录态断掉时的兜底，因为会话能翻页追全量；但那把 Cookie 被上游 302 到
// /accounts/update_risky_contactpoint（要验证邮箱/手机号）之后，会话这条路整条走不通了，
// 于是量了「免登录到底能拿到什么」，结论是：**能，且能拿到原图**。
//
// 逐条实测（同一台机器、同一个出口 IP、同一分钟，2026-11-04 15:4x）：
//
//  1. 普通 Chrome UA 的 `GET /{handle}/` 是 500 KB 登录壳，真号假号一个字节数；
//     把 UA 换成 Googlebot，同一条 URL 回 1265407 字节的真主页。页里
//     `xig_user_by_igid_v2` 带 full_name / username / follower_count / pk / profile_pic_url，
//     以及 `polaris_timeline_connection`（12 条帖子 + page_info）。LSD 令牌也在这一页里（27 字节）。
//  2. 用那一页的 lsd 去 `POST /api/graphql`：
//     - doc 39451372751143943（PolarisProfilePostsLoggedOutTabGridUIContentQuery）→ 200。
//     `first` 服务端上限 100：first=12 回 12 条 / 311 KB，first=155 回 **100 条** / 5.1 MB。
//     `after`、`cursor`、`max_id`、`end_cursor`、`offset`、`skip` 全被忽略（回的还是第一页）
//     —— 这条查询**没有翻页**，免登录能拿到的就是最新 100 条。
//     - doc 29600383452901789（登录版 PolarisProfilePostsQuery）仍然
//     `{"message":"Rate limit exceeded","code":1675004}`，换 crawler 令牌也一样：那条要登录态。
//  3. 单帖：doc 28459077533770840（PolarisLoggedOutDesktopWWWPostRootContentQuery，
//     variables 只有 `{"media_id": pk}`）→ 238 KB JSON，带 taken_at 与 carousel_media 的
//     全部子图（每条子图各有 14 档 candidates）。轮播帖只靠网格拿不到子图，就靠这一条。
//  4. 时间戳：免登录网格里没有 taken_at，但 pk 是雪花 id，
//     `taken_at ≈ (pk>>23) + 1314220021721 ms`。实测两帖误差 0.66 s 与 40 s——排序够用，
//     轮播帖反正会走第 3 条拿到精确值。
//
// 所以免登录 = 资料 + 最新 100 条（原图、正文、时间、轮播全部子图），缺的只有第 101 条
// 往前的老帖子。这一点在界面上要说清楚：`backfill_done` 会直接落真，不再有「越同步越全」。
package igram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

const (
	// crawlerUA 是冒充搜索引擎爬虫的 UA。这是整条免登录路径的开关：
	// 换成普通浏览器 UA，同一台机器、同一条 URL 只会拿到 500 KB 登录壳。
	crawlerUA = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	// anonGridDocID / anonGridName 是「已登出」的网格查询，免登录可调，first 上限 100。
	anonGridDocID = "39451372751143943"
	anonGridName  = "PolarisProfilePostsLoggedOutTabGridUIContentQuery"
	// anonMaxFirst 实测服务端上限：传 155 或 1000 都只回 100 条。
	anonMaxFirst = 100
	// anonCursorPrefix 标记「这个断点是免登录路径的本地切片序号」。
	//
	// 免登录查询不能翻页，所以一页 100 条拉回来后在本地按 pageSize 切片，
	// 断点写成 anon:{offset}。缓存过期也不丢进度：拿不到缓存就重新拉一遍 100 条再切。
	anonCursorPrefix = "anon:"
	// anonPageTTL 是本地切片缓存的存活时间：一轮同步（几页）内复用，跨轮次重拉。
	anonPageTTL = 10 * time.Minute
	// anonPostDocID / anonPostName 是「已登出」的单帖查询，只要 media_id。
	anonPostDocID = "28459077533770840"
	anonPostName  = "PolarisLoggedOutDesktopWWWPostRootContentQuery"
	// anonDetailTTL 是单帖详情的缓存时间。
	//
	// 只为了两件事才打它：轮播帖的子图、以及精确的 taken_at。但 ListPosts 不知道
	// 「这条帖子库里有没有」，不缓存的话同一批轮播帖每轮都要重拉一遍（238 KB × N）。
	anonDetailTTL = 6 * time.Hour
	// igEpochMs 是 pk → 时间的基准：Instagram 的媒体 id 是雪花格式，
	// (pk>>23) 是从这个毫秒数起算的偏移。
	igEpochMs = 1314220021721
)

// anonRoot 是免登录页里那段 xig_user_by_igid_v2 的结构（资料 + 首屏网格）。
type anonRoot struct {
	FullName      string `json:"full_name"`
	Username      string `json:"username"`
	PK            string `json:"pk"`
	ID            string `json:"id"`
	ProfilePicURL string `json:"profile_pic_url"`
	FollowerCount int    `json:"follower_count"`
	Conn          *struct {
		Edges []struct {
			Node   anonMedia `json:"node"`
			Cursor string    `json:"cursor"`
		} `json:"edges"`
		PageInfo struct {
			HasNextPage bool   `json:"has_next_page"`
			EndCursor   string `json:"end_cursor"`
		} `json:"page_info"`
	} `json:"polaris_timeline_connection"`
}

// anonMedia 是免登录路径里的一条帖子（网格与单帖详情共用一个结构）。
//
// 和 gqlMedia 的区别：网格里没有 `code` 与 `taken_at`，只有 `pk` 和 `media_type`，
// 所以帖子 id 得从 pk 推短码、时间得从 pk 推毫秒。
type anonMedia struct {
	PK            string            `json:"pk"`
	Code          string            `json:"code"`
	TakenAt       int64             `json:"taken_at"`
	MediaType     int               `json:"media_type"`
	IsPinned      bool              `json:"is_timeline_pinned"`
	VideoVersions []json.RawMessage `json:"video_versions"`
	Caption       *struct {
		Text string `json:"text"`
	} `json:"caption"`
	ImageVersions struct {
		Candidates []candidate `json:"candidates"`
	} `json:"image_versions2"`
	CarouselMedia []anonMedia `json:"carousel_media"`
}

// anonCache 是「免登录首屏」的内存缓存：一处抓取，资料和帖子都用它。
//
// 存的是**原始网格节点**而不是解析好的帖子：一条轮播帖要额外打一次单帖详情（3 s 间隔），
// 100 条里 40 条轮播的话全解析要两分多钟——2026-11-04 第一版就是这么写的，
// 结果连「只要资料」的 GetProfile 都花了 3 分 39 秒。现在按页懒解析，
// 解析过的帖子进 anonPosts（pk → 帖子，6 小时），翻页与下一轮都不重算。
type anonCache struct {
	root  anonRoot
	lsd   string
	nodes []anonMedia // 最新在先，最多 anonMaxFirst 条
	at    time.Time
}

// anonProfile 走免登录页拿博主资料。
func (a *Adapter) anonProfile(ctx context.Context, handle string) (*social.Account, error) {
	c, err := a.anonGrid(ctx, handle, false)
	if err != nil {
		return nil, err
	}
	if c.root.Username == "" {
		return nil, social.NotFound(fmt.Sprintf("Instagram 上看不到 @%s", social.NormalizeHandle(handle)))
	}
	h := social.NormalizeHandle(c.root.Username)
	return &social.Account{
		Platform:    a.Key(),
		Handle:      h,
		DisplayName: strings.TrimSpace(c.root.FullName),
		AvatarURL:   c.root.ProfilePicURL,
		ProfileURL:  wwwBase + "/" + h + "/",
	}, nil
}

// anonCursorPage 处理 `anon:{offset}` 断点：从缓存（或重新拉一遍首屏）里切下一页。
func (a *Adapter) anonCursorPage(ctx context.Context, handle, cursor string) (*social.Page, error) {
	offset, ok := anonOffset(cursor)
	if !ok {
		return nil, social.StaleParser("Instagram 的免登录断点坏了：" + cursor)
	}
	c, err := a.anonGrid(ctx, handle, false)
	if err != nil {
		return nil, err
	}
	return a.anonSlicePage(ctx, handle, c, offset), nil
}

// anonFirstPage 是第一页：拿一次免登录首屏，解析前 pageSize 条。
func (a *Adapter) anonFirstPage(ctx context.Context, handle string) (*social.Page, error) {
	c, err := a.anonGrid(ctx, handle, false)
	if err != nil {
		return nil, err
	}
	return a.anonSlicePage(ctx, handle, c, 0), nil
}

// anonSlicePage 把缓存的节点列表按 pageSize 切一页，**只解析这一页**。
//
// 一页 33 条里通常有十几条轮播，每条要一次单帖详情（3 s 间隔）：这是免登录路径的主要耗时，
// 也是为什么 GetProfile 绝不走这里（它只读 root）。
func (a *Adapter) anonSlicePage(ctx context.Context, handle string, c *anonCache, offset int) *social.Page {
	page := &social.Page{}
	if offset >= len(c.nodes) {
		return page
	}
	end := offset + pageSize
	if end > len(c.nodes) {
		end = len(c.nodes)
	}
	for _, n := range c.nodes[offset:end] {
		if p, ok := a.anonPostOf(ctx, handle, c.lsd, n); ok {
			page.Posts = append(page.Posts, p)
		}
	}
	if end < len(c.nodes) {
		page.NextCursor = fmt.Sprintf("%s%d", anonCursorPrefix, end)
	}
	return page
}

// anonOffset 解析 `anon:{n}` 里的 n。
func anonOffset(cursor string) (int, bool) {
	v := strings.TrimPrefix(cursor, anonCursorPrefix)
	if v == cursor || v == "" {
		return 0, false
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// anonGrid 拿「资料 + 最新 100 条」。缓存命中直接返回。
//
// 抓取分两步：先 GET 主页 HTML（要 lsd 令牌与首屏 12 条，也顺便验一下账号还在不在），
// 再用令牌调免登录网格查询把页大小顶到 100。网格查询挂了就用 HTML 里那 12 条——宁可少拿，
// 也别整条路什么都不给。
func (a *Adapter) anonGrid(ctx context.Context, handle string, force bool) (*anonCache, error) {
	a.mu.Lock()
	if !force && a.anon != nil && a.now().Sub(a.anon.at) < anonPageTTL {
		c := a.anon
		a.mu.Unlock()
		return c, nil
	}
	a.mu.Unlock()

	root, lsd, err := a.anonFetchPage(ctx, handle)
	if err != nil {
		return nil, err
	}
	var nodes []anonMedia
	if lsd != "" && root.PK != "" {
		nodes, err = a.anonGridNodes(ctx, handle, lsd, root.PK, anonMaxFirst)
		if err != nil {
			// 网格查询不可用（限流/改版）：退回页面里自带的首屏。
			nodes = nil
		}
	}
	if len(nodes) == 0 && root.Conn != nil {
		for _, e := range root.Conn.Edges {
			nodes = append(nodes, e.Node)
		}
	}
	if len(nodes) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, social.StaleParser("Instagram 的免登录页里一条帖子都没有（页面结构可能又变了）")
	}

	c := &anonCache{root: root, lsd: lsd, nodes: nodes, at: a.now()}
	a.mu.Lock()
	a.anon = c
	a.mu.Unlock()
	return c, nil
}

// anonFetchPage 用爬虫 UA 抓主页 HTML，抠出资料 JSON 与 LSD 令牌。
func (a *Adapter) anonFetchPage(ctx context.Context, handle string) (anonRoot, string, error) {
	var root anonRoot
	if err := a.throttle.Wait(ctx); err != nil {
		return root, "", err
	}
	target := a.www + "/" + url.PathEscape(handle) + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return root, "", social.Upstream("构造请求失败", err)
	}
	req.Header.Set("User-Agent", crawlerUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return root, "", ctx.Err()
		}
		return root, "", social.Upstream("请求 Instagram 主页失败", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return root, "", social.Upstream("读取 Instagram 主页失败", err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case isBlockPage(resp, body):
		// 拦截页和「真限流」是两件事（和 a.get 里的判断同一套）：拦截页等下去不会好，
		// 所以只报 blocked、**不设冷却**，免得把整个平台锁 15 分钟。
		return root, "", social.Blocked(fmt.Sprintf(
			"Instagram 连免登录爬虫页都拦了（%d，%d 字节的 HTML）：出口 IP 大概进了风控名单", resp.StatusCode, len(body)))
	case resp.StatusCode == http.StatusTooManyRequests:
		until := a.now().Add(cooldown)
		a.throttle.CoolUntil(until)
		return root, "", social.RateLimited("Instagram 对免登录抓取也限流了", until)
	case resp.StatusCode == http.StatusNotFound:
		return root, "", social.NotFound(fmt.Sprintf("Instagram 上没有 @%s", social.NormalizeHandle(handle)))
	default:
		return root, "", social.Upstream(fmt.Sprintf("Instagram 返回 %d", resp.StatusCode), nil)
	}

	html := string(body)
	at := strings.Index(html, `"xig_user_by_igid_v2":`)
	if at < 0 {
		return root, "", social.StaleParser(fmt.Sprintf(
			"Instagram 的免登录页里没有资料段（%d 字节）：爬虫页面结构可能又变了", len(body)))
	}
	if err := decodeJSONAt(html, at+len(`"xig_user_by_igid_v2":`), &root); err != nil {
		return root, "", social.StaleParser("Instagram 的免登录资料段解析失败")
	}
	return root, lsdToken(html), nil
}

// anonGridNodes 用免登录网格查询把首屏顶到 first 条（服务端上限 100，且不能翻页）。
func (a *Adapter) anonGridNodes(ctx context.Context, handle, lsd, userPK string, first int) ([]anonMedia, error) {
	vars := map[string]any{
		"enable_blocking_post_navigation": false,
		"enable_grid_prefetch":            false,
		"enable_slim_grid_cells":          false,
		"first":                           first,
		"media_types":                     nil,
		"shid":                            "",
		"user_id":                         userPK,
		"__relay_internal__pv__PolarisSEOIsCrawlerrelayprovider": false,
	}
	body, err := a.anonGraphQL(ctx, handle, lsd, anonGridDocID, anonGridName, vars)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			User *anonRoot `json:"xig_user_by_igid_v2"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, social.StaleParser("Instagram 的免登录网格响应解析失败")
	}
	if resp.Data.User == nil || resp.Data.User.Conn == nil {
		return nil, social.StaleParser("Instagram 的免登录网格响应里没有帖子连接")
	}
	out := make([]anonMedia, 0, len(resp.Data.User.Conn.Edges))
	for _, e := range resp.Data.User.Conn.Edges {
		out = append(out, e.Node)
	}
	return out, nil
}

// anonPostOf 把网格里的一条帖子转成统一结构：轮播帖再打一次单帖详情拿全部子图。
func (a *Adapter) anonPostOf(ctx context.Context, handle, lsd string, n anonMedia) (social.Post, bool) {
	pk := n.PK
	if pk == "" {
		return social.Post{}, false
	}
	code := n.Code
	if code == "" {
		code = shortcodeOfPK(pk)
	}
	if code == "" {
		return social.Post{}, false
	}
	post := social.Post{
		PostID:   code,
		URL:      wwwBase + "/p/" + code + "/",
		Caption:  anonCaption(n),
		PostedAt: takenAtOfPK(pk),
	}
	// media_type: 1 单图 / 2 视频 / 8 轮播。视频按现有约定跳过取图（照片墙只收静态图），
	// 但帖子本身照样入库——和登录态那条路（postOfGQL）保持一致。
	if n.MediaType == 2 || len(n.VideoVersions) > 0 {
		return post, true
	}
	if n.MediaType == 8 || len(n.CarouselMedia) > 0 {
		// 网格只给轮播的第一张，子图得问单帖详情（带 TTL 缓存，不然每轮都重拉一遍）。
		detail, err := a.anonDetail(ctx, handle, lsd, pk)
		if err == nil {
			if len(detail.Photos) > 0 {
				post.Photos = detail.Photos
			}
			if !detail.PostedAt.IsZero() {
				post.PostedAt = detail.PostedAt
			}
			if detail.Caption != "" {
				post.Caption = detail.Caption
			}
			return post, true
		}
		if best, ok := pickBest(n.ImageVersions.Candidates); ok {
			post.Photos = append(post.Photos, social.Photo{URL: best.URL, Width: best.Width, Height: best.Height, Seq: 1})
		}
		return post, true
	}
	if best, ok := pickBest(n.ImageVersions.Candidates); ok {
		post.Photos = append(post.Photos, social.Photo{URL: best.URL, Width: best.Width, Height: best.Height, Seq: 1})
	}
	return post, true
}

// anonDetail 用免登录单帖查询拿精确 taken_at 与轮播的全部子图。
func (a *Adapter) anonDetail(ctx context.Context, handle, lsd, pk string) (social.Post, error) {
	a.mu.Lock()
	if p, ok := a.anonPosts[pk]; ok && a.now().Sub(p.at) < anonDetailTTL {
		out := p.post
		a.mu.Unlock()
		return out, nil
	}
	a.mu.Unlock()
	if lsd == "" {
		return social.Post{}, social.StaleParser("Instagram 的免登录令牌没拿到，单帖详情走不了")
	}
	body, err := a.anonGraphQL(ctx, handle, lsd, anonPostDocID, anonPostName, map[string]any{"media_id": pk})
	if err != nil {
		return social.Post{}, err
	}
	// 响应形状是 `data.xig_polaris_media.if_not_gated_logged_out.<媒体>`，
	// 顶层那个对象只是个壳（实测它的 pk/code 都是空的）。
	var resp struct {
		Data struct {
			Media struct {
				IfNotGated *anonMedia `json:"if_not_gated_logged_out"`
			} `json:"xig_polaris_media"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return social.Post{}, social.StaleParser("Instagram 的免登录单帖响应解析失败")
	}
	media := resp.Data.Media.IfNotGated
	if media == nil || (media.PK == "" && media.Code == "" && len(media.CarouselMedia) == 0) {
		return social.Post{}, social.StaleParser("Instagram 的免登录单帖响应里没有媒体")
	}
	out := social.Post{PostID: shortcodeOfPK(pk), Caption: anonCaption(*media)}
	if media.Code != "" {
		out.PostID = media.Code
	}
	if media.TakenAt > 0 {
		out.PostedAt = time.Unix(media.TakenAt, 0).UTC()
	}
	children := media.CarouselMedia
	if len(children) == 0 {
		children = []anonMedia{*media}
	}
	seq := 0
	for _, c := range children {
		if len(c.VideoVersions) > 0 {
			continue
		}
		if c.Code == "" && c.PK == "" {
			continue
		}
		best, ok := pickBest(c.ImageVersions.Candidates)
		if !ok {
			continue
		}
		seq++
		out.Photos = append(out.Photos, social.Photo{URL: best.URL, Width: best.Width, Height: best.Height, Seq: seq})
	}
	a.mu.Lock()
	if a.anonPosts == nil {
		a.anonPosts = map[string]anonPostCache{}
	}
	a.anonPosts[pk] = anonPostCache{post: out, at: a.now()}
	a.mu.Unlock()
	return out, nil
}

// anonGraphQL 用免登录令牌调一次 /api/graphql。
//
// 和登录态那条路（graphql.go）是同一台端点、同一套 form，唯一的区别是令牌来自爬虫页面、
// 且 `fb_dtsg` 为空（爬虫页里只有 LSD）。实测这样服务端照收。
func (a *Adapter) anonGraphQL(ctx context.Context, handle, lsd, docID, friendly string, vars map[string]any) ([]byte, error) {
	if err := a.throttle.Wait(ctx); err != nil {
		return nil, err
	}
	vb, err := json.Marshal(vars)
	if err != nil {
		return nil, social.Upstream("序列化请求失败", err)
	}
	form := url.Values{}
	form.Set("doc_id", docID)
	form.Set("variables", string(vb))
	form.Set("server_timestamps", "true")
	form.Set("fb_api_caller_class", "RelayModern")
	form.Set("fb_api_req_friendly_name", friendly)
	form.Set("lsd", lsd)
	form.Set("fb_dtsg", "")
	form.Set("jazoest", "2")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.www+"/api/graphql", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, social.Upstream("构造请求失败", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", crawlerUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("x-ig-app-id", appID)
	req.Header.Set("x-fb-lsd", lsd)
	req.Header.Set("x-fb-friendly-name", friendly)
	req.Header.Set("Origin", wwwBase)
	req.Header.Set("Referer", wwwBase+"/"+url.PathEscape(handle)+"/")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")

	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, social.Upstream("请求 Instagram 失败", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, social.Upstream("读取 Instagram 响应失败", err)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusTooManyRequests {
			until := a.now().Add(cooldown)
			a.throttle.CoolUntil(until)
			return nil, social.RateLimited("Instagram 对免登录抓取也限流了", until)
		}
		return nil, social.Upstream(fmt.Sprintf("Instagram 返回 %d", resp.StatusCode), nil)
	}
	if len(body) > 0 && body[0] == '{' {
		var probe struct {
			Errors []gqlError `json:"errors"`
		}
		if json.Unmarshal(body, &probe) == nil && len(probe.Errors) > 0 {
			e := probe.Errors[0]
			// 1675004 = 匿名额度用完（登录版查询的典型回法）。这条查询要是也撞上，
			// 说明免登录额度是一段时间的，等一等；不设 15 分钟冷却，因为爬虫页那条路可能还好。
			if e.Code == 1675004 {
				return nil, social.RateLimited("Instagram 的免登录额度也用完了", a.now().Add(cooldown))
			}
			return nil, social.StaleParser(fmt.Sprintf("Instagram 的免登录查询报错：%s（code %d）", e.Message, e.Code))
		}
	}
	return body, nil
}

// gqlError 是 IG GraphQL 的错误条目（只需 message 与 code 两项就够定性）。
type gqlError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// shortcodeOfPK 由 pk 推短码。
//
// IG 的短码就是 pk 的 base64 变体（字母表 A-Za-z0-9-_，无填充）。实测
// pk 4020717614598471869 → DfMdV4oTEC9，4019241275077392564 → DfHNqUTk3i0，
// 与页面上 `code` 字段逐字一致（这两对就是测试用例）。
func shortcodeOfPK(pk string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	n := uint64(0)
	for _, r := range pk {
		if r < '0' || r > '9' {
			return ""
		}
		n = n*10 + uint64(r-'0')
	}
	if n == 0 {
		return ""
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = alphabet[n%64]
		n /= 64
	}
	return string(buf[i:])
}

// takenAtOfPK 由 pk 推发帖时间：雪花 id 的高位是从 igEpochMs 起算的毫秒。
//
// 实测偏差 0.66 s 与 40 s（两帖对照页面上的 taken_at）。排序、显示都够用；
// 轮播帖会走单帖详情拿到精确值，所以这里只当兜底。
func takenAtOfPK(pk string) time.Time {
	n := uint64(0)
	for _, r := range pk {
		if r < '0' || r > '9' {
			return time.Time{}
		}
		n = n*10 + uint64(r-'0')
	}
	if n == 0 {
		return time.Time{}
	}
	ms := int64(n>>23) + igEpochMs
	if ms <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ms*int64(time.Millisecond)).UTC()
}

func anonCaption(n anonMedia) string {
	if n.Caption != nil {
		return strings.TrimSpace(n.Caption.Text)
	}
	return ""
}

// lsdToken 从页面 HTML 里抠 LSD 令牌（`["LSD",[],{"token":"…"}]`）。
func lsdToken(html string) string {
	const anchor = `["LSD",[],{"token":"`
	i := strings.Index(html, anchor)
	if i < 0 {
		return ""
	}
	rest := html[i+len(anchor):]
	j := strings.IndexByte(rest, '"')
	if j <= 0 {
		return ""
	}
	return rest[:j]
}

// decodeJSONAt 把 s[at:] 当成一段 JSON 解出来。
//
// 用 Decoder 而不是手写括号配平：正文里出现 `}` 的概率不低，配平迟早切在字符串中间。
func decodeJSONAt(s string, at int, dst any) error {
	if at < 0 || at >= len(s) {
		return fmt.Errorf("位置越界")
	}
	return json.NewDecoder(strings.NewReader(s[at:])).Decode(dst)
}
