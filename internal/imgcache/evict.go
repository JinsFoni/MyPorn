package imgcache

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// 磁盘缓存的容量上限与淘汰。
//
// 为什么 M5 才需要它：封面是「一部影片一张」，量级跟库里的影片数走，几万张也就几个 G；
// 但照片墙是「一条帖子 N 张」，订阅十几个博主就能轻易堆出十万张，放任下去会把盘吃满。
//
// 策略很简单——按修改时间（≈ 最后一次被读取/写入的时间，见 touch）从旧到新删，
// 一直删到上限的 90%。不做 LRU 链表：这层缓存的全部价值是「别重复去图床抓」，
// 淘汰得粗糙一点只会多几次回源，比维护一张精确的访问序列表划算得多。

const (
	// sweepInterval 是两次清扫之间的最小间隔：每写一张图都扫一遍目录太浪费。
	sweepInterval = 10 * time.Minute
	// sweepTarget 是清扫后保留的比例（留出余量，别刚扫完又满了）。
	sweepTarget = 0.9
)

var sweepState struct {
	mu   sync.Mutex
	last time.Time
}

// SetMaxBytes 设置缓存上限（字节，0 = 不限）。
func (s *Service) SetMaxBytes(n int64) {
	s.mu.Lock()
	s.maxBytes = n
	s.mu.Unlock()
}

// MaxBytes 返回当前上限。
func (s *Service) MaxBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxBytes
}

// maybeSweep 在写缓存之后被调用：限流到每 10 分钟一次，且在后台跑，不占请求时间。
func (s *Service) maybeSweep() {
	if s.MaxBytes() <= 0 {
		return
	}
	sweepState.mu.Lock()
	if time.Since(sweepState.last) < sweepInterval {
		sweepState.mu.Unlock()
		return
	}
	sweepState.last = time.Now()
	sweepState.mu.Unlock()

	go func() {
		if removed, freed := s.Sweep(); removed > 0 {
			s.log.Info("图片缓存超限，已淘汰旧文件", "files", removed, "freedMB", freed/(1<<20))
		}
	}()
}

// Sweep 立即执行一次清扫，返回删除的文件数与释放的字节数。
func (s *Service) Sweep() (int, int64) {
	max := s.MaxBytes()
	if max <= 0 {
		return 0, 0
	}
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var all []entry
	var total int64
	err := filepath.Walk(s.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 目录可能正被并发删改，跳过就好。
			return nil //nolint:nilerr
		}
		if info.IsDir() {
			return nil
		}
		all = append(all, entry{path: path, size: info.Size(), mod: info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil || total <= max {
		return 0, 0
	}

	sort.Slice(all, func(i, j int) bool { return all[i].mod.Before(all[j].mod) })
	target := int64(float64(max) * sweepTarget)
	removed := 0
	var freed int64
	for _, e := range all {
		if total <= target {
			break
		}
		if err := os.Remove(e.path); err != nil {
			continue
		}
		total -= e.size
		freed += e.size
		removed++
	}
	return removed, freed
}

// touch 把文件的修改时间推到现在，让「最近被用到」的图排到淘汰队的末尾。
//
// 用 mtime 当访问序号的代价是：读一次就要写一次文件元信息。图片缓存命中率很高，
// 但每次命中只是一次 utimes 系统调用，比每次读都维护一张表简单得多。
func touch(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}
