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
	"strings"
	"sync"
	"time"
)

// Muxer 是真正干活的那一层（生产里是 *library.Encoder）。
type Muxer interface {
	RemuxTo(ctx context.Context, videoURL, audioURL, referer, ua, outPath string) error
}

// Source 是一次合流的输入。
type Source struct {
	VideoURL string
	AudioURL string
	Referer  string
	UA       string
}

// Cache 是合流缓存的目录 + 单飞表。
type Cache struct {
	Dir   string
	Muxer Muxer
	// TTL 是成品在盘上留多久，默认 7 天（再大的片子也就几百 MB，留着比重新合便宜）。
	TTL time.Duration
	// Timeout 是单次合流的时限，默认 10 分钟。
	Timeout time.Duration

	mu       sync.Mutex
	flights  map[string]*flight
	prunedAt time.Time
}

type flight struct {
	done chan struct{}
	path string
	err  error
}

// New 建一个缓存。dir 不存在会在第一次合流时创建。
func New(dir string, m Muxer) *Cache {
	return &Cache{
		Dir:     dir,
		Muxer:   m,
		TTL:     7 * 24 * time.Hour,
		Timeout: 10 * time.Minute,
		flights: map[string]*flight{},
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
		return "", errors.New("合流器没配好（缺 ffmpeg）")
	}
	c.prune()

	c.mu.Lock()
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		return c.wait(ctx, f)
	}
	f := &flight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()

	go c.run(f, key, path, produce)
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
		err = errors.New("拿到的不是两条流，没法合流")
	}
	if err == nil {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			err = fmt.Errorf("创建合流缓存目录失败: %w", mkErr)
		} else {
			err = c.Muxer.RemuxTo(ctx, src.VideoURL, src.AudioURL, src.Referer, src.UA, path)
		}
	}

	c.mu.Lock()
	f.err = err
	f.path = path
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
