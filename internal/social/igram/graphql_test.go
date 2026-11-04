package igram

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// bootstrapHTML 是主页 HTML 里跟令牌相关的那一小段（照真实页面结构与上下文裁出来）：
// LSD 在 `["LSD",[],{"token":"…"},323]` 里，fb_dtsg 在 MRequestConfig 里。
const bootstrapHTML = `<html><head><script type="application/json">["LSD",[],{"token":"LSD1"},323]</script>
<script type="application/json">["MRequestConfig",[],{"dtsg":{"token":"DTSG1","valid_for":86400,"expire":1793820694}}</script>
</head><body>513 KB 的网页壳就是这个样子，一行数据都没有</body></html>`

// gqlFixture 照 PolarisProfilePostsQuery 真实的响应结构裁出来：3 条帖子
// （单图 3072×4096、carousel 两张 + 一个视频 child、纯视频），带 end_cursor。
// 实测单图帖的 candidates 第一档就是原图 3072×4096，后面依次是 1080/720/…/150 共 14 档。
const gqlFixture = `{"data":{"xdt_api__v1__feed__user_timeline_graphql_connection":{
  "edges":[
    {"node":{"code":"AAA","pk":"1","taken_at":1761200000,
      "caption":{"text":"first post"},
      "image_versions2":{"candidates":[
        {"url":"https://scontent.cdninstagram.com/AAA.jpg","width":3072,"height":4096},
        {"url":"https://scontent.cdninstagram.com/AAA-s.jpg","width":480,"height":640}]},
      "user":{"pk":"42","username":"mini","full_name":"Mini Diva",
        "profile_pic_url":"https://scontent.cdninstagram.com/avatar150.jpg",
        "hd_profile_pic_url_info":{"url":"https://scontent.cdninstagram.com/avatarHD.jpg","width":320,"height":320}}}},
    {"node":{"code":"BBB","pk":"2","taken_at":1761100000,
      "caption":{"text":"carousel"},
      "carousel_media":[
        {"image_versions2":{"candidates":[{"url":"https://scontent.cdninstagram.com/BBB0.jpg","width":1080,"height":1350}]}},
        {"image_versions2":{"candidates":[{"url":"https://scontent.cdninstagram.com/BBB1.jpg","width":1080,"height":1080}]}},
        {"video_versions":[{"url":"https://scontent.cdninstagram.com/BBB2.mp4"}],
         "image_versions2":{"candidates":[{"url":"https://scontent.cdninstagram.com/BBB2.jpg","width":1080,"height":1080}]}}]}},
    {"node":{"code":"CCC","pk":"3","taken_at":1761000000,
      "video_versions":[{"url":"https://scontent.cdninstagram.com/CCC.mp4"}],
      "image_versions2":{"candidates":[{"url":"https://scontent.cdninstagram.com/CCC.jpg","width":1080,"height":1920}]}}}],
  "page_info":{"has_next_page":true,"end_cursor":"CUR1"}}}}`

