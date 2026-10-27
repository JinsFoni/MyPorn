package igram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mario/myporn/internal/social"
)

// profileFixture 照真实 web_profile_info 的结构裁出来：3 条帖子
// （单图、carousel 两张、视频），带 end_cursor。
const profileFixture = `{"status":"ok","data":{"user":{
  "id":"42","username":"mini","full_name":"Mini Diva",
  "profile_pic_url_hd":"https://scontent.cdninstagram.com/avatar.jpg",
  "edge_owner_to_timeline_media":{
    "count":3,
    "edges":[
      {"node":{"shortcode":"AAA","is_video":false,"taken_at_timestamp":1761200000,
        "display_url":"https://scontent.cdninstagram.com/AAA.jpg",
        "dimensions":{"width":1080,"height":1350},
        "edge_media_to_caption":{"edges":[{"node":{"text":"first post"}}]}}},
      {"node":{"shortcode":"BBB","is_video":false,"taken_at_timestamp":1761100000,
        "display_url":"https://scontent.cdninstagram.com/BBB0.jpg",
        "dimensions":{"width":1080,"height":1080},
        "edge_media_to_caption":{"edges":[{"node":{"text":"carousel"}}]},
        "edge_sidecar_to_children":{"edges":[
          {"node":{"display_url":"https://scontent.cdninstagram.com/BBB0.jpg","dimensions":{"width":1080,"height":1080}}},
          {"node":{"display_url":"https://scontent.cdninstagram.com/BBB1.jpg","dimensions":{"width":1080,"height":1350}}}]}}},
      {"node":{"shortcode":"CCC","is_video":true,"taken_at_timestamp":1761000000,
        "display_url":"https://scontent.cdninstagram.com/CCC.jpg",
        "dimensions":{"width":1080,"height":1920}}}],
    "page_info":{"has_next_page":true,"end_cursor":"CURSOR1"}}}}}`

// feedFixture 是后续页（app 接口），用 image_versions2 + carousel_media。
const feedFixture = `{"status":"ok","more_available":false,"next_max_id":"",
  "items":[{"code":"DDD","media_type":8,"taken_at":1760900000,
    "caption":{"text":"later post"},
    "carousel_media":[
      {"media_type":1,"image_versions2":{"candidates":[{"url":"https://scontent.cdninstagram.com/DDD0.jpg","width":1080,"height":1350},{"url":"https://scontent.cdninstagram.com/DDD0s.jpg","width":480,"height":600}]}},
      {"media_type":1,"image_versions2":{"candidates":[{"url":"https://scontent.cdninstagram.com/DDD1.jpg","width":1080,"height":1080}]}}]}]}`

const cookieJSON = `{"sessionid":"S1","csrftoken":"C1","ds_user_id":"42"}`

func newTestAdapter(t *testing.T, h http.HandlerFunc, cookie string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := New(srv.Client(), func(key string) string {
		if key == cookieKey {
			return cookie
		}
		return ""
	})
	a.base = srv.URL
	a.throttle = social.NewThrottle(time.Millisecond)
	return a
}

func TestParseProfileURL(t *testing.T) {
	a := New(nil, nil)
	cases := []struct{ in, want string }{
		{"https://www.instagram.com/mini/", "mini"},
		{"https://instagram.com/Mini.Diva", "mini.diva"},
		{"instagram.com/mini", "mini"},
		{"@mini", "mini"},
		{"mini", "mini"},
	}
	for _, c := range cases {
		got, err := a.ParseProfileURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseProfileURL(%q) = (%q, %v)，期望 %q", c.in, got, err, c.want)
		}
	}
	bad := []string{"https://www.instagram.com/p/ABC/", "https://x.com/mini", "https://www.instagram.com/reel/x/", ""}
	for _, in := range bad {
		if got, err := a.ParseProfileURL(in); err == nil {
			t.Errorf("ParseProfileURL(%q) 应该报错，却得到 %q", in, got)
		}
	}
}

// 没配 Cookie 必须明确报 needs_auth，不能静默返回空列表。
func TestNeedsAuthWithoutCookie(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("没有 Cookie 时不应该发请求")
	}, "")
	_, err := a.ListPosts(context.Background(), "mini", "")
	if k := social.KindOf(err); k != social.KindNeedsAuth {
		t.Fatalf("错误种类 = %s，期望 needs_auth（%v）", k, err)
	}
}

