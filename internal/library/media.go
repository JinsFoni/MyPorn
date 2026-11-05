package library

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/muxcache"
)

// Encoder 封装 ffmpeg / ffprobe 调用。只做 remux 与图片缩放，永不做转码。
type Encoder struct {
	FFmpeg  string
	FFprobe string
}

// NewEncoder 按显式路径或 PATH 里的默认名构造。
func NewEncoder(ffmpeg, ffprobe string) *Encoder {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	return &Encoder{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

// Check 校验 ffmpeg 是否可用，启动时调用一次，缺失只影响下载任务的封装步骤。
func (e *Encoder) Check(ctx context.Context) error {
	if _, err := exec.LookPath(e.FFmpeg); err != nil {
		return fmt.Errorf("找不到 ffmpeg（%s）: %w", e.FFmpeg, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, e.FFmpeg, "-hide_banner", "-version").CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg 不可用: %w (%s)", err, firstLine(out))
	}
	return nil
}

// MediaInfo 是 remux 之后回填到库索引里的媒体信息。
type MediaInfo struct {
	Width       int
	Height      int
	DurationSec int
	VideoCodec  string
	AudioCodec  string
	BitRate     int64
	SizeBytes   int64
}

// Probe 读取媒体文件的宽高、时长与编码。
func (e *Encoder) Probe(ctx context.Context, path string) (MediaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.FFprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,codec_name",
		"-show_entries", "format=duration,bit_rate",
		"-of", "json", path)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderr, max: 4096}
	out, err := cmd.Output()
	if err != nil {
		return MediaInfo{}, fmt.Errorf("ffprobe 失败: %w %s", err, strings.TrimSpace(stderr.String()))
	}
	var payload struct {
		Streams []struct {
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return MediaInfo{}, fmt.Errorf("解析 ffprobe 输出: %w", err)
	}
	info := MediaInfo{}
	if len(payload.Streams) > 0 {
		info.Width = payload.Streams[0].Width
		info.Height = payload.Streams[0].Height
		info.VideoCodec = payload.Streams[0].CodecName
	}
	if d, err := strconv.ParseFloat(payload.Format.Duration, 64); err == nil {
		info.DurationSec = int(d + 0.5)
	}
	if br, err := strconv.ParseInt(payload.Format.BitRate, 10, 64); err == nil {
		info.BitRate = br
	}
	if st, err := os.Stat(path); err == nil {
		info.SizeBytes = st.Size()
	}
	return info, nil
}

// Remux 把拼接好的分段流封装成 mp4；音频容器不兼容时退化为 mkv（仍然只 copy）。
// 返回实际产出的文件路径（扩展名可能是 .mkv）。
func (e *Encoder) Remux(ctx context.Context, input, desiredBase string) (string, error) {
	mp4 := desiredBase + ".mp4"
	err := e.remuxTo(ctx, input, mp4, "mp4")
	if err == nil {
		return mp4, nil
	}
	mp4Err := err
	mkv := desiredBase + ".mkv"
	if err2 := e.remuxTo(ctx, input, mkv, "matroska"); err2 != nil {
		return "", fmt.Errorf("封装失败：mp4 %v；mkv %v", mp4Err, err2)
	}
	return mkv, nil
}

func (e *Encoder) remuxTo(ctx context.Context, input, output, format string) error {
	tmp := output + ".tmp"
	_ = os.Remove(tmp)
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", input,
		"-map", "0:v:0", "-map", "0:a:0?",
		"-c", "copy",
	}
	if format == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, "-f", format, tmp)

	cmd := exec.CommandContext(ctx, e.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderr, max: 8192}
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(tmp, output); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ResizeImage 把图片按「填满后居中裁切」的画面比例出图（用于 poster/thumb/fanart）。
//
// **目标尺寸只是上限，不放大**：源图比目标小时按源图能给的最大尺寸出图。
// 2026-11-02 之前这里是硬缩放（`scale=w:h:force_original_aspect_ratio=increase,crop=w:h`），
// 而站方缩略图普遍很小——YouTube 的 `hqdefault` 只有 480×360（实测 13034 字节）、
// pornhub 的 thumb URL 里直接写死 `rs:fit:320:180`、艺人头像常常 399×399。
// 480×360 塞进 1000×1500 的海报＝先放大 4.17 倍到 2000×1500 再从中裁走 1000×1500，
// 海报里真正的像素只有 240×360，糊到看不出人。
// 现在一律「先裁到目标比例 → 只缩不放」：宁可交一张 720×1080 的海报，也不交 4 倍放大的糊图
// （Emby / 浏览器的海报格子也就 300–500px 宽，720 宽绰绰有余）。
func (e *Encoder) ResizeImage(ctx context.Context, input, output string, w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("目标尺寸非法 %dx%d", w, h)
	}
	sw, sh, err := e.ImageInfo(ctx, input)
	if err != nil || sw <= 0 || sh <= 0 {
		return fmt.Errorf("读不出底图尺寸: %w", err)
	}
	cw, ch := CoverCrop(sw, sh, w, h)
	ow, oh := FitWithin(cw, ch, w, h)
	filter := fmt.Sprintf("crop=%d:%d:(iw-%d)/2:(ih-%d)/2,scale=%d:%d", cw, ch, cw, ch, ow, oh)
	return e.imageFilter(ctx, input, output, filter)
}

