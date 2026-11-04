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
	"sync/atomic"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
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

	// rcBad 记住「这个版本的 yt-dlp 不认 --remote-components」（老版本带上就整个跑不动）。
	// 第一次撞上 unrecognized arguments 就地置位，然后自动重跑一遍不带它的命令。
	rcBad atomic.Bool

	// jar 管 cookies.txt 的发/收：每轮发一份私有副本给 yt-dlp，跑完把它写回来的
	// 轮换结果存回设置（见 cookiejar.go）。nil = 没有落点（单测里常见），
	// 退回「每次一份临时文件、用完即删」的老做法。
	jar *CookieJar
}

// SetCookieJar 让 yt-dlp 用同一个稳定文件读写 cookies.txt。
func (y *YtDlp) SetCookieJar(j *CookieJar) { y.jar = j }

// NewYtDlp 构造调用器。pathFor 每次现取（设置页可改），返回空串时从 PATH 里找 yt-dlp。
func NewYtDlp(pathFor func() string, proxyFor func() string, cookies func() string, format func() string, log *slog.Logger) *YtDlp {
	if log == nil {
		log = slog.Default()
	}
	return &YtDlp{pathFor: pathFor, proxyFor: proxyFor, cookies: cookies, format: format, log: log}
}

// Bin 返回实际使用的可执行文件路径（找不到时返回空串）。
//
// 「找到了」必须是**真的能跑**：设置里填的绝对路径也要过一遍 os.Stat + 可执行位。
// 2026-11-02 的教训——/tmp/ytvenv/bin/yt-dlp 被 macOS 的定时 tmp 清理删了
// （同一目录里的 python 符号链接还在，所以目录看着正常），而这里当时把配置的路径
// 原样返回，于是 /api/yt/health 报 found=true、version=""，界面显示「版本新鲜」，
// 点开任何一条 YouTube 视频都是 502「fork/exec /tmp/ytvenv/bin/yt-dlp: no such file
// or directory」——用户看到的是「解析失败：<链接>」，像是 YouTube 抽风。
// 现在路径不可执行就当没找到，各调用点会给出「没有找到 yt-dlp，去设置里填路径」。
//
// 注意**不要**在这里退回去 LookPath 兜底：设置里明确填了某个版本，就应该用它或者
// 明说它不在；悄悄换成本机 brew 那个过期版本，只会让故障从「找不到」变成「403」。
//
// 唯一的例外是「填的是命令名而不是路径」（默认配置就是 "yt-dlp"，见 config.YtDlpPath）：
// 这种必须交给 PATH 去找——拿它去 os.Stat 会得到相对路径 "./yt-dlp"，永远不存在。
func (y *YtDlp) Bin() string {
	if y.pathFor != nil {
		if p := strings.TrimSpace(y.pathFor()); p != "" {
			if !strings.ContainsRune(p, os.PathSeparator) {
				if q, err := exec.LookPath(p); err == nil {
					return q
				}
				return ""
			}
			if executableFile(p) {
				return p
			}
			return ""
		}
	}
	p, err := exec.LookPath("yt-dlp")
	if err != nil {
		return ""
	}
	return p
}

// executableFile 报告 path 是不是一个能跑的文件：存在、不是目录、带可执行位。
func executableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode().Perm()&0o111 != 0
}

// Available 报告 yt-dlp 是否可用。
func (y *YtDlp) Available() bool { return y.Bin() != "" }

// VersionInfo 是给界面看的健康度。
//
// Error 只在「配了路径但用不了」时非空（文件没了 / 不可执行 / --version 跑不起来），
// 里面直接写人话加路径：这一格存在的意义就是回答「为什么下载总是失败」，
// 显示一行「yt-dlp 版本新鲜」而实际 fork 不了，比不显示更糟。
type VersionInfo struct {
	Path     string `json:"path"`
	Found    bool   `json:"found"`
	Version  string `json:"version"`
	AgeDays  int    `json:"ageDays"`
	Stale    bool   `json:"stale"`
	FFmpegOK bool   `json:"ffmpegOk"`
	Error    string `json:"error"`
}

