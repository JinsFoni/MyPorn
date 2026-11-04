package igram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// newTestAdapter 起一个本地假 IG：`a.www` 指过去，限流间隔缩到 1 ms。
//
// 适配器不再需要任何凭证（2026-11-04 起只走免登录），所以这里也不用给 Cookie。
func newTestAdapter(t *testing.T, h http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a := New(srv.Client())
	a.www = srv.URL
	a.throttle = social.NewThrottle(time.Millisecond)
	return a
}

func TestParseProfileURL(t *testing.T) {
	a := New(nil)
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
	for _, bad := range []string{
		"https://www.instagram.com/p/DfMdV4oTEC9/",
		"https://www.instagram.com/reel/xyz/",
		"https://twitter.com/mini",
		"https://www.instagram.com/",
		"",
	} {
		if got, err := a.ParseProfileURL(bad); err == nil {
			t.Errorf("ParseProfileURL(%q) 应该报错，却给了 %q", bad, got)
		}
	}
}

// TestLegacyCursorRestartsFromNewest：库里登录态时代留下的断点（gql:… / {id}|{max}）
// 现在没人能兑现，要能原地退回「最新一页」，而不是整轮报错。
func TestLegacyCursorRestartsFromNewest(t *testing.T) {
	a, _, _ := anonServer(t, "")
	for _, cursor := range []string{"gql:QVFB", "42|QVFB"} {
		page, err := a.ListPosts(context.Background(), "kaedekaren0825", cursor)
		if err != nil {
			t.Fatalf("老断点 %q 应退回最新一页，实际报错：%v", cursor, err)
		}
		if len(page.Posts) != 3 {
			t.Fatalf("老断点 %q 应回最新 3 条，实际 %d", cursor, len(page.Posts))
		}
	}
}

// TestBlockPageIsNotRateLimited：风控拦截页（429 + HTML）只提示、不设冷却。
//
// 冷却 15 分钟会让用户以为「站坏了」，而拦截页等下去也不会好——要去换出口 IP。
func TestBlockPageIsNotRateLimited(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Page Not Found</title></head>` +
			`<body>Please wait a few minutes before you try again.</body></html>`))
	})
	_, err := a.GetProfile(context.Background(), "mini")
	if k := social.KindOf(err); k != social.KindBlocked {
		t.Fatalf("错误种类 = %s，期望 blocked（%v）", k, err)
	}
	if d := a.throttle.CooldownRemaining(); d > 0 {
		t.Errorf("拦截页不是限流，不该设冷却，实际还有 %s", d)
	}
}

// TestJSON429LocksPlatform：真限流（429 + JSON）要设冷却，别继续撞。
func TestJSON429LocksPlatform(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"Please wait a few minutes before you try again."}`))
	})
	_, err := a.GetProfile(context.Background(), "mini")
	if k := social.KindOf(err); k != social.KindRateLimited {
		t.Fatalf("错误种类 = %s，期望 rate_limited（%v）", k, err)
	}
	if d := a.throttle.CooldownRemaining(); d < 9*time.Minute {
		t.Fatalf("JSON 限流应按 Retry-After 锁 10 分钟，实际 %s", d)
	}
}

// TestNotFoundWhenProfileMissing：爬虫页里没有资料段也不该报「限流/改版」，要报没这个号。
func TestNotFoundWhenProfileMissing(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>找不到</body></html>`))
	})
	_, err := a.GetProfile(context.Background(), "nosuchhandle")
	if err == nil {
		t.Fatal("空页面应报错")
	}
	if !strings.Contains(err.Error(), "爬虫页") {
		t.Fatalf("文案要说清是爬虫页结构的问题：%v", err)
	}
}