// FitImage 把图片等比缩到不超过 maxW×maxH（只缩不放），**不裁切、不改比例**。
//
// 和 ResizeImage 的分工：那个按目标比例居中裁（卡片 / 背景要固定 16:9、海报要 2:3 时用它），
// 这个整张照搬。用武之地是**站方海报本身就是一版设计好的竖图**（krx18 的 5:7 封面，
// 韩文片名压在画面上）——按 2:3 裁会从两侧各切掉十几个像素，可能正好切到字。
// 实测 krx18 只给一档 353×500，所以这里也只缩不放，不硬吹成 1000×1500。
func (e *Encoder) FitImage(ctx context.Context, input, output string, maxW, maxH int) error {
	if maxW <= 0 || maxH <= 0 {
		return fmt.Errorf("目标尺寸非法 %dx%d", maxW, maxH)
	}
	sw, sh, err := e.ImageInfo(ctx, input)
	if err != nil || sw <= 0 || sh <= 0 {
		return fmt.Errorf("读不出底图尺寸: %w", err)
	}
	ow, oh := FitWithin(sw, sh, maxW, maxH)
	return e.imageFilter(ctx, input, output, fmt.Sprintf("scale=%d:%d", ow, oh))
}

// imageFilter 跑一遍 ffmpeg：只处理第一帧、jpg q3、最多等 60 秒。
func (e *Encoder) imageFilter(ctx context.Context, input, output, filter string) error {
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", input,
		"-vf", filter,
		"-frames:v", "1",
		"-q:v", "3",
		output,
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderr, max: 4096}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("图片处理失败: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ImageSize 读取图片宽高，用于判断原图是否竖版。
func ImageSize(path string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	return decodeConfig(f)
}

// CoverCrop 返回「按目标比例填满、且不超过源图」的裁切区域（源图像素，居中裁）。
//
// 目标比源图更竖（或更方）时裁掉左右，更宽时裁掉上下；比例一致时就是整张图。
// 关键在于**永远不会超过源图**：区域一旦比源图大就得靠放大来填，那正是糊的来源。
func CoverCrop(sw, sh, tw, th int) (int, int) {
	if sw <= 0 || sh <= 0 || tw <= 0 || th <= 0 {
		return 0, 0
	}
	cw := sw
	if lim := float64(sh) * float64(tw) / float64(th); lim < float64(sw) {
		cw = int(math.Round(lim))
	}
	if cw < 1 {
		cw = 1
	}
	ch := int(math.Round(float64(cw) * float64(th) / float64(tw)))
	if ch > sh {
		ch = sh
	}
	if ch < 1 {
		ch = 1
	}
	return cw, ch
}

// FitWithin 把 w×h 等比缩到不超过 maxW×maxH（**只缩不放**）。
func FitWithin(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 || maxW <= 0 || maxH <= 0 {
		return 0, 0
	}
	if w <= maxW && h <= maxH {
		return w, h
	}
	s := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	ow := int(math.Round(float64(w) * s))
	oh := int(math.Round(float64(h) * s))
	if ow < 1 {
		ow = 1
	}
	if oh < 1 {
		oh = 1
	}
	return ow, oh
}

// ImageInfo 读出图片宽高。
//
// 先走标准库（jpeg/png/gif，够快），失败再走 ffprobe：站方缩略图常是 webp / avif
// （bilibili 的 thumb 就是 `@960w_540h_1c.webp`，pornhub 按 `Accept: image/avif` 直接返 avif），
// Go 标准库这两个都不认。没有尺寸就没法判断「这张底图够不够大」，所以这一步必须靠得住。
func (e *Encoder) ImageInfo(ctx context.Context, path string) (int, int, error) {
	if w, h, err := ImageSize(path); err == nil && w > 0 && h > 0 {
		return w, h, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, e.FFprobe,
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", path).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe 读图片失败: %w", err)
	}
	line := firstLine(out)
	wStr, hStr, ok := strings.Cut(line, "x")
	if !ok {
		return 0, 0, fmt.Errorf("ffprobe 输出看不懂: %q", line)
	}
	w, _ := strconv.Atoi(strings.TrimSpace(wStr))
	h, _ := strconv.Atoi(strings.TrimSpace(hStr))
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("尺寸非法: %q", line)
	}
	return w, h, nil
}

