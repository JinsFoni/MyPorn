package jobs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/social"
	"github.com/JinsFoni/MyPorn/internal/store"
	"github.com/JinsFoni/MyPorn/internal/ytsub"
)

// 假 yt-dlp：只做两件事——吐一份 -J 的 JSON，和把一段 1 秒的视频"下"到 -P 指定的目录里。
// 真跑 yt-dlp 要联网、要几十秒，还会因为版本过期而红；这里要验的是它下游那条链路。
const fakeYtDlp = `#!/bin/sh
# -J 模式：给 VideoInfo / ListChannel 用
for a in "$@"; do
  if [ "$a" = "-J" ]; then
    cat <<'JSON'
{"id":"vidTEST01","title":"测试视频","description":"这段简介来自 yt-dlp 详情","duration":1,
 "view_count":4242,"timestamp":1727769600,"live_status":"not_live"}
JSON
    exit 0
  fi
done
# 下载模式：找到 -P <dir> 和 -o <模板>，把 1 秒 mp4 写进去
dir=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-P" ]; then dir="$a"; fi
  prev="$a"
done
echo "YTDLP 512000/1024000 1000000.0 1"
ffmpeg -y -hide_banner -loglevel error -f lavfi -i testsrc=size=320x180:rate=10:duration=1 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p "$dir/vidTEST01.mp4"
echo "YTDLP 1024000/1024000 1000000.0 0"
`

