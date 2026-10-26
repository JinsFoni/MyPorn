package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mario/myporn/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("二次打开应成功: %v", err)
	}
	defer st2.Close()
}

func TestUpsertPerformerAndSubscription(t *testing.T) {
	st := newTestStore(t)
	id, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		URL: "https://cn.pornhub.com/model/mini-diva", AvatarURL: "https://x/y.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 幂等：同平台同 id 不应新增
	id2, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva 2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != id2 {
		t.Fatalf("幂等失败: %d != %d", id, id2)
	}
	p, err := st.GetPerformer(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Mini Diva 2" {
		t.Errorf("名称未更新: %q", p.Name)
	}
	if p.AvatarURL != "https://x/y.jpg" {
		t.Errorf("空字段不应覆盖旧值: %q", p.AvatarURL)
	}

	subID, err := st.CreateSubscription(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := st.GetSubscriptionByPerformer(id)
	if err != nil {
		t.Fatal(err)
	}
	if sub.ID != subID || sub.Enabled != true || sub.BackfillDone {
		t.Fatalf("订阅状态异常: %+v", sub)
	}

	// 软删除后应查不到，但重新订阅会恢复同一条记录
	if err := st.SoftDeleteSubscription(subID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSubscription(subID); err != ErrNotFound {
		t.Errorf("软删后应返回 NotFound, 得到 %v", err)
	}
	reID, err := st.CreateSubscription(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reID != subID {
		t.Errorf("恢复订阅应复用原 id: %d != %d", reID, subID)
	}
}

func TestPerformerProfileAndSocialRoundTrip(t *testing.T) {
	st := newTestStore(t)
	id, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		Profile: []model.ProfileField{
			{Label: "性别", Value: "女性"},
			{Label: "身高", Value: `5' 6" (167cm)`},
		},
		Social: []model.SocialLink{
			{Label: "Twitter", URL: "https://twitter.com/minidivaonly"},
			{Label: "官方网址", URL: "https://onlyfans.com/minidivaonly/c1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPerformer(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Profile) != 2 || p.Profile[1].Label != "身高" || p.Profile[1].Value != `5' 6" (167cm)` {
		t.Errorf("资料行未按顺序存取: %+v", p.Profile)
	}
	if len(p.Social) != 2 || p.Social[0].URL != "https://twitter.com/minidivaonly" {
		t.Errorf("站外链接未存取: %+v", p.Social)
	}

	// 页面这次没抓到资料（空数组）时，不能把库里已有的清空。
	if _, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
	}); err != nil {
		t.Fatal(err)
	}
	p, err = st.GetPerformer(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Profile) != 2 || len(p.Social) != 2 {
		t.Errorf("空资料不应覆盖旧值: profile=%+v social=%+v", p.Profile, p.Social)
	}

	// 真的抓到新内容时整体替换（顺序也换）。
	if _, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		Profile: []model.ProfileField{{Label: "性别", Value: "女性"}},
	}); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPerformer(id)
	if len(p.Profile) != 1 {
		t.Errorf("新资料应整体覆盖旧值，实际 %+v", p.Profile)
	}
}

// 身材：四项目存取要一致；这次没解析到（Body 全 0）时不能把库里的抹掉。
func TestPerformerBodyRoundTrip(t *testing.T) {
	st := newTestStore(t)
	id, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		Body: model.Body{HeightCm: 167, Bust: 89, Waist: 64, Hips: 91},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPerformer(id)
	if err != nil {
		t.Fatal(err)
	}
	want := model.Body{HeightCm: 167, Bust: 89, Waist: 64, Hips: 91}
	if p.Body != want {
		t.Errorf("身材未存取: %+v，期望 %+v", p.Body, want)
	}

	if _, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
	}); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPerformer(id)
	if p.Body != want {
		t.Errorf("空身材不应覆盖旧值，实际 %+v", p.Body)
	}

	// 只有身高时，三围归零（整组覆盖，不残留上一次的腰臀）。
	if _, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		Body: model.Body{HeightCm: 168},
	}); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPerformer(id)
	if p.Body != (model.Body{HeightCm: 168}) {
		t.Errorf("新身材应整体覆盖，实际 %+v", p.Body)
	}
}

func TestPerformerRankRoundTrip(t *testing.T) {
	st := newTestStore(t)
	id, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva", Rank: 269,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPerformer(id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Rank != 269 {
		t.Errorf("排名未存取: %d，期望 269", p.Rank)
	}

	// 这次没抓到排名（0）：保留库里的旧值，不要被清掉。
	if _, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
	}); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPerformer(id)
	if p.Rank != 269 {
		t.Errorf("空排名不应覆盖旧值，实际 %d", p.Rank)
	}

	// 排名变了就整体覆盖。
	if _, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva", Rank: 254,
	}); err != nil {
		t.Fatal(err)
	}
	p, _ = st.GetPerformer(id)
	if p.Rank != 254 {
		t.Errorf("新排名应覆盖旧值，实际 %d", p.Rank)
	}
}

func TestFeedPaginationByCursor(t *testing.T) {
	st := newTestStore(t)
	pid, _ := st.UpsertPerformer(&model.Performer{Platform: "pornhub", PerformerID: "p1", Name: "P1"})

	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		pub := base.Add(time.Duration(i) * time.Hour)
		vid, _, err := st.UpsertVideo(&model.Video{
			Platform: "pornhub", PlatformVideoID: "v" + string(rune('a'+i)),
			Title: "T", URL: "u", PublishedAt: &pub,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.LinkVideoPerformer(vid, pid); err != nil {
			t.Fatal(err)
		}
	}

	page1, next, err := st.ListFeed(FeedQuery{PerformerID: pid, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 {
		t.Fatalf("第一页应有 2 条, 得到 %d", len(page1))
	}
	if next == "" {
		t.Fatal("应有 nextCursor")
	}
	// 必须按发布时间倒序
	if page1[0].PublishedAt.Before(*page1[1].PublishedAt) {
		t.Error("排序应为发布时间倒序")
	}

	page2, next2, err := st.ListFeed(FeedQuery{PerformerID: pid, Limit: 2, Cursor: next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || next2 == "" {
		t.Fatalf("第二页异常: %d, cursor=%q", len(page2), next2)
	}
	page3, next3, err := st.ListFeed(FeedQuery{PerformerID: pid, Limit: 2, Cursor: next2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3) != 1 {
		t.Fatalf("第三页应有 1 条, 得到 %d", len(page3))
	}
	if next3 != "" {
		t.Errorf("最后一页不应有 cursor: %q", next3)
	}

	// 全部 5 条不重复
	seen := map[int64]bool{}
	for _, v := range append(append(page1, page2...), page3...) {
		if seen[v.ID] {
			t.Errorf("游标分页出现重复: %d", v.ID)
		}
		seen[v.ID] = true
	}
	if len(seen) != 5 {
		t.Errorf("应覆盖 5 条, 实际 %d", len(seen))
	}
}

func TestJobLifecycle(t *testing.T) {
	st := newTestStore(t)
	id, err := st.Enqueue(model.JobTypeSync, map[string]any{"subscriptionId": 7, "mode": "full"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	active, err := st.HasActiveSyncJob(7)
	if err != nil || !active {
		t.Fatalf("应检测到活跃任务: %v %v", active, err)
	}
	j, err := st.Claim(model.JobTypeSync)
	if err != nil || j == nil {
		t.Fatalf("领取失败: %v %v", j, err)
	}
	if j.ID != id || j.Status != model.JobRunning {
		t.Fatalf("任务状态异常: %+v", j)
	}
	// 已领取的任务不应被再次领取
	if again, _ := st.Claim(model.JobTypeSync); again != nil {
		t.Error("同一任务被重复领取")
	}
	if err := st.FinishJob(id, model.JobDone, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetJob(id)
	if got.Status != model.JobDone || got.FinishedAt == nil {
		t.Errorf("完成任务状态异常: %+v", got)
	}
}

func TestResetRunningJobsOnRestart(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.Enqueue(model.JobTypeSync, map[string]any{"subscriptionId": 1}, 100); err != nil {
		t.Fatal(err)
	}
	if j, _ := st.Claim(model.JobTypeSync); j == nil {
		t.Fatal("领取失败")
	}
	n, err := st.ResetRunningJobs()
	if err != nil || n != 1 {
		t.Fatalf("应重置 1 个任务, 得到 %d (%v)", n, err)
	}
	if j, _ := st.Claim(model.JobTypeSync); j == nil {
		t.Error("重置后应可再次领取")
	}
}

// 回归：fetched_at 曾经用 SQLite 的 datetime('now') 写入，却只按 RFC3339 解析，
// 解析失败得到零值时间，于是「5 分钟清晰度缓存」永远算作过期，每次播放都要重新解析。
func TestVariantsCacheTimestampIsFresh(t *testing.T) {
	st := newTestStore(t)
	v, _, err := st.UpsertVideo(&model.Video{
		Platform:        "pornhub",
		PlatformVideoID: "abc123",
		Title:           "缓存测试",
		URL:             "https://example.invalid/view_video.php?viewkey=abc123",
	})
	if err != nil {
		t.Fatal(err)
	}
	vs := []model.Variant{{Height: 1080, Width: 1920, Bandwidth: 1353004, URL: "https://cdn.invalid/1080.mp4"}}
	if err := st.SaveVariants(v, vs); err != nil {
		t.Fatal(err)
	}
	got, fetchedAt, err := st.GetVariants(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("期望 1 条，得到 %d", len(got))
	}
	if fetchedAt.IsZero() {
		t.Fatal("fetched_at 解析成了零值时间，缓存会永远失效")
	}
	if age := time.Since(fetchedAt); age < 0 || age > time.Minute {
		t.Fatalf("fetched_at 应该就在刚才，实际差 %v", age)
	}
}

// 快路径解析出来的码率是「猜的」，标记要跟着一起落库：下载体积预估要知道它不可信。
func TestVariantsKeepApproxBandwidthFlag(t *testing.T) {
	st := newTestStore(t)
	v, _, err := st.UpsertVideo(&model.Video{
		Platform:        "pornhub",
		PlatformVideoID: "approx1",
		Title:           "码率标记",
		URL:             "https://example.invalid/view_video.php?viewkey=approx1",
	})
	if err != nil {
		t.Fatal(err)
	}
	vs := []model.Variant{
		{Height: 1080, Width: 1920, Bandwidth: 4000000, URL: "https://cdn.invalid/a.mp4", ApproxBandwidth: true},
		{Height: 720, Width: 1280, Bandwidth: 787716, URL: "https://cdn.invalid/b.mp4"},
	}
	if err := st.SaveVariants(v, vs); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.GetVariants(v)
	if err != nil {
		t.Fatal(err)
	}
	flag := map[int]bool{}
	for _, x := range got {
		flag[x.Height] = x.ApproxBandwidth
	}
	if !flag[1080] {
		t.Fatal("1080p 的「码率是猜的」标记丢失了")
	}
	if flag[720] {
		t.Fatal("720p 本该是真实码率，别被标成猜的")
	}
}

func TestMustParseAcceptsSQLiteDatetime(t *testing.T) {
	if got := mustParse("2026-10-26 04:35:47"); got.IsZero() {
		t.Fatal("SQLite datetime('now') 格式应能解析")
	}
	if got := mustParse("2026-10-26T04:35:47Z"); got.IsZero() {
		t.Fatal("RFC3339 格式应能解析")
	}
	if got := mustParse("bogus"); !got.IsZero() {
		t.Fatal("无法解析时应返回零值")
	}
}

// 标签只在详情页里有：解析播放地址时顺手补进库里，只填空缺、不覆盖已有内容。
func TestFillVideoMetaFillsOnlyEmptyFields(t *testing.T) {
	st := newTestStore(t)
	v, _, err := st.UpsertVideo(&model.Video{
		Platform:        "pornhub",
		PlatformVideoID: "meta1",
		Title:           "元数据补齐",
		URL:             "https://example.invalid/view_video.php?viewkey=meta1",
		Description:     "列表页没有简介",
	})
	if err != nil {
		t.Fatal(err)
	}
	meta := &model.Video{
		Tags:        []string{"标签A", "标签B"},
		Description: "详情页简介",
		DurationSec: 300,
	}
	if err := st.FillVideoMeta(v, meta); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetVideo(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("标签应被补上，得到 %v", got.Tags)
	}
	if got.Description != "列表页没有简介" {
		t.Fatalf("已有简介不该被覆盖，得到 %q", got.Description)
	}
	if got.DurationSec != 300 {
		t.Fatalf("时长应补上，得到 %d", got.DurationSec)
	}

	// 再补一次，标签不该被清空或重复追加。
	if err := st.FillVideoMeta(v, &model.Video{Tags: []string{"标签C"}}); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.GetVideo(v); len(again.Tags) != 2 || again.Tags[0] != "标签A" {
		t.Fatalf("已有标签不该被覆盖，得到 %v", again.Tags)
	}
}

func TestListVideosMissingMeta(t *testing.T) {
	st := newTestStore(t)
	withTags, _, _ := st.UpsertVideo(&model.Video{Platform: "pornhub", PlatformVideoID: "has", Title: "有标签", URL: "u1",
		Tags: []string{"x"}})
	bare, _, _ := st.UpsertVideo(&model.Video{Platform: "pornhub", PlatformVideoID: "bare", Title: "没标签", URL: "u2"})
	items, err := st.ListVideosMissingMeta(10)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, it := range items {
		ids[it.ID] = true
	}
	if !ids[bare] || ids[withTags] {
		t.Fatalf("应只列出没有标签的影片，得到 %v (bare=%d has=%d)", ids, bare, withTags)
	}
}

func TestSetPerformerAvatarAndListMissing(t *testing.T) {
	st := newTestStore(t)
	withAvatar, err := st.UpsertPerformer(&model.Performer{Platform: "pornhub", PerformerID: "a", Name: "有头像",
		URL: "https://x/a", AvatarURL: "https://img/a.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := st.UpsertPerformer(&model.Performer{Platform: "pornhub", PerformerID: "b", Name: "没头像",
		URL: "https://x/b"})
	if err != nil {
		t.Fatal(err)
	}

	items, err := st.ListPerformersWithoutAvatar()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != bare {
		t.Fatalf("应只列出没头像的艺人，得到 %d 条", len(items))
	}

	if err := st.SetPerformerAvatar(bare, "https://img/b.jpg"); err != nil {
		t.Fatal(err)
	}
	p, err := st.GetPerformer(bare)
	if err != nil {
		t.Fatal(err)
	}
	if p.AvatarURL != "https://img/b.jpg" {
		t.Fatalf("头像没写进去: %q", p.AvatarURL)
	}
	// 其他字段不能被这次更新弄丢。
	if p.Name != "没头像" || p.URL != "https://x/b" {
		t.Fatalf("不该改到别的字段: %+v", p)
	}
	items, _ = st.ListPerformersWithoutAvatar()
	if len(items) != 0 {
		t.Fatalf("补完应当没有缺头像的了，还剩 %d 条", len(items))
	}
	// 已经有头像的不该被覆盖成空。
	if err := st.SetPerformerAvatar(withAvatar, ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetPerformer(withAvatar); p.AvatarURL != "" {
		t.Fatalf("SetPerformerAvatar 是直接覆盖语义，这里应为空: %q", p.AvatarURL)
	}
}
