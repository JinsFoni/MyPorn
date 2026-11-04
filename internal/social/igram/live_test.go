package igram

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// TestLiveInstagram 是可选的真实网络冒烟测试（LIVE=1 才跑）。
//
// 用法（Cookie 直接从浏览器导出的 cookies.txt 原文贴进来即可）：
//
//	LIVE=1 LIVE_IG_COOKIE=$PWD/.tmp/ig/cookies.txt LIVE_IG_HANDLE=kaedekaren0825 \
//	  go test ./internal/social/igram -run TestLiveInstagram -v
//
// 它验证的是这条链路今天还通不通：主页能不能抠到 lsd/fb_dtsg、
// GraphQL 能不能回 JSON（缺了 Referer/Sec-Fetch-* 会回网页壳）、
// 翻页游标能不能兑现成第二页。失败时打印错误种类，方便区分「限流」「Cookie 失效」「解析过期」。
func TestLiveInstagram(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("设置 LIVE=1 才跑真实网络测试")
	}
	cookiePath := os.Getenv("LIVE_IG_COOKIE")
	if cookiePath == "" {
		t.Skip("设置 LIVE_IG_COOKIE=<cookies.txt 路径> 才跑")
	}
	raw, err := os.ReadFile(cookiePath)
	if err != nil {
		t.Fatalf("读不到 Cookie 文件: %v", err)
	}
	handle := os.Getenv("LIVE_IG_HANDLE")
	if handle == "" {
		handle = "natgeo"
	}

	a := New(nil, func(key string) string {
		if key == cookieKey {
			return string(raw)
		}
		return ""
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	start := time.Now()
	acct, err := a.GetProfile(ctx, handle)
	if err != nil {
		t.Fatalf("GetProfile 失败（%s）: %v", social.KindOf(err), err)
	}
	t.Logf("资料 %s（%s）头像 %d 字节的地址，耗时 %s",
		acct.DisplayName, acct.Handle, len(acct.AvatarURL), time.Since(start).Round(time.Millisecond))

	for page, cursor := 1, ""; page <= 2; page++ {
		t0 := time.Now()
		p, err := a.ListPosts(ctx, handle, cursor)
		if err != nil {
			t.Fatalf("第 %d 页失败（%s）: %v", page, social.KindOf(err), err)
		}
		photos := 0
		for _, post := range p.Posts {
			photos += len(post.Photos)
		}
		t.Logf("第 %d 页：%d 条帖子 / %d 张照片，%s，游标 %q",
			page, len(p.Posts), photos, time.Since(t0).Round(time.Millisecond), shortCursor(p.NextCursor))
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
}

// TestLiveInstagramAnon 验证「完全不配 Cookie」时的免登录路径（2026-11-04 用户要求：
// 会话被风控之后，试试免登录能不能抓）。
//
//	LIVE=1 LIVE_IG_HANDLE=kaedekaren0825 go test ./internal/social/igram -run TestLiveInstagramAnon -v
//
// 它把 Cookie 留空，所以只会走 anon.go 那条路：爬虫页拿资料 + 免登录网格拿最新 ≤100 条 +
// 轮播帖走单帖详情补子图。不打库、不下载图片，只数条数与体积。
func TestLiveInstagramAnon(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("设置 LIVE=1 才跑真实网络测试")
	}
	handle := os.Getenv("LIVE_IG_HANDLE")
	if handle == "" {
		handle = "natgeo"
	}
	a := New(nil, func(string) string { return "" })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t0 := time.Now()
	acct, err := a.GetProfile(ctx, handle)
	if err != nil {
		t.Fatalf("免登录 GetProfile 失败（%s）: %v", social.KindOf(err), err)
	}
	t.Logf("资料：%s（@%s），耗时 %s", acct.DisplayName, acct.Handle, time.Since(t0).Round(time.Millisecond))

	t1 := time.Now()
	page, err := a.ListPosts(ctx, handle, "")
	if err != nil {
		t.Fatalf("免登录 ListPosts 失败（%s）: %v", social.KindOf(err), err)
	}
	photos, carousels, videos, withTime := 0, 0, 0, 0
	for _, p := range page.Posts {
		photos += len(p.Photos)
		if !p.PostedAt.IsZero() {
			withTime++
		}
		switch {
		case len(p.Photos) > 1:
			carousels++
		case len(p.Photos) == 0:
			videos++
		}
	}
	t.Logf("第一页：%d 条 / %d 张照片（轮播 %d 条、无图 %d 条、有时间 %d 条），耗时 %s，游标 %q",
		len(page.Posts), photos, carousels, videos, withTime,
		time.Since(t1).Round(time.Millisecond), shortCursor(page.NextCursor))
	if len(page.Posts) == 0 {
		t.Fatal("一条帖子都没拿到")
	}
	first := page.Posts[0]
	t.Logf("最新一条：id=%s %s 时间 %s 正文 %q 首图 %dx%d",
		first.PostID, first.URL, first.PostedAt.Format(time.RFC3339),
		first.Caption, first.Photos[0].Width, first.Photos[0].Height)
}

func shortCursor(c string) string {
	if len(c) > 24 {
		return c[:24] + "…"
	}
	return c
}

// TestLiveCookieShape 顺手检查用户贴进来的 Cookie 长什么样（不联网）：
// 缺 sessionid 直接报错，Netscape 文件里别的站的 Cookie 要丢掉。
func TestLiveCookieShape(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("LIVE_IG_COOKIE"))
	if raw == "" {
		t.Skip("设置 LIVE_IG_COOKIE=<cookies.txt 内容或路径> 才跑")
	}
	// 允许直接给路径（跟 TestLiveInstagram 用法一致）。
	if b, err := os.ReadFile(raw); err == nil {
		raw = string(b)
	}
	c, ok := ParseCredentials(raw)
	if !ok {
		t.Fatalf("这个 Cookie 里没有 sessionid")
	}
	t.Logf("sessionid=%d 字节 csrftoken=%v ds_user_id=%s cookies=%d 项",
		len(c.SessionID), c.CSRFToken != "", c.UserID, len(c.Cookies))
}
