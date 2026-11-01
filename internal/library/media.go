package library

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// ResizeImage 把图片按"填满后居中裁切"的方式缩放到目标尺寸（用于 poster/thumb/fanart）。
func (e *Encoder) ResizeImage(ctx context.Context, input, output string, w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("目标尺寸非法 %dx%d", w, h)
	}
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d", w, h, w, h)
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
