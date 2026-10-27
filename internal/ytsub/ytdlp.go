package ytsub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mario/myporn/internal/social"
)

// YtDlp 是 yt-dlp 可执行文件的封装：列频道（深度回填）与下载视频。
//
// 为什么必须外挂 yt-dlp 而不是自己解析（docs/M6-YOUTUBE.md §11.4）：
// YouTube 的 streamingData 已经没有 progressive formats，adaptiveFormats 里连 url 字段都没了
// （在迁 SABR），签名与 PO token 天天变。yt-dlp 自己也在跟着改，但它是别人维护的，
// 我们只要保证「版本新鲜 + 失败时说得清楚」。
//
// 顺带一个实测结论（§11.5）：本机 homebrew 的 2026.07.04 直接 403，换成 2026.10.26 一次成功。
// 所以 Version() 的「过期多少天」是界面上的重要信息，不是摆设。

// DefaultFormat 是默认的格式选择串：
//
//	第一优先：AV1 视频（最高画质）+ 最高音质的音频
//	第二优先：任意编码的视频 + 最高音质音频
//	第三优先：任意单文件（保底能下下来）
//
// 注意 AV1 永远只有视频轨（YouTube 不提供 AV1+音轨的现成文件），
// 所以音视频合成一个文件这一步躲不掉，靠 yt-dlp 调 ffmpeg `-c copy` 封装——
// 只换容器、不重编码，实测 0.05 秒。
const DefaultFormat = "bv*[vcodec^=av01]+ba/bv*+ba/b"

// maxStaleDays 与 yt-dlp 自己的警告口径一致：超过 90 天就提示升级。
const maxStaleDays = 90

// YtDlp 是一个可复用的 yt-dlp 调用器。代理与 Cookie 每次调用现取，
// 这样设置页改完立刻生效，不需要重启（和站内 HTTP 客户端一个道理）。
type YtDlp struct {
	pathFor  func() string
	proxyFor func() string
	cookies  func() string
	format   func() string
	log      *slog.Logger
}

// NewYtDlp 构造调用器。pathFor 每次现取（设置页可改），返回空串时从 PATH 里找 yt-dlp。
func NewYtDlp(pathFor func() string, proxyFor func() string, cookies func() string, format func() string, log *slog.Logger) *YtDlp {
	if log == nil {
		log = slog.Default()
	}
	return &YtDlp{pathFor: pathFor, proxyFor: proxyFor, cookies: cookies, format: format, log: log}
}

// Bin 返回实际使用的可执行文件路径（找不到时返回空串）。
func (y *YtDlp) Bin() string {
	if y.pathFor != nil {
		if p := strings.TrimSpace(y.pathFor()); p != "" {
			return p
		}
	}
	p, err := exec.LookPath("yt-dlp")
	if err != nil {
		return ""
	}
	return p
}

// Available 报告 yt-dlp 是否可用。
func (y *YtDlp) Available() bool { return y.Bin() != "" }

// VersionInfo 是给界面看的健康度。
type VersionInfo struct {
	Path     string `json:"path"`
	Found    bool   `json:"found"`
	Version  string `json:"version"`
	AgeDays  int    `json:"ageDays"`
	Stale    bool   `json:"stale"`
	FFmpegOK bool   `json:"ffmpegOk"`
}

// Version 读版本号并算出「距发布多少天」。
func (y *YtDlp) Version(ctx context.Context) VersionInfo {
	info := VersionInfo{}
	bin := y.Bin()
	if bin == "" {
		return info
	}
	info.Path = bin
	info.Found = true
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		info.FFmpegOK = true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "--version").Output()
	if err != nil {
		return info
	}
	ver := strings.TrimSpace(string(out))
	if i := strings.LastIndex(ver, " "); i >= 0 {
		ver = ver[i+1:]
	}
	info.Version = ver
	if t, ok := parseVersionDate(ver); ok {
		info.AgeDays = int(time.Since(t).Hours() / 24)
		if info.AgeDays < 0 {
			info.AgeDays = 0
		}
		info.Stale = info.AgeDays > maxStaleDays
	}
	return info
}

