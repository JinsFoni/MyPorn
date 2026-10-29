// Package muxcache 把「音画分开的两条流」合成一份能播的 mp4，并且落盘缓存。
//
// 为什么需要它：DASH 站（目前是 B 站）把 1080P 以上的片源拆成 video.m4s + audio.m4s，
// 两条流各自独立。浏览器只有一个 <video>，喂它画面那条就只有画面没有声音。
// 后端能做的只有一件事——把两条流按时间轴合起来（ffmpeg -c copy，不转码），
// 再把成品当成普通文件发给浏览器，这样进度条拖动、Range、缓存全都白拿。
//
// 三个设计取舍：
//
//  1. **落盘缓存而不是实时管道**：实时管道（不带 Content-Length、不支持 Range）看着省事，
//     但进度条会拖不动、时长显示成 Infinity，而且同样的片子每次重看都要现合一遍。
//     落盘之后第二次起播是 0 延迟，代价是第一次要多等几秒（实测 480P/286 秒的片子 2 秒合完，
//     1080P 的长片大概十几秒），这段时间前端显示的就是普通的加载遮罩。
//  2. **请求上下文不参与合流**：用户点开就切走是很常见的，若合流跟着请求一起取消，
//     下一个人进来又得从头再来。所以合流跑在自己的 background ctx 上（有时限）。
//  3. **同一个 key 单飞**：浏览器起播会连开好几个 Range 请求，不能每个请求都启动一次 ffmpeg。
package muxcache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Muxer 是真正干活的那一层（生产里是 *library.Encoder）。
//
// totalSec / onProgress 是给「进度可见」用的：4K 那一档要整条流下完（554 MB 起）
// 才开始合成，用户可能等好几分钟，前端得能说出「现在到哪了」。
type Muxer interface {
	RemuxTo(ctx context.Context, videoURL, audioURL, referer, ua, outPath string,
		totalSec int, onProgress func(percent float64)) error
}

// RangedMuxer 是可选增强：当两条直链都支持 Range 时，先分段并行下载到本地再合，
// 比让 ffmpeg 单连接拉快好几倍（单连接受 CDN 单流限速，与链路带宽无关）。
//
// 实测 YFsMnJEbV1U 的 2160p AV1（540 MB）：ffmpeg 单连接 2.27 MB/s（4 分 0.5 秒）；
// 分段并行见 library.fetchRanges 的注释。
type RangedMuxer interface {
	MuxRanged(ctx context.Context, src Source, outPath string, onProgress func(percent float64)) error
}

// Source 是一次合流的输入。
type Source struct {
	VideoURL string
	AudioURL string
	Referer  string
	UA       string
	// TotalSec 是片子时长（秒），只用来把合流进度换算成百分比；0 = 不报进度。
	TotalSec int
	// VideoBytes 是画面轨的字节数（DASH 的 clen），0 = 不知道。
	// 只有它和 Workers 都合适时才会走 RangedMuxer 的分段下载。
	VideoBytes int64
	// Workers 是分段下载的并发数（<=1 表示不分段）。
	Workers int
}

// State 是合流在外部看来的状态。
type State string

const (
	StateIdle    State = "idle"    // 还没有人要求过
	StateMerging State = "merging" // 正在下 + 合
	StateReady   State = "ready"   // 成品在盘上
	StateFailed  State = "failed"  // 最近一次失败了（带着原因）
)

// Snapshot 是给「等的那一头」看的一张快照。
type Snapshot struct {
	State   State
	Percent float64
	Elapsed time.Duration
	Err     error
}

var (
	errNoMuxer      = errors.New("合流器没配好（缺 ffmpeg）")
	errNotTwoTracks = errors.New("拿到的不是两条流，没法合流")
)

