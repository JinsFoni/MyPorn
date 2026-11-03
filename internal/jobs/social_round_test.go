package jobs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/social"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// fakeSocial 是可控的社交适配器：按 handle 决定「限流」还是「给一帖一图」。
type fakeSocial struct {
	rateLimited map[string]bool // handle → 这一轮报限流
	calls       []string        // 每次 ListPosts/Discover 的 handle，按顺序
	discovers   []string
	tr          *social.Throttle
}

func newFakeSocial() *fakeSocial {
	return &fakeSocial{rateLimited: map[string]bool{}, tr: social.NewThrottle(time.Millisecond)}
}

func (f *fakeSocial) Key() string  { return "x" }
func (f *fakeSocial) Name() string { return "X" }
func (f *fakeSocial) ParseProfileURL(raw string) (string, error) {
	return social.NormalizeHandle(raw), nil
}
func (f *fakeSocial) NeedsCookie() bool          { return false }
func (f *fakeSocial) Throttle() *social.Throttle { return f.tr }

func (f *fakeSocial) GetProfile(ctx context.Context, handle string) (*social.Account, error) {
	return &social.Account{Platform: "x", Handle: social.NormalizeHandle(handle), DisplayName: handle}, nil
}

func (f *fakeSocial) page(handle string) (*social.Page, error) {
	if f.rateLimited[handle] {
		return nil, social.RateLimited("widget 限流（测试）", time.Now().Add(time.Minute))
	}
	return &social.Page{Posts: []social.Post{{
		PostID:   "post-" + handle,
		URL:      "https://x.com/" + handle + "/status/" + handle,
		PostedAt: time.Now().UTC(),
		Photos:   []social.Photo{{URL: "https://pbs.twimg.com/media/" + handle + ".jpg", Width: 1080, Height: 1350, Seq: 1}},
	}}}, nil
}

func (f *fakeSocial) ListPosts(ctx context.Context, handle, cursor string) (*social.Page, error) {
	f.calls = append(f.calls, handle)
	return f.page(handle)
}

func (f *fakeSocial) Discover(ctx context.Context, handle string) (*social.Page, error) {
	f.discovers = append(f.discovers, handle)
	return f.page(handle)
}

func newSocialFixture(t *testing.T, handles ...string) (*Runner, *store.Store, *fakeSocial) {
	t.Helper()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "social.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	fs := newFakeSocial()
	reg := social.NewRegistry()
	reg.Register(fs)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRunner(&config.Config{MaxDownload: 1, SyncHours: 6}, st, platform.NewRegistry(), reg,
		nil, nil, events.NewHub(), log, imgcache.New(t.TempDir(), nil, "", log))

	for _, h := range handles {
		if _, err := st.UpsertSocialAccount(&store.SocialAccount{
			Platform: "x", Handle: h, DisplayName: h, ProfileURL: "https://x.com/" + h, Enabled: true,
		}); err != nil {
			t.Fatalf("建博主失败: %v", err)
		}
	}
	return r, st, fs
}

func runSocialRound(t *testing.T, r *Runner, st *store.Store, mode string) {
	t.Helper()
	payload, _ := json.Marshal(socialSyncPayload{AccountID: 0, Mode: mode})
	job := &model.Job{ID: 1, Type: model.JobTypeSocialSync, Payload: string(payload)}
	ctx := context.Background()
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: st, hub: events.NewHub(),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), job: job}
	if err := r.runSocialSyncJob(ctx, job, ctl); err != nil {
		t.Fatalf("整轮不该失败: %v", err)
	}
}

// 一个账号撞限流，不该让同一轮后面的账号全都跳过。
//
// 2026-11-04 之前 jobs/social.go 遇到 rate_limited 直接 return，整轮收工——而 X 的额度
// 是按宿主分的（widget / 主页 / 单帖各一份，§15.15），第一个账号撞上的往往只是它自己那份，
// 后面的账号换个宿主照样能抓。实测：widget 桶 429 那天，手动同步两个账号都成功
// （13 s / 16 s，各「新增 0」）。
func TestSocialRoundContinuesAfterRateLimit(t *testing.T) {
	r, st, fs := newSocialFixture(t, "first", "second")
	fs.rateLimited["first"] = true

	runSocialRound(t, r, st, "auto")

	// 顺序由 ListSocialAccounts 定（新加的在前），这里只关心「两个都抓了」。
	seen := map[string]bool{}
	for _, h := range fs.calls {
		seen[h] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("限流之后应该继续抓下一个账号，实际只抓了 %v", fs.calls)
	}
	ok, err := st.HasSocialPost("x", "post-second")
	if err != nil || !ok {
		t.Fatalf("第二个账号的帖子没入库: %v %v", ok, err)
	}
	// 撞限流那个账号要把原因留在行上，界面才看得见。
	acc, err := st.GetSocialAccountByHandle("x", "first")
	if err == nil && acc.LastError == "" {
		t.Error("限流账号应该留下 last_error")
	}
}

// discover 轮只跑支持 Discoverer 的适配器，且不该碰完整同步那套（不刷资料、不动回填游标）。
func TestSocialDiscoverRoundUsesDiscover(t *testing.T) {
	r, st, fs := newSocialFixture(t, "first", "second")

	runSocialRound(t, r, st, "discover")

	if len(fs.calls) != 0 {
		t.Errorf("discover 轮不该调 ListPosts，实际调了 %v", fs.calls)
	}
	if len(fs.discovers) != 2 {
		t.Fatalf("discover 轮应该逐个发现，实际 %v", fs.discovers)
	}
	ok, err := st.HasSocialPost("x", "post-first")
	if err != nil || !ok {
		t.Fatalf("发现轮的帖子没入库: %v %v", ok, err)
	}
}