// ExtractFrame 从视频里取一帧当图片底图（poster / thumb / fanart 用）。
//
// 下载完的那份文件就是最高画质的那一份（库里 4K / 8K 的片子很常见），
// 从里面裁出来的海报比站方 480×360 的缩略图清楚一个量级。atSec 是取帧位置（秒，从片头算）。
func (e *Encoder) ExtractFrame(ctx context.Context, video string, atSec float64, dst string) error {
	if atSec < 0 {
		atSec = 0
	}
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error", "-nostdin",
		// -ss 放在 -i 前面＝按关键帧快速定位，只需要一张静帧，不必整段解码
		"-ss", strconv.FormatFloat(atSec, 'f', 3, 64),
		"-i", video,
		"-frames:v", "1",
		"-q:v", "2",
		dst,
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.FFmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderr, max: 4096}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("取帧失败: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// limitedWriter 只保留前 max 字节，避免 ffmpeg 日志吃掉内存。
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remain := w.max - w.buf.Len()
	if remain > 0 {
		if len(p) > remain {
			w.buf.Write(p[:remain])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// RemoveTemp 清理临时目录（忽略错误）。
func RemoveTemp(dir string) {
	if dir == "" || !strings.Contains(filepath.Base(dir), ".myporn-") {
		return
	}
	_ = os.RemoveAll(dir)
}

// RemuxFromURL 把远端 HLS 地址直接交给 ffmpeg 拉流并 remux（下载器兜底路径）。
// totalSec 用于把 ffmpeg 的进度换算成百分比；onProgress 可为 nil。
func (e *Encoder) RemuxFromURL(ctx context.Context, rawURL, referer, ua string,
	desiredBase string, totalSec int, onProgress func(percent float64, bytesWritten int64)) (string, error) {
	return e.remuxFromURLs(ctx, []string{rawURL}, referer, ua, desiredBase, totalSec, onProgress)
}

// RemuxFromURLs 是「音画分轨」的下载路径：画面一条、声音一条，合起来 remux 成一份。
//
// B 站 1080P 以上只给 DASH（video.m4s + audio.m4s），两条流谁也不带对方，
// 下载下来必须当场合流，否则片子只有画面没声音。
func (e *Encoder) RemuxFromURLs(ctx context.Context, videoURL, audioURL, referer, ua string,
	desiredBase string, totalSec int, onProgress func(percent float64, bytesWritten int64)) (string, error) {
	return e.remuxFromURLs(ctx, []string{videoURL, audioURL}, referer, ua, desiredBase, totalSec, onProgress)
}

// remuxFromURLs 先试 mp4（faststart、浏览器直接能播），失败再退 mkv（音轨编码更宽松）。
func (e *Encoder) remuxFromURLs(ctx context.Context, urls []string, referer, ua string,
	desiredBase string, totalSec int, onProgress func(percent float64, bytesWritten int64)) (string, error) {

	mp4 := desiredBase + ".mp4"
	err := e.remuxURLTo(ctx, urls, referer, ua, mp4, "mp4", totalSec, onProgress)
	if err == nil {
		return mp4, nil
	}
	mp4Err := err
	mkv := desiredBase + ".mkv"
	if err2 := e.remuxURLTo(ctx, urls, referer, ua, mkv, "matroska", totalSec, onProgress); err2 != nil {
		return "", fmt.Errorf("封装失败：mp4 %v；mkv %v", mp4Err, err2)
	}
	return mkv, nil
}

// RemuxTo 把两条分轨（画面 + 声音）合成一份 mp4，同样 -c copy、不转码。
//
// 需要它的是 DASH 站：B 站把 1080P 以上的片源拆成 video.m4s / audio.m4s 两条流，
// 各拉各的、谁也不带对方。播放时不先合流就只有画面没有声音（或者干脆播不动）。
// 两条流都要带 Referer —— B 站媒体 CDN 不带 Referer 直接 403（实测）。
//
// 输出走 faststart：moov 挪到文件头，浏览器拿到前几 MB 就能起播并拖动进度条。
//
// totalSec / onProgress 只为「让等待可见」：4K 那一档光画面就有 554 MB（AV1）
// 到 1.77 GB（H.264），整条流下完才开始合，用户要在遮罩上等 1~3 分钟。
func (e *Encoder) RemuxTo(ctx context.Context, videoURL, audioURL, referer, ua, outPath string,
	totalSec int, onProgress func(percent float64)) error {
	wrapped := func(percent float64, _ int64) {
		if onProgress != nil {
			onProgress(percent)
		}
	}
	return e.remuxURLTo(ctx, []string{videoURL, audioURL}, referer, ua, outPath, "mp4", totalSec, wrapped)
}

// MuxRanged 先把画面/声音两条直链分段并行拉到本地，再本地 -c copy 合成一份 mp4。
//
// 只在直链支持 Range 且知道体积时才走这条（见 muxcache.RangedMuxer）：
// 实测单连接 2.27 MB/s，而同一台机器 6 路并发能到 6.4–7.6 MB/s，
// 4K 那一档「准备 4 分钟」里有一多半是在等单流限速。
//
// 进度按「已下载字节」算（0→95%），剩下 5% 留给本地 remux：
// 本地 -c copy 只是搬字节，实测 540 MB 几秒钟，给 5% 是故意留宽。
func (e *Encoder) MuxRanged(ctx context.Context, src muxcache.Source, outPath string,
	onProgress func(percent float64)) error {

	vpath := outPath + ".video.dash"
	apath := outPath + ".audio.dash"
	defer func() { _ = os.Remove(vpath) }()
	defer func() { _ = os.Remove(apath) }()

	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConnsPerHost:   src.Workers + 2,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}}

	report := func(pct float64) {
		if onProgress != nil {
			onProgress(pct)
		}
	}

	// 声音先下（几 MB，一两秒）：它的体积不知道的话没法算总进度。
	aSize, err := probeSize(ctx, client, src.AudioURL, src.Referer, src.UA)
	if errors.Is(err, errNoRange) {
		return e.remuxDirect(ctx, src, outPath, onProgress)
	}
	if err != nil {
		return fmt.Errorf("探测音轨体积失败: %w", err)
	}
	vSize := src.VideoBytes
	if vSize <= 0 {
		if vSize, err = probeSize(ctx, client, src.VideoURL, src.Referer, src.UA); err != nil {
			return fmt.Errorf("探测画面体积失败: %w", err)
		}
	}
	total := vSize + aSize

	var audioDone int64
	if err := fetchRanges(ctx, client, src.AudioURL, src.Referer, src.UA, apath, aSize, 2,
		func(done, _ int64) {
			audioDone = done
			report(float64(done) / float64(total) * 95)
		}); err != nil {
		return fmt.Errorf("下载音轨失败: %w", err)
	}

	if err := fetchRanges(ctx, client, src.VideoURL, src.Referer, src.UA, vpath, vSize, src.Workers,
		func(done, _ int64) {
			report(float64(audioDone+done) / float64(total) * 95)
		}); err != nil {
		if errors.Is(err, errNoRange) {
			return e.remuxDirect(ctx, src, outPath, onProgress)
		}
		return fmt.Errorf("下载画面失败: %w", err)
	}
	report(95)

	// 本地合：Source 里的 UA/Referer 不用传，这里两条都是盘上的文件。
	if err := e.remuxURLTo(ctx, []string{vpath, apath}, "", "", outPath, "mp4", 0,
		func(pct float64, _ int64) { report(95 + pct*0.04) }); err != nil {
		return err
	}
	report(99)
	return nil
}

// remuxDirect 是老的单连接路：两条直链直接喂给 ffmpeg（上游不支持 Range 时用）。
func (e *Encoder) remuxDirect(ctx context.Context, src muxcache.Source, outPath string,
	onProgress func(percent float64)) error {
	return e.RemuxTo(ctx, src.VideoURL, src.AudioURL, src.Referer, src.UA, outPath, src.TotalSec, onProgress)
}

// remuxInputFlags 是每条输入前面的那串参数。
//
// -user_agent / -headers 是**输入侧**参数，必须紧挨着各自那条 -i（B 站两条流都要 Referer）。
// 另外几条是「别被 CDN 拖死」用的：B 站媒体流里混着 PCDN 节点，实测会出现
// 「连接没断、字节不来了」的假死（写满 649 MB 后 3 分半零字节），而 ffmpeg 默认会一直等，
// 最后只能由外层整体超时收场——那时用户已经等了 10 分钟。
// -rw_timeout 让单次读写 30 秒没数据就报错，-reconnect* 让真断流自己接上。
func remuxInputFlags(ua, referer string) []string {
	args := []string{
		"-rw_timeout", "30000000",
		"-reconnect", "1",
		"-reconnect_streamed", "1",
		"-reconnect_on_network_error", "1",
		"-reconnect_delay_max", "5",
	}
	if ua != "" {
		args = append(args, "-user_agent", ua)
	}
	if referer != "" {
		args = append(args, "-headers", "Referer: "+referer+"\r\n")
	}
	return args
}

// remuxURLTo 拉一组输入（1 条 = 音画一体的单文件；2 条 = 画面 + 声音分轨）remux 到 output。
func (e *Encoder) remuxURLTo(ctx context.Context, urls []string, referer, ua, output, format string,
	totalSec int, onProgress func(percent float64, bytesWritten int64)) error {

	tmp := output + ".tmp"
	_ = os.Remove(tmp)
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-nostdin"}
	for _, u := range urls {
		args = append(args, remuxInputFlags(ua, referer)...)
		args = append(args, "-i", u)
	}
	if len(urls) > 1 {
		// 画面取第 1 条、声音取第 2 条：DASH 就是这样给的，别指望顺序颠倒。
		args = append(args, "-map", "0:v:0", "-map", "1:a:0?")
	} else {
		args = append(args, "-map", "0:v:0", "-map", "0:a:0?")
	}
	args = append(args, "-c", "copy")
	if format == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, "-f", format, "-progress", "pipe:1", "-nostats", tmp)

	cmd := exec.CommandContext(ctx, e.FFmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{buf: &stderr, max: 8192}
	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		sc := bufio.NewScanner(stdout)
		var written int64
		for sc.Scan() {
			line := sc.Text()
			// 同一个进度块里 total_size 在前、out_time_us 在后，先记住体积再报进度。
			if v, ok := strings.CutPrefix(line, "total_size="); ok {
				if n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64); perr == nil && n >= 0 {
					written = n
				}
				continue
			}
			if !strings.HasPrefix(line, "out_time_us=") || onProgress == nil {
				continue
			}
			us, perr := strconv.ParseInt(strings.TrimPrefix(line, "out_time_us="), 10, 64)
			if perr != nil {
				continue
			}
			pct := 0.0
			if totalSec > 0 {
				pct = float64(us) / 1e6 / float64(totalSec) * 100
				if pct > 99 {
					pct = 99
				}
			}
			onProgress(pct, written)
		}
	}()

	if err := cmd.Wait(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Rename(tmp, output); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