// parseVersionDate 从 "2026.10.26" 这种版本号里取日期（yt-dlp 的版本号就是发布日）。
func parseVersionDate(ver string) (time.Time, bool) {
	parts := strings.Split(ver, ".")
	if len(parts) < 3 {
		return time.Time{}, false
	}
	day := parts[2]
	if i := strings.IndexFunc(day, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		day = day[:i]
	}
	t, err := time.Parse("2006.01.02", parts[0]+"."+parts[1]+"."+day)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ---- 列频道（深度回填）----

type flatItem struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Duration   *float64 `json:"duration"`
	ViewCount  *int64   `json:"view_count"`
	Timestamp  *float64 `json:"timestamp"`
	LiveStatus string   `json:"live_status"`
}

type flatOut struct {
	Entries []flatItem `json:"entries"`
}

// ListChannel 用 yt-dlp 的 flat 模式列一个频道的视频（默认前 limit 条）。
//
// 实测（§11.3）：200 条 5.8 秒，每条都带时长与播放量，但**没有任何时间戳**——
// 所以这里返回的 Video.PublishedAt 基本是零值，墙上的时间是靠 RSS 那条通道补的。
func (y *YtDlp) ListChannel(ctx context.Context, channelID string, limit int) ([]Video, error) {
	bin := y.Bin()
	if bin == "" {
		return nil, social.StaleParser("没有找到 yt-dlp：去设置 → YouTube 里填路径，或先装一个（版本越新越好）")
	}
	if limit <= 0 {
		limit = 200
	}
	url := BaseURL + "/channel/" + channelID + "/videos"
	args := []string{
		"-J", "--flat-playlist",
		"--playlist-end", strconv.Itoa(limit),
		"--no-warnings", "--socket-timeout", "20",
	}
	args = append(args, y.commonArgs()...)
	args = append(args, url)

	stdout, stderr, err := y.run(ctx, bin, args...)
	if err != nil {
		return nil, y.mapError(err, stderr)
	}
	var out flatOut
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		return nil, social.StaleParser("yt-dlp 的频道输出解析失败（版本可能过期）：" + err.Error())
	}
	videos := make([]Video, 0, len(out.Entries))
	for _, e := range out.Entries {
		if e.ID == "" || len(e.ID) > 20 {
			// 频道页有时会混进 playlist / channel 条目，11 位才是视频
			continue
		}
		v := Video{
			VideoID:      e.ID,
			Title:        e.Title,
			ThumbnailURL: ThumbURL(e.ID, "mqdefault"),
		}
		if strings.HasPrefix(e.LiveStatus, "is_live") || e.LiveStatus == "post_live" {
			v.IsLive = true
		}
		if e.Duration != nil {
			v.DurationSec = int(*e.Duration + 0.5)
		}
		if e.ViewCount != nil {
			v.Views = *e.ViewCount
		}
		if e.Timestamp != nil && *e.Timestamp > 0 {
			v.PublishedAt = time.Unix(int64(*e.Timestamp), 0).UTC()
			v.DateSource = "ytdlp"
		}
		videos = append(videos, v)
	}
	return videos, nil
}

// ---- 单条视频的完整信息 ----

type fullItem struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Duration    *float64 `json:"duration"`
	ViewCount   *int64   `json:"view_count"`
	Timestamp   *float64 `json:"timestamp"`
	UploadDate  string   `json:"upload_date"`
	LiveStatus  string   `json:"live_status"`
}