// Cache 是合流缓存的目录 + 单飞表。
type Cache struct {
	Dir   string
	Muxer Muxer
	// TTL 是成品在盘上留多久，默认 7 天（再大的片子也就几百 MB，留着比重新合便宜）。
	TTL time.Duration
	// Timeout 是单次合流的时限，默认 10 分钟。
	Timeout time.Duration
	// OnMerged 在每次真的跑完一趟合流（成功或失败）后被调一次，给外面记日志用。
	// 走回调而不是自己写日志，是因为合流可能由「查进度」那条请求起头，
	// 真正跑完时 HTTP 请求早就返回了——只有这里知道它花了多久。
	OnMerged func(key string, elapsed time.Duration, size int64, err error)

	mu       sync.Mutex
	flights  map[string]*flight
	failures map[string]failure
	prunedAt time.Time
}

type flight struct {
	done chan struct{}
	path string
	err  error

	started time.Time
	pct     atomic.Uint32 // 0~99 的整数百分比（原子读，避免每次查进度都抢锁）
}

func (f *flight) progress() (float64, time.Duration) {
	return float64(f.pct.Load()), time.Since(f.started)
}

type failure struct {
	err error
	at  time.Time
}

// New 建一个缓存。dir 不存在会在第一次合流时创建。
func New(dir string, m Muxer) *Cache {
	return &Cache{
		Dir:      dir,
		Muxer:    m,
		TTL:      7 * 24 * time.Hour,
		Timeout:  10 * time.Minute,
		flights:  map[string]*flight{},
		failures: map[string]failure{},
	}
}

// Path 返回某个 key 对应的成品路径（不检查是否存在）。
func (c *Cache) Path(key string) string {
	return filepath.Join(c.Dir, safeName(key)+".mp4")
}

// Ready 报告成品是否已经可用。
func (c *Cache) Ready(key string) bool {
	st, err := os.Stat(c.Path(key))
	return err == nil && st.Size() > 0
}

// Begin 开始（或接上）一次合流，立刻返回，不等它跑完。
//
// 存在的理由：4K 要等好几分钟，前端不能傻等着一个不回来的 HTTP 请求——
// 先 Begin 起来，再轮询 Snapshot 报进度，好了才把地址交给 <video>。
func (c *Cache) Begin(key string, produce func(context.Context) (Source, error)) {
	if c.Muxer == nil || c.Ready(key) {
		return
	}
	c.prune()
	c.mu.Lock()
	if _, ok := c.flights[key]; ok {
		c.mu.Unlock()
		return
	}
	f := &flight{done: make(chan struct{}), started: time.Now()}
	c.flights[key] = f
	delete(c.failures, key)
	c.mu.Unlock()
	go c.run(f, key, c.Path(key), produce)
}

// Snapshot 报告某个 key 现在的状态（不启动任何东西）。
func (c *Cache) Snapshot(key string) Snapshot {
	if c.Ready(key) {
		return Snapshot{State: StateReady, Percent: 100}
	}
	c.mu.Lock()
	f, running := c.flights[key]
	fail, failed := c.failures[key]
	c.mu.Unlock()
	if running {
		pct, elapsed := f.progress()
		return Snapshot{State: StateMerging, Percent: pct, Elapsed: elapsed}
	}
	// 5 分钟内的失败还留着：前端刷新一眼就能看到「上次为什么没好」，
	// 而不是无限「正在准备」。
	if failed && time.Since(fail.at) < 5*time.Minute {
		return Snapshot{State: StateFailed, Err: fail.err, Elapsed: time.Since(fail.at)}
	}
	return Snapshot{State: StateIdle}
}

// KeysWithPrefix 返回缓存里已经就绪、且 key 以 prefix 开头的那些（按文件名排序）。
//
// 用在「上游这会儿解析不出来，但成品还在盘上」的时候：YouTube 的 IP 级风控会让
// yt-dlp 连续几分钟拿不到地址，可已经合好的 4K 文件跟上游一点关系都没有，没必要陪它一起失败。
func (c *Cache) KeysWithPrefix(prefix string) []string {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".mp4") {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ".mp4")
		if key == prefix || strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Clear 抹掉某个 key 的失败记录，让下一次 Begin/Ensure 能重新试一遍。
//
// 5 分钟的失败备忘是给「刚失败、用户刷新一眼」用的；但用户主动点「重试」时，
// 拿着同一条错误再等 5 分钟没有任何意义——那是把重试按钮变成了装饰。
func (c *Cache) Clear(key string) {
	c.mu.Lock()
	delete(c.failures, key)
	c.mu.Unlock()
}

