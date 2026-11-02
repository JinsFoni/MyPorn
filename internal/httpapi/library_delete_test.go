package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/model"
)

type deleteResult struct {
	OK      bool     `json:"ok"`
	Removed []string `json:"removed"`
	Kept    []struct {
		LibraryID int64  `json:"libraryId"`
		Path      string `json:"path"`
		Reason    string `json:"reason"`
	} `json:"kept"`
}

func deleteLibraryVideo(t *testing.T, srv *Server, id string) (int, deleteResult) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/library/videos/"+id, nil)
	// chi 的 URLParam 要靠路由解析，这里直接塞进 ctx（和 adjacent_test 同一套做法）
	srv.handleDeleteLibraryVideo(rec, withRouteParam(req, "id", id))
	var out deleteResult
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// 播放页那颗「删除」：磁盘文件、边角图、空目录、库记录四样都要清干净。
func TestDeleteLibraryVideoRemovesFiles(t *testing.T) {
	fx := newLibraryServer(t)
	dir := filepath.Dir(fx.mp4)

	code, res := deleteLibraryVideo(t, fx.srv, itoa(fx.videoID))
	if code != http.StatusOK || !res.OK {
		t.Fatalf("应 200 + ok，得到 %d ok=%v kept=%+v", code, res.OK, res.Kept)
	}
	if len(res.Removed) != 3 { // mp4 + thumb.jpg + poster.jpg
		t.Errorf("应删掉 3 个文件（视频 + 两张图），得到 %v", res.Removed)
	}
	for _, p := range []string{fx.mp4, fx.thumb} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("文件还在：%s", p)
		}
	}
	// 一片一个文件夹的结构：删完片不该在盘上留一个空壳（一路收回到库根为止）
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("空目录没清掉：%s", dir)
	}
	if _, err := os.Stat(fx.libRoot); err != nil {
		t.Errorf("库根不能被删掉：%v", err)
	}
	if files, err := fx.st.ListLibraryFilesByVideo(fx.videoID); err != nil || len(files) != 0 {
		t.Errorf("库记录该删掉：%v / %d 条", err, len(files))
	}
	// 影片本身留着（只是本地没了）：播放页要能退回平台在线播放
	if _, err := fx.st.GetVideo(fx.videoID); err != nil {
		t.Errorf("videos 行不该被删：%v", err)
	}
	// 已经没有本地文件了，再点一次是 404 而不是 500
	if code, _ := deleteLibraryVideo(t, fx.srv, itoa(fx.videoID)); code != http.StatusNotFound {
		t.Errorf("第二次删除应 404，得到 %d", code)
	}
}

// 记录被写脏 / 库根被改小之后，这条接口绝不能变成「按库里的字符串删本机文件」。
func TestDeleteLibraryVideoRefusesOutsideRoot(t *testing.T) {
	fx := newLibraryServer(t)
	libraries, err := fx.st.ListLibraries()
	if err != nil || len(libraries) == 0 {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "别动我.mp4")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	vid, _, err := fx.st.UpsertVideo(&model.Video{
		Platform: "xhamster", PlatformVideoID: "evil-1", Title: "越界记录", URL: "https://example.invalid/v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.st.SaveLibraryFile(&model.LibraryFile{
		VideoID: vid, LibraryID: libraries[0].ID, RelDir: "x", VideoName: "别动我.mp4",
		VideoPath: outside, SizeBytes: 7,
	}); err != nil {
		t.Fatal(err)
	}

	code, res := deleteLibraryVideo(t, fx.srv, itoa(vid))
	if code != http.StatusOK {
		t.Fatalf("应 200（拒绝删但请求本身合法），得到 %d", code)
	}
	if res.OK || len(res.Kept) != 1 || len(res.Removed) != 0 {
		t.Fatalf("越界路径必须拒绝删除：ok=%v removed=%v kept=%+v", res.OK, res.Removed, res.Kept)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("库根之外的文件被删了：%v", err)
	}
	if files, err := fx.st.ListLibraryFilesByVideo(vid); err != nil || len(files) != 1 {
		t.Errorf("删不掉时要留着记录，否则媒体库里剩一条指着空气的卡：%v / %d 条", err, len(files))
	}
}
