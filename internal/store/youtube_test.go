package store

import (
	"testing"
	"time"
)

// YouTube 订阅（M6）的存储层测试：这几条都对应实现时真实踩到的坑。

func ytTestChannel(id string) *YtChannel {
	return &YtChannel{
		ChannelID:  id,
		Title:      "T-Series",
		AvatarURL:  "https://yt3.googleusercontent.com/a.jpg",
		ProfileURL: "https://www.youtube.com/channel/" + id,
		Enabled:    true,
	}
}

// TestUpsertYtChannelWithoutPerformer 覆盖外键那条坑：performer_id 是外键，
// 直接写 0 会撞 FOREIGN KEY constraint；读回来时 NULL 也不能扫进 int64。
func TestUpsertYtChannelWithoutPerformer(t *testing.T) {
	st := newTestStore(t)

	id, created, err := st.UpsertYtChannel(ytTestChannel("UCq-Fj5jknLsUf-MWSy4_brA"))
	if err != nil {
		t.Fatalf("不关联艺人也应该能写入: %v", err)
	}
	if !created || id == 0 {
		t.Fatalf("首次写入应 created=true 且拿到 id，得到 created=%v id=%d", created, id)
	}

	got, err := st.GetYtChannel(id)
	if err != nil {
		t.Fatalf("读回频道失败: %v", err)
	}
	if got.PerformerID != 0 {
		t.Errorf("performer_id 应为 0（NULL），得到 %d", got.PerformerID)
	}
	if got.Title != "T-Series" || !got.Enabled {
		t.Errorf("字段没读对: %+v", got)
	}
}

