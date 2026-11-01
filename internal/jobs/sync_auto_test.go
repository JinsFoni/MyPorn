package jobs

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// listAdapter 只实现同步需要的分页接口，固定返回给定的影片列表。
type listAdapter struct {
	fakeAdapter
	items []model.Video
	pages []int // 被请求过的页码，用来盯「从第几页开始扫」
}

func (l *listAdapter) ListPerformerVideos(ctx context.Context, id string, page int) (*platform.Page, error) {
	l.pages = append(l.pages, page)
	if page > 1 {
		return &platform.Page{Page: page, HasMore: false, TotalPages: 1}, nil
	}
	return &platform.Page{Page: 1, Items: l.items, HasMore: false, TotalPages: 1}, nil
}

type syncFixture struct {
	st    *store.Store
	r     *Runner
	ad    *listAdapter
	subID int64
	perf  int64
}

func newSyncFixture(t *testing.T, items []model.Video) *syncFixture {
	t.Helper()
	return newSyncFixtureWith(t, &listAdapter{items: items})
}

// newSyncFixtureWith 允许换成别的假适配器（比如专门模拟限流的那个）。
func newSyncFixtureWith(t *testing.T, list platform.Adapter) *syncFixture {
	t.Helper()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "sync.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	perfID, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		URL: "https://example.invalid/model/mini-diva",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateLibrary(&model.Library{
		Name: "主库", RootPath: filepath.Join(tmp, "media"), Enabled: true,
		IsDefault: true, UploadPolicy: model.PolicyLocal, NamingTemplate: "{title}",
	}); err != nil {
		t.Fatal(err)
	}
	subID, err := st.CreateSubscription(perfID, 0)
	if err != nil {
		t.Fatal(err)
	}

	// 只有走默认 fixture 的用例需要断言「请求过哪些页」，其余给 nil 就行。
	ad, _ := list.(*listAdapter)
	reg := platform.NewRegistry()
	reg.Register(list)
	cfg := &config.Config{MaxDownload: 2, SyncHours: 6}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := NewRunner(cfg, st, reg, nil, nil, nil, events.NewHub(), log, imgcache.New(t.TempDir(), nil, "", log))
	return &syncFixture{st: st, r: runner, ad: ad, subID: subID, perf: perfID}
}

func (f *syncFixture) runSync(t *testing.T, mode string) {
	t.Helper()
	ctx := context.Background()
	job := &model.Job{ID: 1, Type: model.JobTypeSync}
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: f.st, hub: events.NewHub(),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)), job: job}
	if err := f.r.syncSubscription(ctx, ctl, syncPayload{SubscriptionID: f.subID, Mode: mode}); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
}

func sampleVideos() []model.Video {
	return []model.Video{
		{Platform: "pornhub", PlatformVideoID: "aaa", Title: "片 A", DurationSec: 600},
		{Platform: "pornhub", PlatformVideoID: "bbb", Title: "片 B", DurationSec: 700},
	}
}

// 默认（没设开关）：同步只入库，绝不自动下载。
func TestSyncDoesNotAutoDownloadByDefault(t *testing.T) {
	f := newSyncFixture(t, sampleVideos())
	_ = f.st.MarkBackfillProgress(f.subID, 1, nil, true) // 视为已完成首次全量回填

	f.runSync(t, "incremental")

	if n := countVideoRows(t, f.st); n != 2 {
		t.Fatalf("应入库 2 部，实际 %d", n)
	}
	if jobs := listDownloadJobs(t, f.st); len(jobs) != 0 {
		t.Fatalf("默认不该自动下载，却排了 %d 个任务", len(jobs))
	}
}

// 开关打开：增量同步到的新片自动进下载队列（进默认库）。
func TestSyncAutoDownloadsWhenEnabled(t *testing.T) {
	f := newSyncFixture(t, sampleVideos())
	_ = f.st.MarkBackfillProgress(f.subID, 1, nil, true)
	if err := f.st.SetSetting("auto_download_new", "1"); err != nil {
		t.Fatal(err)
	}

	f.runSync(t, "incremental")

	jobs := listDownloadJobs(t, f.st)
	if len(jobs) != 2 {
		t.Fatalf("应自动排队 2 个下载任务，实际 %d", len(jobs))
	}
	want := map[int64]bool{}
	for _, v := range mustVideos(t, f.st) {
		want[v.ID] = true
	}
	for _, j := range jobs {
		if !want[j.TargetID] {
			t.Fatalf("任务目标 %d 不是新片", j.TargetID)
		}
	}
}

// 首次全量回填：即使开关打开也不自动下载（避免一订阅就下几百部）。
func TestSyncBackfillNeverAutoDownloads(t *testing.T) {
	f := newSyncFixture(t, sampleVideos())
	if err := f.st.SetSetting("auto_download_new", "1"); err != nil {
		t.Fatal(err)
	}

	f.runSync(t, "full")

	if n := countVideoRows(t, f.st); n != 2 {
		t.Fatalf("回填应入库 2 部，实际 %d", n)
	}
	if jobs := listDownloadJobs(t, f.st); len(jobs) != 0 {
		t.Fatalf("全量回填不该自动下载，却排了 %d 个任务", len(jobs))
	}
}

func listDownloadJobs(t *testing.T, st *store.Store) []*model.Job {
	t.Helper()
	jobs, err := st.ListJobs(model.JobTypeDownload, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

func countVideoRows(t *testing.T, st *store.Store) int {
	t.Helper()
	return len(mustVideos(t, st))
}

func mustVideos(t *testing.T, st *store.Store) []*model.Video {
	t.Helper()
	vids, _, err := st.ListFeed(store.FeedQuery{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return vids
}

// 回填已经完成过，再点「完整同步」要从第 1 页重扫：从断点页续跑会落在末页之外
// （单页创作者上表现成「同步完成，新增 0 部」），而用户点它的意思就是「从头再扫一遍」。
func TestFullSyncRestartsWhenBackfillDone(t *testing.T) {
	f := newSyncFixture(t, sampleVideos())
	if err := f.st.MarkBackfillProgress(f.subID, 1, nil, true); err != nil {
		t.Fatal(err)
	}

	f.runSync(t, "full")

	if len(f.ad.pages) == 0 || f.ad.pages[0] != 1 {
		t.Fatalf("应从第 1 页开始重扫，实际请求了 %v", f.ad.pages)
	}
}

// 上次回填被打断（还没标记完成）：仍然从断点下一页续，不把前面几百页重扫一遍。
func TestFullSyncResumesInterruptedBackfill(t *testing.T) {
	f := newSyncFixture(t, sampleVideos())
	if err := f.st.MarkBackfillProgress(f.subID, 3, nil, false); err != nil {
		t.Fatal(err)
	}

	f.runSync(t, "full")

	if len(f.ad.pages) == 0 || f.ad.pages[0] != 4 {
		t.Fatalf("应从断点下一页（第 4 页）续跑，实际请求了 %v", f.ad.pages)
	}
}