// gqlHandler 把「主页给令牌 + GraphQL 给数据」两跳都接上，顺便断言请求长什么样。
func gqlHandler(t *testing.T, respBody string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/mini/":
			if ck := r.Header.Get("Cookie"); !strings.Contains(ck, "sessionid=S1") {
				t.Errorf("抓令牌的请求也要带 Cookie: %q", ck)
			}
			w.Write([]byte(bootstrapHTML))
		case r.Method == http.MethodPost && r.URL.Path == "/api/graphql":
			if err := r.ParseForm(); err != nil {
				t.Errorf("表单解析失败: %v", err)
			}
			if got := r.PostForm.Get("doc_id"); got != gqlDocID {
				t.Errorf("doc_id = %q，期望 %q", got, gqlDocID)
			}
			if got := r.PostForm.Get("fb_api_req_friendly_name"); got != gqlFriendlyName {
				t.Errorf("friendly_name = %q", got)
			}
			if got := r.PostForm.Get("lsd"); got != "LSD1" {
				t.Errorf("表单里的 lsd = %q，期望从主页抠到的 LSD1", got)
			}
			if got := r.Header.Get("x-fb-lsd"); got != "LSD1" {
				t.Errorf("x-fb-lsd 头 = %q", got)
			}
			if got := r.Header.Get("x-csrftoken"); got != "C1" {
				t.Errorf("x-csrftoken 头 = %q，期望取自 Cookie 里的 csrftoken", got)
			}
			// 少了 Referer/Sec-Fetch-* 时 IG 会回 200 + 网页壳而不是 JSON（实测），必须发出去。
			if got := r.Header.Get("Referer"); got != "http://"+r.Host+"/mini/" && !strings.HasSuffix(got, "/mini/") {
				t.Errorf("Referer = %q，期望指向该博主主页", got)
			}
			if got := r.Header.Get("Sec-Fetch-Site"); got != "same-origin" {
				t.Errorf("Sec-Fetch-Site = %q，缺了它 IG 会回网页壳", got)
			}
			if got := r.PostForm.Get("jazoest"); got != "2"+fmt.Sprint(sumRunes(r.PostForm.Get("fb_dtsg"))) {
				t.Errorf("jazoest = %q 与 fb_dtsg 不符", got)
			}
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Write([]byte(respBody))
		default:
			t.Errorf("意外的请求 %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func sumRunes(s string) int {
	n := 0
	for _, r := range s {
		n += int(r)
	}
	return n
}

func TestGraphQLFirstPage(t *testing.T) {
	a := newTestAdapter(t, gqlHandler(t, gqlFixture), cookieJSON)
	page, err := a.ListPosts(context.Background(), "mini", "")
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if len(page.Posts) != 3 {
		t.Fatalf("帖子数 = %d，期望 3", len(page.Posts))
	}
	if page.NextCursor != "gql:CUR1" {
		t.Errorf("翻页游标 = %q，期望 gql:CUR1（带前缀才能和旧断点区分）", page.NextCursor)
	}

	p := page.Posts[0]
	if p.PostID != "AAA" || p.URL != "https://www.instagram.com/p/AAA/" || p.Caption != "first post" {
		t.Errorf("帖子字段不对: %+v", p)
	}
	if len(p.Photos) != 1 || p.Photos[0].Width != 3072 || p.Photos[0].Height != 4096 {
		t.Errorf("单图帖应当取 candidates 里最大的一档: %+v", p.Photos)
	}
	if p.PostedAt.Unix() != 1761200000 {
		t.Errorf("发布时间 = %v", p.PostedAt)
	}

	// carousel：两个图片 child 都收，视频 child 跳过（照片墙只放静态图）。
	car := page.Posts[1]
	if len(car.Photos) != 2 {
		t.Fatalf("carousel 照片数 = %d，期望 2（第三张是视频，要跳过）", len(car.Photos))
	}
	if car.Photos[1].URL != "https://scontent.cdninstagram.com/BBB1.jpg" || car.Photos[1].Seq != 2 {
		t.Errorf("carousel 第二张不对: %+v", car.Photos[1])
	}

	// 纯视频帖不留照片。
	if len(page.Posts[2].Photos) != 0 {
		t.Errorf("视频帖不该有照片: %+v", page.Posts[2])
	}
}

func TestGraphQLSecondPage(t *testing.T) {
	var gotAfter atomic.Value
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/mini/" {
			w.Write([]byte(bootstrapHTML))
			return
		}
		r.ParseForm()
		gotAfter.Store(r.PostForm.Get("variables"))
		w.Write([]byte(gqlFixture))
	}, cookieJSON)

	// 第二页要带 after，且不该再带 username 之外的无关参数。
	page, err := a.ListPosts(context.Background(), "mini", "gql:CUR1")
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if len(page.Posts) != 3 {
		t.Fatalf("帖子数 = %d，期望 3", len(page.Posts))
	}
	vars, _ := gotAfter.Load().(string)
	if !strings.Contains(vars, `"after":"CUR1"`) {
		t.Errorf("第二页的 variables 里没有 after=CUR1: %s", vars)
	}
}

// 资料从 count=1 的那一页帖子里的 node.user 拿：一次请求就够，不用先解析数字 id
// （实测 PolarisProfilePageContentQuery 只认 id，传 username 直接 1675012）。
func TestGraphQLProfile(t *testing.T) {
	var count atomic.Value
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(bootstrapHTML))
			return
		}
		r.ParseForm()
		count.Store(r.PostForm.Get("variables"))
		w.Write([]byte(gqlFixture))
	}, cookieJSON)

	acct, err := a.GetProfile(context.Background(), "Mini")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if acct.Handle != "mini" || acct.DisplayName != "Mini Diva" {
		t.Errorf("账号资料不对: %+v", acct)
	}
	// 有高清头像就用高清的。
	if acct.AvatarURL != "https://scontent.cdninstagram.com/avatarHD.jpg" {
		t.Errorf("头像 = %q，期望优先用 hd_profile_pic_url_info", acct.AvatarURL)
	}
	if acct.ProfileURL != "https://www.instagram.com/mini/" {
		t.Errorf("主页地址 = %q", acct.ProfileURL)
	}
	vars, _ := count.Load().(string)
	if !strings.Contains(vars, `"count":1`) {
		t.Errorf("资料只需要 1 条帖子，实际 variables=%s", vars)
	}
	// 那 5 个 relay provider 开关少一个就 1675012，必须具备。
	for k := range gqlPV {
		if !strings.Contains(vars, k) {
			t.Errorf("variables 里缺 relay provider 开关 %s", k)
		}
	}
}

