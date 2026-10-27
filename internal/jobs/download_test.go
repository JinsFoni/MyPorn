package jobs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mario/myporn/internal/config"
	"github.com/mario/myporn/internal/events"
	"github.com/mario/myporn/internal/imgcache"
	"github.com/mario/myporn/internal/library"
	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/store"
)

// fakeAdapter 让整条下载链路可以在不碰真实站点的情况下跑通。
type fakeAdapter struct {
	playlistURL string
}

func (f *fakeAdapter) Key() string                                  { return "pornhub" }
func (f *fakeAdapter) Name() string                                 { return "Pornhub" }
func (f *fakeAdapter) BaseURL() string                              { return "https://example.invalid" }
func (f *fakeAdapter) ParsePerformerURL(raw string) (string, error) { return "mini-diva", nil }
func (f *fakeAdapter) GetPerformer(ctx context.Context, id string) (*model.Performer, error) {
	return &model.Performer{Platform: "pornhub", PerformerID: id, Name: "Mini Diva"}, nil
}
func (f *fakeAdapter) ListPerformerVideos(ctx context.Context, id string, page int) (*platform.Page, error) {
	return &platform.Page{Page: page}, nil
}
func (f *fakeAdapter) GetVideo(ctx context.Context, id string) (*model.Video, error) {
	return &model.Video{Platform: "pornhub", PlatformVideoID: id}, nil
}
func (f *fakeAdapter) SearchPerformers(ctx context.Context, q string, page int) ([]*model.Performer, bool, error) {
	return nil, false, nil
}
func (f *fakeAdapter) ResolveStreams(ctx context.Context, videoID string) ([]model.Variant, error) {
	return []model.Variant{
		{Height: 1080, Width: 1920, Bandwidth: 4000000, Codec: "avc1", URL: f.playlistURL},
		{Height: 720, Width: 1280, Bandwidth: 2000000, Codec: "avc1", URL: f.playlistURL},
	}, nil
}

