package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// 本地播放这条线的测试：YouTube 影片没有平台 adapter，详情页要是不认本地文件，
// 用户下完之后点开还是「平台未注册」——这正是 M6 验收第 7 条踩到的坑。

// withRouteParam 给直接调 handler 的测试补上 chi 的 URL 参数。
func withRouteParam(req *http.Request, key, val string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, val)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func newPlaybackServer(t *testing.T) (*Server, *store.Store, int64, string) {
	t.Helper()
	tmp := t.TempDir()
	libRoot := filepath.Join(tmp, "media")
	fileDir := filepath.Join(libRoot, "YouTube", "测试频道", "2026-10-20 - 测试视频")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(fileDir, "测试视频.mp4")
	// 内容无所谓，ServeFile 只关心长度和 Range。
	if err := os.WriteFile(videoPath, make([]byte, 4096), 0o644); err != nil {
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
	cfg := &config.Config{DataDir: tmp, LibraryRoot: libRoot}
	srv := New(cfg, st, platform.NewRegistry(), nil, events.NewHub(), nil, nil, log, nil,
		imgcache.New(t.TempDir(), nil, "", log))
	vidID, _, err := st.UpsertVideo(&model.Video{
		Platform: "youtube", PlatformVideoID: "abcd1234", Title: "测试视频", DurationSec: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveLibraryFile(&model.LibraryFile{
		VideoID: vidID, LibraryID: libID, VideoPath: videoPath, SizeBytes: 4096,
	}); err != nil {
		t.Fatal(err)
	}
	return srv, st, vidID, libRoot
}

// TestPlaybackServesLocalFileWithoutAdapter 没有 adapter 也要能播：本地那一路排第一且是默认档。
func TestPlaybackServesLocalFileWithoutAdapter(t *testing.T) {
	// 注册表里没有 youtube，这条影片只有本地文件可播。
	srv, _, vidID, _ := newPlaybackServer(t)
	path := "/api/videos/" + strconv.FormatInt(vidID, 10) + "/playback"

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.handlePlayback(rec, withRouteParam(req, "id", strconv.FormatInt(vidID, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Variants []struct {
			ID        string `json:"id"`
			URL       string `json:"url"`
			IsDefault bool   `json:"isDefault"`
			Local     bool   `json:"local"`
		} `json:"variants"`
		DefaultVariantID string `json:"defaultVariantId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Variants) != 1 {
		t.Fatalf("应该只有本地一路，得到 %d 路", len(got.Variants))
	}
	v := got.Variants[0]
	if !v.Local || !v.IsDefault || got.DefaultVariantID != "local" {
		t.Errorf("本地档应当是默认档: %+v default=%q", v, got.DefaultVariantID)
	}
	wantURL := "/api/videos/" + strconv.FormatInt(vidID, 10) + "/file?libraryId=1"
	if v.URL != wantURL {
		t.Errorf("本地播放地址不对: %q，应为 %q", v.URL, wantURL)
	}
}

// TestVideoFileServesRange 浏览器拖动进度条靠 Range，必须回 206。
func TestVideoFileServesRange(t *testing.T) {
	srv, _, vidID, _ := newPlaybackServer(t)
	path := "/api/videos/" + strconv.FormatInt(vidID, 10) + "/file?libraryId=1"

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=100-199")
	rec := httptest.NewRecorder()
	srv.handleVideoFile(rec, withRouteParam(req, "id", strconv.FormatInt(vidID, 10)))
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求应回 206，得到 %d", rec.Code)
	}
	if n := rec.Body.Len(); n != 100 {
		t.Errorf("应发 100 字节，得到 %d", n)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("Content-Type 不对: %q", ct)
	}
}

// TestVideoFileRejectsOutsideLibrary 库根被改小 / 记录被写脏时不能变成任意文件读取。
func TestVideoFileRejectsOutsideLibrary(t *testing.T) {
	srv, st, vidID, libRoot := newPlaybackServer(t)

	// 把记录指到库外面去（模拟库根被改小 / 记录被写脏）
	outside := filepath.Join(filepath.Dir(libRoot), "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveLibraryFile(&model.LibraryFile{
		VideoID: vidID, LibraryID: 1, VideoPath: outside, SizeBytes: 10,
	}); err != nil {
		t.Fatal(err)
	}

	path := "/api/videos/" + strconv.FormatInt(vidID, 10) + "/file?libraryId=1"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.handleVideoFile(rec, withRouteParam(req, "id", strconv.FormatInt(vidID, 10)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("库外文件应被拒，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() == "top secret" {
		t.Fatal("文件内容泄漏了")
	}
}
