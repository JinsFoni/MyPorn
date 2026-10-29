package library

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// 分段并行下载的测试：字节要一模一样、分段要真的并发、不支持 Range 时要老实报错。
//
// 为什么值得测：这条路只有「几十个 8 MiB 的 Range 请求 + WriteAt 到同一个文件」这一种形态，
// 写错一位就是「文件大小对、内容错位」——那种错要等到 ffmpeg 报
// `Invalid OBU length` 才会暴露，排查成本极高。

// servingFile 起一个「像 googlevideo 一样」的测试服务器：支持 Range、记下并发峰值。
func servingFile(t *testing.T, data []byte, supportRange bool) (*httptest.Server, *int32) {
	t.Helper()
	var inFlight, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		// 让请求有一点重叠，否则「并发」这件事测不出来
		time.Sleep(20 * time.Millisecond)

		rng := r.Header.Get("Range")
		if !supportRange || rng == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		var start, end int64
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if end >= int64(len(data)) {
			end = int64(len(data)) - 1
		}
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
	}))
	t.Cleanup(srv.Close)
	return srv, &peak
}

func TestFetchRangesCopiesEveryByte(t *testing.T) {
	// 3 个 8 MiB 段 + 一个尾巴，故意不是整段对齐
	data := make([]byte, 3*fetchChunk+12345)
	rnd := rand.New(rand.NewSource(7))
	_, _ = rnd.Read(data)

	srv, peak := servingFile(t, data, true)
	dst := filepath.Join(t.TempDir(), "out.dash")

	var last int64
	reports := 0
	err := fetchRanges(context.Background(), srv.Client(), srv.URL, "https://example.com/",
		"UA/1.0", dst, int64(len(data)), 4, func(done, total int64) {
			if total != int64(len(data)) {
				t.Errorf("总字节报错了: %d，想要 %d", total, len(data))
			}
			if done < last {
				t.Errorf("进度倒退了: %d → %d", last, done)
			}
			last = done
			reports++
		})
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	if reports == 0 {
		t.Fatal("一次进度都没报")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("落盘内容不对: %d 字节 vs 期望 %d（首位差异 %d）",
			len(got), len(data), firstDiff(got, data))
	}
	if p := atomic.LoadInt32(peak); p < 2 {
		t.Fatalf("并发峰值只有 %d，分段没并行起来", p)
	}
}

// 上游忽略 Range 时不能「按 200 的整份响应当分段写」，必须报错——否则文件会被写坏。
func TestFetchRangesRejectsServerWithoutRange(t *testing.T) {
	data := make([]byte, 1024)
	srv, _ := servingFile(t, data, false)
	dst := filepath.Join(t.TempDir(), "out.dash")
	err := fetchRanges(context.Background(), srv.Client(), srv.URL, "", "", dst, int64(len(data)), 3, nil)
	if err == nil {
		t.Fatal("不支持 Range 的服务器应当报错")
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		t.Fatal("失败之后不该留下半成品")
	}
}

// 体积不知道时要自己探测（Content-Range 的 /total 那一段）。
func TestProbeSize(t *testing.T) {
	data := make([]byte, 777777)
	srv, _ := servingFile(t, data, true)
	size, err := probeSize(context.Background(), srv.Client(), srv.URL, "", "")
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("探测出 %d，想要 %d", size, len(data))
	}
}

// 中途断流（这次只给一半字节）要能自己接上，而不是整段重来或直接失败。
func TestFetchSegmentResumesAfterShortRead(t *testing.T) {
	data := make([]byte, 4<<20)
	rnd := rand.New(rand.NewSource(9))
	_, _ = rnd.Read(data)

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var start, end int64
		_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		if end >= int64(len(data)) {
			end = int64(len(data)) - 1
		}
		stop := end
		if n == 1 { // 第一次只给一半就断，模拟 googlevideo 的半路 EOF
			stop = start + (end-start)/2
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, stop, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : stop+1])
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "out.dash")
	if err := fetchRanges(context.Background(), srv.Client(), srv.URL, "", "", dst,
		int64(len(data)), 2, nil); err != nil {
		t.Fatalf("应当续传成功: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("续传之后内容不对（首个差异 %d）", firstDiff(got, data))
	}
	if c := atomic.LoadInt32(&calls); c < 2 {
		t.Fatalf("应当至少重试一次，实际 %d 次请求", c)
	}
}

func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}