// VideoInfo 取一条视频的完整信息（含发布时间）。
//
// 为什么要单独一条：深度回填走 flat 模式，实测 200 条里 `timestamp` 一个都没有，
// 所以「时间未知」的条目要靠这个接口按需补（实测单条 4.1 秒，只在该条目被点开时才值得花）。
func (y *YtDlp) VideoInfo(ctx context.Context, videoID string) (Video, error) {
	bin := y.Bin()
	if bin == "" {
		return Video{}, social.StaleParser("没有找到 yt-dlp：去设置 → YouTube 里填路径，或先装一个（版本越新越好）")
	}
	args := []string{"-J", "--no-playlist", "--no-warnings", "--socket-timeout", "20"}
	args = append(args, y.commonArgs()...)
	args = append(args, BaseURL+"/watch?v="+videoID)

	stdout, stderr, err := y.run(ctx, bin, args...)
	if err != nil {
		return Video{}, y.mapError(err, stderr)
	}
	var it fullItem
	if err := json.Unmarshal([]byte(stdout), &it); err != nil {
		return Video{}, social.StaleParser("yt-dlp 的视频信息解析失败（版本可能过期）：" + err.Error())
	}
	v := Video{VideoID: videoID, Title: it.Title, Description: it.Description}
	if it.Duration != nil {
		v.DurationSec = int(*it.Duration + 0.5)
	}
	if it.ViewCount != nil {
		v.Views = *it.ViewCount
	}
	v.ThumbnailURL = ThumbURL(videoID, "mqdefault")
	if it.Timestamp != nil && *it.Timestamp > 0 {
		v.PublishedAt = time.Unix(int64(*it.Timestamp), 0).UTC()
		v.DateSource = "ytdlp"
	} else if t, err := time.Parse("20060102", it.UploadDate); err == nil {
		v.PublishedAt = t.UTC()
		v.DateSource = "ytdlp"
	}
	if strings.HasPrefix(it.LiveStatus, "is_live") || it.LiveStatus == "post_live" {
		v.IsLive = true
	}
	return v, nil
}

// ---- 下载 ----

// DownloadRequest 是一次下载的全部输入。
type DownloadRequest struct {
	VideoID    string
	WorkDir    string           // 工作目录（.part 放这里，重跑可续传）
	OnProgress func(p Progress) // 可为 nil
}

// Progress 是一行 yt-dlp 进度。
type Progress struct {
	Percent   float64
	Done      int64
	Total     int64
	Speed     float64 // 字节/秒
	ETASecond int64
}

// Download 下载一条视频，返回落盘后的文件绝对路径。
//
// 音视频分开下、最后合并：yt-dlp 会调 ffmpeg 做 `-c copy` 封装（只换容器不重编码）。
func (y *YtDlp) Download(ctx context.Context, req DownloadRequest) (string, error) {
	bin := y.Bin()
	if bin == "" {
		return "", social.StaleParser("没有找到 yt-dlp：去设置 → YouTube 里填路径，或先装一个（版本越新越好）")
	}
	if err := os.MkdirAll(req.WorkDir, 0o755); err != nil {
		return "", fmt.Errorf("创建临时目录失败: %w", err)
	}

	args := []string{
		"--no-playlist", "--newline", "--no-warnings", "--continue",
		"--socket-timeout", "20", "--retries", "3", "--concurrent-fragments", "4",
		"-f", y.formatString(),
		"--merge-output-format", "mp4",
		"-P", req.WorkDir,
		"-o", "%(id)s.%(ext)s",
		"--progress-template",
		"download:YTDLP %(progress.downloaded_bytes)s %(progress.total_bytes)s " +
			"%(progress.total_bytes_estimate)s %(progress.speed)s %(progress.eta)s",
	}
	args = append(args, y.commonArgs()...)
	cookiePath, cleanup, err := writeCookies(req.WorkDir, y.cookieText())
	if err != nil {
		return "", fmt.Errorf("写入临时 Cookie 失败: %w", err)
	}
	defer cleanup()
	if cookiePath != "" {
		args = append(args, "--cookies", cookiePath)
	}
	args = append(args, BaseURL+"/watch?v="+req.VideoID)

	cmd := y.command(ctx, bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", y.mapError(err, "")
	}

	var errBuf strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanProgress(stdout, req.OnProgress)
	}()
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				if errBuf.Len() < 64<<10 {
					errBuf.Write(buf[:n])
				}
			}
			if err != nil {
				return
			}
		}
	}()
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", y.mapError(err, errBuf.String())
	}

	// 产物名由 -o 模板决定，直接找目录里以 videoID 开头的成品（忽略 .part / 中间文件）。
	matches, err := filepath.Glob(filepath.Join(req.WorkDir, req.VideoID+".*"))
	if err != nil {
		return "", err
	}
	best := ""
	for _, m := range matches {
		if strings.HasSuffix(m, ".part") || strings.HasSuffix(m, ".ytdl") {
			continue
		}
		// 合并完成后 yt-dlp 会删掉中间的音视频文件，剩下的就是成品。
		if strings.HasSuffix(m, ".mp4") {
			return m, nil
		}
		if best == "" {
			best = m
		}
	}
	if best == "" {
		return "", fmt.Errorf("下载结束但没找到产物文件")
	}
	return best, nil
}

