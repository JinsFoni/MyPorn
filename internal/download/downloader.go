// Package download 实现自研 HLS 分段下载器：并发拉分片、断点续传、精确进度、限速。
//
// 设计要点（对应 ARCHITECTURE.md 的内存预算）：
//   - 每个分片直接流式写盘，绝不整段读进内存；
//   - 已完成的分片留在工作目录，重试/恢复时自动跳过；
//   - 进度按"已完成分片 / 总片数"计算；速度由 internal/speed 按「最近 3 秒的字节增量」平均
//     （字节边收边记，见 fetchFile 的 onBytes），不用瞬时差——那是「网速一会 MB/s 一会 KB/s」的来源。
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/JinsFoni/MyPorn/internal/hls"
	"github.com/JinsFoni/MyPorn/internal/speed"
)

// ErrUnsupported 表示清单形态超出内置下载器能力（加密、BYTERANGE 等），
// 上层应退化为直接交给 ffmpeg 处理。
var ErrUnsupported = errors.New("清单超出内置下载器能力")

// Progress 是一次进度快照。
type Progress struct {
	DoneSegments  int
	TotalSegments int
	BytesDone     int64
	BytesTotal    int64
	SpeedBPS      int64
	ETASeconds    int
	Percent       float64
}

// Downloader 负责把一条 media playlist 落成一个可封装的裸流文件。
type Downloader struct {
	Client      *http.Client
	Concurrency int // 并发分片数，默认 8
	Retries     int // 单分片重试次数，默认 4
	UserAgent   string
	Referer     string
	BytePerSec  int64 // 全局限速（字节/秒），0 表示不限
}

// New 用给定 HTTP 客户端构造下载器并填空默认值。
func New(client *http.Client) *Downloader {
	return &Downloader{
		Client:      client,
		Concurrency: 8,
		Retries:     4,
		UserAgent:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}
}

// Resolve 拉取清单并返回 media playlist；若给的是 master 则自动取最高带宽子清单。
func (d *Downloader) Resolve(ctx context.Context, playlistURL string) (*hls.MediaPlaylist, error) {
	body, err := d.getText(ctx, playlistURL)
	if err != nil {
		return nil, err
	}
	if hls.IsMaster(body) {
		child := hls.BestChildURL(body, playlistURL)
		if child == "" {
			return nil, fmt.Errorf("master 清单里没有可用变体")
		}
		playlistURL = child
		if body, err = d.getText(ctx, playlistURL); err != nil {
			return nil, err
		}
	}
	pl, err := hls.ParseMediaPlaylist(body, playlistURL)
	if err != nil {
		return nil, err
	}
	if pl.Encrypted || pl.ByteRange {
		return nil, ErrUnsupported
	}
	return pl, nil
}

