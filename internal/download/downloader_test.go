package download

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newFakeCDN 搭一个提供 master / media / 分片的假 CDN。
func newFakeCDN(t *testing.T, segCount int) (*httptest.Server, *int64) {
	t.Helper()
	var segmentHits int64
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	mux.HandleFunc("/hls/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=640x360
low/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=6000000,RESOLUTION=1920x1080
high/index.m3u8
`)
	})
	handleMedia := func(w http.ResponseWriter, r *http.Request) {
		var sb strings.Builder
		sb.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := 0; i < segCount; i++ {
			sb.WriteString("#EXTINF:4.0,\n")
			sb.WriteString(fmt.Sprintf("seg-%d.ts\n", i))
		}
		sb.WriteString("#EXT-X-ENDLIST\n")
		fmt.Fprint(w, sb.String())
	}
	mux.HandleFunc("/hls/high/index.m3u8", handleMedia)
	mux.HandleFunc("/hls/low/index.m3u8", handleMedia)
	serveSegment := func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&segmentHits, 1)
		// 内容 = 分片序号，方便断言拼接顺序。
		idx := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(r.URL.Path), "seg-"), ".ts")
		fmt.Fprint(w, "SEG"+idx+";")
	}
	mux.HandleFunc("/hls/high/seg-", serveSegment)
	mux.HandleFunc("/hls/high/", serveSegment)
	return srv, &segmentHits
}

func TestResolvePicksHighestVariant(t *testing.T) {
	srv, _ := newFakeCDN(t, 3)
	defer srv.Close()

	d := New(srv.Client())
	d.Concurrency = 2
	pl, err := d.Resolve(context.Background(), srv.URL+"/hls/master.m3u8")
	if err != nil {
		t.Fatalf("解析清单失败: %v", err)
	}
	if len(pl.Segments) != 3 {
		t.Fatalf("分片数期望 3，得到 %d", len(pl.Segments))
	}
	if !strings.Contains(pl.Segments[0].URI, "/hls/high/") {
		t.Fatalf("应选中最高码率子清单，实际分片地址 %s", pl.Segments[0].URI)
	}
}

func TestRunDownloadsAndConcatenatesInOrder(t *testing.T) {
	srv, hits := newFakeCDN(t, 5)
	defer srv.Close()

	dir := t.TempDir()
	out := filepath.Join(dir, "out.bin")
	d := New(srv.Client())
	d.Concurrency = 3

	var lastP Progress
	pl, err := d.Resolve(context.Background(), srv.URL+"/hls/master.m3u8")
	if err != nil {
		t.Fatalf("解析清单失败: %v", err)
	}
	if err := d.Run(context.Background(), pl, filepath.Join(dir, "work"), out, func(p Progress) {
		lastP = p
	}); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("读取结果失败: %v", err)
	}
	want := "SEG0;SEG1;SEG2;SEG3;SEG4;"
	if string(data) != want {
		t.Fatalf("拼接顺序错误，期望 %q，得到 %q", want, string(data))
	}
	if atomic.LoadInt64(hits) != 5 {
		t.Fatalf("每个分片应只下载一次，实际 %d 次", atomic.LoadInt64(hits))
	}
	if lastP.DoneSegments != 5 || lastP.Percent < 99.9 {
		t.Fatalf("进度回调不完整: %+v", lastP)
	}
}

func TestRunResumesFromExistingSegments(t *testing.T) {
	srv, hits := newFakeCDN(t, 4)
	defer srv.Close()

	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// 预置前两个已完成分片，模拟上次中断。
	for _, i := range []int{0, 1} {
		p := filepath.Join(work, fmt.Sprintf("seg_%06d.bin", i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("SEG%d;", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	d := New(srv.Client())
	d.Concurrency = 2
	pl, err := d.Resolve(context.Background(), srv.URL+"/hls/master.m3u8")
	if err != nil {
		t.Fatalf("解析清单失败: %v", err)
	}
	out := filepath.Join(dir, "out.bin")
	if err := d.Run(context.Background(), pl, work, out, nil); err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	data, _ := os.ReadFile(out)
	if string(data) != "SEG0;SEG1;SEG2;SEG3;" {
		t.Fatalf("续传结果错误: %q", string(data))
	}
	if n := atomic.LoadInt64(hits); n != 2 {
		t.Fatalf("断点续传应只下载缺失的 2 个分片，实际 %d 次", n)
	}
}

func TestRunHonorsContextCancel(t *testing.T) {
	srv, _ := newFakeCDN(t, 40)
	defer srv.Close()

	dir := t.TempDir()
	d := New(srv.Client())
	d.Concurrency = 1
	pl, err := d.Resolve(context.Background(), srv.URL+"/hls/master.m3u8")
	if err != nil {
		t.Fatalf("解析清单失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = d.Run(ctx, pl, filepath.Join(dir, "work"), filepath.Join(dir, "out.bin"), nil)
	if err == nil {
		t.Fatal("取消后应返回错误")
	}
}

// TestBytesCountedWhileSegmentStreams 字节数必须边收边记，不能等整片写完才加一次。
//
// 这一条是上一条的地基：台阶的来源就是「分片写完才 os.Stat 加一次」。
// 服务端把一个 2MB 的分片拆 16 次、每次隔 150ms 吐出来，要求在**单个分片**下载过程中
// 就能看到至少 4 个不同的 BytesDone 值。
func TestBytesCountedWhileSegmentStreams(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/hls/high/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:4.0,\nseg-0.ts\n#EXT-X-ENDLIST\n")
	})
	chunk := make([]byte, 128<<10)
	mux.HandleFunc("/hls/high/", func(w http.ResponseWriter, r *http.Request) {
		// 2MB 分片拆 16 次吐、每次隔 150ms（总 2.4 秒）→ 心跳能响三次
		for i := 0; i < 16; i++ {
			_, _ = w.Write(chunk)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(150 * time.Millisecond)
		}
	})

	d := New(srv.Client())
	d.Concurrency = 1
	pl, err := d.Resolve(context.Background(), srv.URL+"/hls/high/index.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var seen []int64
	err = d.Run(context.Background(), pl, dir, filepath.Join(dir, "out.mp4"), func(p Progress) {
		if n := len(seen); n == 0 || seen[n-1] != p.BytesDone {
			seen = append(seen, p.BytesDone)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("单个分片下载过程中看到的体积序列：%v", seen)
	// 全片 1MB，只有落地那一下才加的话序列长度是 1（0 不进采样）
	if len(seen) < 4 {
		t.Errorf("字节数是台阶式的，只看到 %d 个中间值：%v", len(seen), seen)
	}
	if last := seen[len(seen)-1]; last != 2<<20 {
		t.Errorf("最终体积应是 2MB，得到 %d", last)
	}
}
