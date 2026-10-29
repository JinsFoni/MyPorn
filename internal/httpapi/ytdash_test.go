package httpapi

import (
	"testing"

	"github.com/mario/myporn/internal/ytdash"
)

// 4K 单文件 HLS 里最容易被写错的两个纯函数：Range 解析和「这个 Range 是哪一段」。
//
// 写错的后果不是报错，而是**画面花花绿绿**：拿到偏移对上但长度不对的字节，
// MSE 会 append 成功、解码出来是垃圾或者黑屏。所以这两个函数单独钉住。

func TestParseByteRange(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantA  int64
		wantB  int64
		wantOK bool
	}{
		{"标准分段", "bytes=1417-8837404", 1417, 8837404, true},
		{"带空格", " bytes=0-99 ", 0, 99, true},
		{"init 段", "bytes=0-1416", 0, 1416, true},
		{"多段只取第一段", "bytes=100-199,300-399", 100, 199, true},
		{"结束小于开始算无效", "bytes=500-100", 0, 0, false},
		{"没有 Range", "", 0, 0, false},
		{"不是 bytes 单位", "items=0-1", 0, 0, false},
		{"开区间不支持（hls.js 不会发）", "bytes=100-", 0, 0, false},
	}
	for _, c := range cases {
		a, b, ok := parseByteRange(c.raw)
		if ok != c.wantOK || (ok && (a != c.wantA || b != c.wantB)) {
			t.Fatalf("%s: 得到 (%d, %d, %v)，想要 (%d, %d, %v)",
				c.name, a, b, ok, c.wantA, c.wantB, c.wantOK)
		}
	}
}

func TestSegmentAt(t *testing.T) {
	// 真实采样：2160p AV1 的前两段
	segs := []ytdash.Segment{
		{Offset: 1417, Size: 8835988, Duration: 5.272},
		{Offset: 8837405, Size: 11760299, Duration: 7.014},
	}
	if i, ok := segmentAt(segs, 1417, 1417+8835988-1); !ok || i != 0 {
		t.Fatalf("第一段 = (%d, %v)，想要 (0, true)", i, ok)
	}
	if i, ok := segmentAt(segs, 8837405, 8837405+11760299-1); !ok || i != 1 {
		t.Fatalf("第二段 = (%d, %v)，想要 (1, true)", i, ok)
	}
	// 差一个字节就不算命中（宁可回源，也不发错字节）
	if _, ok := segmentAt(segs, 1417, 1417+8835988-2); ok {
		t.Fatal("长度对不上不该算命中")
	}
	if _, ok := segmentAt(segs, 12345, 67890); ok {
		t.Fatal("对不上任何一段不该算命中")
	}
}

// 预取缓存按「哪条流 + 第几段」区分：画面和音轨的段号会把彼此顶掉的话，
// 播放器每次要音轨都会 miss，等于预取白做。
func TestSegmentCacheKeyedBySide(t *testing.T) {
	sess := &ytDashSession{}
	sess.cachePut("v", 0, []byte("video-0"))
	sess.cachePut("a", 0, []byte("audio-0"))

	if b, ok := sess.cacheGet("v", 0); !ok || string(b) != "video-0" {
		t.Fatalf("画面第 0 段被音轨顶掉了: %q %v", b, ok)
	}
	if b, ok := sess.cacheGet("a", 0); !ok || string(b) != "audio-0" {
		t.Fatalf("音轨第 0 段不对: %q %v", b, ok)
	}
}

func TestSegmentCacheEvictsOldest(t *testing.T) {
	sess := &ytDashSession{}
	var dropped int
	for i := 0; i < ytDashPrefetch*2+3; i++ {
		dropped += sess.cachePut("v", i, make([]byte, 10))
	}
	sess.cacheMu.Lock()
	got := len(sess.cache)
	sess.cacheMu.Unlock()
	if got > ytDashPrefetch*2 {
		t.Fatalf("缓存里有 %d 段，上限应当是 %d", got, ytDashPrefetch*2)
	}
	if dropped == 0 {
		t.Fatal("淘汰时应当回报被丢掉的字节数（全局占用要跟着减）")
	}
	if _, ok := sess.cacheGet("v", 0); ok {
		t.Fatal("最早的段应当已经被淘汰")
	}
}

func TestClaimFetchDedupes(t *testing.T) {
	sess := &ytDashSession{}
	if !sess.claimFetch("v", 3) {
		t.Fatal("第一次应当能占住")
	}
	if sess.claimFetch("v", 3) {
		t.Fatal("同一段不该被重复占")
	}
	sess.releaseFetch("v", 3)
	if !sess.claimFetch("v", 3) {
		t.Fatal("放掉之后应当能再占")
	}
}

// 分块只影响「几条连接去取同一份数据」，切错了就会漏字节或重复取。
func TestYtBlockSpans(t *testing.T) {
	spans := ytBlockSpans(1000, 1000+8835988-1, 3)
	if len(spans) != 3 {
		t.Fatalf("切成 %d 块，想要 3 块", len(spans))
	}
	if spans[0][0] != 1000 || spans[2][1] != 1000+8835988-1 {
		t.Fatalf("首尾没对齐: %v", spans)
	}
	var sum int64
	for i, sp := range spans {
		if sp[0] > sp[1] {
			t.Fatalf("第 %d 块是空区间: %v", i, sp)
		}
		if i > 0 && sp[0] != spans[i-1][1]+1 {
			t.Fatalf("第 %d 块和上一块断开了: %v", i, spans)
		}
		sum += sp[1] - sp[0] + 1
	}
	if sum != 8835988 {
		t.Fatalf("总字节数 %d，想要 8835988", sum)
	}
	// 段比块数还小时不能切出空块
	if got := ytBlockSpans(0, 1, 3); len(got) != 2 {
		t.Fatalf("2 字节切成 %d 块，应当只切 2 块", len(got))
	}
}
