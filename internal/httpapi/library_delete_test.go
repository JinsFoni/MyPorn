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
	// 收空目录必须一路收到库根：整片文件夹是被 RemoveAll 端掉的，第一层就 ReadDir 失败，
	// 早先那种「err != nil 就 return」会停在原地，留下 `YouTube/测试频道` 这种空壳
	for _, up := range []string{
		filepath.Join(fx.libRoot, "YouTube", "测试频道"),
		filepath.Join(fx.libRoot, "YouTube"),
	} {
		if _, err := os.Stat(up); !os.IsNotExist(err) {
			t.Errorf("上层空目录没清掉：%s", up)
		}
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

// 「删除」要连文件夹一起端掉：下载失败留下的半截文件、字幕、临时图都不在 library_files
// 记录里，只按记录删就会剩一个装着垃圾的壳（2026-11-03 用户要求「目录和元数据都要删掉」）。
func TestDeleteLibraryVideoPurgesWholeFolder(t *testing.T) {
	fx := newLibraryServer(t)
	dir := filepath.Dir(fx.mp4)
	// 记录里没有、但实际躺在同一个文件夹里的东西
	strays := []string{
		filepath.Join(dir, "测试视频.zh.srt"),
		filepath.Join(dir, "测试视频.mp4.part"),
		filepath.Join(dir, ".thumb.src"),
		filepath.Join(dir, "thumb-sheet.jpg"),
	}
	for _, p := range strays {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	code, res := deleteLibraryVideo(t, fx.srv, itoa(fx.videoID))
	if code != http.StatusOK || !res.OK {
		t.Fatalf("应 200 + ok，得到 %d ok=%v kept=%+v", code, res.OK, res.Kept)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("影片文件夹应整个消失：%v", err)
	}
	got := map[string]bool{}
	for _, p := range res.Removed {
		got[p] = true
	}
	for _, p := range append(strays, fx.mp4) {
		if !got[p] {
			t.Errorf("removed 里应列出 %s", p)
		}
	}
}

// 两条片被记成同一个目录（rel_dir 写脏）时，端文件夹就是替别人删片——必须退回只删自己那份。
func TestDeleteLibraryVideoKeepsSharedFolder(t *testing.T) {
	fx := newLibraryServer(t)
	dir := filepath.Dir(fx.mp4)
	libraries, err := fx.st.ListLibraries()
	if err != nil || len(libraries) == 0 {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "另一部.mp4")
	if err := os.WriteFile(other, []byte("别人的片"), 0o644); err != nil {
		t.Fatal(err)
	}
	vid, _, err := fx.st.UpsertVideo(&model.Video{
		Platform: "xhamster", PlatformVideoID: "shared-1", Title: "共用一个目录", URL: "https://example.invalid/v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.st.SaveLibraryFile(&model.LibraryFile{
		VideoID: vid, LibraryID: libraries[0].ID, RelDir: "x", VideoName: "另一部.mp4",
		VideoPath: other, SizeBytes: 12,
	}); err != nil {
		t.Fatal(err)
	}

	code, res := deleteLibraryVideo(t, fx.srv, itoa(fx.videoID))
	if code != http.StatusOK || !res.OK {
		t.Fatalf("应 200 + ok，得到 %d ok=%v", code, res.OK)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("同目录里别人的片被删了：%v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("同目录还有人时不该端文件夹：%v", err)
	}
}

// 有的库是「根目录直接堆片」（没有一片一个文件夹），这时候清空目录等于清空整个库。
func TestDeleteLibraryVideoKeepsRoot(t *testing.T) {
	fx := newLibraryServer(t)
	libraries, err := fx.st.ListLibraries()
	if err != nil || len(libraries) == 0 {
		t.Fatal(err)
	}
	flat := filepath.Join(fx.libRoot, "平铺的片.mp4")
	sibling := filepath.Join(fx.libRoot, "同库另一部.mp4")
	for _, p := range []string{flat, sibling} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vid, _, err := fx.st.UpsertVideo(&model.Video{
		Platform: "xhamster", PlatformVideoID: "flat-1", Title: "平铺", URL: "https://example.invalid/v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.st.SaveLibraryFile(&model.LibraryFile{
		VideoID: vid, LibraryID: libraries[0].ID, RelDir: "", VideoName: "平铺的片.mp4",
		VideoPath: flat, SizeBytes: 1,
	}); err != nil {
		t.Fatal(err)
	}

	if code, res := deleteLibraryVideo(t, fx.srv, itoa(vid)); code != http.StatusOK || !res.OK {
		t.Fatalf("应 200 + ok，得到 %d ok=%v", code, res.OK)
	}
	if _, err := os.Stat(flat); !os.IsNotExist(err) {
		t.Errorf("这条片自己该被删：%v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("库根直接被端了：%v", err)
	}
	if _, err := os.Stat(fx.libRoot); err != nil {
		t.Errorf("库根不该消失：%v", err)
	}
}
