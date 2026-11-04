package store

import (
	"testing"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// B 站独立页面的取数：UP 主列表要带本地收录数 / 未看数 / 已落盘数，
// 而且「取消订阅」后必须从墙上和管理页一起消失（软删）。
func TestListBiliChannels(t *testing.T) {
	st := newTestStore(t)
	biliID := mustPerformer(t, st, "bilibili", "946974", "影视飓风")
	pornID := mustPerformer(t, st, "pornhub", "mini-diva", "Mini Diva")
	if _, err := st.CreateSubscription(biliID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSubscription(pornID, 0); err != nil {
		t.Fatal(err)
	}

	// 三条投稿：两条没看过、一条看过且已落盘。
	seen := mustVideo(t, st, "bilibili", "BV1seen", biliID)
	newA := mustVideo(t, st, "bilibili", "BV1newa", biliID)
	newB := mustVideo(t, st, "bilibili", "BV1newb", biliID)
	if err := st.MarkVideoSeen(seen); err != nil {
		t.Fatal(err)
	}
	libID := mustLibrary(t, st)
	if err := st.SaveLibraryFile(&model.LibraryFile{
		VideoID: seen, LibraryID: libID, RelDir: "bilibili", VideoName: "a.mp4", VideoPath: "/tmp/a.mp4",
	}); err != nil {
		t.Fatal(err)
	}
	// 别人的片子不该被算进这位 UP 主
	mustVideo(t, st, "pornhub", "ph-1", pornID)

	channels, err := st.ListBiliChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("只该列出 B 站那一位，实际 %d 位", len(channels))
	}
	c := channels[0]
	if c.PlatformID != "946974" || c.Name != "影视飓风" {
		t.Fatalf("UP 主认错了：%+v", c)
	}
	if c.VideoCount != 3 || c.UnseenCount != 2 || c.Downloaded != 1 {
		t.Fatalf("计数不对：收录 %d 未看 %d 已落盘 %d（期望 3/2/1）", c.VideoCount, c.UnseenCount, c.Downloaded)
	}

	// 软删订阅之后，墙上不该再出现这位 UP 主。
	sub, err := st.GetSubscriptionByPerformer(biliID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SoftDeleteSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	after, err := st.ListBiliChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("取消订阅后还列出来 %d 位", len(after))
	}
	_ = newA
	_ = newB
}

// 未看计数与「标已看」的作用域：按 UP 主标只影响他自己。
func TestMarkBiliSeenScoped(t *testing.T) {
	st := newTestStore(t)
	a := mustPerformer(t, st, "bilibili", "1", "甲")
	b := mustPerformer(t, st, "bilibili", "2", "乙")
	// 两位都订阅着：未看口径只数「还在订阅里」的 UP 主（见 liveSubFilter）。
	if _, err := st.CreateSubscription(a, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSubscription(b, 0); err != nil {
		t.Fatal(err)
	}
	mustVideo(t, st, "bilibili", "BV1", a)
	mustVideo(t, st, "bilibili", "BV2", b)
	mustVideo(t, st, "bilibili", "BV3", b)

	if n, err := st.CountBiliUnseen(); err != nil || n != 3 {
		t.Fatalf("未看应为 3，实际 %d（err=%v）", n, err)
	}
	left, err := st.MarkBiliSeen(a)
	if err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Fatalf("只标甲之后应剩 2 条未看，实际 %d", left)
	}
	left, err = st.MarkBiliSeen(0)
	if err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("全站标已看之后应剩 0，实际 %d", left)
	}
}