// IG 的 GraphQL 会「部分成功」：data 完整，errors 里却挂着每个被 geotag 节点的
// `location.profile_pic_url` 的 field_exception（实测原文见 docs/M5-SOCIAL.md）。
// 这种必须当成功用，否则每翻一页都在「服务端错误」上停住。
func TestGraphQLPartialFieldErrors(t *testing.T) {
	// 真实形状：data 里整页数据齐全，同时又有一条指向 location.profile_pic_url 的 error。
	const body = `{"data":{"xdt_api__v1__feed__user_timeline_graphql_connection":{
	  "edges":[{"node":{"code":"AAA","taken_at":1761200000,"caption":{"text":"ok"},
	    "image_versions2":{"candidates":[{"url":"https://scontent.cdninstagram.com/AAA.jpg","width":3072,"height":4096}]},
	    "user":{"pk":"42","username":"mini","full_name":"Mini Diva"}}}],
	  "page_info":{"has_next_page":false,"end_cursor":""}}},
	  "errors":[{"message":"A server error field_exception occured. Check server logs for details.",
	    "path":["xdt_api__v1__feed__user_timeline_graphql_connection","edges",0,"node","location","profile_pic_url"],
	    "severity":"ERROR"}]}`
	a := newTestAdapter(t, gqlHandler(t, body), cookieJSON)

	page, err := a.ListPosts(context.Background(), "mini", "")
	if err != nil {
		t.Fatalf("data 完整时不该因为 errors 失败: %v", err)
	}
	if len(page.Posts) != 1 || page.Posts[0].PostID != "AAA" {
		t.Fatalf("帖子不对: %+v", page.Posts)
	}
	if page.NextCursor != "" {
		t.Errorf("has_next_page=false 时不该给游标，实际 %q", page.NextCursor)
	}
}

// 回网页壳（200 + HTML）时换一对令牌重试一次；两次都这样才报 blocked，且不设冷却。
func TestGraphQLDegradedToHTML(t *testing.T) {
	var gets, posts int32
	// 只数「带会话」的请求：免登录爬虫页（anon.go）是不带 Cookie 的另一条路，
	// 这里让它直接 404，免得把它的 1 次 GET 算进令牌重抓的次数里。
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("User-Agent"), "Googlebot") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			atomic.AddInt32(&gets, 1)
			w.Write([]byte(bootstrapHTML))
			return
		}
		atomic.AddInt32(&posts, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html><html><body>513 KB 的网页壳</body></html>`))
	}, cookieJSON)

	_, err := a.ListPosts(context.Background(), "mini", "")
	if k := social.KindOf(err); k != social.KindBlocked {
		t.Fatalf("错误种类 = %s，期望 blocked（%v）", k, err)
	}
	if n := atomic.LoadInt32(&gets); n != 2 {
		t.Errorf("应该重新抓一次主页换令牌，实际抓了 %d 次", n)
	}
	if n := atomic.LoadInt32(&posts); n != 2 {
		t.Errorf("应该重试一次 GraphQL，实际打了 %d 次", n)
	}
	if d := a.throttle.CooldownRemaining(); d > 0 {
		t.Errorf("网页壳不是限流，不该设冷却，实际还有 %s", d)
	}
	if msg := err.Error(); !strings.Contains(msg, "网页壳") {
		t.Errorf("错误文案要说清是网页壳，实际 %q", msg)
	}
}

// 未登录额度用完时 IG 回 `{"errors":[{"message":"Rate limit exceeded","code":1675004}]}`：
// 这是限流，要冷却。
func TestGraphQLRateLimitCode(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(bootstrapHTML))
			return
		}
		w.Write([]byte(`{"errors":[{"message":"Rate limit exceeded","severity":"CRITICAL","code":1675004}]}`))
	}, cookieJSON)

	_, err := a.ListPosts(context.Background(), "mini", "")
	if k := social.KindOf(err); k != social.KindRateLimited {
		t.Fatalf("错误种类 = %s，期望 rate_limited（%v）", k, err)
	}
	if a.throttle.CooldownRemaining() <= 0 {
		t.Error("限流应当进入冷却")
	}
}

// 缺 relay provider 开关会被 IG 报成「服务端抽风」（1675012）：
// 这种情况是协议层变了，要当 stale_parser 并回退到 web_profile_info。
func TestGraphQLMissingVariablesFallsBackToREST(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/mini/":
			w.Write([]byte(bootstrapHTML))
		case r.Method == http.MethodPost:
			w.Write([]byte(`{"errors":[{"message":"A server error missing_required_variable_value occured.","code":1675012}]}`))
		case strings.Contains(r.URL.Path, "/users/web_profile_info/"):
			w.Write([]byte(profileFixture))
		default:
			t.Errorf("意外的请求 %s %s", r.Method, r.URL.Path)
		}
	}, cookieJSON)

	page, err := a.ListPosts(context.Background(), "mini", "")
	if err != nil {
		t.Fatalf("回退之后应当成功: %v", err)
	}
	if len(page.Posts) != 3 {
		t.Fatalf("回退路径给了 %d 条帖子，期望 3", len(page.Posts))
	}
	// 回退路径的游标是旧格式（没有 gql: 前缀），下次会继续走 REST。
	if page.NextCursor != "42|CURSOR1" {
		t.Errorf("回退路径的游标 = %q，期望旧格式 42|CURSOR1", page.NextCursor)
	}
}

// 两条路都不通时，错误里要把两边的真实原因都写出来（不然用户只看到「解析失败」）。
func TestGraphQLAndRESTBothFailing(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/mini/":
			w.Write([]byte(bootstrapHTML))
		case r.Method == http.MethodPost:
			w.Write([]byte(`{"errors":[{"message":"missing_required_variable_value","code":1675012}]}`))
		default:
			w.WriteHeader(http.StatusTooManyRequests)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<!DOCTYPE html><html class="logged-in"><title>Page Not Found</title></html>`))
		}
	}, cookieJSON)

	_, err := a.ListPosts(context.Background(), "mini", "")
	if k := social.KindOf(err); k != social.KindStaleParser {
		t.Fatalf("错误种类 = %s，期望 stale_parser（要提示更新 doc_id）: %v", k, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, gqlDocID) || !strings.Contains(msg, "web_profile_info") {
		t.Errorf("错误里要同时写清 GraphQL 与旧接口两边的原因，实际 %q", msg)
	}
}

