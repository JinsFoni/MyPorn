package store

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// seedLibraryVideos 建两个库、五部影片，落盘时间/体积/时长/文件名各不相同。
// created_at 走的是列默认值 datetime('now')，同一秒内建的行分不出先后，所以直接用 SQL 改成固定值。
func seedLibraryVideos(t *testing.T, st *Store) (libA, libB int64, videoIDs []int64) {
	t.Helper()
	var err error
	libA, err = st.CreateLibrary(&model.Library{Name: "主库", RootPath: filepath.Join(t.TempDir(), "a"),
		Enabled: true, IsDefault: true, UploadPolicy: model.PolicyLocal})
	if err != nil {
		t.Fatal(err)
	}
	libB, err = st.CreateLibrary(&model.Library{Name: "备份库", RootPath: filepath.Join(t.TempDir(), "b"),
		Enabled: true, UploadPolicy: model.PolicyLocal})
	if err != nil {
		t.Fatal(err)
	}

	// 体积/时长/文件名/落盘时间都错开，任何两种排序都不该给出同一个顺序
	specs := []struct {
		title    string
		size     int64
		duration int
		created  string
	}{
		{"alpha", 100, 10, "2026-10-01 10:00:00"},
		{"bravo", 500, 50, "2026-10-02 10:00:00"},
		{"charlie", 300, 30, "2026-10-03 10:00:00"},
		{"delta", 200, 40, "2026-10-04 10:00:00"},
		{"echo", 400, 20, "2026-10-05 10:00:00"},
	}
	for _, s := range specs {
		vid, _, err := st.UpsertVideo(&model.Video{Platform: "pornhub", PlatformVideoID: s.title,
			Title: "T-" + s.title, URL: "u-" + s.title, ThumbURL: "https://x/" + s.title + ".jpg"})
		if err != nil {
			t.Fatal(err)
		}
		videoIDs = append(videoIDs, vid)
		f := &model.LibraryFile{VideoID: vid, LibraryID: libA, RelDir: "Pornhub/X/" + s.title,
			VideoName: s.title + ".mp4", VideoPath: "/media/" + s.title + ".mp4",
			SizeBytes: s.size, Width: 1920, Height: 1080, DurationSec: s.duration}
		if err := st.SaveLibraryFile(f); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE library_files SET created_at = ? WHERE video_id = ?`, s.created, vid); err != nil {
			t.Fatal(err)
		}
	}
	// 第一部影片再落一份到备份库，用来验「同一部片可以同时在两个库里」。
	// 文件名故意改掉（alpha-b.mp4）：排序用例按文件名/体积断言，多一份同名同体积的行
	// 会让期望列表变得没法读，而这条用例要验的只是「多库多行」。
	if err := st.SaveLibraryFile(&model.LibraryFile{VideoID: videoIDs[0], LibraryID: libB,
		RelDir: "Pornhub/X/alpha", VideoName: "alpha-b.mp4", VideoPath: "/media-b/alpha.mp4",
		SizeBytes: 100, Width: 1920, Height: 1080, DurationSec: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE library_files SET created_at = '2026-09-01 10:00:00' WHERE library_id = ?`, libB); err != nil {
		t.Fatal(err)
	}
	return libA, libB, videoIDs
}

// walk 按游标把整条列表翻完，返回每行的 VideoName。
func walk(t *testing.T, st *Store, q LibraryVideoQuery) []string {
	t.Helper()
	var out []string
	cursor := ""
	for page := 0; page < 20; page++ {
		q.Cursor = cursor
		items, next, err := st.ListLibraryVideos(q)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			out = append(out, it.File.VideoName)
		}
		if next == "" {
			return out
		}
		cursor = next
	}
	t.Fatal("游标没有收敛（翻页死循环）")
	return nil
}