func TestListPostsFirstPage(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/users/web_profile_info/") {
			t.Errorf("第一页应该打 web_profile_info，实际 %s", r.URL.Path)
		}
		if q := r.URL.Query().Get("username"); q != "mini" {
			t.Errorf("username 参数 = %q", q)
		}
		if ck := r.Header.Get("Cookie"); !strings.Contains(ck, "sessionid=S1") {
			t.Errorf("Cookie 头没带上 sessionid: %q", ck)
		}
		if app := r.Header.Get("x-ig-app-id"); app != appID {
			t.Errorf("缺 x-ig-app-id 头: %q", app)
		}
		w.Write([]byte(profileFixture))
	}, cookieJSON)

	page, err := a.ListPosts(context.Background(), "mini", "")
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if len(page.Posts) != 3 {
		t.Fatalf("帖子数 = %d，期望 3", len(page.Posts))
	}
	if page.NextCursor != "42|CURSOR1" {
		t.Errorf("翻页游标 = %q，期望 42|CURSOR1", page.NextCursor)
	}

	p := page.Posts[0]
	if p.PostID != "AAA" || p.URL != "https://www.instagram.com/p/AAA/" || p.Caption != "first post" {
		t.Errorf("帖子字段不对: %+v", p)
	}
	if len(p.Photos) != 1 || p.Photos[0].Width != 1080 || p.Photos[0].Height != 1350 {
		t.Errorf("单图帖照片不对: %+v", p.Photos)
	}

	// carousel：两个 child 都要收，序号从 1 开始。
	car := page.Posts[1]
	if len(car.Photos) != 2 {
		t.Fatalf("carousel 照片数 = %d，期望 2", len(car.Photos))
	}
	if car.Photos[1].URL != "https://scontent.cdninstagram.com/BBB1.jpg" || car.Photos[1].Seq != 2 {
		t.Errorf("carousel 第二张不对: %+v", car.Photos[1])
	}

	// 视频帖不留照片。
	if len(page.Posts[2].Photos) != 0 {
		t.Errorf("视频帖不该有照片: %+v", page.Posts[2])
	}
}

func TestListPostsSecondPage(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/feed/user/42/") {
			t.Errorf("第二页应该打 feed/user，实际 %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("max_id"); got != "CURSOR1" {
			t.Errorf("max_id = %q", got)
		}
		w.Write([]byte(feedFixture))
	}, cookieJSON)

	page, err := a.ListPosts(context.Background(), "mini", "42|CURSOR1")
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if len(page.Posts) != 1 {
		t.Fatalf("帖子数 = %d，期望 1", len(page.Posts))
	}
	if page.NextCursor != "" {
		t.Errorf("more_available=false 时不该给游标，得到 %q", page.NextCursor)
	}
	p := page.Posts[0]
	if p.PostID != "DDD" || p.Caption != "later post" {
		t.Errorf("帖子字段不对: %+v", p)
	}
	if len(p.Photos) != 2 {
		t.Fatalf("carousel 照片数 = %d，期望 2", len(p.Photos))
	}
	// image_versions2 的 candidates 取最大的一档。
	if p.Photos[0].URL != "https://scontent.cdninstagram.com/DDD0.jpg" || p.Photos[0].Width != 1080 {
		t.Errorf("尺寸选择不对: %+v", p.Photos[0])
	}
}

// 401 必须报 needs_auth（Cookie 失效），不能报成通用错误。
func TestUnauthorized(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"message": "Please wait", "require_login": true, "status": "fail"})
	}, cookieJSON)
	_, err := a.GetProfile(context.Background(), "mini")
	if k := social.KindOf(err); k != social.KindNeedsAuth {
		t.Errorf("错误种类 = %s，期望 needs_auth（%v）", k, err)
	}
}

// 429 报限流并冷却。
func TestRateLimited(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{"message": "", "spam": true, "status": "fail"})
	}, cookieJSON)
	_, err := a.GetProfile(context.Background(), "mini")
	if k := social.KindOf(err); k != social.KindRateLimited {
		t.Fatalf("错误种类 = %s，期望 rate_limited（%v）", k, err)
	}
	if a.throttle.CooldownRemaining() <= 0 {
		t.Error("撞到 429 之后应该进入冷却")
	}
}

func TestParseCredentials(t *testing.T) {
	if _, ok := ParseCredentials(""); ok {
		t.Error("空串应为未配置")
	}
	if _, ok := ParseCredentials(`{"csrftoken":"x"}`); ok {
		t.Error("缺 sessionid 应为未配置")
	}
	c, ok := ParseCredentials(cookieJSON)
	if !ok || c.SessionID != "S1" || c.UserID != "42" {
		t.Errorf("解析结果不对: %+v %v", c, ok)
	}
}

// Netscape cookies.txt（浏览器扩展 / curl cookie jar 导出的那种）要能直接用：
// 每行 7 个制表符分隔字段，HttpOnly 的行首多一个 `#HttpOnly_`，
// 而且整份导出里其它站的 Cookie 必须被丢掉。
func TestParseCredentialsNetscape(t *testing.T) {
	raw := "# Netscape HTTP Cookie File\n" +
		"# https://curl.se/docs/http-cookies.html\n" +
		"# This file was generated by libcurl! Edit at your own risk.\n" +
		"\n" +
		"#HttpOnly_.instagram.com\tTRUE\t/\tTRUE\t1767225600\tsessionid\t123%3Aabc%3A9\n" +
		".instagram.com\tTRUE\t/\tTRUE\t1767225600\tcsrftoken\tCSRF123\n" +
		".instagram.com\tTRUE\t/\tTRUE\t1767225600\tds_user_id\t4567\n" +
		".instagram.com\tTRUE\t/\tTRUE\t1767225600\tmid\tMID789\n" +
		".youtube.com\tTRUE\t/\tTRUE\t1767225600\tSID\tSHOULD-BE-DROPPED\n"

	c, ok := ParseCredentials(raw)
	if !ok {
		t.Fatal("Netscape 格式应当解析成功")
	}
	if c.SessionID != "123%3Aabc%3A9" || c.CSRFToken != "CSRF123" || c.UserID != "4567" {
		t.Fatalf("三项关键 Cookie 没解出来: %+v", c)
	}
	if c.Cookies["mid"] != "MID789" {
		t.Errorf("其余 Cookie 也应当留着（IG 会看 mid），得到 %v", c.Cookies)
	}
	if _, ok := c.Cookies["SID"]; ok {
		t.Error("别的站的 Cookie 不该带进来")
	}
}