// Version 读版本号并算出「距发布多少天」。
func (y *YtDlp) Version(ctx context.Context) VersionInfo {
	info := VersionInfo{}
	// 先把「用户填的那个路径」原样放进 Path：找不到时界面要能显示它到底往哪儿找过
	if y.pathFor != nil {
		info.Path = strings.TrimSpace(y.pathFor())
	}
	bin := y.Bin()
	if bin == "" {
		if info.Path != "" {
			info.Error = "这个路径上没有可执行的 yt-dlp：" + info.Path
		} else {
			info.Error = "没找到 yt-dlp：设置里没填路径，PATH 里也没有"
		}
		return info
	}
	info.Path = bin
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		info.FFmpegOK = true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "--version").Output()
	if err != nil {
		// 文件在、可执行位也有，但跑不起来（架构不符 / 依赖缺失 / 权限被拦）。
		// Found 保持 false：界面那一格问的是「能不能用」，不是「文件在不在」。
		info.Error = "yt-dlp 跑不起来：" + err.Error()
		return info
	}
	// 能跑通 --version 才算「可用」：这一格问的是能不能用，不是文件在不在。
	info.Found = true
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
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Duration     *float64 `json:"duration"`
	ViewCount    *int64   `json:"view_count"`
	Timestamp    *float64 `json:"timestamp"`
	LiveStatus   string   `json:"live_status"`
	Availability string   `json:"availability"`
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
	args, dropCookies, err := y.addCookies(args)
	defer dropCookies()
	if err != nil {
		y.log.Debug("Cookie 写不出来，这条照旧不带 cookie 跑", "err", err)
	}
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
			// 会员抢先看：flat 列表把它标成 subscriber_only。实测频道 4kaamg 的前 40 条里
			// **21 条**是它、19 条该字段为 null——不是偶发个例，一个频道可能大半都是。
			// 这里只**报告**事实，要不要入库由调用方决定（jobs 的 ytBackfill 会丢掉它，
			// 见那里的注释）。
			MembersOnly: e.Availability == "subscriber_only",
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
	args, dropCookies, err := y.addCookies(args)
	defer dropCookies()
	if err != nil {
		y.log.Debug("Cookie 写不出来，这条照旧不带 cookie 跑", "err", err)
	}
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
	args, dropCookies, cerr := y.addCookies(args)
	defer dropCookies()
	if cerr != nil {
		return "", fmt.Errorf("写入 Cookie 失败: %w", cerr)
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
	// 先 Wait 再等读 goroutine，顺序不能反：yt-dlp 自己会调 ffmpeg 合轨，
	// 那些孙进程也继承着这两根管道。只等 EOF 的话，取消之后要一直挂到它们自然结束
	// （实测 60 秒），`WaitDelay` 到期后 Wait 会把管道关掉，读取这边才收得了尾。
	// 代价是极端情况下丢掉最后几行进度，无所谓。
	waitErr := cmd.Wait()
	wg.Wait()
	if waitErr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if unknownRemoteComponents(errBuf.String()) && y.rcBad.CompareAndSwap(false, true) {
			return y.Download(ctx, req) // 老版本 yt-dlp：去掉那个参数重跑一次
		}
		return "", y.mapError(waitErr, errBuf.String())
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

// commonArgs 是每次调用都要带上的动态参数（代理 + 挑战求解）。
func (y *YtDlp) commonArgs() []string {
	args := []string{}
	if y.proxyFor != nil {
		if p := strings.TrimSpace(y.proxyFor()); p != "" {
			args = append(args, "--proxy", p)
		}
	}
	// 解 n 挑战的求解脚本（yt-dlp-ejs）。
	//
	// 2026-10-29 实测，视频 YFsMnJEbV1U：不带这个参数时，4K 的 AV1 画面（itag 401）
	// 与 m4a 音轨拿到的都是「半残」地址——头几 MB 能下，随后一律 403
	// （yt-dlp 自己也在 1.9% 处 HTTP 403，ffmpeg 直接 403，curl 拿到 302 但跟过去就 403）。
	// 带上之后同样两条地址能连续下完 540 MB + 5 MB，拼出的 mp4 音画俱全。
	// 求解脚本首次运行时从 yt-dlp-ejs 的 GitHub 拉一份（之后走缓存），本机的 deno 提供运行时；
	// 没有 JS 运行时的话 yt-dlp 只会打一条 warning，行为与不加此参数一致，不会把功能弄坏。
	if y.remoteComponentsArg() != nil {
		args = append(args, "--remote-components", "ejs:github")
	}
	return args
}

// remoteComponentsArg 报告能不能带 --remote-components（撞过 unrecognized arguments 就永久关掉）。
func (y *YtDlp) remoteComponentsArg() []string {
	if y.rcBad.Load() {
		return nil
	}
	return []string{"--remote-components", "ejs:github"}
}

// unknownRemoteComponents 判断这条 stderr 是不是「这个版本的 yt-dlp 不认 --remote-components」。
// 老版本（2025.11 之前）没有这个参数，如果不认还硬带着，整个 YouTube 功能都会报错。
func unknownRemoteComponents(stderr string) bool {
	s := strings.ToLower(stderr)
	if !strings.Contains(s, "remote-components") {
		return false
	}
	return strings.Contains(s, "unrecognized") || strings.Contains(s, "no such option") ||
		strings.Contains(s, "invalid option") || strings.Contains(s, "not found")
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
	// SIGINT 之后最多再等 3 秒：yt-dlp 收到中断会立刻收尾（.part 留着续传），
	// 万一它卡住（死掉的 DNS、僵住的 CDN、攥着管道的孙进程），这里负责把它 kill 掉，
	// 别让「取消」形同虚设。
	cmd.WaitDelay = 3 * time.Second
	return cmd
}

func (y *YtDlp) run(ctx context.Context, bin string, args ...string) (string, string, error) {
	cmd := y.command(ctx, bin, args...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil && unknownRemoteComponents(errBuf.String()) && y.rcBad.CompareAndSwap(false, true) {
		// 老版本 yt-dlp：去掉这个参数重跑一遍（rcBad 已经置位，不会再带上）。
		return y.run(ctx, bin, withoutRemoteComponents(args)...)
	}
	return out.String(), errBuf.String(), err
}

// withoutRemoteComponents 从参数里摘掉 "--remote-components ejs:github"。
func withoutRemoteComponents(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--remote-components" {
			i++ // 顺便跳过它的值
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// writeCookies 是「没有 jar」时的退路：把内容写进 dir 下的一次性文件（0600），用完即删。
// 这里单独抽出来是因为 yt-dlp 只认文件路径，不认环境变量。
//
// **生产路径不用它**：用完即删会让 yt-dlp 回写的轮换结果一起消失，登录态就活不过一轮
// （见 cookiejar.go 的 4238→2894 实测）。只有单测和「还没配落点」时才走这里。
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

// addCookies 把「当前配置的 cookies.txt」交给 yt-dlp 并追加 --cookies，返回收尾函数。
//
// 为什么要单独一个函数：**解析（-j）这条路原来一条 cookie 都不带**，只有下载那条带了。
// 2026-11-03 的现场是用户在设置里填好了 cookies，播放照样被要求人机验证——把库里那份
// cookies 导出成文件再手工跑 `yt-dlp -j`，报的却是「The provided YouTube account cookies
// are no longer valid」，也就是说登录态对解析一样是刚需（年龄限制/要验证的片子连清单都不给），
// 而当时 ResolveStream 的命令行里根本没有 --cookies。四条路径（播放清单、频道列表、单条信息、
// 下载）现在都从这里加。
//
// 配了 jar 就由 jar 发一份私有副本、并在收尾时把 yt-dlp 写回来的轮换结果存回设置
// （见 cookiejar.go；不这么做的话登录态活不过一轮）；没配才退到「用完即删」的临时文件。
// 失败不致命：写不出来就照旧不带 cookie 跑一次，让原因落在 yt-dlp 的报错上。
func (y *YtDlp) addCookies(args []string) ([]string, func(), error) {
	content := strings.TrimSpace(y.cookieText())
	if content == "" {
		return args, func() {}, nil
	}
	if y.jar != nil {
		path, done, err := y.jar.Checkout(content)
		if err != nil || path == "" {
			return args, func() {}, err
		}
		return append(args, "--cookies", path), done, nil
	}
	dir, err := os.MkdirTemp("", "myporn-ytcookies-")
	if err != nil {
		return args, func() {}, err
	}
	path, cleanup, err := writeCookies(dir, content)
	if err != nil || path == "" {
		os.RemoveAll(dir)
		return args, func() {}, err
	}
	return append(args, "--cookies", path), func() { cleanup(); os.RemoveAll(dir) }, nil
}

// mapError 把 yt-dlp 的报错翻成统一语义。
// 「版本过期」是这里最值钱的一条：它占了实际失败的绝大多数（§11.5）。
func (y *YtDlp) mapError(err error, stderr string) error {
	// stderr 里既有 WARNING 也有 ERROR：'这条片子怎么了' 只看 ERROR 行（yt-dlp 的 warning 里
	// 常有 `nsig extraction failed` 这类噪音，混着看会把原因判错），而 cookies 被轮换的提示
	// 偏偏是 WARNING——两个视图分开，各自取自己那段。
	warn := strings.ToLower(stderr)
	msg := strings.ToLower(errorLines(stderr) + " " + err.Error())
	switch {
	case strings.Contains(warn, "no longer valid") || strings.Contains(warn, "rotated in the browser"):
		// 2026-11-03 实测：填了一份「正常浏览器里导出」的 cookies，YouTube 第一条请求之后就把它
		// 清了（LOGIN_INFO/SID/APISID 全没），yt-dlp 原话就是这句。原因是浏览器那边还在轮换
		// __Secure-3PSIDTS，导出的是一份「用过就死」的副本。
		return social.Blocked("这份 cookies.txt 已经失效（YouTube 说它在浏览器里被轮换了）：用无痕窗口登录 YouTube → 导出 cookies → 立刻关掉那个窗口，再用导出的文件")
	case strings.Contains(msg, "sign in to confirm") || strings.Contains(msg, "not a bot"):
		return social.Blocked("YouTube 要求人机验证（出口 IP 被盯上了）：去设置里给 YouTube 填 cookies.txt（登录态最有效），或换一条真换出口 IP 的代理")
	case strings.Contains(msg, "http error 403"):
		return social.Blocked("YouTube 拒绝下载（403）：多半是 yt-dlp 过期或出口 IP 被限，先升级 yt-dlp 再试")
	case strings.Contains(msg, "http error 429") || strings.Contains(msg, "too many requests"):
		return social.RateLimited("YouTube 限流了，等一会儿再试", time.Now().Add(15*time.Minute))
	case strings.Contains(msg, "members-only") || strings.Contains(msg, "join this channel"):
		// 实测原话：`Join this channel to get access to members-only content like this video,
		// and other exclusive perks.` 这类视频我们根本不该入库（ytBackfill 会丢），
		// 除非它是「先会员、30 天后转公开」的那段时间里被人硬打开的。
		return social.NotFound("这条是「会员抢先观看」：加入该频道会员才能播放，本站不抓取这类视频")
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

// errorLines 只取 stderr 里以 `ERROR:` 开头的行（没有就返回空串）。
func errorLines(stderr string) string {
	var b strings.Builder
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ERROR:") {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
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