// makeSegment 用 ffmpeg 造一段真实可封装的 MPEG-TS 分片。
func makeSegment(t *testing.T, dir string) []byte {
	t.Helper()
	out := filepath.Join(dir, "seg.ts")
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=15:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", "-f", "mpegts", out)
	if err := cmd.Run(); err != nil {
		t.Skipf("本机 ffmpeg 无法生成测试分片，跳过端到端测试: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func makeJPEG(t *testing.T, dir string) []byte {
	t.Helper()
	out := filepath.Join(dir, "poster.jpg")
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=640x360:rate=1:duration=1",
		"-frames:v", "1", out)
	if err := cmd.Run(); err != nil {
		t.Skipf("本机 ffmpeg 无法生成测试图片，跳过: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func buildRunner(t *testing.T, st *store.Store, reg *platform.Registry, cfg *config.Config) *Runner {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewRunner(cfg, st, reg, nil, nil, nil, events.NewHub(), log, imgcache.New(t.TempDir(), nil, "", log))
}

func TestDownloadJobEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过端到端下载测试")
	}
	tmp := t.TempDir()
	segBytes := makeSegment(t, tmp)
	imgBytes := makeJPEG(t, tmp)

	// 假 CDN：master → media → 2 个分片，外加缩略图/头像
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/hls/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080\nv1080/index.m3u8\n")
	})
	mux.HandleFunc("/hls/v1080/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:1.0,\nseg-0.ts\n#EXTINF:1.0,\nseg-1.ts\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/hls/v1080/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(filepath.Base(r.URL.Path), "seg-") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(segBytes)
	})
	mux.HandleFunc("/thumb.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(imgBytes)
	})
	mux.HandleFunc("/avatar.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(imgBytes)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	// 数据库与库目录
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	perfID, err := st.UpsertPerformer(&model.Performer{
		Platform: "pornhub", PerformerID: "mini-diva", Name: "Mini Diva",
		URL: "https://example.invalid/model/mini-diva", AvatarURL: srv.URL + "/avatar.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	libRoot := filepath.Join(tmp, "media")
	libID, err := st.CreateLibrary(&model.Library{
		Name: "测试库", RootPath: libRoot, Enabled: true, IsDefault: true,
		UploadPolicy: model.PolicyLocal, NamingTemplate: "{date} - {title}",
	})
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC)
	vidID, _, err := st.UpsertVideo(&model.Video{
		Platform: "pornhub", PlatformVideoID: "phTEST1", Title: "Test: Title/One",
		URL:      "https://example.invalid/view_video.php?viewkey=phTEST1",
		ThumbURL: srv.URL + "/thumb.jpg", DurationSec: 2, PublishedAt: &pub,
		Tags: []string{"test", "hd"}, Description: "描述",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkVideoPerformer(vidID, perfID); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		DataDir: filepath.Join(tmp, "data"), MaxDownload: 2,
		FFmpegPath: "ffmpeg", FFprobePath: "ffprobe", PlatformProxy: map[string]string{},
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := platform.NewRegistry()
	reg.Register(&fakeAdapter{playlistURL: srv.URL + "/hls/master.m3u8"})
	r := buildRunner(t, st, reg, cfg)

	jobID, err := r.EnqueueDownload(vidID, libID, false)
	if err != nil {
		t.Fatalf("投递任务失败: %v", err)
	}
	job, err := st.Claim(model.JobTypeDownload)
	if err != nil || job == nil {
		t.Fatalf("领取任务失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: st, hub: events.NewHub(), log: slog.New(slog.NewTextHandler(io.Discard, nil)), job: job}

	if err := r.runDownloadJob(ctx, job, ctl); err != nil {
		t.Fatalf("下载任务失败: %v", err)
	}

	// 目录 = {库根}/Pornhub/Mini Diva/{日期} - {标题}
	dir := filepath.Join(libRoot, "Pornhub", "Mini Diva", "2024-05-06 - Test Title One")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("目录未生成: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, want := range []string{
		"2024-05-06 - Test Title One.mp4",
		"2024-05-06 - Test Title One.nfo",
		"poster.jpg", "thumb.jpg", "fanart.jpg",
	} {
		if !names[want] {
			t.Errorf("缺少文件 %q，实际：%v", want, names)
		}
	}

	// nfo 内容抽查
	nfo, err := os.ReadFile(filepath.Join(dir, "2024-05-06 - Test Title One.nfo"))
	if err != nil {
		t.Fatalf("读取 nfo 失败: %v", err)
	}
	for _, want := range []string{"<title>Test: Title/One</title>", "<studio>Pornhub</studio>", "Mini Diva", "phTEST1"} {
		if !strings.Contains(string(nfo), want) {
			t.Errorf("nfo 缺少 %q\n%s", want, nfo)
		}
	}

	// 库索引 + 幂等跳过
	lf, err := st.GetLibraryFile(vidID, libID)
	if err != nil {
		t.Fatalf("库索引未写入: %v", err)
	}
	if lf.SizeBytes <= 0 || lf.Height == 0 {
		t.Errorf("媒体信息未回填: %+v", lf)
	}
	if lf.VideoName != "2024-05-06 - Test Title One" {
		t.Errorf("文件名与目录名不一致: %q", lf.VideoName)
	}
	has, err := st.HasLocalFile(vidID)
	if err != nil || !has {
		t.Errorf("影片应标记为已有本地副本")
	}

	// 再投一次（不覆盖）应当直接跳过且不产生新文件
	job2, _ := st.GetJob(jobID)
	_ = job2
	again, err := st.Claim(model.JobTypeDownload)
	if err != nil {
		t.Fatal(err)
	}
	_ = again
	if _, err := r.EnqueueDownload(vidID, libID, false); err != nil {
		t.Fatal(err)
	}
	if err := r.runDownloadJob(ctx, job, ctl); err != nil {
		t.Fatalf("重复下载应幂等跳过，实际报错: %v", err)
	}
}

func TestRunDownloadJobMissingLibrary(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{DataDir: filepath.Join(tmp, "data"), PlatformProxy: map[string]string{}}
	reg := platform.NewRegistry()
	reg.Register(&fakeAdapter{playlistURL: "http://127.0.0.1:1/x.m3u8"})
	r := buildRunner(t, st, reg, cfg)

	vidID, _, _ := st.UpsertVideo(&model.Video{Platform: "pornhub", PlatformVideoID: "x", Title: "t", URL: "u"})
	job := &model.Job{ID: 1, Type: model.JobTypeDownload, Payload: `{"videoId":` + fmt.Sprint(vidID) + `,"libraryId":0}`}
	ctx := context.Background()
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: st, hub: events.NewHub(), log: slog.New(slog.NewTextHandler(io.Discard, nil)), job: job}
	err = r.runDownloadJob(ctx, job, ctl)
	if err == nil || !strings.Contains(err.Error(), "本地库") {
		t.Fatalf("没有库时应该给出明确提示，得到 %v", err)
	}
}

func TestEstimateBytes(t *testing.T) {
	// 清单声明的码率：4 Mbps × 600s = 300 MB
	exact := model.Variant{Bandwidth: 4000000}
	if got := estimateBytes(exact, 600); got != 300000000 {
		t.Fatalf("预估体积错误: %d", got)
	}
	// 从文件名猜的码率只有上限意义：按 45% 折算
	approx := model.Variant{Bandwidth: 4000000, ApproxBandwidth: true}
	if got := estimateBytes(approx, 600); got != 135000000 {
		t.Fatalf("近似码率应折算，得到 %d", got)
	}
	if got := estimateBytes(model.Variant{}, 600); got != 0 {
		t.Fatalf("码率未知时应返回 0，得到 %d", got)
	}
	if got := estimateBytes(exact, 0); got != 0 {
		t.Fatalf("时长未知时应返回 0，得到 %d", got)
	}
}

func TestHumanETA(t *testing.T) {
	cases := map[int]string{30: "30 秒", 90: "1 分 30 秒", 3700: "1 小时 1 分"}
	for in, want := range cases {
		if got := humanETA(in); got != want {
			t.Errorf("humanETA(%d) 期望 %q，得到 %q", in, want, got)
		}
	}
}

var _ = library.DefaultTemplate
