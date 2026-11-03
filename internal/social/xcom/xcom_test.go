package xcom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// fixture 是真实响应裁出来的：保留 JSON 结构，只留两条帖子
// （一条两张图的照片帖、一条纯视频帖）和一条转推。
const fixture = `<!DOCTYPE html><html><head></head><body><div id="__next">
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{
"contextProvider":{"hasResults":true,"widget_data_source":"screen-name:mini"},
"timeline":{"entries":[
 {"type":"tweet","entry_id":"tweet-1","content":{"tweet":{
   "id_str":"111","created_at":"Fri Oct 23 23:37:56 +0000 2026",
   "full_text":"two pics","permalink":"/mini/status/111",
   "user":{"id_str":"9","name":"Mini Diva","screen_name":"mini",
           "profile_image_url_https":"https://pbs.twimg.com/profile_images/1/abc_normal.jpg"},
   "extended_entities":{"media":[
     {"type":"photo","media_url_https":"https://pbs.twimg.com/media/AAA.jpg",
      "original_info":{"width":4000,"height":3000},"expanded_url":"https://x.com/mini/status/111/photo/1"},
     {"type":"photo","media_url_https":"https://pbs.twimg.com/media/BBB.jpg?format=jpg&name=small",
      "original_info":{"width":1200,"height":1600},"expanded_url":"https://x.com/mini/status/111/photo/2"}]}
 }}},
 {"type":"tweet","entry_id":"tweet-2","content":{"tweet":{
   "id_str":"222","created_at":"Thu Oct 22 10:00:00 +0000 2026",
   "full_text":"just a video","permalink":"/mini/status/222",
   "user":{"name":"Mini Diva","screen_name":"mini"},
   "extended_entities":{"media":[
     {"type":"video","media_url_https":"https://pbs.twimg.com/media/CCC.jpg",
      "original_info":{"width":1920,"height":1080},
      "expanded_url":"https://x.com/mini/status/222/video/1"}]}
 }}},
 {"type":"tweet","entry_id":"tweet-3","content":{"tweet":{
   "id_str":"333","created_at":"Wed Oct 21 08:00:00 +0000 2026",
   "full_text":"RT @other: hi","permalink":"/mini/status/333",
   "user":{"name":"Mini Diva","screen_name":"mini"},
   "retweeted_status":{"id_str":"999","created_at":"Wed Oct 21 07:59:00 +0000 2026",
     "full_text":"original post","permalink":"/other/status/999",
     "user":{"name":"Other","screen_name":"other"},
     "extended_entities":{"media":[
       {"type":"photo","media_url_https":"https://pbs.twimg.com/media/DDD.jpg",
        "original_info":{"width":1080,"height":1350}}]}}
 }}}]}}}}</script></div></body></html>`

const emptyFixture = `<html><body><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"contextProvider":{"hasResults":false},"timeline":{"entries":[]}}}}</script></body></html>`

func newTestAdapter(t *testing.T, h http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.base = srv.URL
	a.throttle = social.NewThrottle(time.Millisecond)
	return a
}

func TestParseProfileURL(t *testing.T) {
	a := New(nil)
	cases := []struct{ in, want string }{
		{"https://x.com/mini", "mini"},
		{"https://twitter.com/Mini_Diva/", "mini_diva"},
		{"https://www.x.com/mini/media", "mini"},
		{"x.com/mini", "mini"},
		{"@mini", "mini"},
		{"mini", "mini"},
	}
	for _, c := range cases {
		got, err := a.ParseProfileURL(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseProfileURL(%q) = (%q, %v)，期望 %q", c.in, got, err, c.want)
		}
	}
	bad := []string{"https://x.com/mini/status/123", "https://beeg.com/mini", "https://x.com/", ""}
	for _, in := range bad {
		if got, err := a.ParseProfileURL(in); err == nil {
			t.Errorf("ParseProfileURL(%q) 应该报错，却得到 %q", in, got)
		}
	}
}

