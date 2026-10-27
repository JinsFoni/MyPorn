package imgcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 造 n 个缓存文件，每个 size 字节，mtime 依次倒退（第一个最新）。
func seedCache(t *testing.T, dir string, n int, size int64) []string {
	t.Helper()
	paths := make([]string, 0, n)
	now := time.Now()
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, "imgcache", string(rune('a'+i%26)), string(rune('0'+i/26))+".bin")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		// 越靠后的越旧
		mt := now.Add(-time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

func TestSweepKeepsUnderLimitAndDropsOldest(t *testing.T) {
	svc := testService(t)
	paths := seedCache(t, svc.dir, 10, 1000) // 10 KB
	svc.SetMaxBytes(5000)                    // 上限 5 KB → 清扫到 90%（4500），每份 1 KB，于是只剩 4 个

	removed, freed := svc.Sweep()
	if removed == 0 || freed == 0 {
		t.Fatalf("应当淘汰一些文件，实际 removed=%d freed=%d", removed, freed)
	}
	// 目标 4500 字节，每份 1000 → 至少要删到剩 4 个
	left := 0
	var total int64
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil {
			left++
			total += info.Size()
		}
	}
	if total > 4500 {
		t.Errorf("清扫后仍有 %d 字节，超过目标 4500", total)
	}
	if left < 4 {
		t.Errorf("只剩 %d 个文件，删多了（目标至少留 4 个）", left)
	}
	// 留下来的必须是最新的那几个：最后几个（最旧）应当已经不在
	if _, err := os.Stat(paths[len(paths)-1]); err == nil {
		t.Error("最旧的文件应当被删掉")
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Error("最新的文件不该被删")
	}
}

func TestSweepNoopWhenUnlimitedOrUnderLimit(t *testing.T) {
	svc := testService(t)
	seedCache(t, svc.dir, 3, 1000)

	if removed, _ := svc.Sweep(); removed != 0 {
		t.Errorf("未设上限时不该删任何东西，删了 %d 个", removed)
	}
	svc.SetMaxBytes(1 << 20)
	if removed, _ := svc.Sweep(); removed != 0 {
		t.Errorf("没超限时不该删任何东西，删了 %d 个", removed)
	}
}

func TestSweepEmptyDir(t *testing.T) {
	svc := testService(t)
	svc.SetMaxBytes(1000)
	if removed, freed := svc.Sweep(); removed != 0 || freed != 0 {
		t.Errorf("空目录应当什么都不做，得到 %d / %d", removed, freed)
	}
}