// 「关注」流要把 B 站和 YouTube 都摘掉，B 站墙上则只看得到 B 站。
func TestFeedPlatformFilter(t *testing.T) {
	st := newTestStore(t)
	bili := mustPerformer(t, st, "bilibili", "946974", "影视飓风")
	ph := mustPerformer(t, st, "pornhub", "mini-diva", "Mini Diva")
	mustVideo(t, st, "bilibili", "BV1", bili)
	mustVideo(t, st, "pornhub", "ph-1", ph)
	// YouTube 的 videos 行是下载时才建的，媒体库里删掉下载之后行还在（2026-11-03 关注页
	// 因此冒回 5 条：id 1/2/3/15/60）。关注流固定不收 YouTube，这条守着它。
	if _, _, err := st.UpsertVideo(&model.Video{
		Platform: "youtube", PlatformVideoID: "yt-1", Title: "yt-1", URL: "https://example.invalid/yt-1",
	}); err != nil {
		t.Fatal(err)
	}

	feed, _, err := st.ListFeed(FeedQuery{ExcludePlatforms: FeedExcludedPlatforms, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed) != 1 || feed[0].Platform != "pornhub" {
		t.Fatalf("关注流里不该有 B 站/YouTube：%+v", feed)
	}
	// 侧边栏角标走的是另一个查询，口径必须一样（否则角标数得出来、流里看不到）
	if n, err := st.CountNewVideos(); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("关注流新片计数该只算 pornhub 那 1 条，实际 %d", n)
	}

	// krx18 也一样：2026-11-04 用户要求「关注页不要显示 krx18 的视频」。
	// 这里守两件事：① 排除名单里有它；② CountNewVideos 跟着同一份名单走
	// （它原来自己抄了份 'bilibili','youtube' 字面量，正是这么漏的）。
	if _, _, err := st.UpsertVideo(&model.Video{
		Platform: "krx18", PlatformVideoID: "office-wifes-affair-2026",
		Title: "office-wifes-affair-2026", URL: "https://krx18.com/movies/office-wifes-affair-2026/",
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountNewVideos(); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("krx18 的新片不该算进关注流角标，实际 %d", n)
	}
	// 但指定了艺人就不能排：艺人页问的是「这个人的片子」，答案不该因为出自哪个站而变空
	// （实测 /performer/147 是 B 站 UP 主、/performer/152 是 krx18 tag，两个都曾经是 0 条）。
	krxPerformer := mustPerformer(t, st, "krx18", "do-hee-도희", "Do Hee (도희)")
	krxVideo, _, err := st.UpsertVideo(&model.Video{
		Platform: "krx18", PlatformVideoID: "taste-of-swapping-2",
		Title: "taste-of-swapping-2", URL: "https://krx18.com/movies/taste-of-swapping-2/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkVideoPerformer(krxVideo, krxPerformer); err != nil {
		t.Fatal(err)
	}
	scoped, _, err := st.ListFeed(FeedQuery{PerformerID: krxPerformer, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].Platform != "krx18" {
		t.Fatalf("艺人页（带 performerId）不该排 krx18：%+v", scoped)
	}

	wall, _, err := st.ListFeed(FeedQuery{Platform: "bilibili", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(wall) != 1 || wall[0].Platform != "bilibili" {
		t.Fatalf("B 站墙上只该有 B 站：%+v", wall)
	}

	// 通用列表（艺人页 / 关注管理）同样不含 B 站
	if performers, _, err := st.ListPerformers(false, 0, 50); err != nil {
		t.Fatal(err)
	} else {
		for _, p := range performers {
			if p.Platform == "bilibili" {
				t.Fatalf("艺人列表里混进了 B 站：%s", p.Name)
			}
		}
	}
	if subs, err := st.ListSubscriptions(); err != nil {
		t.Fatal(err)
	} else if len(subs) != 0 {
		t.Fatalf("关注管理里不该有 B 站（当前没订阅任何人），实际 %d 条", len(subs))
	}
}

// BiliStats 是管理页顶部读数：UP 主数、收录数、未看数、已落盘数、出错数。
func TestBiliStats(t *testing.T) {
	st := newTestStore(t)
	a := mustPerformer(t, st, "bilibili", "1", "甲")
	b := mustPerformer(t, st, "bilibili", "2", "乙")
	mustPerformer(t, st, "bilibili", "3", "丙") // 没订阅，不该进统计
	subA, err := st.CreateSubscription(a, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSubscription(b, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE subscriptions SET last_error = '上游限流：B 站限流了，等一会儿再同步' WHERE id = ?`, subA); err != nil {
		t.Fatal(err)
	}
	v := mustVideo(t, st, "bilibili", "BV1", a)
	libID := mustLibrary(t, st)
	if err := st.SaveLibraryFile(&model.LibraryFile{
		VideoID: v, LibraryID: libID, VideoPath: "/tmp/x.mp4", VideoName: "x.mp4",
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := st.BiliStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Channels != 2 || stats.Videos != 1 || stats.Unseen != 1 || stats.Downloaded != 1 || stats.Errors != 1 {
		t.Fatalf("统计不对：%+v", stats)
	}

	// 未看片单供「下载未看」批量排队
	ids, err := st.ListUnseenVideoIDs("bilibili", a, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != v {
		t.Fatalf("未看片单不对：%v", ids)
	}
}

// 取消订阅（软删）之后投稿要跟着下墙：墙、未看角标、管理页读数、下载未看片单全都要同口径，
// 但库里那条影片和已落盘的文件不能动（重新订阅要能原样回来）。
func TestUnsubscribedUpVideosLeaveWall(t *testing.T) {
	st := newTestStore(t)
	gone := mustPerformer(t, st, "bilibili", "946974", "影视飓风")
	keep := mustPerformer(t, st, "bilibili", "372397103", "dprprince")
	goneSub, err := st.CreateSubscription(gone, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSubscription(keep, 0); err != nil {
		t.Fatal(err)
	}
	vGone := mustVideo(t, st, "bilibili", "BV1gone", gone)
	mustVideo(t, st, "bilibili", "BV1keep", keep)
	// 已落盘一条：取消订阅不该把文件或库记录带走
	libID := mustLibrary(t, st)
	if err := st.SaveLibraryFile(&model.LibraryFile{
		VideoID: vGone, LibraryID: libID, VideoPath: "/tmp/gone.mp4", VideoName: "gone.mp4",
	}); err != nil {
		t.Fatal(err)
	}

	wall := func() []*model.Video {
		t.Helper()
		items, _, err := st.ListFeed(FeedQuery{Platform: "bilibili", SubscribedOnly: true, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	if n := len(wall()); n != 2 {
		t.Fatalf("订阅都在时墙上该有 2 条，实际 %d", n)
	}
	if n, _ := st.CountBiliUnseen(); n != 2 {
		t.Fatalf("订阅都在时未看该是 2，实际 %d", n)
	}

	if err := st.SoftDeleteSubscription(goneSub); err != nil {
		t.Fatal(err)
	}

	if items := wall(); len(items) != 1 || items[0].PlatformVideoID != "BV1keep" {
		t.Fatalf("取消订阅后墙上只剩另一位 UP 主的，实际 %+v", items)
	}
	if n, err := st.CountBiliUnseen(); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("未看角标该跟着降到 1，实际 %d", n)
	}
	stats, err := st.BiliStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Channels != 1 || stats.Videos != 1 || stats.Unseen != 1 || stats.Downloaded != 0 {
		t.Fatalf("管理页读数没跟着降：%+v", stats)
	}
	ids, err := st.ListUnseenVideoIDs("bilibili", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("「下载未看」不该再排上已取消 UP 主的投稿，实际 %v", ids)
	}

	// 数据本身还在：影片行、已落盘的文件都没被删。
	files, err := st.ListLibraryFilesByVideo(vGone)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("已落盘的文件不该因为取消订阅消失，实际 %d 条", len(files))
	}

	// 重新订阅 → 原样回来（这就是软删而不是硬删的理由）。
	if _, err := st.CreateSubscription(gone, 0); err != nil {
		t.Fatal(err)
	}
	if n := len(wall()); n != 2 {
		t.Fatalf("重新订阅后该回到 2 条，实际 %d", n)
	}
	if n, _ := st.CountBiliUnseen(); n != 2 {
		t.Fatalf("重新订阅后未看该回到 2，实际 %d", n)
	}
}

// ---- 小工具 ----

func mustPerformer(t *testing.T, st *Store, platform, pid, name string) int64 {
	t.Helper()
	id, err := st.UpsertPerformer(&model.Performer{
		Platform: platform, PerformerID: pid, Name: name,
		URL: "https://example.invalid/" + pid,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// 一个 UP 主都没有时读数必须是 0，不能报错：SUM(...) 在空集上返回 NULL，
// 扫进 int 会「converting NULL to int is unsupported」，管理页就会显示「读取统计失败」。
func TestBiliStatsEmpty(t *testing.T) {
	st := newTestStore(t)
	stats, err := st.BiliStats()
	if err != nil {
		t.Fatalf("空库取读数不该报错：%v", err)
	}
	if stats != (BiliStats{}) {
		t.Fatalf("空库读数该全是 0，实际 %+v", stats)
	}
	// 有 UP 主但没订阅（投稿还在库里）：同样要能取，且影片数为 0。
	up := mustPerformer(t, st, "bilibili", "946974", "影视飓风")
	mustVideo(t, st, "bilibili", "BV1", up)
	stats, err = st.BiliStats()
	if err != nil {
		t.Fatalf("有投稿但没订阅时取读数不该报错：%v", err)
	}
	if stats.Channels != 0 || stats.Videos != 0 || stats.Unseen != 0 {
		t.Fatalf("没订阅时读数该全是 0，实际 %+v", stats)
	}
}

func mustLibrary(t *testing.T, st *Store) int64 {
	t.Helper()
	id, err := st.CreateLibrary(&model.Library{Name: "主库", RootPath: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustVideo(t *testing.T, st *Store, platform, pid string, performerID int64) int64 {
	t.Helper()
	v, _, err := st.UpsertVideo(&model.Video{
		Platform: platform, PlatformVideoID: pid, Title: pid,
		URL: "https://example.invalid/" + pid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkVideoPerformer(v, performerID); err != nil {
		t.Fatal(err)
	}
	return v
}