func TestListPosts(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/screen-name/mini") {
			t.Errorf("请求路径不对: %s", r.URL.Path)
		}
		w.Header().Set("x-rate-limit-remaining", "27")
		w.Write([]byte(fixture))
	})
	page, err := a.ListPosts(context.Background(), "mini", "")
	if err != nil {
		t.Fatalf("ListPosts: %v", err)
	}
	if len(page.Posts) != 3 {
		t.Fatalf("帖子数 = %d，期望 3", len(page.Posts))
	}
	if page.NextCursor != "" {
		t.Errorf("X 没有翻页，NextCursor 应为空，得到 %q", page.NextCursor)
	}

	// 照片帖：两张图，原图后缀是 ?name=orig，帖内序号从 1 开始。
	p := page.Posts[0]
	if p.PostID != "111" || p.URL != "https://x.com/mini/status/111" || p.Caption != "two pics" {
		t.Errorf("帖子字段不对: %+v", p)
	}
	if len(p.Photos) != 2 {
		t.Fatalf("照片数 = %d，期望 2", len(p.Photos))
	}
	if got := p.Photos[0].URL; !strings.Contains(got, "name=orig") || !strings.HasPrefix(got, "https://pbs.twimg.com/media/AAA.jpg") {
		t.Errorf("原图地址不对: %s", got)
	}
	if p.Photos[1].Seq != 2 || p.Photos[1].Width != 1200 {
		t.Errorf("第二张照片字段不对: %+v", p.Photos[1])
	}
	if want := time.Date(2026, 10, 23, 23, 37, 56, 0, time.UTC); !p.PostedAt.Equal(want) {
		t.Errorf("发布时间 = %v，期望 %v", p.PostedAt, want)
	}

	// 纯视频帖：帖子留着（增量同步靠它判「已看过」），但不产生照片。
	if v := page.Posts[1]; len(v.Photos) != 0 || v.PostID != "222" {
		t.Errorf("视频帖应该没有照片: %+v", v)
	}

	// 转推：取原帖的 id/链接/照片，避免同一张图被算两次。
	rt := page.Posts[2]
	if rt.PostID != "999" || rt.URL != "https://x.com/other/status/999" || rt.Caption != "original post" {
		t.Errorf("转推应该归到原帖: %+v", rt)
	}
	if len(rt.Photos) != 1 {
		t.Errorf("转推原帖应有 1 张照片，得到 %d", len(rt.Photos))
	}
}

func TestGetProfile(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture))
	})
	acc, err := a.GetProfile(context.Background(), "mini")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if acc.DisplayName != "Mini Diva" || acc.Handle != "mini" {
		t.Errorf("资料不对: %+v", acc)
	}
	if !strings.Contains(acc.AvatarURL, "_400x400") {
		t.Errorf("头像应换成 400x400: %s", acc.AvatarURL)
	}
	if acc.ProfileURL != "https://x.com/mini" {
		t.Errorf("主页地址不对: %s", acc.ProfileURL)
	}
}

// 账号不存在是 200 + hasResults:false（实测），必须报 not_found，不能当成功。
func TestGetProfileEmpty(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(emptyFixture))
	})
	_, err := a.GetProfile(context.Background(), "qweqweqwe123zzz")
	if err == nil {
		t.Fatal("应该报错")
	}
	if k := social.KindOf(err); k != social.KindNotFound {
		t.Errorf("错误种类 = %s，期望 not_found（%v）", k, err)
	}
}

