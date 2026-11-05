package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/library"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// makeTestVideo 造一段纯色静帧视频（够取帧就行）。
func makeTestVideo(t *testing.T, path string, w, h int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size="+strconv.Itoa(w)+"x"+strconv.Itoa(h)+":rate=10:duration=4",
		"-pix_fmt", "yuv420p", "-c:v", "libx264", "-preset", "ultrafast", path)
	if err := cmd.Run(); err != nil {
		t.Skipf("本机 ffmpeg 造不出测试视频，跳过: %v", err)
	}
}

// makeTestImage 造一张静态图（当站方缩略图 / 艺人头像）。
func makeTestImage(t *testing.T, path string, w, h int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size="+strconv.Itoa(w)+"x"+strconv.Itoa(h)+":rate=1:duration=1",
		"-frames:v", "1", "-q:v", "3", path)
	if err := cmd.Run(); err != nil {
		t.Skipf("本机 ffmpeg 造不出测试图片，跳过: %v", err)
	}
}

func dimsOf(t *testing.T, path string) (int, int) {
	t.Helper()
	w, h, err := library.ImageSize(path)
	if err != nil {
		t.Fatalf("读不出 %s 的尺寸: %v", path, err)
	}
	return w, h
}

// TestWriteArtworkFallsBackToFrame 验「官方图小到没救时换视频帧」：
// 站方缩略图只有 480×360、头像 200×200，三张图全部改用视频帧，并且一张都不放大。
func TestWriteArtworkFallsBackToFrame(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "movie")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "movie.mp4")
	// 1080p 的片子 + 480×360 的站方缩略图 + 200×200 的头像——就是线上最常见的那种组合
	makeTestVideo(t, video, 1920, 1080)
	makeTestImage(t, filepath.Join(tmp, "thumb.jpg"), 480, 360)
	makeTestImage(t, filepath.Join(tmp, "avatar.jpg"), 200, 200)

	var thumbHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/thumb.jpg", func(w http.ResponseWriter, r *http.Request) {
		thumbHits++
		http.ServeFile(w, r, filepath.Join(tmp, "thumb.jpg"))
	})
	mux.HandleFunc("/avatar.jpg", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(tmp, "avatar.jpg"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	st, err := store.Open(filepath.Join(tmp, "art.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	perfID, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "p1", Name: "P1", AvatarURL: srv.URL + "/avatar.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: filepath.Join(tmp, "data"), PlatformProxy: map[string]string{}}
	r := buildRunner(t, st, platform.NewRegistry(), cfg)
	video2 := &model.Video{
		Platform: "pornhub", PlatformVideoID: "v1", Title: "T",
		ThumbURL: srv.URL + "/thumb.jpg", DurationSec: 4,
		Performers: []model.PerformerRef{{ID: perfID, Name: "P1"}},
	}

	art := r.writeArtwork(context.Background(), video2, video, dir, 4)
	if len(art.warnings) > 0 {
		t.Fatalf("不该有警告：%v", art.warnings)
	}
	if art.poster == "" || art.thumb == "" || art.fanart == "" {
		t.Fatalf("三张图都要有：%+v", art)
	}

	// fanart/thumb：视频是 1920×1080，目标 1920×1080 / 1280×720 → 刚好原尺寸出图
	if w, h := dimsOf(t, art.fanart); w != 1920 || h != 1080 {
		t.Errorf("fanart 应为 1920x1080（视频帧原尺寸），实为 %dx%d", w, h)
	}
	if w, h := dimsOf(t, art.thumb); w != 1280 || h != 720 {
		t.Errorf("thumb 应为 1280x720，实为 %dx%d", w, h)
	}
	// poster：1920×1080 按 2:3 裁 = 720×1080，**正好是 1000×1500 的 0.72 倍**，
	// 低于目标就按源图出（只缩不放），绝不能放大成 1000×1500
	if w, h := dimsOf(t, art.poster); w != 720 || h != 1080 {
		t.Errorf("poster 应为 720x1080（2:3 裁切、不放大），实为 %dx%d", w, h)
	}

	// 4K 竖屏的片子：poster 应该吃满 1000×1500（源图够大就按规格出）
	dir4k := filepath.Join(tmp, "vertical")
	if err := os.MkdirAll(dir4k, 0o755); err != nil {
		t.Fatal(err)
	}
	video4k := filepath.Join(dir4k, "v.mp4")
	makeTestVideo(t, video4k, 2160, 3840)
	art4k := r.writeArtwork(context.Background(), video2, video4k, dir4k, 4)
	if w, h := dimsOf(t, art4k.poster); w != 1000 || h != 1500 {
		t.Errorf("竖屏 4K 的 poster 应吃满 1000x1500，实为 %dx%d", w, h)
	}
	if w, h := dimsOf(t, art4k.fanart); w != 1920 || h != 1080 {
		t.Errorf("竖屏 4K 的 fanart 应为 1920x1080，实为 %dx%d", w, h)
	}
	t.Logf("缩略图被请求 %d 次（太小进不了候选，帧赢了）", thumbHits)
}

// TestWriteArtworkKeepsOfficialArt 验「官方封面够大就继续用」：
// 哪怕视频是 4K、帧更高清，设计过的封面也比随机一帧更适合当海报。
func TestWriteArtworkKeepsOfficialArt(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "movie")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "movie.mp4")
	makeTestVideo(t, video, 3840, 2160)
	makeTestImage(t, filepath.Join(tmp, "thumb.jpg"), 1280, 720)

	mux := http.NewServeMux()
	mux.HandleFunc("/thumb.jpg", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(tmp, "thumb.jpg"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	st, err := store.Open(filepath.Join(tmp, "art2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{DataDir: filepath.Join(tmp, "data"), PlatformProxy: map[string]string{}}
	r := buildRunner(t, st, platform.NewRegistry(), cfg)
	v := &model.Video{Platform: "pornhub", PlatformVideoID: "v1", Title: "T", ThumbURL: srv.URL + "/thumb.jpg", DurationSec: 4}

	art := r.writeArtwork(context.Background(), v, video, dir, 4)
	// 16:9 的两张：官方封面 1280×720 直接达标（不放大）
	if w, h := dimsOf(t, art.fanart); w != 1280 || h != 720 {
		t.Errorf("fanart 应用官方封面（1280×720，不放大），实为 %dx%d", w, h)
	}
	if w, h := dimsOf(t, art.thumb); w != 1280 || h != 720 {
		t.Errorf("thumb 应为 1280×720，实为 %dx%d", w, h)
	}
	// 海报：横屏片子用官方封面裁 2:3 = 480×720（不放大），比随机一帧更像海报
	if w, h := dimsOf(t, art.poster); w != 480 || h != 720 {
		t.Errorf("横屏片子的 poster 应用官方封面裁切（480×720），实为 %dx%d", w, h)
	}

	// 竖屏片子反过来：帧本身就是竖的，裁 2:3 不浪费，直接吃满 1000×1500
	dirV := filepath.Join(tmp, "vertical")
	if err := os.MkdirAll(dirV, 0o755); err != nil {
		t.Fatal(err)
	}
	videoV := filepath.Join(dirV, "v.mp4")
	makeTestVideo(t, videoV, 2160, 3840)
	artV := r.writeArtwork(context.Background(), v, videoV, dirV, 4)
	if w, h := dimsOf(t, artV.poster); w != 1000 || h != 1500 {
		t.Errorf("竖屏片子的 poster 应吃满 1000×1500（视频帧），实为 %dx%d", w, h)
	}
}

// TestPickSource 验选底图的两步规则：先按偏好顺序取「够清楚」的，都不过门槛才比面积。
func TestPickSource(t *testing.T) {
	small := []artSource{
		{kind: "frame", path: "f", w: 3840, h: 2160},
		{kind: "thumb", path: "t", w: 480, h: 360},
		{kind: "avatar", path: "a", w: 399, h: 399},
	}
	// 480×360 / 399×399 都画不出 400 宽 → 三张图都退到视频帧
	if got := pickSource(small, posterW, posterH, 400, "avatar", "thumb", "frame"); got.kind != "frame" {
		t.Errorf("官方图都太小，海报应退到视频帧，实为 %s", got.kind)
	}
	big := []artSource{
		{kind: "frame", path: "f", w: 3840, h: 2160},
		{kind: "thumb", path: "t", w: 1280, h: 720},
	}
	// 1280×720 裁 2:3 能画 480 宽，过门槛 → 继续用官方封面（设计过的图比随机一帧更像海报）
	if got := pickSource(big, posterW, posterH, 400, "avatar", "thumb", "frame"); got.kind != "thumb" {
		t.Errorf("官方封面过门槛时应继续用它，实为 %s", got.kind)
	}
	// 16:9 的目标上官方封面更该赢（不用裁）
	if got := pickSource(big, fanartW, fanartH, fanartW/2, "thumb", "frame", "avatar"); got.kind != "thumb" {
		t.Errorf("fanart 应优先用官方封面，实为 %s", got.kind)
	}
	// 头像够大时海报仍按偏好顺序用头像（原来的「脸更像海报」没丢）
	bigAvatar := []artSource{
		{kind: "avatar", path: "a", w: 1200, h: 1800},
		{kind: "thumb", path: "t", w: 1280, h: 720},
	}
	if got := pickSource(bigAvatar, posterW, posterH, 400, "avatar", "thumb", "frame"); got.kind != "avatar" {
		t.Errorf("头像够大时应继续当海报，实为 %s", got.kind)
	}
	if got := pickSource(nil, posterW, posterH, 400, "frame"); got != nil {
		t.Errorf("没有底图时应返回 nil")
	}
}

// TestBiggerThumbVariants 验各平台的大图 URL 改写（都得把原 URL 留在最后兜底）。
func TestBiggerThumbVariants(t *testing.T) {
	cases := []struct {
		platform string
		in       string
		first    string
	}{
		{"youtube", "https://i1.ytimg.com/vi/lO9pLX4jQ9Q/hqdefault.jpg", "https://i1.ytimg.com/vi/lO9pLX4jQ9Q/maxresdefault.jpg"},
		{"pornhub", "https://pix-fl.phncdn.com/x/plain/rs:fit:320:180/vts:315?a=1", "https://pix-fl.phncdn.com/x/plain/rs:fit:1920:1080/vts:315?a=1"},
		{"bilibili", "https://i0.hdslb.com/bfs/archive/abc.jpg@960w_540h_1c.webp", "https://i0.hdslb.com/bfs/archive/abc.jpg@1920w_1080h_1c.webp"},
		{"xhamster", "https://ic-vt-nss.xhcdn.com/a/xx/s(w:1280,h:720),webp/023.jpg", "https://ic-vt-nss.xhcdn.com/a/xx/s(w:1920,h:1080),webp/023.jpg"},
	}
	for _, c := range cases {
		got := biggerThumbVariants(c.platform, c.in)
		if got[0] != c.first {
			t.Errorf("%s 大图地址不对\n 得到 %s\n 期望 %s", c.platform, got[0], c.first)
		}
		if got[len(got)-1] != c.in {
			t.Errorf("%s 必须把原地址留在最后兜底，得到 %v", c.platform, got)
		}
	}
	// 认不出的平台：原样返回
	if got := biggerThumbVariants("beeg", "https://x/y.jpg"); len(got) != 1 || got[0] != "https://x/y.jpg" {
		t.Errorf("未知平台应原样返回，得到 %v", got)
	}
}

// TestFrameAt 取帧位置：够长的片子取 1/4，短视频取中段。
func TestFrameAt(t *testing.T) {
	if got := frameAt(100); got != 25 {
		t.Errorf("100 秒的片子应取第 25 秒，得到 %v", got)
	}
	if got := frameAt(4); got != 2 {
		t.Errorf("4 秒应取中段 2，得到 %v", got)
	}
	if got := frameAt(0); got != 1 {
		t.Errorf("时长未知时应取 1 秒，得到 %v", got)
	}
}

// TestWriteArtworkUsesPortraitCover 验「站方封面本身就是竖版海报时，poster 就用它」：
// krx18 只给 353×500 的 5:7 封面，它过不了「不放大能给 400 宽」的门槛，以前会被视频帧顶掉
// （实测 3355 的 poster.jpg 是黑屋子里两个人，跟片名毫无关系）。现在整张照搬：不裁、不放大。
func TestWriteArtworkUsesPortraitCover(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "movie")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "movie.mp4")
	// 横屏 1080p 的片子 + krx18 那种 353×500 的竖版封面
	makeTestVideo(t, video, 1920, 1080)
	makeTestImage(t, filepath.Join(tmp, "cover.jpg"), 353, 500)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(tmp, "cover.jpg"))
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(tmp, "art.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{DataDir: filepath.Join(tmp, "data"), PlatformProxy: map[string]string{}}
	r := buildRunner(t, st, platform.NewRegistry(), cfg)
	v := &model.Video{
		Platform: "krx18", PlatformVideoID: "77806-x", Title: "T",
		ThumbURL: srv.URL + "/cover.jpg", DurationSec: 4,
	}

	art := r.writeArtwork(context.Background(), v, video, dir, 4)
	if len(art.warnings) > 0 {
		t.Fatalf("不该有警告：%v", art.warnings)
	}
	w, h := dimsOf(t, art.poster)
	if w != 353 || h != 500 {
		t.Errorf("poster 应为封面的 353x500（5:7，原样不裁不放大），实为 %dx%d", w, h)
	}
	// 卡片小图 / 背景仍是视频帧（16:9），别一起换成竖图
	if w, h := dimsOf(t, art.thumb); w != 1280 || h != 720 {
		t.Errorf("thumb 应仍是 1280x720（视频帧），实为 %dx%d", w, h)
	}
	if w, h := dimsOf(t, art.fanart); w != 1920 || h != 1080 {
		t.Errorf("fanart 应仍是 1920x1080（视频帧），实为 %dx%d", w, h)
	}
}

// TestWriteArtworkPortraitCoverTooSmall：小到头像级别的「竖图」不当海报，
// 仍旧走「够大就官方图、不够就换一帧」的老规矩。
func TestWriteArtworkPortraitCoverTooSmall(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "movie")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "movie.mp4")
	makeTestVideo(t, video, 1920, 1080)
	makeTestImage(t, filepath.Join(tmp, "cover.jpg"), 120, 170)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(tmp, "cover.jpg"))
	}))
	defer srv.Close()

	st, err := store.Open(filepath.Join(tmp, "art2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{DataDir: filepath.Join(tmp, "data"), PlatformProxy: map[string]string{}}
	r := buildRunner(t, st, platform.NewRegistry(), cfg)
	v := &model.Video{Platform: "krx18", PlatformVideoID: "x", Title: "T",
		ThumbURL: srv.URL + "/cover.jpg", DurationSec: 4}

	art := r.writeArtwork(context.Background(), v, video, dir, 4)
	if w, h := dimsOf(t, art.poster); w != 720 || h != 1080 {
		t.Errorf("封面太小应退回视频帧（720x1080 @2:3），实为 %dx%d", w, h)
	}
}
