package muxcache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeMuxer struct {
	calls int32
	delay time.Duration
	fail  bool
	got   Source
}

func (f *fakeMuxer) RemuxTo(_ context.Context, videoURL, audioURL, referer, ua, outPath string) error {
	atomic.AddInt32(&f.calls, 1)
	f.got = Source{VideoURL: videoURL, AudioURL: audioURL, Referer: referer, UA: ua}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.fail {
		return errors.New("ffmpeg 炸了")
	}
	return os.WriteFile(outPath, []byte("fake mp4"), 0o644)
}

func srcProvider(calls *int32) func(context.Context) (Source, error) {
	return func(context.Context) (Source, error) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		return Source{VideoURL: "v.m4s", AudioURL: "a.m4s", Referer: "https://www.bilibili.com/"}, nil
	}
}

func TestEnsureMuxesOnceThenServesFromDisk(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMuxer{}
	c := New(dir, m)
	var produced int32

	path, err := c.Ensure(context.Background(), "55-1080p", srcProvider(&produced))
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if path != filepath.Join(dir, "55-1080p.mp4") {
		t.Errorf("路径 = %q", path)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "fake mp4" {
		t.Fatalf("产物不对: %q %v", b, err)
	}
	if m.got.AudioURL != "a.m4s" || m.got.Referer == "" {
		t.Errorf("交给 ffmpeg 的参数不对: %+v", m.got)
	}
	if !c.Ready("55-1080p") {
		t.Error("Ready 应当是 true")
	}

	// 第二次：不重新合流，也不再去问上游要地址（签名地址可能已经过期）。
	path2, err := c.Ensure(context.Background(), "55-1080p", srcProvider(&produced))
	if err != nil {
		t.Fatalf("Ensure 二次: %v", err)
	}
	if path2 != path {
		t.Errorf("路径变了: %q vs %q", path2, path)
	}
	if got := atomic.LoadInt32(&m.calls); got != 1 {
		t.Errorf("ffmpeg 跑了 %d 次，期望 1 次（第二次应当命中磁盘）", got)
	}
	if got := atomic.LoadInt32(&produced); got != 1 {
		t.Errorf("上游被问了 %d 次，期望 1 次", got)
	}
}

func TestEnsureSingleFlight(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMuxer{delay: 120 * time.Millisecond}
	c := New(dir, m)
	var produced int32

	// 浏览器起播会同时打好几个 Range 请求：只能跑一次 ffmpeg。
	var wg sync.WaitGroup
	errs := make([]error, 8)
	paths := make([]string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = c.Ensure(context.Background(), "k", srcProvider(&produced))
		}(i)
	}
	wg.Wait()

	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("第 %d 个请求失败: %v", i, errs[i])
		}
		if paths[i] != paths[0] {
			t.Fatalf("路径不一致: %q vs %q", paths[i], paths[0])
		}
	}
	if got := atomic.LoadInt32(&m.calls); got != 1 {
		t.Errorf("ffmpeg 跑了 %d 次，期望 1 次（单飞）", got)
	}
	if got := atomic.LoadInt32(&produced); got != 1 {
		t.Errorf("上游被问了 %d 次，期望 1 次", got)
	}
}

func TestEnsureCallerCancelDoesNotKillMux(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMuxer{delay: 150 * time.Millisecond}
	c := New(dir, m)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Ensure(ctx, "k", srcProvider(nil))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("调用方取消后应当立刻返回 context.Canceled，得到 %v", err)
	}
	// 合流本身继续跑：下一个人进来直接拿现成的。
	if _, err := c.Ensure(context.Background(), "k", srcProvider(nil)); err != nil {
		t.Fatalf("第二次应当成功: %v", err)
	}
	if got := atomic.LoadInt32(&m.calls); got != 1 {
		t.Errorf("ffmpeg 跑了 %d 次，期望 1 次", got)
	}
}

func TestEnsureFailureIsRetriable(t *testing.T) {
	dir := t.TempDir()
	m := &fakeMuxer{fail: true}
	c := New(dir, m)

	if _, err := c.Ensure(context.Background(), "k", srcProvider(nil)); err == nil {
		t.Fatal("ffmpeg 失败时应当把错误抛出来")
	}
	if c.Ready("k") {
		t.Error("失败后不应当留有成品")
	}
	// 失败不缓存：下一次还会重试。
	m.fail = false
	if _, err := c.Ensure(context.Background(), "k", srcProvider(nil)); err != nil {
		t.Fatalf("重试应当成功: %v", err)
	}
	if got := atomic.LoadInt32(&m.calls); got != 2 {
		t.Errorf("ffmpeg 跑了 %d 次，期望 2 次", got)
	}
}

func TestEnsureRejectsSingleTrack(t *testing.T) {
	c := New(t.TempDir(), &fakeMuxer{})
	_, err := c.Ensure(context.Background(), "k", func(context.Context) (Source, error) {
		return Source{VideoURL: "v.m4s"}, nil
	})
	if err == nil {
		t.Fatal("只有一条流时应当报错")
	}
}

func TestEnsureNoMuxer(t *testing.T) {
	c := New(t.TempDir(), nil)
	if _, err := c.Ensure(context.Background(), "k", srcProvider(nil)); err == nil {
		t.Fatal("没有合流器时应当报错")
	}
}

func TestPruneRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, &fakeMuxer{})
	c.TTL = time.Hour
	stale := filepath.Join(dir, "old.mp4")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "new.mp4")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ensure(context.Background(), "k", srcProvider(nil)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("过期成品应当被清掉")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("新成品不该被清掉")
	}
}

func TestSafeName(t *testing.T) {
	// 清晰度 id 里带点（1080p.av1），文件名要原样保留点；斜杠等一律换掉。
	if got := safeName("55-1080p.av1"); got != "55-1080p.av1" {
		t.Errorf("safeName = %q", got)
	}
	if got := safeName("../../etc/passwd"); got != ".._.._etc_passwd" {
		t.Errorf("safeName 没挡住路径穿越: %q", got)
	}
	if got := safeName(""); got != "mux" {
		t.Errorf("空 key = %q", got)
	}
}
