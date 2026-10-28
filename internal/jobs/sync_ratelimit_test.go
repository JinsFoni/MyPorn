package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/mario/myporn/internal/events"
	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
)

// rateLimitedAdapter 前 failOn-1 页正常给片，到 failOn 页返回限流。
type rateLimitedAdapter struct {
	fakeAdapter
	items  []model.Video
	failOn int
	pages  []int
}

func (a *rateLimitedAdapter) ListPerformerVideos(ctx context.Context, id string, page int) (*platform.Page, error) {
	a.pages = append(a.pages, page)
	if page == a.failOn {
		return nil, fmt.Errorf("%w：B 站回 -799 请求过于频繁", platform.ErrRateLimited)
	}
	if page > 1 {
		return &platform.Page{Page: page, HasMore: false, TotalPages: 2}, nil
	}
	return &platform.Page{Page: 1, Items: a.items, HasMore: true, TotalPages: 2}, nil
}

func runSyncAllowErr(t *testing.T, f *syncFixture, mode string) error {
	t.Helper()
	ctx := context.Background()
	job := &model.Job{ID: 1, Type: model.JobTypeSync}
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: f.st, hub: events.NewHub(),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), job: job}
	return f.r.syncSubscription(ctx, ctl, syncPayload{SubscriptionID: f.subID, Mode: mode})
}

// 翻页途中被限流：不算失败，已抓的照常入库，断点留在第 1 页。
//
// 这是 B 站列表接口的日常（/x/space/arc/search 限流极紧）。当失败处理的话，
// 一个几百部投稿的 UP 主要天天红一次、而且回填永远停在原地。
func TestSyncRateLimitedMidwayKeepsProgress(t *testing.T) {
	ad := &rateLimitedAdapter{items: sampleVideos(), failOn: 2}
	f := newSyncFixtureWith(t, ad)

	if err := runSyncAllowErr(t, f, "auto"); err != nil {
		t.Fatalf("翻页被限流不该当失败: %v", err)
	}
	if n := countVideoRows(t, f.st); n != 2 {
		t.Fatalf("第 1 页的两部应当入库，实际 %d 部", n)
	}
	sub, err := f.st.GetSubscription(f.subID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.BackfillDone {
		t.Error("限流中断不该标记回填完成")
	}
	if sub.BackfillPage != 1 {
		t.Errorf("断点 = %d，期望停在第 1 页", sub.BackfillPage)
	}
	if got := fmt.Sprint(ad.pages); got != "[1 2]" {
		t.Errorf("请求过的页 = %s，期望 [1 2]", got)
	}
}

// 第 1 页就被限流 = 这一轮什么都没拿到，仍然要报失败，否则订阅会静悄悄地不动。
func TestSyncRateLimitedOnFirstPageFails(t *testing.T) {
	ad := &rateLimitedAdapter{items: sampleVideos(), failOn: 1}
	f := newSyncFixtureWith(t, ad)

	err := runSyncAllowErr(t, f, "auto")
	if err == nil {
		t.Fatal("第 1 页被限流应当报错")
	}
	if !errors.Is(err, platform.ErrRateLimited) {
		t.Fatalf("错误应当能被 errors.Is 认成限流: %v", err)
	}
	if n := countVideoRows(t, f.st); n != 0 {
		t.Fatalf("不该入库任何影片，实际 %d 部", n)
	}
}