func writeFakeYtDlp(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过 yt-dlp 下载链路测试")
	}
	path := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(path, []byte(fakeYtDlp), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestYtDownloadJobEndToEnd 假设最糟的情况：**注册表里根本没有 youtube 这个平台**。
// 这正是实现时最容易漏的一处——yt_download 不该去 reg.Get("youtube")，
// 一旦有人照抄 runDownloadJob 的写法，这条测试就会红。
func TestYtDownloadJobEndToEnd(t *testing.T) {
	bin := writeFakeYtDlp(t)
	tmp := t.TempDir()
	libRoot := filepath.Join(tmp, "media")
	if err := os.MkdirAll(libRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	libID, err := st.CreateLibrary(&model.Library{Name: "主库", RootPath: libRoot, Enabled: true, IsDefault: true})
	if err != nil {
		t.Fatalf("建库失败: %v", err)
	}

	chID, _, err := st.UpsertYtChannel(&store.YtChannel{
		ChannelID: "UCq-Fj5jknLsUf-MWSy4_brA", Title: "测试频道", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 故意只写"平铺列表能给到的字段"：没有简介、没有发布时间。
	ids, err := st.UpsertYtVideos(chID, []store.YtVideoInput{
		{VideoID: "vidTEST01", Title: "测试视频", DurationSec: 1, Views: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	ytRowID := ids[0]

	cfg := &config.Config{DataDir: tmp, LibraryRoot: libRoot}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// 注册表里只有 pornhub：YouTube 没有 adapter，这本来就是事实。
	reg := platform.NewRegistry()
	r := NewRunner(cfg, st, reg, nil, nil, nil, events.NewHub(), log, imgcache.New(t.TempDir(), nil, "", log))
	dlp := ytsub.NewYtDlp(
		func() string { return bin },
		func() string { return "" },
		func() string { return "" },
		func() string { return "" },
		log,
	)
	r.SetYouTube(dlp, social.NewThrottle(0))

	jobID, err := r.EnqueueYtDownload(ytRowID, libID, false)
	if err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	if jobID == 0 {
		t.Fatal("应该拿到任务 id")
	}
	job, err := st.Claim(model.JobTypeYtDownload)
	if err != nil || job == nil {
		t.Fatalf("领取任务失败: %v", err)
	}
	// target_id 必须是 videos 的行 id（下载页按它查标题与本地文件）。
	video, err := st.GetVideo(job.TargetID)
	if err != nil {
		t.Fatalf("target_id 指向的不是 videos 行: %v", err)
	}
	if video.Platform != "youtube" || video.Title != "测试视频" {
		t.Errorf("videos 行不对: %+v", video)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: st, hub: events.NewHub(), log: log, job: job}
	if err := r.runYtDownloadJob(ctx, job, ctl); err != nil {
		t.Fatalf("yt_download 失败: %v", err)
	}

	// 落盘结构 = {库}/YouTube/{频道}/{标题}/，和普通影片同一套
	entries, err := os.ReadDir(filepath.Join(libRoot, "YouTube", "测试频道"))
	if err != nil {
		t.Fatalf("目录未生成: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("应有 1 个影片目录，得到 %d", len(entries))
	}
	dir := filepath.Join(libRoot, "YouTube", "测试频道", entries[0].Name())
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range files {
		names[f.Name()] = true
	}
	var mp4, nfo string
	for n := range names {
		switch {
		case strings.HasSuffix(n, ".mp4"):
			mp4 = n
		case strings.HasSuffix(n, ".nfo"):
			nfo = n
		}
	}
	if mp4 == "" {
		t.Fatalf("没有 mp4，目录里只有 %v", names)
	}

	// 库里要有一行 library_files，且指向真实文件
	lf, err := st.GetLibraryFile(job.TargetID, libID)
	if err != nil {
		t.Fatalf("library_files 没写入: %v", err)
	}
	if _, err := os.Stat(lf.VideoPath); err != nil {
		t.Errorf("库里记的路径不存在: %v", err)
	}
	if lf.SizeBytes <= 0 {
		t.Errorf("体积应大于 0，得到 %d", lf.SizeBytes)
	}

	// nfo 里的 plot 应该来自 yt-dlp 的简介，而不是标题
	if nfo != "" {
		b, err := os.ReadFile(filepath.Join(dir, nfo))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "这段简介来自 yt-dlp 详情") {
			t.Errorf("nfo 的 plot 没用 yt-dlp 的简介:\n%s", string(b))
		}
		if !strings.Contains(string(b), `type="youtube"`) {
			t.Errorf("nfo 缺少 youtube 的 uniqueid:\n%s", string(b))
		}
	}

	// 下载顺手补回的发布时间要写回 yt_videos（墙上「时间未知」就是这么消掉的）
	row, err := st.GetYtVideo(ytRowID)
	if err != nil {
		t.Fatal(err)
	}
	if row.PublishedAt == nil {
		t.Error("发布时间没回写")
	}
	if !row.Seen {
		t.Error("下载完应标记已看")
	}

	// 幂等：再点一次不该重新下载（先把上一个任务收尾，否则会被"已有在跑的任务"挡住）
	if err := st.FinishJob(jobID, model.JobDone, ""); err != nil {
		t.Fatal(err)
	}
	job2ID, err := r.EnqueueYtDownload(ytRowID, libID, false)
	if err != nil {
		t.Fatal(err)
	}
	if job2ID == 0 {
		t.Fatal("应能再次投递（上一个已结束）")
	}
	job2, err := st.Claim(model.JobTypeYtDownload)
	if err != nil || job2 == nil {
		t.Fatal("第二次领取失败")
	}
	ctl2 := &Ctl{ctx: ctx, cancel: func(error) {}, st: st, hub: events.NewHub(), log: log, job: job2}
	if err := r.runYtDownloadJob(ctx, job2, ctl2); err != nil {
		t.Fatalf("第二次任务应直接跳过而不是失败: %v", err)
	}
}

// TestYtDownloadPayloadMatchesDownloadPage 保证任务的 payload 用的是下载页认识的那套键名：
// 页面靠 libraryId / videoId 读出所属库和影片，键名一改就静默变成空白。
func TestYtDownloadPayloadMatchesDownloadPage(t *testing.T) {
	raw, err := json.Marshal(ytDownloadPayload{YtVideoID: 7, VideoID: 9, LibraryID: 3, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	var info DownloadPayloadInfo
	if err := ParseDownloadPayload(string(raw), &info); err != nil {
		t.Fatal(err)
	}
	if info.VideoID != 9 || info.LibraryID != 3 || !info.Overwrite {
		t.Errorf("下载页读不出 payload: %+v", info)
	}
}

// 慢吞吞的假 yt-dlp：什么都不产出，只是耗着。
// 像真的 yt-dlp 一样：收到 SIGINT 就收手（真 yt-dlp 会在这一下把 .part 保下来）。
const slowYtDlp = `#!/bin/sh
trap 'exit 130' INT
sleep 60 &
wait
`

// TestYtDownloadHonoursCancel 取消/暂停必须能把 yt-dlp 掐掉。
//
// 这里曾经有个真坑：下载阶段一直卡在子进程里，没人去查取消标志，
// 用户点了「取消」得等它下完才生效——几十秒的短片看不出来，几个 G 的长片就是灾难。
func TestYtDownloadHonoursCancel(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(bin, []byte(slowYtDlp), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	libRoot := filepath.Join(tmp, "media")
	if err := os.MkdirAll(libRoot, 0o755); err != nil {
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
	chID, _, err := st.UpsertYtChannel(&store.YtChannel{ChannelID: "UCcccccccccccccccccccccc", Title: "频道", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.UpsertYtVideos(chID, []store.YtVideoInput{{VideoID: "vidSLOW01", Title: "慢视频"}})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{DataDir: tmp, LibraryRoot: libRoot}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRunner(cfg, st, platform.NewRegistry(), nil, nil, nil, events.NewHub(), log, imgcache.New(t.TempDir(), nil, "", log))
	r.SetYouTube(ytsub.NewYtDlp(func() string { return bin }, func() string { return "" },
		func() string { return "" }, func() string { return "" }, log), social.NewThrottle(0))

	jobID, err := r.EnqueueYtDownload(ids[0], libID, false)
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.Claim(model.JobTypeYtDownload)
	if err != nil || job == nil {
		t.Fatalf("领取任务失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: st, hub: events.NewHub(), log: log, job: job}

	done := make(chan error, 1)
	go func() { done <- r.runYtDownloadJob(ctx, job, ctl) }()

	// 等它进到「下载中」，再点取消
	time.Sleep(1500 * time.Millisecond)
	if _, err := st.RequestCancel(jobID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消后任务不该正常返回")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("取消没有被及时响应：yt-dlp 还在跑")
	}
}

// 假 yt-dlp 的 flat 模式：只认 --flat-playlist，吐一个频道列表（带时长、没有时间戳）。
// 不碰 ffmpeg，所以这条测试在没有 ffmpeg 的机器上也能跑。
const fakeFlatYtDlp = `#!/bin/sh
for a in "$@"; do
  if [ "$a" = "--flat-playlist" ]; then
    cat <<'JSON'
{"id":"UCtest","title":"测试频道 - Videos","entries":[
 {"id":"vidFLAT00001","title":"一","duration":301},
 {"id":"vidFLAT00002","title":"二","duration":42}]}
JSON
    exit 0
  fi
done
echo '{}'
`

// TestYtMetaJobFillsDurationsFromFlatList 覆盖 2026-11-04 的修法：
// 观看页那条路被登录墙挡住之后（匿名抓 /watch 只剩 1.3MB 的空壳、零个 lengthSeconds），
// yt_meta 先用**频道 flat 列表**把时长一次性补掉——一次请求补一个频道，
// 不依赖登录态。剩下的（短视频 / 超列表深度）才逐条看观看页。
//
// 这里两条行都在 flat 列表里，所以第二遍应当一条都不剩、一次网络请求都不发。
func TestYtMetaJobFillsDurationsFromFlatList(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(bin, []byte(fakeFlatYtDlp), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	chID, _, err := st.UpsertYtChannel(&store.YtChannel{
		ChannelID: "UC7h5bqq_rzVxqhLyKtA7JRw", Title: "测试频道", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2026, 11, 3, 14, 18, 10, 0, time.UTC)
	// RSS 那条通道给的样子：有发布时间、**没有时长**。
	if _, err := st.UpsertYtVideos(chID, []store.YtVideoInput{
		{VideoID: "vidFLAT00001", Title: "一", PublishedAt: pub},
		{VideoID: "vidFLAT00002", Title: "二", PublishedAt: pub},
	}); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{DataDir: tmp}
	r := NewRunner(cfg, st, platform.NewRegistry(), nil, nil, nil, events.NewHub(), log,
		imgcache.New(t.TempDir(), nil, "", log))
	dlp := ytsub.NewYtDlp(
		func() string { return bin },
		func() string { return "" },
		func() string { return "" },
		func() string { return "" },
		log,
	)
	r.SetYouTube(dlp, social.NewThrottle(0))

	if _, err := r.EnqueueYtMeta(0, false); err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	job, err := st.Claim(model.JobTypeYtMeta)
	if err != nil || job == nil {
		t.Fatalf("领取任务失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ctl := &Ctl{ctx: ctx, cancel: func(error) {}, st: st, hub: events.NewHub(), log: log, job: job}
	if err := r.runYtMetaJob(ctx, job, ctl); err != nil {
		t.Fatalf("yt_meta 失败: %v", err)
	}

	for _, want := range []struct {
		videoID string
		dur     int
	}{{"vidFLAT00001", 301}, {"vidFLAT00002", 42}} {
		row, err := st.GetYtVideoByVideoID(want.videoID)
		if err != nil {
			t.Fatalf("%s 读不回来: %v", want.videoID, err)
		}
		if row.DurationSec != want.dur {
			t.Errorf("%s 时长 = %d，期望 %d（flat 列表里就有）", want.videoID, row.DurationSec, want.dur)
		}
		if row.PublishedAt == nil {
			t.Errorf("%s 的发布时间被补没了", want.videoID)
		}
	}
	gaps, err := st.CountYtVideosMissingMeta()
	if err != nil {
		t.Fatal(err)
	}
	if gaps.Rows != 0 || gaps.Stuck != 0 {
		t.Fatalf("两条都补上了，不该再有缺口: %+v", gaps)
	}
}
