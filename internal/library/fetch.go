package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// 分段并行下载：把一条 DASH 直链切成若干段，用多条 HTTP Range 连接同时拉到本地文件。
//
// 为什么值得单独写一套：googlevideo 对**单条连接**限速，跟整条链路无关。同一个 2160p AV1
// 直链（540 MB）用 ffmpeg 单连接拉只有 2.27 MB/s（4 分 0.5 秒），而同一台机器多路并发
// 取 HLS 分片能到 6.4–7.6 MB/s。也就是说这条 4K「准备 4 分钟」里有一多半是在等单流限速。
//
// 三个约束：
//   - Range 不被支持（回 200 而不是 206）时不能硬分段，得老实回退单连接；
//   - 每条连接都可能中途被 403 / 提前 EOF（googlevideo 常见的半路失效），所以每段自己重试，
//     重试时从已经落盘的位置接着要，而不是整段重来；
//   - 写入用 WriteAt 定位，所以不需要锁，也不怕段与段交错。

const (
	// fetchWorkers 是默认并发数。6 路在实测里已经吃满链路，再多只是给 CDN 添乱。
	fetchWorkers = 6
	// fetchChunk 是每一段的粒度。太大的话一次 403 要重下很多字节，太小则请求开销上来。
	fetchChunk = 8 << 20
	// fetchAttempts 是单段最多试几次。
	fetchAttempts = 3
)

// errNoRange 表示对面根本不支持 Range（回 200 而不是 206）。
// 这种上游不能分段，得回退给 ffmpeg 单连接拉，而不是当成失败。
var errNoRange = errors.New("上游不支持 Range")

// fetchRanges 把 url 拉到 dst（整份文件，用于后面 ffmpeg -c copy）。
//
// size 是预告的文件体积（DASH 的 clen）；传 0 时自己探测一次。
// onBytes 报告累计下载字节数，调用方自己换算百分比。
func fetchRanges(ctx context.Context, client *http.Client, url, referer, ua, dst string,
	size int64, workers int, onBytes func(done, total int64)) (err error) {

	if workers <= 1 {
		workers = fetchWorkers
	}
	if size <= 0 {
		size, err = probeSize(ctx, client, url, referer, ua)
		if err != nil {
			return err
		}
	}

	f, err := os.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		cerr := f.Close()
		if err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	// 先按预告体积铺开，这样 WriteAt 到任意偏移都不会出现空洞（末尾也一定是真实字节）。
	if err := f.Truncate(size); err != nil {
		return err
	}

	// 段起点：0, 8MiB, 16MiB …；每段长度不超过 fetchChunk。
	starts := make([]int64, 0, size/fetchChunk+1)
	for off := int64(0); off < size; off += fetchChunk {
		starts = append(starts, off)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var done atomic.Int64
	var firstErr atomic.Value
	reported := atomic.Int64{} // 上次上报的字节数，用来做节流

	report := func(force bool) {
		if onBytes == nil {
			return
		}
		cur := done.Load()
		if !force && cur-reported.Load() < 1<<20 && cur < size {
			return
		}
		reported.Store(cur)
		onBytes(cur, size)
	}

	work := make(chan int64, len(starts))
	for _, s := range starts {
		work <- s
	}
	close(work)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for start := range work {
				if ctx.Err() != nil {
					return
				}
				end := start + fetchChunk - 1
				if end > size-1 {
					end = size - 1
				}
				if err := fetchSegment(ctx, client, f, url, referer, ua, start, end, &done, report); err != nil {
					if ctx.Err() != nil {
						return
					}
					firstErr.CompareAndSwap(nil, err)
					cancel()
					return
				}
			}
		}()
	}
	wg.Wait()
	report(true)

	if v := firstErr.Load(); v != nil {
		return v.(error)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// fetchSegment 下 [start, end] 这一段，断了就从已落盘的位置续。
func fetchSegment(ctx context.Context, client *http.Client, f *os.File, url, referer, ua string,
	start, end int64, done *atomic.Int64, report func(force bool)) error {

	var lastErr error
	off := start
	for attempt := 0; attempt < fetchAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 0 {
			select { // 小退避：连撞两次 403 多半是同一个边缘节点，等一下会换到别的
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
		}
		n, err := fetchOnce(ctx, client, f, url, referer, ua, off, end, done, report)
		off += n
		if err == nil && off > end {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = err
		if err == nil {
			lastErr = fmt.Errorf("这一段的响应提前结束（已到 %d，想要 %d）", off, end+1)
		}
	}
	return fmt.Errorf("第 %d-%d 字节下载失败: %w", start, end, lastErr)
}

// fetchOnce 发一次 Range 请求，把拿到的字节写进去，返回写入的字节数。
func fetchOnce(ctx context.Context, client *http.Client, f *os.File, url, referer, ua string,
	off, end int64, done *atomic.Int64, report func(force bool)) (int64, error) {

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		// 200 = 对面忽略了 Range（整份都给了），分段策略在这里不成立。
		return 0, fmt.Errorf("%w: %s", errNoRange, resp.Status)
	}
	if resp.StatusCode != http.StatusPartialContent {
		// 403/404/416 都是这条直链不能用了，交给外层换地址重试。
		return 0, fmt.Errorf("上游没有按 Range 返回: %s", resp.Status)
	}

	var written int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], off+written); werr != nil {
				return written, werr
			}
			written += int64(n)
			done.Add(int64(n))
			report(false)
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			return written, rerr
		}
	}
}

// probeSize 用一次 Range: 0-0 问出整份文件的体积。
func probeSize(ctx context.Context, client *http.Client, url, referer, ua string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", "bytes=0-0")
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return 0, fmt.Errorf("%w: 探测体积时回 200", errNoRange)
	}
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("探测体积失败: %s", resp.Status)
	}
	// Content-Range: bytes 0-0/540420903
	cr := resp.Header.Get("Content-Range")
	slash := -1
	for i := len(cr) - 1; i >= 0; i-- {
		if cr[i] == '/' {
			slash = i
			break
		}
	}
	if slash < 0 {
		return 0, errors.New("探测体积失败: 没有 Content-Range")
	}
	size, err := strconv.ParseInt(cr[slash+1:], 10, 64)
	if err != nil || size <= 0 {
		return 0, fmt.Errorf("探测体积失败: Content-Range=%q", cr)
	}
	return size, nil
}