func TestListLibraryVideosSortsAndPaging(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	libA, _, _ := seedLibraryVideos(t, st)

	cases := []struct {
		name string
		sort string
		want []string
	}{
		// 落盘时间倒序 = delta 之前的最新在前
		{"recent", LibrarySortRecent, []string{"echo.mp4", "delta.mp4", "charlie.mp4", "bravo.mp4", "alpha.mp4"}},
		{"size", LibrarySortSize, []string{"bravo.mp4", "echo.mp4", "charlie.mp4", "delta.mp4", "alpha.mp4"}},
		{"duration", LibrarySortDuration, []string{"bravo.mp4", "delta.mp4", "charlie.mp4", "echo.mp4", "alpha.mp4"}},
		{"title", LibrarySortTitle, []string{"alpha.mp4", "bravo.mp4", "charlie.mp4", "delta.mp4", "echo.mp4"}},
	}
	for _, c := range cases {
		// limit=2 逼出多次翻页：游标错一位、或者哪一页漏一行，这里就露馅。
		// 只看主库：备份库那份（alpha-b）是另一条用例的事，混进来会把期望列表搅乱。
		got := walk(t, st, LibraryVideoQuery{Sort: c.sort, Limit: 2, LibraryID: libA})
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s 排序不对\n got=%v\nwant=%v", c.name, got, c.want)
		}
	}

	// 空 sort 等同 recent
	if got := walk(t, st, LibraryVideoQuery{Limit: 3, LibraryID: libA}); len(got) != 5 {
		t.Fatalf("默认排序应翻出 5 行，得到 %d 行: %v", len(got), got)
	}
}

func TestListLibraryVideosFiltersAndSummary(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	libA, libB, videoIDs := seedLibraryVideos(t, st)

	// 只筛备份库：那里只有 alpha 一份
	items, _, err := st.ListLibraryVideos(LibraryVideoQuery{LibraryID: libB, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].File.VideoID != videoIDs[0] {
		t.Fatalf("按库筛选不对: %+v", items)
	}
	if items[0].LibraryName != "备份库" {
		t.Fatalf("库名没带出来: %q", items[0].LibraryName)
	}
	if items[0].Video == nil || items[0].Video.Title != "T-alpha" {
		t.Fatalf("影片信息没联接上: %+v", items[0].Video)
	}
	// created_at 现在是 `YYYY-MM-DD HH:MM:SS`，早先只按 RFC3339 解会变成零值时间
	if items[0].File.CreatedAt.IsZero() {
		t.Fatal("CreatedAt 解析成了零值（SQLite 时间格式没认出来）")
	}

	// 标题 / 文件名两边都匹配
	items, _, err = st.ListLibraryVideos(LibraryVideoQuery{Query: "T-del", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].File.VideoName != "delta.mp4" {
		t.Fatalf("标题模糊匹配不对: %+v", items)
	}

	// 汇总：总数/总占用按库筛选算，分组计数不跟库筛选（那是筛选胶囊上的数字）
	total, bytes, counters, err := st.LibraryVideoSummary(LibraryVideoQuery{LibraryID: libA})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || bytes != 1500 {
		t.Fatalf("总览不对: total=%d bytes=%d（应为 5 / 1500）", total, bytes)
	}
	if len(counters) != 2 {
		t.Fatalf("应有 2 个库的分组计数，得到 %d: %+v", len(counters), counters)
	}
	// 默认库排前面
	if counters[0].LibraryID != libA || counters[0].FileCount != 5 || counters[0].SizeBytes != 1500 {
		t.Fatalf("主库计数不对: %+v", counters[0])
	}
	if counters[1].LibraryID != libB || counters[1].FileCount != 1 || counters[1].SizeBytes != 100 {
		t.Fatalf("备份库计数不对: %+v", counters[1])
	}
}

func TestListLibraryVideosSameVideoInTwoLibraries(t *testing.T) {
	st := newTestStore(t)
	defer st.Close()
	_, _, videoIDs := seedLibraryVideos(t, st)

	// alpha 同时躺在两个库里 → 两行，但共享同一个 video 指针（艺人只补一次）
	items, _, err := st.ListLibraryVideos(LibraryVideoQuery{Query: "alpha", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("应有 2 行（同一部片在两个库），得到 %d", len(items))
	}
	if items[0].Video != items[1].Video {
		t.Fatal("同一部片的两行应共享 video 指针")
	}
	if items[0].File.VideoID != videoIDs[0] {
		t.Fatalf("video_id 不对: %d", items[0].File.VideoID)
	}
}
