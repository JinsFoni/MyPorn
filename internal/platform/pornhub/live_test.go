package pornhub

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveResolveStreams 是可选的真实网络冒烟测试（LIVE=1 才跑）：
// 需要能访问 cn.pornhub.com，用来确认选择器与签名直链仍然有效。
func TestLiveResolveStreams(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("设置 LIVE=1 才跑真实网络测试")
	}
	vkey := os.Getenv("LIVE_VKEY")
	if vkey == "" {
		vkey = "6798a78e9c631"
	}
	a, err := New("https://cn.pornhub.com", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	vs, err := a.ResolveStreams(ctx, vkey)
	if err != nil {
		t.Fatalf("解析流地址失败: %v", err)
	}
	if len(vs) == 0 {
		t.Fatal("没有解析到任何画质")
	}
	for _, v := range vs {
		t.Logf("%4dp bw=%-8d %.80s", v.Height, v.Bandwidth, v.URL)
	}
	// 同一清晰度不应重复（HLS 与直链应合并成一条）
	seen := map[int]bool{}
	for _, v := range vs {
		if seen[v.Height] {
			t.Errorf("画质 %dp 重复出现", v.Height)
		}
		seen[v.Height] = true
	}
	if vs[0].Height < vs[len(vs)-1].Height {
		t.Errorf("画质未按从高到低排序: %d → %d", vs[0].Height, vs[len(vs)-1].Height)
	}
}