// Run 下载全部分片并顺序拼接为 outPath。
// workDir 用于存放分片；已存在的分片视为已完成（断点续传）。
func (d *Downloader) Run(ctx context.Context, pl *hls.MediaPlaylist, workDir, outPath string, onProgress func(Progress)) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("创建下载目录: %w", err)
	}
	conc := d.Concurrency
	if conc <= 0 {
		conc = 8
	}
	var limiter *rate.Limiter
	if d.BytePerSec > 0 {
		burst := d.BytePerSec
		if burst > 4<<20 {
			burst = 4 << 20
		}
		if burst < 64<<10 {
			burst = 64 << 10
		}
		limiter = rate.NewLimiter(rate.Limit(d.BytePerSec), int(burst))
	}

	// init 分段（fMP4）必须先下载并拼在最前。
	initPath := filepath.Join(workDir, "init.bin")
	if pl.InitURI != "" {
		if err := d.fetchFile(ctx, pl.InitURI, initPath, limiter, nil); err != nil {
			return fmt.Errorf("下载初始化分段失败: %w", err)
		}
	}

	total := len(pl.Segments)
	var done int64
	var bytesDone int64
	// 速度按最近 3 秒的字节增量平均（见 internal/speed）：分片是整块落地的，
	// 用「两次快照之间的瞬时差」算出来会一会 MB/s 一会 KB/s。
	meter := speed.New(speed.DefaultWindow)
	var lastSpeed int64
	var mu sync.Mutex // 保护 meter / lastSpeed（心跳与分片协程都会来算速度）
	begin := time.Now()

	report := func(force bool) {
		dn := int(atomic.LoadInt64(&done))
		bd := atomic.LoadInt64(&bytesDone)
		mu.Lock()
		lastSpeed = meter.Update(time.Now(), bd)
		mu.Unlock()
		if onProgress == nil {
			return
		}
		// 一个分片还没落地也要报：大分片一拉十几秒，中间不报的话界面既没有速度也没有进度，
		// 看着像卡死（字节数已经能边收边算了，这时候报出来的是真数值）。
		if !force && dn == 0 && bd == 0 {
			return
		}
		p := Progress{DoneSegments: dn, TotalSegments: total, BytesDone: bd, SpeedBPS: lastSpeed}
		if total > 0 {
			p.Percent = float64(dn) / float64(total) * 100
			// 用已下载分片反推整体体积，用于 ETA。
			if dn > 0 {
				est := int64(float64(bd) / float64(dn) * float64(total))
				if est > bd {
					p.BytesTotal = est
				}
			}
		}
		if p.BytesTotal > bd && lastSpeed > 0 {
			p.ETASeconds = int(float64(p.BytesTotal-bd) / float64(lastSpeed))
		} else if dn > 0 {
			// 还没有速度（预热期）时按「已过的墙钟时间 / 已完成片数」外推。
			// 原来这段是拿**播放时长**除以片数当每片耗时，实测报出「剩余 4 分 46 秒」而整部片
			// 2 秒后下完（2026-11-03）；下载快慢和片子长短没关系，只有墙钟时间说了算。
			if el := time.Since(begin).Seconds(); el > 0.5 {
				p.ETASeconds = int(el / float64(dn) * float64(total-dn))
			}
		}
		onProgress(p)
	}

	// 进度心跳：即使某些分片很慢也能看到速度变化。
	stop := make(chan struct{})
	var wgHeart sync.WaitGroup
	wgHeart.Add(1)
	go func() {
		defer wgHeart.Done()
		t := time.NewTicker(700 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				report(true)
			}
		}
	}()

	sem := make(chan struct{}, conc)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for i, seg := range pl.Segments {
		if err := ctx.Err(); err != nil {
			break
		}
		select {
		case <-ctx.Done():
		case sem <- struct{}{}:
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(idx int, uri string) {
			defer wg.Done()
			defer func() { <-sem }()
			path := filepath.Join(workDir, fmt.Sprintf("seg_%06d.bin", idx))
			if ok := fileExists(path); ok {
				// 续传时这一片是老的，没有流可数，直接把体积记上
				if st, err := os.Stat(path); err == nil {
					atomic.AddInt64(&bytesDone, st.Size())
				}
			} else if err := d.fetchFile(ctx, uri, path, limiter, func(n int64) {
				// 边收边记：字节数不再「一片一跳」，速度才有连续的分母
				atomic.AddInt64(&bytesDone, n)
			}); err != nil {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("第 %d 个分片下载失败: %w", idx+1, err)
					cancel()
				})
				return
			}
			atomic.AddInt64(&done, 1)
		}(i, seg.URI)
	}
	wg.Wait()
	close(stop)
	wgHeart.Wait()
	report(true)

	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// 顺序拼接（流式，内存占用与文件大小无关）。
	if err := concat(workDir, initPath, pl.InitURI != "", total, outPath); err != nil {
		return err
	}
	if st, err := os.Stat(outPath); err == nil && st.Size() == 0 {
		return fmt.Errorf("拼接结果为空")
	}
	return nil
}

func concat(workDir, initPath string, hasInit bool, total int, outPath string) error {
	_ = os.Remove(outPath)
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 1<<20)
	copyFrom := func(p string) error {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.CopyBuffer(out, f, buf)
		return err
	}
	if hasInit {
		if err := copyFrom(initPath); err != nil {
			return fmt.Errorf("拼接初始化分段: %w", err)
		}
	}
	for i := 0; i < total; i++ {
		p := filepath.Join(workDir, fmt.Sprintf("seg_%06d.bin", i))
		if err := copyFrom(p); err != nil {
			return fmt.Errorf("拼接第 %d 个分片: %w", i+1, err)
		}
	}
	return out.Sync()
}

func (d *Downloader) getText(ctx context.Context, u string) (string, error) {
	body, err := d.fetch(ctx, u)
	if err != nil {
		return "", err
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// fetch 带重试地打开一个 URL 的响应体。
func (d *Downloader) fetch(ctx context.Context, u string) (io.ReadCloser, error) {
	var lastErr error
	for attempt := 0; attempt <= d.Retries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", d.UserAgent)
		req.Header.Set("Accept", "*/*")
		if d.Referer != "" {
			req.Header.Set("Referer", d.Referer)
		}
		resp, err := d.Client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			// 4xx（除 429）重试意义不大，直接失败让上层换策略。
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return nil, lastErr
			}
			continue
		}
		return resp.Body, nil
	}
	return nil, lastErr
}

// fetchFile 下载到 path（先写 .tmp 再原子改名），支持全局限速。
// onBytes 在每写一块之后被调用（可为 nil），进度与速度靠它做到「边收边算」。
func (d *Downloader) fetchFile(ctx context.Context, u, path string, limiter *rate.Limiter,
	onBytes func(int64)) error {
	if fileExists(path) {
		return nil
	}
	body, err := d.fetch(ctx, u)
	if err != nil {
		return err
	}
	defer body.Close()

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			_ = os.Remove(tmp)
			return err
		}
		n, rerr := body.Read(buf)
		if n > 0 {
			if limiter != nil {
				if werr := limiter.WaitN(ctx, n); werr != nil {
					f.Close()
					_ = os.Remove(tmp)
					return werr
				}
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				_ = os.Remove(tmp)
				return werr
			}
			if onBytes != nil {
				onBytes(int64(n))
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			f.Close()
			_ = os.Remove(tmp)
			return rerr
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

// SortSegments 保证分片按序号升序（清单顺序即播放顺序，这里仅防御）。
func SortSegments(segs []hls.Segment) {
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Seq < segs[j].Seq })
}