// scanProgress 逐行读 yt-dlp 的输出，把进度行交给回调。
func scanProgress(r io.Reader, cb func(Progress)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "YTDLP ") {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, "YTDLP "))
		if len(f) < 5 {
			continue
		}
		p := Progress{Done: parseInt(f[0]), Total: parseInt(f[1])}
		if p.Total <= 0 {
			p.Total = parseInt(f[2])
		}
		p.Speed = parseFloat(f[3])
		p.ETASecond = int64(parseFloat(f[4]))
		if p.Total > 0 {
			p.Percent = float64(p.Done) / float64(p.Total) * 100
			if p.Percent > 100 {
				p.Percent = 100
			}
		}
		if cb != nil {
			cb(p)
		}
	}
}

func (y *YtDlp) formatString() string {
	if y.format != nil {
		if f := strings.TrimSpace(y.format()); f != "" {
			return f
		}
	}
	return DefaultFormat
}

// cookieText 取当前配置的 cookies.txt 内容（Netscape 格式），空 = 不用 Cookie。
func (y *YtDlp) cookieText() string {
	if y.cookies == nil {
		return ""
	}
	return y.cookies()
}

// commonArgs 是每次调用都要带上的动态参数（代理）。
func (y *YtDlp) commonArgs() []string {
	args := []string{}
	if y.proxyFor != nil {
		if p := strings.TrimSpace(y.proxyFor()); p != "" {
			args = append(args, "--proxy", p)
		}
	}
	return args
}

// command 建子进程：取消时先 SIGINT（yt-dlp 会体面收尾、留住 .part），超时再强杀。
func (y *YtDlp) command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = cmd.Process.Signal(os.Interrupt)
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

func (y *YtDlp) run(ctx context.Context, bin string, args ...string) (string, string, error) {
	cmd := y.command(ctx, bin, args...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

// cookiesArg 在有 Cookie 时把内容写进工作目录的临时文件（0600），用完即删。
// 这里单独抽出来是因为 yt-dlp 只认文件路径，不认环境变量。
func writeCookies(dir, content string) (string, func(), error) {
	if strings.TrimSpace(content) == "" {
		return "", func() {}, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", func() {}, err
	}
	f, err := os.CreateTemp(dir, "cookies-*.txt")
	if err != nil {
		return "", func() {}, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// mapError 把 yt-dlp 的报错翻成统一语义。
// 「版本过期」是这里最值钱的一条：它占了实际失败的绝大多数（§11.5）。
func (y *YtDlp) mapError(err error, stderr string) error {
	msg := strings.ToLower(stderr + " " + err.Error())
	switch {
	case strings.Contains(msg, "sign in to confirm") || strings.Contains(msg, "not a bot"):
		return social.Blocked("YouTube 要求人机验证（出口 IP 被盯上了）：去设置里给 YouTube 换个代理")
	case strings.Contains(msg, "http error 403"):
		return social.Blocked("YouTube 拒绝下载（403）：多半是 yt-dlp 过期或出口 IP 被限，先升级 yt-dlp 再试")
	case strings.Contains(msg, "http error 429") || strings.Contains(msg, "too many requests"):
		return social.RateLimited("YouTube 限流了，等一会儿再试", time.Now().Add(15*time.Minute))
	case strings.Contains(msg, "video unavailable") || strings.Contains(msg, "private video") ||
		strings.Contains(msg, "removed by the uploader"):
		return social.NotFound("这条视频已不可用（私有 / 已删除 / 分区限制）")
	case strings.Contains(msg, "unable to extract") || strings.Contains(msg, "failed to parse") ||
		strings.Contains(msg, "nsig") || strings.Contains(msg, "player response"):
		return social.StaleParser("YouTube 的接口又变了，先升级 yt-dlp（它每天都在跟）")
	case strings.Contains(msg, "requested format is not available"):
		return social.StaleParser("没有符合当前格式串的流，去设置里放宽 YouTube 的格式串")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if stderr != "" {
		return social.Upstream("yt-dlp 失败："+tail(strings.TrimSpace(stderr), 300), err)
	}
	return social.Upstream("yt-dlp 失败", err)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func parseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSuffix(s, ".0"), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSuffix(s, ".0"), 64)
	if err != nil {
		return 0
	}
	return f
}