// 没有 Cookie 时不该带着会话去敲门（连令牌都不抓）。
//
// 免登录爬虫页（anon.go）是另一回事：那条路不需要 Cookie，2026-11-04 起没有 Cookie 也会走它，
// 所以这里只禁止「非爬虫 UA」的请求，并让爬虫页回 404 —— 结果是 needs_auth（会话那条的错因）。
func TestGraphQLNeedsCookie(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("User-Agent"), "Googlebot") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Errorf("没 Cookie 不该带着会话去请求: %s %s", r.Method, r.URL.Path)
	}, "")
	if _, err := a.GetProfile(context.Background(), "mini"); social.KindOf(err) != social.KindNeedsAuth {
		t.Errorf("错误种类 = %s，期望 needs_auth", social.KindOf(err))
	}
}

// jazoest 必须和 IG 页面自己算的一致：拿一份真实的 fb_dtsg 对答案（页面里的 qm 参数是 26448）。
func TestJazoest(t *testing.T) {
	const dtsg = "NAfwykz6VdrNrnV7BGRaEG9OfnmBrZrmBPLfbsvMy4FLrXtVH0z6nEA:17864789131057511:1793129828"
	if got := jazoest(dtsg); got != "26448" {
		t.Errorf("jazoest(%q) = %q，期望 26448（与 IG 页面里的 qm 参数逐位一致）", dtsg, got)
	}
}

// 令牌缓存在有效期内只抓一次主页；过期后要重新抓。
func TestTokenCacheTTL(t *testing.T) {
	var gets int32
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			atomic.AddInt32(&gets, 1)
			w.Write([]byte(bootstrapHTML))
			return
		}
		w.Write([]byte(gqlFixture))
	}, cookieJSON)

	if _, err := a.ListPosts(context.Background(), "mini", ""); err != nil {
		t.Fatalf("第一页: %v", err)
	}
	if _, err := a.ListPosts(context.Background(), "mini", "gql:CUR1"); err != nil {
		t.Fatalf("第二页: %v", err)
	}
	if n := atomic.LoadInt32(&gets); n != 1 {
		t.Errorf("同一轮同步里令牌应当复用，实际抓了 %d 次主页", n)
	}

	// 把时钟拨过 TTL。
	a.mu.Lock()
	a.tok.at = a.now().Add(-tokenTTL - time.Minute)
	a.mu.Unlock()
	if _, err := a.ListPosts(context.Background(), "mini", "gql:CUR2"); err != nil {
		t.Fatalf("第三页: %v", err)
	}
	if n := atomic.LoadInt32(&gets); n != 2 {
		t.Errorf("令牌过期后应当重新抓主页，实际 %d 次", n)
	}
}
