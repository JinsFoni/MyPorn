package ytstream

import (
	"context"
	"errors"
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
