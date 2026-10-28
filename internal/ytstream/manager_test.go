package ytstream

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionRefIsStable(t *testing.T) {
	m := NewManager(nil, nil)
	s := m.NewSession("vid1", time.Now().Add(time.Hour))

	// 同一个地址两次登记必须拿到同一个号：清单被重新拉一次时，
	// 播放器手里的旧分片地址不能突然指向别处。
	first := s.Ref("https://rr1.googlevideo.com/seg/1")
	if again := s.Ref("https://rr1.googlevideo.com/seg/1"); again != first {
		t.Fatalf("同一地址两次登记得到不同地址: %s vs %s", first, again)
	}
	second := s.Ref("https://rr1.googlevideo.com/seg/2")
	if first == second {
		t.Fatal("不同地址拿到了同一个号")
	}
	if s.Len() != 2 {
		t.Fatalf("地址簿条目 = %d，想要 2", s.Len())
	}

	if got, ok := s.URL(0); !ok || got != "https://rr1.googlevideo.com/seg/1" {
		t.Fatalf("按号取回错了: %q ok=%v", got, ok)
	}
	if _, ok := s.URL(9); ok {
		t.Fatal("越界号不该取到地址")
	}
}

func TestManagerCachesResolve(t *testing.T) {
	calls := 0
	m := NewManager(func(context.Context, string) (Upstream, error) {
		calls++
		return Upstream{VideoID: "vid1", MasterURL: "https://x/m.m3u8", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, nil)

	for i := 0; i < 3; i++ {
		if _, err := m.Upstream(context.Background(), "vid1"); err != nil {
			t.Fatal(err)
		}
	}
	// 解析一次 2~4 秒，三个调用只该问一次 YouTube。
	if calls != 1 {
		t.Fatalf("解析被调了 %d 次，想要 1", calls)
	}
}

func TestManagerReResolvesWhenExpired(t *testing.T) {
	calls := 0
	now := time.Now()
	m := NewManager(func(context.Context, string) (Upstream, error) {
		calls++
		return Upstream{VideoID: "vid1", MasterURL: "https://x/m.m3u8", ExpiresAt: now.Add(2 * time.Minute)}, nil
	}, nil)
	m.now = func() time.Time { return now }

	if _, err := m.Upstream(context.Background(), "vid1"); err != nil {
		t.Fatal(err)
	}
	// 签名只剩 2 分钟：缓存里的地址已经不能用了，必须重新解析。
	now = now.Add(90 * time.Second)
	if _, err := m.Upstream(context.Background(), "vid1"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("快过期时没有重新解析（调了 %d 次）", calls)
	}
}

func TestManagerResolveErrorPropagates(t *testing.T) {
	want := errors.New("yt-dlp 挂了")
	m := NewManager(func(context.Context, string) (Upstream, error) { return Upstream{}, want }, nil)
	if _, err := m.Upstream(context.Background(), "vid1"); !errors.Is(err, want) {
		t.Fatalf("错误没透传: %v", err)
	}
	// 失败不该被缓存：下一轮还得能重试。
	if _, err := m.Upstream(context.Background(), "vid1"); !errors.Is(err, want) {
		t.Fatalf("第二次的错误不对: %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, nil)
	m.now = func() time.Time { return now }
	s := m.NewSession("vid1", now.Add(time.Hour))

	if _, ok := m.Session(s.ID); !ok {
		t.Fatal("刚建的会话就查不到")
	}
	// 宽限期内还认（播放器可能攥着上一版清单）。
	now = now.Add(time.Hour + 5*time.Minute)
	if _, ok := m.Session(s.ID); !ok {
		t.Fatal("宽限期内不该失效")
	}
	now = now.Add(10 * time.Minute)
	if _, ok := m.Session(s.ID); ok {
		t.Fatal("宽限期过了还认这个会话")
	}
}

func TestManagerEvictsOldSessions(t *testing.T) {
	m := NewManager(nil, nil)
	first := m.NewSession("vid1", time.Now().Add(time.Hour))
	for i := 0; i < maxSessions+2; i++ {
		m.NewSession("vid-other", time.Now().Add(time.Hour))
	}
	if _, ok := m.Session(first.ID); ok {
		t.Fatal("超过上限后最老的会话应该被淘汰")
	}
}

// 悬停预热、弹窗信息、播放器清单会挤在一两秒内发出：
// 没有去重就会并发起多个 yt-dlp，谁都慢。
func TestManagerResolveSingleFlight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	m := NewManager(func(context.Context, string) (Upstream, error) {
		calls.Add(1)
		<-release
		return Upstream{VideoID: "vid1", MasterURL: "https://x/m.m3u8", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Upstream(context.Background(), "vid1"); err != nil {
				t.Error(err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // 让五个都进到等待里
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("解析跑了 %d 次，想要 1", got)
	}
}

// 调用方等不及了（关掉页面）也不能把别人拖住：自己的 ctx 取消只影响自己。
func TestManagerResolveWaiterCancel(t *testing.T) {
	release := make(chan struct{})
	m := NewManager(func(context.Context, string) (Upstream, error) {
		<-release
		return Upstream{VideoID: "vid1", MasterURL: "https://x/m.m3u8", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}, nil)

	go func() { _, _ = m.Upstream(context.Background(), "vid1") }()
	time.Sleep(30 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Upstream(ctx, "vid1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的调用方应该立刻返回 context.Canceled，得到 %v", err)
	}
	close(release)
	time.Sleep(30 * time.Millisecond)
}

// 预热和播放器会要同一段分片：认领过的地址不该被第二个人再认领一次。
func TestWarmClaimIsExclusive(t *testing.T) {
	w := NewManager(nil, nil).Warm("vid1")
	abs := "https://rr1.googlevideo.com/seg/init"

	if !w.Claim(abs) {
		t.Fatal("第一次认领应该成功")
	}
	if w.Claim(abs) {
		t.Fatal("已经有人在取，第二次认领不该成功")
	}
	w.Settle(abs, "video/mp4", []byte("init"))

	// 取回来以后同样不再认领：调用方该走缓存（Wait 会立刻命中），别再去上游下一遍。
	if w.Claim(abs) {
		t.Fatal("已经取回来的地址不该再认领")
	}
}

// 等的人拿到的是认领方取回来的那份字节，而不是自己去上游再要一份。
func TestWarmWaitGetsOwnerBytes(t *testing.T) {
	w := NewManager(nil, nil).Warm("vid1")
	abs := "https://rr1.googlevideo.com/seg/0"
	if !w.Claim(abs) {
		t.Fatal("认领失败")
	}

	got := make(chan Prefetched, 1)
	go func() {
		p, ok := w.Wait(context.Background(), abs)
		if !ok {
			t.Error("等待方应当拿到字节")
		}
		got <- p
	}()
	time.Sleep(30 * time.Millisecond) // 让等待方进到 select 里
	w.Settle(abs, "video/mp4", []byte("bytes"))

	select {
	case p := <-got:
		if string(p.Body) != "bytes" {
			t.Fatalf("拿到 %q，想要 bytes", p.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("Settle 之后等待方没有醒过来")
	}
}

// 认领方失败（签名过期之类）也要放行：等待方退回自己回源，绝不能一直挂着。
func TestWarmWaitReleasedOnFailure(t *testing.T) {
	w := NewManager(nil, nil).Warm("vid1")
	abs := "https://rr1.googlevideo.com/seg/1"
	if !w.Claim(abs) {
		t.Fatal("认领失败")
	}

	done := make(chan bool, 1)
	go func() {
		_, ok := w.Wait(context.Background(), abs)
		done <- ok
	}()
	time.Sleep(30 * time.Millisecond)
	w.Settle(abs, "", nil)

	select {
	case ok := <-done:
		if ok {
			t.Fatal("没取到字节却报告命中")
		}
	case <-time.After(time.Second):
		t.Fatal("失败之后等待方没有被放行")
	}
}

// 等待方自己的 ctx 取消（关掉页面）只影响自己，不能把在途的预热也弄坏。
func TestWarmWaitHonoursCancel(t *testing.T) {
	w := NewManager(nil, nil).Warm("vid1")
	abs := "https://rr1.googlevideo.com/seg/2"
	if !w.Claim(abs) {
		t.Fatal("认领失败")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := w.Wait(ctx, abs); ok {
		t.Fatal("取消的等待方不该报告命中")
	}
	w.Settle(abs, "video/mp4", []byte("late"))
	if p, ok := w.Get(abs); !ok || string(p.Body) != "late" {
		t.Fatal("等待方取消不该影响预热结果")
	}
}

// 没有在途取数的地址，Wait 就是一次普通的缓存读。
func TestWarmWaitFallsBackToGet(t *testing.T) {
	w := NewManager(nil, nil).Warm("vid1")
	if _, ok := w.Wait(context.Background(), "https://x/none"); ok {
		t.Fatal("空缓存不该命中")
	}
	w.Settle("https://x/one", "video/mp4", []byte("x"))
	if p, ok := w.Wait(context.Background(), "https://x/one"); !ok || string(p.Body) != "x" {
		t.Fatal("已取回的地址该命中")
	}
}