// Ensure 保证某个 key 的成品已就绪，返回它的绝对路径。
//
// produce 只在真的要合流时被调用，用来（重新）解析两条流的地址——签名地址会过期，
// 缓存命中时绝不该再去问上游。合流跑在后台 context 上：这个请求被取消不影响别人，
// 但调用方自己的 ctx 一旦结束就立刻返回，不必陪着等。
func (c *Cache) Ensure(ctx context.Context, key string, produce func(context.Context) (Source, error)) (string, error) {
	path := c.Path(key)
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return path, nil
	}
	if c.Muxer == nil {
		return "", errNoMuxer
	}
	c.prune()

	c.mu.Lock()
	f, ok := c.flights[key]
	if !ok {
		f = &flight{done: make(chan struct{}), started: time.Now()}
		c.flights[key] = f
		delete(c.failures, key)
	}
	c.mu.Unlock()

	if !ok {
		go c.run(f, key, path, produce)
	}
	return c.wait(ctx, f)
}

// wait 等一个正在跑的合流结束（或者调用方先放弃）。
func (c *Cache) wait(ctx context.Context, f *flight) (string, error) {
	select {
	case <-f.done:
		if f.err != nil {
			return "", f.err
		}
		return f.path, nil
	case <-ctx.Done():
		// 合流本身继续跑，下一个人进来就是现成的。
		return "", ctx.Err()
	}
}

func (c *Cache) run(f *flight, key, path string, produce func(context.Context) (Source, error)) {
	defer close(f.done)
	ctx := context.Background()
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	src, err := produce(ctx)
	if err == nil && (src.VideoURL == "" || src.AudioURL == "") {
		err = errNotTwoTracks
	}
	if err == nil {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			err = fmt.Errorf("创建合流缓存目录失败: %w", mkErr)
		} else {
			report := func(pct float64) {
				if pct < 0 {
					pct = 0
				}
				if pct > 99 {
					pct = 99 // 100 留给「盘上已经有成品」这个确定状态
				}
				f.pct.Store(uint32(pct))
			}
			if rm, ok := c.Muxer.(RangedMuxer); ok && src.Workers > 1 {
				err = rm.MuxRanged(ctx, src, path, report)
			} else {
				err = c.Muxer.RemuxTo(ctx, src.VideoURL, src.AudioURL, src.Referer, src.UA, path,
					src.TotalSec, report)
			}
		}
	}

	if c.OnMerged != nil {
		var size int64
		if err == nil {
			if st, statErr := os.Stat(path); statErr == nil {
				size = st.Size()
			}
		}
		c.OnMerged(key, time.Since(f.started), size, err)
	}

	c.mu.Lock()
	f.err = err
	f.path = path
	if err != nil {
		// 超时要在错误链里带上 context.DeadlineExceeded：外面靠 errors.Is 区分
		// 「用户切走了」和「这条线路卡死了」，前者不该回话，后者要给出明确原因。
		if cerr := ctx.Err(); cerr != nil {
			err = fmt.Errorf("%w: %v", cerr, err)
			f.err = err
		}
		c.failures[key] = failure{err: err, at: time.Now()}
	}
	delete(c.flights, key)
	c.mu.Unlock()
}

// prune 按 TTL 清掉旧成品。调用频率不高（每 10 分钟最多一次），
// 一次扫一个目录对单用户场景足够了。
func (c *Cache) prune() {
	if c.TTL <= 0 {
		return
	}
	c.mu.Lock()
	if time.Since(c.prunedAt) < 10*time.Minute {
		c.mu.Unlock()
		return
	}
	c.prunedAt = time.Now()
	c.mu.Unlock()

	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	deadline := time.Now().Add(-c.TTL)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().After(deadline) {
			continue
		}
		_ = os.Remove(filepath.Join(c.Dir, e.Name()))
	}
}

// safeName 把 key 压成安全的文件名：只留字母数字和 . _ -，其余换下划线。
func safeName(key string) string {
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 120 {
		out = out[:120]
	}
	if out == "" {
		out = "mux"
	}
	return out
}
