package igram

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// TestLiveInstagramAnon 是可选的真实网络冒烟测试（LIVE=1 才跑）。
//
//	LIVE=1 LIVE_IG_HANDLE=kaedekaren0825 go test ./internal/social/igram -run TestLiveInstagramAnon -v
//
// 走的正是产品里唯一的那条路：爬虫页拿资料 + 免登录网格拿最新 ≤100 条 + 轮播帖走单帖详情
// 补子图。不打库、不下载图片，只数条数。
func TestLiveInstagramAnon(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("设置 LIVE=1 才跑真实网络测试")
	}
	handle := os.Getenv("LIVE_IG_HANDLE")
	if handle == "" {
		handle = "natgeo"
	}
	a := New(nil)
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
