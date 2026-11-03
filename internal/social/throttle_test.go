package social

import (
	"context"
	"testing"
	"time"
)

// 按宿主的冷却：一个宿主被限流，另一个宿主必须照常放行。
// 依据是 X 实测的三份独立额度（docs/M5-SOCIAL.md §15.15）。
func TestThrottleHostCooldown(t *testing.T) {
	tr := NewThrottle(time.Millisecond)
	if err := tr.WaitHost(context.Background(), "widget"); err != nil {
		t.Fatalf("空闲时不该拦: %v", err)
	}
	until := time.Now().Add(15 * time.Minute)
	tr.CoolUntilHost("widget", until)

	if d := tr.HostCooldown("widget"); d <= 0 {
		t.Fatal("widget 应该在冷却")
	}
	if d := tr.HostCooldown("cdn"); d != 0 {
		t.Fatalf("cdn 不该被连坐，却在冷却 %s", d)
	}
	if err := tr.WaitHost(context.Background(), "cdn"); err != nil {
		t.Fatalf("另一个宿主应该放行: %v", err)
	}

	err := tr.WaitHost(context.Background(), "widget")
	if KindOf(err) != KindRateLimited {
		t.Fatalf("冷却中的宿主应报限流，得到 %v", err)
	}
	if reset, ok := ResetAtOf(err); !ok || reset.Unix() != until.Unix() {
		t.Errorf("reset 时间不对: %v %v", reset, ok)
	}

	// 整站冷却仍然管所有宿主（Instagram 那种只有一个配额的站用它）。
	tr.CoolUntil(time.Now().Add(10 * time.Minute))
	if err := tr.WaitHost(context.Background(), "cdn"); KindOf(err) != KindRateLimited {
		t.Fatalf("整站冷却时任何宿主都该拦，得到 %v", err)
	}

	// Clear 一次清干净：换 Cookie / 换 IP 之后不该还等旧冷却。
	tr.Clear()
	if d := tr.HostCooldown("widget"); d != 0 {
		t.Errorf("Clear 之后宿主冷却应该清零，还剩 %s", d)
	}
	if d := tr.CooldownRemaining(); d != 0 {
		t.Errorf("Clear 之后整站冷却应该清零，还剩 %s", d)
	}
}

// 冷却只压住「自己这个宿主」，不该影响同一个 Throttle 上的串行间隔判断。
func TestThrottleHostCooldownKeepsGap(t *testing.T) {
	tr := NewThrottle(30 * time.Millisecond)
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := tr.WaitHost(context.Background(), "a"); err != nil {
			t.Fatalf("第 %d 次不该拦: %v", i, err)
		}
	}
	if el := time.Since(start); el < 55*time.Millisecond {
		t.Errorf("三次之间应该有间隔，实测只花了 %s", el)
	}
}