// 时间线解析不出帖子（账号不存在 / 结构变了）必须报错，不能交一个「成功但 0 条」的空页：
// 那条静默路径会让动态页永远显示「同步成功」，用户看到的就是「怎么一直不更新」。
func TestListPostsEmpty(t *testing.T) {
	noResults := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(emptyFixture))
	})
	_, err := noResults.ListPosts(context.Background(), "qweqweqwe123zzz", "")
	if err == nil {
		t.Fatal("hasResults:false 时应报错")
	}
	if k := social.KindOf(err); k != social.KindNotFound {
		t.Errorf("错误种类 = %s，期望 not_found（%v）", k, err)
	}

	// 有结果但一条帖子都解析不出来：是解析器坏了，不能当「没有新帖」。
	emptyEntries := `<html><body><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"contextProvider":{"hasResults":true},"timeline":{"entries":[]}}}}</script></body></html>`
	noPosts := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(emptyEntries))
	})
	_, err = noPosts.ListPosts(context.Background(), "mini", "")
	if err == nil {
		t.Fatal("有结果但 0 条时应报错")
	}
	if k := social.KindOf(err); k != social.KindStaleParser {
		t.Errorf("错误种类 = %s，期望 stale_parser（%v）", k, err)
	}
}

// 429 要带着 reset 时间回来，并且之后直接冷却、不再打网络。
// 窗口已经翻篇、但响应头里还留着 remaining=0 的那种响应（CDN 缓存的旧头）：
// 不该判死，照常解析。
func TestStaleRateLimitHeader(t *testing.T) {
	staleReset := time.Now().Add(-10 * time.Minute).Unix()
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-rate-limit-limit", "30")
		w.Header().Set("x-rate-limit-remaining", "0")
		w.Header().Set("x-rate-limit-reset", strconv.FormatInt(staleReset, 10))
		w.Write([]byte(fixture))
	})
	page, err := a.ListPosts(context.Background(), "mini", "")
	if err != nil {
		t.Fatalf("过期的限流头不该报错: %v", err)
	}
	if len(page.Posts) != 3 {
		t.Errorf("帖子数 = %d，期望 3", len(page.Posts))
	}
}

func TestRateLimited(t *testing.T) {
	calls := 0
	resetIn := time.Now().Add(15 * time.Minute).Unix()
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("x-rate-limit-limit", "30")
		w.Header().Set("x-rate-limit-remaining", "0")
		w.Header().Set("x-rate-limit-reset", strconv.FormatInt(resetIn, 10))
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("Rate limit exceeded"))
	})
	_, err := a.ListPosts(context.Background(), "mini", "")
	if err == nil {
		t.Fatal("应该报限流")
	}
	if k := social.KindOf(err); k != social.KindRateLimited {
		t.Fatalf("错误种类 = %s，期望 rate_limited", k)
	}
	reset, ok := social.ResetAtOf(err)
	if !ok || reset.Unix() != resetIn+5 {
		t.Errorf("reset 时间不对: %v %v", reset, ok)
	}
	// 冷却期内再打，应该立刻失败且不再发请求。
	if _, err := a.ListPosts(context.Background(), "mini", ""); err == nil {
		t.Fatal("冷却期内应该继续报限流")
	}
	if calls != 1 {
		t.Errorf("冷却期内不该再发请求，实际发了 %d 次", calls)
	}
}

// 200 但没有 __NEXT_DATA__：风控页/改版，必须报 stale_parser，不能当「没帖子」。
func TestNoNextData(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>nothing here</body></html>"))
	})
	_, err := a.ListPosts(context.Background(), "mini", "")
	if k := social.KindOf(err); k != social.KindStaleParser {
		t.Errorf("错误种类 = %s，期望 stale_parser（%v）", k, err)
	}
}

// 正文清理：HTML 实体要解开、t.co 短链（附件/链接卡片）要去掉、段落空行最多留一个。
func TestCleanCaption(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			in:   "Roll Tide &amp; full throttle\n\nhttps://t.co/AbCdEf123",
			want: "Roll Tide & full throttle",
		},
		{
			in:   "第一段\n\n\n\n第二段 https://t.co/xyz",
			want: "第一段\n\n第二段",
		},
		{
			in:   "  @astro_anil and @astro_kutryk — not for Halloween!  ",
			want: "@astro_anil and @astro_kutryk — not for Halloween!",
		},
		{in: "", want: ""},
	}
	for _, c := range cases {
		if got := cleanCaption(c.in); got != c.want {
			t.Errorf("cleanCaption(%q)\n得到 %q\n期望 %q", c.in, got, c.want)
		}
	}
}
