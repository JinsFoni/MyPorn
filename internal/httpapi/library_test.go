package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// 媒体库接口：一页列出磁盘上已落盘的影片，封面优先用落盘那张 thumb.jpg。

const testJPEG = "\xff\xd8\xff\xe0test-jpeg-bytes"

type libraryFixture struct {
	srv     *Server
	st      *store.Store
	libRoot string
	videoID int64
	mp4     string
	thumb   string
}

func newLibraryServer(t *testing.T) *libraryFixture {
	t.Helper()
	tmp := t.TempDir()
	libRoot := filepath.Join(tmp, "media")
	dir := filepath.Join(libRoot, "YouTube", "测试频道", "2026-10-20 - 测试视频")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mp4 := filepath.Join(dir, "测试视频.mp4")
	if err := os.WriteFile(mp4, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	thumb := filepath.Join(dir, "thumb.jpg")
	if err := os.WriteFile(thumb, []byte(testJPEG), 0o644); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(dir, "poster.jpg")
	if err := os.WriteFile(poster, []byte(testJPEG+"-poster"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	libID, err := st.CreateLibrary(&model.Library{Name: "主库", RootPath: libRoot, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(&config.Config{DataDir: tmp, LibraryRoot: libRoot}, st, platform.NewRegistry(), nil,
		events.NewHub(), nil, nil, log, nil, imgcache.New(t.TempDir(), nil, "", log))

	vidID, _, err := st.UpsertVideo(&model.Video{
		Platform: "youtube", PlatformVideoID: "abcd1234", Title: "测试视频", DurationSec: 30,
		ThumbURL: "https://i.ytimg.com/vi/abcd1234/hqdefault.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveLibraryFile(&model.LibraryFile{
		VideoID: vidID, LibraryID: libID, RelDir: "YouTube/测试频道/2026-10-20 - 测试视频",
		VideoName: "测试视频.mp4", VideoPath: mp4, ThumbPath: thumb, PosterPath: poster,
		SizeBytes: 4096, Width: 3840, Height: 2160, DurationSec: 30,
	}); err != nil {
		t.Fatal(err)
	}
	return &libraryFixture{srv: srv, st: st, libRoot: libRoot, videoID: vidID, mp4: mp4, thumb: thumb}
}

type libraryItem struct {
	VideoID       int64    `json:"videoId"`
	LibraryID     int64    `json:"libraryId"`
	LibraryName   string   `json:"libraryName"`
	Title         string   `json:"title"`
	FileName      string   `json:"fileName"`
	Platform      string   `json:"platform"`
	DurationSec   int      `json:"durationSec"`
	Width         int      `json:"width"`
	Height        int      `json:"height"`
	SizeBytes     int64    `json:"sizeBytes"`
	DownloadedAt  *string  `json:"downloadedAt"`
	ThumbURL      string   `json:"thumbUrl"`
	LocalThumbURL *string  `json:"localThumbUrl"`
	VideoPath     string   `json:"videoPath"`
	Missing       bool     `json:"missing"`
	FileURL       string   `json:"fileUrl"`
	Performers    []string `json:"performers"`
}

type libraryPage struct {
	Items      []libraryItem `json:"items"`
	NextCursor *string       `json:"nextCursor"`
	Total      int64         `json:"total"`
	TotalBytes int64         `json:"totalSizeBytes"`
	ByLibrary  []struct {
		LibraryID int64  `json:"libraryId"`
		Name      string `json:"name"`
		FileCount int64  `json:"fileCount"`
		SizeBytes int64  `json:"sizeBytes"`
	} `json:"byLibrary"`
}

func listLibrary(t *testing.T, srv *Server, query string) libraryPage {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/library/videos"+query, nil)
	srv.handleListLibraryVideos(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var page libraryPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestLibraryVideosListsLocalFiles(t *testing.T) {
	fx := newLibraryServer(t)
	page := listLibrary(t, fx.srv, "")

	if len(page.Items) != 1 {
		t.Fatalf("应有 1 条，得到 %d", len(page.Items))
	}
	it := page.Items[0]
	if it.VideoID != fx.videoID || it.LibraryName != "主库" {
		t.Errorf("基本字段不对: %+v", it)
	}
	if it.Title != "测试视频" || it.FileName != "测试视频.mp4" {
		t.Errorf("标题/文件名不对: %q / %q", it.Title, it.FileName)
	}
	if it.Width != 3840 || it.Height != 2160 || it.SizeBytes != 4096 || it.DurationSec != 30 {
		t.Errorf("画质/体积/时长不对: %+v", it)
	}
	if it.Missing {
		t.Error("文件在磁盘上，不该标 missing")
	}
	// 本地封面可用时给地址（另给一份远端兜底）
	if it.LocalThumbURL == nil || *it.LocalThumbURL == "" {
		t.Error("落盘的 thumb.jpg 应该给出 localThumbUrl")
	}
	if it.ThumbURL == "" {
		t.Error("远端封面兜底地址不该是空的")
	}
	if it.DownloadedAt == nil {
		t.Error("downloadedAt 不该是 null（created_at 的 SQLite 时间格式要能解析）")
	}
	wantFileURL := "/api/videos/" + strconv.FormatInt(fx.videoID, 10) + "/file?libraryId=1"
	if it.FileURL != wantFileURL {
		t.Errorf("播放地址不对: %q，应为 %q", it.FileURL, wantFileURL)
	}
	if page.Total != 1 || page.TotalBytes != 4096 {
		t.Errorf("汇总不对: total=%d bytes=%d", page.Total, page.TotalBytes)
	}
	if len(page.ByLibrary) != 1 || page.ByLibrary[0].FileCount != 1 || page.ByLibrary[0].SizeBytes != 4096 {
		t.Errorf("按库计数不对: %+v", page.ByLibrary)
	}
}

// TestLibraryVideosMarksMissing 文件被挪走以后不能装作还能播。
func TestLibraryVideosMarksMissing(t *testing.T) {
	fx := newLibraryServer(t)
	if err := os.Remove(fx.mp4); err != nil {
		t.Fatal(err)
	}
	page := listLibrary(t, fx.srv, "")
	if len(page.Items) != 1 {
		t.Fatalf("记录还在，应仍列出 1 条，得到 %d", len(page.Items))
	}
	if !page.Items[0].Missing {
		t.Error("文件已删除，应标 missing")
	}
}

func TestVideoArtServesLocalThumb(t *testing.T) {
	fx := newLibraryServer(t)
	base := "/api/videos/" + strconv.FormatInt(fx.videoID, 10) + "/art?libraryId=1"

	for _, c := range []struct {
		kind string
		want string
	}{
		{"", testJPEG},
		{"&k=thumb", testJPEG},
		{"&k=poster", testJPEG + "-poster"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, base+c.kind, nil)
		fx.srv.handleVideoArt(rec, withRouteParam(req, "id", strconv.FormatInt(fx.videoID, 10)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%q 应 200，得到 %d: %s", c.kind, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != c.want {
			t.Errorf("%q 发的字节不对: %q", c.kind, rec.Body.String())
		}
	}

	// 没落盘的种类要 404，而不是回一张空白
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, base+"&k=fanart", nil)
	fx.srv.handleVideoArt(rec, withRouteParam(req, "id", strconv.FormatInt(fx.videoID, 10)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("fanart 没落盘应 404，得到 %d", rec.Code)
	}
}

// TestVideoArtRejectsOutsideRoot 记录被写脏时不能变成任意文件读取。
func TestVideoArtRejectsOutsideRoot(t *testing.T) {
	fx := newLibraryServer(t)
	outside := filepath.Join(t.TempDir(), "secret.jpg")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 把记录里的 thumb_path 改成库根之外（模拟被写脏的落盘记录）
	if err := fx.st.SaveLibraryFile(&model.LibraryFile{
		VideoID: fx.videoID, LibraryID: 1, VideoPath: fx.mp4, ThumbPath: outside,
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	path := "/api/videos/" + strconv.FormatInt(fx.videoID, 10) + "/art?libraryId=1"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	fx.srv.handleVideoArt(rec, withRouteParam(req, "id", strconv.FormatInt(fx.videoID, 10)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("库根之外的文件应 403，得到 %d: %s", rec.Code, rec.Body.String())
	}
}