// 整段 Cookie 头也要照原样带上，而不是只挑三项。
func TestParseCredentialsCookieHeaderKeepsAll(t *testing.T) {
	c, ok := ParseCredentials("sessionid=abc; csrftoken=xyz; ds_user_id=42; ig_did=DEV")
	if !ok {
		t.Fatal("Cookie 头应当解析成功")
	}
	if c.Cookies["ig_did"] != "DEV" {
		t.Errorf("多余 Cookie 应当保留，得到 %v", c.Cookies)
	}
}

// 少 sessionid 就是没配置，别让它以「已配置」的样子混过去。
func TestParseCredentialsRequiresSessionID(t *testing.T) {
	if _, ok := ParseCredentials(".instagram.com\tTRUE\t/\tTRUE\t1\tcsrftoken\tX\n"); ok {
		t.Error("没有 sessionid 时应当判定为未配置")
	}
	if _, ok := ParseCredentials("csrftoken=X; ds_user_id=1"); ok {
		t.Error("没有 sessionid 的 Cookie 头也应当判定为未配置")
	}
}

// 旧的 JSON 写法（库里已经存着的那些）必须还能读。
func TestParseCredentialsLegacyJSON(t *testing.T) {
	c, ok := ParseCredentials(`{"sessionid":"s","csrftoken":"c","ds_user_id":"u"}`)
	if !ok || c.SessionID != "s" || c.CSRFToken != "c" || c.UserID != "u" {
		t.Fatalf("老 JSON 读不出来: %+v %v", c, ok)
	}
}

// IG 的两种 429 必须分开：JSON 是真限流（要冷却），HTML 是风控拦截页（等下去没用、也不该锁 15 分钟）。
// 2026-10 实测：Cookie 有效（拦截页 HTML 里带着 logged-in class），但 api/v1 全部回 429 + "Page Not Found"。
func TestBlockPageIsNotRateLimited(t *testing.T) {
	blockPage := `<!DOCTYPE html><html lang="en" class="no-js logged-in"><head><title>Page Not Found &bull; Instagram</title></head></html>`
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(blockPage))
	}, cookieJSON)

	_, err := a.ListPosts(context.Background(), "mini", "")
	if k := social.KindOf(err); k != social.KindBlocked {
		t.Fatalf("错误种类 = %s，期望 blocked（%v）", k, err)
	}
	// 关键：拦截页不该让平台整体冷却 —— 换了 Cookie 的用户应该能立刻重试。
	if d := a.throttle.CooldownRemaining(); d > 0 {
		t.Errorf("拦截页不该设冷却，实际还有 %s", d)
	}
	if msg := err.Error(); !strings.Contains(msg, "拦截页") {
		t.Errorf("错误文案要说清是拦截页，实际 %q", msg)
	}
}

// Retry-After 给了就按它冷却，不给才用默认 15 分钟。
func TestRateLimitedHonoursRetryAfter(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message":"Please wait a few minutes before you try again.","status":"fail"}`))
	}, cookieJSON)
	if _, err := a.ListPosts(context.Background(), "mini", ""); social.KindOf(err) != social.KindRateLimited {
		t.Fatalf("错误种类 = %s，期望 rate_limited", social.KindOf(err))
	}
	d := a.throttle.CooldownRemaining()
	if d <= 30*time.Second || d > 45*time.Second {
		t.Errorf("冷却时长应当接近 42s，实际 %s", d)
	}
}

// 用户换完 Cookie 点「清除冷却并重试」后，必须能立刻再发请求。
func TestThrottleClearAllowsImmediateRetry(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"message":"spam"}`))
	}, cookieJSON)
	if _, err := a.ListPosts(context.Background(), "mini", ""); err == nil {
		t.Fatal("应当先撞一次限流")
	}
	if a.throttle.CooldownRemaining() <= 0 {
		t.Fatal("先要进入冷却，才谈得上清除")
	}
	a.throttle.Clear()
	if d := a.throttle.CooldownRemaining(); d != 0 {
		t.Fatalf("Clear 之后不该还有冷却，实际 %s", d)
	}
	if err := a.throttle.Wait(context.Background()); err != nil {
		t.Errorf("Clear 之后应当可以直接请求，实际 %v", err)
	}
}
