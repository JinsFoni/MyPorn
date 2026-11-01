package pornhub

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/download"
	"github.com/JinsFoni/MyPorn/internal/hls"
)

// TestLiveHLSSegmentDownload 是可选的真实网络冒烟测试（LIVE=1 才跑）：
// 用自研分段下载器从真实 CDN 拉 3 个分片，再用 ffmpeg -c copy 封装成 mp4，
// 以此证明「分片下载 + remux」这条链路在真实站点上成立（而不只是 httptest 假 CDN）。
func TestLiveHLSSegmentDownload(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("设置 LIVE=1 才跑真实网络测试")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过")
	}
	vkey := os.Getenv("LIVE_VKEY")
	if vkey == "" {
		vkey = "6798a78e9c631"
	}

	a, err := New("https://cn.pornhub.com", noProxy, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	// 签名地址偶尔会被 CDN 判 410（重复使用/会话漂移），真实业务里也是重新解析一次，
	// 这里同样重试几次，避免冒烟测试随机红。
	var masterURL string
	var master []byte
	for attempt := 1; attempt <= 3; attempt++ {
		page, perr := a.get(ctx, "/view_video.php", url.Values{"viewkey": {vkey}})
		if perr != nil {
			t.Fatalf("抓视频页失败: %v", perr)
		}
		masterURL = ""
		for _, d := range parseMediaDefinitions(page) {
			if strings.Contains(d.VideoURL, ".m3u8") {
				masterURL = d.VideoURL
				break
			}
		}
		if masterURL == "" {
			t.Skip("该视频没有 HLS 清单（只有直链 mp4），换一个 viewkey 再试")
		}
		master, err = a.getAbsolute(ctx, masterURL)
		if err == nil {
			break
		}
		t.Logf("第 %d 次拉 master 失败（%v），重新解析页面", attempt, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		// 410 是 CDN 对签名地址的拒绝（同一会话里签名被「用过」或被限流），
		// 与下载器实现无关，跳过而不是判失败，免得冒烟测试随机变红。
		if strings.Contains(err.Error(), "410") {
			t.Skipf("CDN 拒绝该签名地址，稍后重跑：%v", err)
		}
		t.Fatalf("拉 master 清单失败: %v", err)
	}
	variantURL := hls.BestChildURL(string(master), masterURL)
	if variantURL == "" {
		t.Fatal("master 里没有可用子清单")
	}

	dl := download.New(a.client)
	dl.Referer = "https://cn.pornhub.com/"
	pl, err := dl.Resolve(ctx, variantURL)
	if err != nil {
		t.Fatalf("解析 media 清单失败: %v", err)
	}
	if len(pl.Segments) < 3 {
		t.Fatalf("分片太少: %d", len(pl.Segments))
	}
	t.Logf("清单：%d 个分片，总时长 %.1fs，加密=%v", len(pl.Segments), pl.TotalDuration, pl.Encrypted)

	// 只取前 3 个分片，验证下载与封装即可（不必拉整部片）。
	pl.Segments = pl.Segments[:3]
	pl.TotalDuration = 0
	for _, s := range pl.Segments {
		pl.TotalDuration += s.Duration
	}

	work := t.TempDir()
	var lastPct float64
	tsPath := filepath.Join(work, "part.ts")
	err = dl.Run(ctx, pl, work, tsPath, func(p download.Progress) { lastPct = p.Percent })
	if err != nil {
		t.Fatalf("分片下载失败: %v", err)
	}
	st, err := os.Stat(tsPath)
	if err != nil || st.Size() == 0 {
		t.Fatalf("下载结果为空: %v", err)
	}
	t.Logf("已下载 %d 个分片，%.1f MB，最后进度 %.0f%%", len(pl.Segments), float64(st.Size())/1e6, lastPct)

	mp4 := filepath.Join(work, "part.mp4")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", tsPath, "-c", "copy", mp4)
	if outp, cerr := cmd.CombinedOutput(); cerr != nil {
		t.Fatalf("remux 失败: %v %s", cerr, outp)
	}
	probe := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries",
		"format=duration,size", "-of", "csv=p=0", mp4)
	pb, perr := probe.Output()
	if perr != nil {
		t.Fatalf("ffprobe 失败: %v", perr)
	}
	fields := strings.Split(strings.TrimSpace(string(pb)), ",")
	if len(fields) < 2 {
		t.Fatalf("ffprobe 输出异常: %s", pb)
	}
	t.Logf("remux 结果：时长=%ss 体积=%s 字节", fields[0], fields[1])
	if fields[0] == "N/A" || fields[0] == "" {
		t.Fatalf("封装后没有时长，文件可能损坏")
	}
}