// TestUpsertYtChannelKeepsToggle 重复添加不能把用户的开关推翻。
func TestUpsertYtChannelKeepsToggle(t *testing.T) {
	st := newTestStore(t)
	id, _, err := st.UpsertYtChannel(ytTestChannel("UCaaaaaaaaaaaaaaaaaaaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetYtChannelEnabled(id, false); err != nil {
		t.Fatal(err)
	}

	// 再添加一次（前端会这么干：重复粘同一个链接），并带上新的头像。
	c := ytTestChannel("UCaaaaaaaaaaaaaaaaaaaaaa")
	c.AvatarURL = "https://yt3.googleusercontent.com/b.jpg"
	c.Title = ""
	again, created, err := st.UpsertYtChannel(c)
	if err != nil {
		t.Fatal(err)
	}
	if created || again != id {
		t.Fatalf("重复添加不该新建行: created=%v id=%d want %d", created, again, id)
	}
	got, err := st.GetYtChannel(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("enabled 不该被重复添加改回 true")
	}
	if got.Title != "T-Series" {
		t.Errorf("空标题不该覆盖已有值，得到 %q", got.Title)
	}
	if got.AvatarURL != "https://yt3.googleusercontent.com/b.jpg" {
		t.Errorf("非空头像应覆盖旧值，得到 %q", got.AvatarURL)
	}
}

func TestYtVideosUpsertAndCounts(t *testing.T) {
	st := newTestStore(t)
	chID, _, err := st.UpsertYtChannel(ytTestChannel("UCbbbbbbbbbbbbbbbbbbbbbb"))
	if err != nil {
		t.Fatal(err)
	}

	pub := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ids, err := st.UpsertYtVideos(chID, []YtVideoInput{
		{VideoID: "vid1", Title: "第一条", Views: 100, DurationSec: 90, PublishedAt: pub},
		{VideoID: "vid2", Title: "第二条", Views: 500, DurationSec: 30},
	})
	if err != nil {
		t.Fatalf("写入视频失败: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("首次写入应返回 2 个新 id，得到 %d", len(ids))
	}

	// 第二次：只有一条是新的；空字段不覆盖已有值，播放量只增不减。
	more, err := st.UpsertYtVideos(chID, []YtVideoInput{
		{VideoID: "vid1", Title: "第一条（改）", Views: 50},
		{VideoID: "vid3", Title: "第三条"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(more) != 1 {
		t.Fatalf("只有 vid3 是新的，得到 %d 个新 id", len(more))
	}

	ch, err := st.GetYtChannel(chID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.VideoCount != 3 || ch.UnseenCount != 3 {
		t.Errorf("计数没刷新: videos=%d unseen=%d", ch.VideoCount, ch.UnseenCount)
	}

	// 有时间的排在前面（倒序），没时间的排最后。
	rows, _, err := st.ListYtVideos(YtVideosQuery{ChannelRowID: chID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("应读到 3 条，得到 %d", len(rows))
	}
	if rows[0].VideoID != "vid1" {
		t.Errorf("有发布时间的应排最前，得到 %q", rows[0].VideoID)
	}
	if rows[0].Views != 100 {
		t.Errorf("播放量只增不减，得到 %d", rows[0].Views)
	}
	if rows[2].PublishedAt != nil {
		t.Errorf("无发布时间的应排最后，得到 %q", rows[2].VideoID)
	}

	// 标记已看之后未读数要归零。
	if err := st.MarkYtVideosSeen(ids); err != nil {
		t.Fatal(err)
	}
	ch, err = st.GetYtChannel(chID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.UnseenCount != 1 {
		t.Errorf("3 条里标了 2 条已看，应剩 1 条（vid3），得到 %d", ch.UnseenCount)
	}
	n, err := st.CountUnseenYtVideos()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("全库未看应为 1，得到 %d", n)
	}
}

// TestFillYtVideoMeta 只补传进来的字段：refresh 只给时间、下载补全给全部。
func TestFillYtVideoMeta(t *testing.T) {
	st := newTestStore(t)
	chID, _, err := st.UpsertYtChannel(ytTestChannel("UCcccccccccccccccccccccc"))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.UpsertYtVideos(chID, []YtVideoInput{{VideoID: "vid9", Title: "无时间"}})
	if err != nil {
		t.Fatal(err)
	}
	rowID := ids[0]

	pub := time.Date(2026, 9, 30, 8, 50, 0, 0, time.UTC)
	if err := st.FillYtVideoMeta(rowID, &pub, "ytdlp", 0, 0); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetYtVideo(rowID)
	if err != nil {
		t.Fatal(err)
	}
	if row.PublishedAt == nil || !row.PublishedAt.Equal(pub) {
		t.Errorf("发布时间没写进去: %v", row.PublishedAt)
	}
	if row.DateSource != "ytdlp" {
		t.Errorf("日期来源没写进去: %q", row.DateSource)
	}
	if row.Title != "无时间" || row.DurationSec != 0 {
		t.Errorf("不该动的字段被改了: %+v", row)
	}

	if err := st.FillYtVideoMeta(rowID, nil, "", 123, 4567); err != nil {
		t.Fatal(err)
	}
	row, err = st.GetYtVideo(rowID)
	if err != nil {
		t.Fatal(err)
	}
	if row.PublishedAt == nil {
		t.Error("传 nil 时间不该把已有时间清掉")
	}
	if row.DurationSec != 123 || row.Views != 4567 {
		t.Errorf("时长/播放量没写进去: %+v", row)
	}
}

// 删掉会员抢先看的那条：行没了、频道计数跟着降，30 天后转公开时下一轮同步能重新入库。
func TestDeleteYtVideosByVideoIDs(t *testing.T) {
	st := newTestStore(t)
	chID, _, err := st.UpsertYtChannel(ytTestChannel("UCcccccccccccccccccccccc"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertYtVideos(chID, []YtVideoInput{
		{VideoID: "keep01", Title: "公开"},
		{VideoID: "member01", Title: "会员抢先看"},
	}); err != nil {
		t.Fatal(err)
	}
	// 库里没有的 id 不该算进删除数（flat 列表里大部分会员视频我们从来没存过）。
	n, err := st.DeleteYtVideosByVideoIDs([]string{"member01", "never-seen"})
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应只删掉 1 条，得到 %d", n)
	}
	ch, err := st.GetYtChannel(chID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.VideoCount != 1 || ch.UnseenCount != 1 {
		t.Errorf("计数没跟着降: videos=%d unseen=%d", ch.VideoCount, ch.UnseenCount)
	}
	// 空列表是常态（flat 列表里一条都不带 availability），不能报错也不能白扫一遍。
	if n, err := st.DeleteYtVideosByVideoIDs(nil); err != nil || n != 0 {
		t.Fatalf("空列表应得 0, nil，得到 %d, %v", n, err)
	}
}

// 「躺着拍」的视频要能被记住转了没转（yt_videos.rotate_deg）。
//
// 这条跑的是完整一圈：默认 0 → 写 90 → 读回来还是 90 → 写 0 能清掉；
// 顺带钉住两件容易写错的事：
//
//	· 非法角度（45）必须被拒，否则前端手滑就能写进一个 CSS 转不正的值；
//	· 不存在的 id 返回 0 行而不是报错，调用方据此回 404。
func TestSetYtVideoRotate(t *testing.T) {
	st := newTestStore(t)
	chID, _, err := st.UpsertYtChannel(ytTestChannel("UCrotrotrotrotrotrotrotr"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertYtVideos(chID, []YtVideoInput{
		{VideoID: "rotate00001", Title: "躺着拍的"},
	}); err != nil {
		t.Fatal(err)
	}
	v, err := st.GetYtVideoByVideoID("rotate00001")
	if err != nil {
		t.Fatal(err)
	}
	if v.RotateDeg != 0 {
		t.Fatalf("新入库应为 0 度，得到 %d", v.RotateDeg)
	}

	for _, deg := range []int{90, 180, 270, 0} {
		n, err := st.SetYtVideoRotate(v.ID, deg)
		if err != nil {
			t.Fatalf("写 %d 度失败: %v", deg, err)
		}
		if n != 1 {
			t.Fatalf("写 %d 度应改动 1 行，得到 %d", deg, n)
		}
		got, err := st.GetYtVideo(v.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.RotateDeg != deg {
			t.Fatalf("读回来的角度不对：想要 %d，得到 %d", deg, got.RotateDeg)
		}
	}

	if _, err := st.SetYtVideoRotate(v.ID, 45); err == nil {
		t.Fatal("45 度应被拒绝")
	}
	if n, err := st.SetYtVideoRotate(v.ID+99999, 90); err != nil || n != 0 {
		t.Fatalf("不存在的 id 应得 0, nil，得到 %d, %v", n, err)
	}
}

// TestYtMetaTriesAccounting 覆盖「补不到就别一直催」的记账。
//
// 2026-11-04 用户看到的「YouTube 页总是显示有 3 条没有时长」就是这里漏的：
// 那 3 条是短视频 / 登录墙挡住的行，补一百次也补不到，而缺口统计每次都把它们数进去，
// 墙上那条提示条于是永远挂着。现在的规矩：
//   - 还值得试的（meta_tries < YtMetaMaxTries）才算缺口，墙上的提示条只数这些；
//   - 试够次数的进 Stuck，只在设置页说明；
//   - 补到了（FillYtVideoMeta）或用户点补齐 / 换了 Cookie（ResetYtMetaTries）就清零重来。
func TestYtMetaTriesAccounting(t *testing.T) {
	st := newTestStore(t)
	chID, _, err := st.UpsertYtChannel(ytTestChannel("UCdddddddddddddddddddddd"))
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	ids, err := st.UpsertYtVideos(chID, []YtVideoInput{
		{VideoID: "vidTry1", Title: "只跑过 RSS 的一条", PublishedAt: pub},
		{VideoID: "vidTry2", Title: "只跑过 RSS 的另一条", PublishedAt: pub, DurationSec: 90},
	})
	if err != nil {
		t.Fatal(err)
	}
	rowA, rowB := ids[0], ids[1]

	// 初始：一条缺时长（A），B 有日期有时长不算缺。
	g, err := st.CountYtVideosMissingMeta()
	if err != nil {
		t.Fatal(err)
	}
	if g.Rows != 1 || g.Durations != 1 || g.Stuck != 0 {
		t.Fatalf("初始缺口不对: %+v", g)
	}
	rows, err := st.ListYtVideosMissingMeta(0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != rowA {
		t.Fatalf("待补列表不对: %+v", rows)
	}

	// 补 3 次都失败：缺口清零、进 Stuck，自动补就不该再拿它当缺口。
	if err := st.BumpYtMetaTries([]int64{rowA}, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.BumpYtMetaTries([]int64{rowA}, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.BumpYtMetaTries([]int64{rowA}, 1); err != nil {
		t.Fatal(err)
	}
	g, err = st.CountYtVideosMissingMeta()
	if err != nil {
		t.Fatal(err)
	}
	if g.Rows != 0 || g.Durations != 0 || g.Stuck != 1 {
		t.Fatalf("试够 3 次后应当只留在 Stuck: %+v", g)
	}
	rows, err = st.ListYtVideosMissingMeta(0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("试够次数的行不该再出现在自动待补列表里: %+v", rows)
	}
	rows, err = st.ListYtVideosMissingMeta(0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("force 要把它重新列出来，得到 %d 条", len(rows))
	}

	// 一次记满（登录墙那种整批挡住的）：直接到上限。
	if err := st.ResetYtMetaTries(); err != nil {
		t.Fatal(err)
	}
	if err := st.BumpYtMetaTries([]int64{rowB, rowA}, YtMetaMaxTries); err != nil {
		t.Fatal(err)
	}
	var tries int
	if err := st.DB().QueryRow(`SELECT meta_tries FROM yt_videos WHERE id = ?`, rowA).Scan(&tries); err != nil {
		t.Fatal(err)
	}
	if tries != YtMetaMaxTries {
		t.Fatalf("一次记满应当是 %d，得到 %d", YtMetaMaxTries, tries)
	}

	// 补到了就归零：下次再缺（比如换了登录态又抓不到）还能重新试。
	if err := st.FillYtVideoMeta(rowA, nil, "", 152, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT meta_tries FROM yt_videos WHERE id = ?`, rowA).Scan(&tries); err != nil {
		t.Fatal(err)
	}
	if tries != 0 {
		t.Fatalf("补到以后记账应归零，得到 %d", tries)
	}
	g, err = st.CountYtVideosMissingMeta()
	if err != nil {
		t.Fatal(err)
	}
	if g.Rows != 0 || g.Stuck != 0 {
		t.Fatalf("两条都齐了，不该再有缺口: %+v", g)
	}
}
