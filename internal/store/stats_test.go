package store

import (
	"testing"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// 管理页的那份快照：分档边界、跨表计数、以及「没有落盘时不该凭空多出体积」。
func TestStats(t *testing.T) {
	st := newTestStore(t)

	perfID, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "p1", Name: "P1", URL: "https://x/p1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSubscription(perfID, 0); err != nil {
		t.Fatal(err)
	}
	// 第二个订阅，手工塞一条错误，用来验「检查失败」的计数
	perf2, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "p2", Name: "P2", URL: "https://x/p2",
	})
	if err != nil {
		t.Fatal(err)
	}
	sub2, err := st.CreateSubscription(perf2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE subscriptions SET last_error = 'HTTP 403', enabled = 0 WHERE id = ?`, sub2); err != nil {
		t.Fatal(err)
	}

	// 三部影片：4 分钟 / 10 分钟 / 50 分钟，标签分别是 [a,b] / [a] / [b]
	mk := func(pid, title string, dur int, tags []string) int64 {
		id, _, err := st.UpsertVideo(&model.Video{
			Platform: "pornhub", PlatformVideoID: pid, Title: title, URL: "https://x/" + pid,
			DurationSec: dur, Tags: tags,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	v1 := mk("v1", "四分", 240, []string{"a", "b"})
	v2 := mk("v2", "十分", 600, []string{"a"})
	mk("v3", "五十分", 3000, []string{"b"})

	if err := st.LinkVideoPerformer(v1, perfID); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkVideoPerformer(v2, perfID); err != nil {
		t.Fatal(err)
	}

	// 只有 v1 落盘，1GB
	libID, err := st.CreateLibrary(&model.Library{Name: "主库", RootPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveLibraryFile(&model.LibraryFile{
		VideoID: v1, LibraryID: libID, SizeBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}

	stt, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}

	if stt.Performers != 2 || stt.PerformersEnabled != 1 || stt.PerformersFailed != 1 {
		t.Fatalf("关注计数不对: %+v", stt)
	}
	if stt.Videos != 3 || stt.VideosNew != 3 {
		t.Fatalf("影片计数不对: videos=%d new=%d", stt.Videos, stt.VideosNew)
	}
	if stt.Files != 1 || stt.Bytes != 1<<30 || stt.VideosWithFile != 1 {
		t.Fatalf("落盘计数不对: files=%d bytes=%d withFile=%d", stt.Files, stt.Bytes, stt.VideosWithFile)
	}

	// 时长分档：240s → lt5，600s → 5to15，3000s → gt40
	want := map[string]int{"lt5": 1, "5to15": 1, "gt40": 1}
	for _, row := range stt.ByDuration {
		if row.N != want[row.Key] {
			t.Errorf("时长分档 %s = %d，期望 %d", row.Key, row.N, want[row.Key])
		}
		delete(want, row.Key)
	}
	if len(want) != 0 {
		t.Errorf("时长分档缺档: %v（实际 %+v）", want, stt.ByDuration)
	}

	// 没解析过清晰度 → 全进 none
	if len(stt.ByQuality) != 1 || stt.ByQuality[0].Key != "none" || stt.ByQuality[0].N != 3 {
		t.Errorf("清晰度分档不对: %+v", stt.ByQuality)
	}

	// 平台分项带体积；标签按次数倒序，同数按名字
	if len(stt.ByPlatform) != 1 || stt.ByPlatform[0].Bytes != 1<<30 {
		t.Errorf("平台分项不对: %+v", stt.ByPlatform)
	}
	if len(stt.Tags) != 2 || stt.Tags[0].Key != "a" || stt.Tags[0].N != 2 {
		t.Errorf("标签应按次数倒序（a 出现两次排第一）: %+v", stt.Tags)
	}

	// 每位艺人本地收录量
	local, err := st.PerformerLocalCounts()
	if err != nil {
		t.Fatal(err)
	}
	if got := local[perfID]; got.Videos != 2 || got.Files != 1 || got.Bytes != 1<<30 {
		t.Fatalf("艺人本地收录量不对: %+v", got)
	}
	if got := local[perf2]; got.Videos != 0 {
		t.Fatalf("没有关联影片的艺人应读 0: %+v", got)
	}
}

// 空库也要给出能用的快照（不能因为 SUM 返回 NULL 就报错）。
func TestStatsOnEmptyDB(t *testing.T) {
	st := newTestStore(t)
	stt, err := st.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stt.Videos != 0 || stt.Files != 0 || stt.Bytes != 0 || stt.Performers != 0 {
		t.Fatalf("空库读数应为 0: %+v", stt)
	}
	if len(stt.ByDuration) != 0 || len(stt.Tags) != 0 || len(stt.Recent) != 0 {
		t.Fatalf("空库不该有分项: %+v", stt)
	}
}
