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
