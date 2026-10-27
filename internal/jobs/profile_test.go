package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/store"
)

// stubProfileSource 是假的资料来源：按名字返回固定的一份资料，并记下被问了几次。
type stubProfileSource struct {
	key   string
	name  string // 只有这个名字才对得上（模拟「名字对不上不给」）
	prof  *model.Performer
	calls int
}

func (s *stubProfileSource) Key() string { return s.key }

func (s *stubProfileSource) ResolveProfile(_ context.Context, name string) (*model.Performer, error) {
	s.calls++
	if name != s.name {
		return nil, errors.New("名字对不上")
	}
	return s.prof, nil
}

func newProfileFixture(t *testing.T) (*store.Store, *ProfileFiller, *stubProfileSource, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "profile.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	four8, subs := 48, 1200
	perfID, err := st.UpsertPerformer(&model.Performer{
		Platform: "beeg", PerformerID: "MiniDiva", Name: "Mini Diva",
		URL:             "https://beeg.com/MiniDiva",
		AvatarURL:       "https://externulls.example/minidiva.jpg",
		Bio:             "beeg 那句很短的简介",
		VideoCount:      &four8,
		SubscriberCount: &subs,
	})
	if err != nil {
		t.Fatal(err)
	}

	src := &stubProfileSource{key: "pornhub", name: "Mini Diva", prof: &model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		Bio:     "Pornhub 上的完整简介",
		Profile: []model.ProfileField{{Label: "性别", Value: "女性"}, {Label: "出身地", Value: "Czech Republic"}},
		Body:    model.Body{HeightCm: 167, Bust: 89, Waist: 64, Hips: 91},
		Social:  []model.SocialLink{{Label: "Twitter", URL: "https://twitter.com/minidivaonly"}},
		Rank:    269,
	}}
	reg := platform.NewProfileRegistry()
	reg.Register(src)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return st, NewProfileFiller(st, reg, log), src, perfID
}

// 借资料只换内容，本站的平台标识、主页链接、头像与平台统计都要留着。
func TestProfileFillerAdoptsContentKeepsPlatformStats(t *testing.T) {
	st, filler, src, perfID := newProfileFixture(t)

	p, err := st.GetPerformer(perfID)
	if err != nil {
		t.Fatal(err)
	}
	if !filler.Fill(context.Background(), p) {
		t.Fatal("应该借到资料")
	}
	if src.calls != 1 {
		t.Errorf("应该只问一次资料源，问了 %d 次", src.calls)
	}

	got, err := st.GetPerformer(perfID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bio != "Pornhub 上的完整简介" {
		t.Errorf("简介没换：%q", got.Bio)
	}
	if len(got.Profile) != 2 || got.Rank != 269 || got.Body.HeightCm != 167 {
		t.Errorf("资料区块没换：%+v rank=%d body=%+v", got.Profile, got.Rank, got.Body)
	}
	if len(got.Social) != 1 || got.Social[0].Label != "Twitter" {
		t.Errorf("社交链接没换：%+v", got.Social)
	}
	// 本站的东西
	if got.Platform != "beeg" || got.Name != "Mini Diva" || got.URL != "https://beeg.com/MiniDiva" {
		t.Errorf("本站标识被改了：%s %q %s", got.Platform, got.Name, got.URL)
	}
	if got.AvatarURL != "https://externulls.example/minidiva.jpg" {
		t.Errorf("本站头像应保留：%q", got.AvatarURL)
	}
	if got.VideoCount == nil || *got.VideoCount != 48 || got.SubscriberCount == nil || *got.SubscriberCount != 1200 {
		t.Errorf("平台统计被改了：%v / %v", got.VideoCount, got.SubscriberCount)
	}

	// 再跑一次：资料没变化就不该再写库。
	if filler.Fill(context.Background(), got) {
		t.Error("资料没变时不该再写一次")
	}
}

// 名字对不上就什么都不做，别给艺人安别人的简介。
func TestProfileFillerSkipsWhenNameMismatch(t *testing.T) {
	st, filler, src, perfID := newProfileFixture(t)
	src.name = "别的人"

	p, _ := st.GetPerformer(perfID)
	if filler.Fill(context.Background(), p) {
		t.Fatal("名字对不上不该借")
	}
	got, _ := st.GetPerformer(perfID)
	if got.Bio != "beeg 那句很短的简介" {
		t.Errorf("简介被改掉了：%q", got.Bio)
	}
}

// Pornhub 自己的艺人不用再去 Pornhub 借资料（同源的直接跳过，连请求都不发）。
func TestProfileFillerSkipsSamePlatform(t *testing.T) {
	st, filler, src, _ := newProfileFixture(t)
	id, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		Bio: "原生简介",
	})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetPerformer(id)
	if filler.Fill(context.Background(), p) {
		t.Fatal("同平台不该借")
	}
	if src.calls != 0 {
		t.Errorf("同平台不该发请求，问了 %d 次", src.calls)
	}
}
