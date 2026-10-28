package xhamster

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLivePerformerProfile 是可选的真实网络冒烟测试（LIVE=1 才跑）：
// 用来看真实的艺人主页还能不能解析下来 —— 尤其是「资料行的 value 是对象数组」
// 这种随改版变脸的形态（2026-10 加上的 "Interests and fetishes" 就是，
// 当年把 value 定成 string 时，这类艺人的整页解析直接失败）。
func TestLivePerformerProfile(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("设置 LIVE=1 才跑真实网络测试")
	}
	slug := os.Getenv("LIVE_XH_SLUG")
	if slug == "" {
		slug = "mysaaat"
	}
	a, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p, err := a.GetPerformer(ctx, slug)
	if err != nil {
		t.Fatalf("抓艺人资料失败: %v", err)
	}
	if p.Name == "" || p.PerformerID != slug {
		t.Fatalf("名字=%q id=%q", p.Name, p.PerformerID)
	}
	if p.VideoCount == nil || *p.VideoCount == 0 {
		t.Errorf("影片数为空，页面结构可能又变了")
	}
	if p.Body.HeightCm <= 0 {
		t.Errorf("身高没从资料行里抽出来: %+v", p.Body)
	}
	if strings.Contains(p.Bio, "&#") {
		t.Errorf("简介里的 HTML 实体没解开: %.60q", p.Bio)
	}
	var interests string
	for _, f := range p.Profile {
		if strings.Contains(f.Label, "Interests") {
			interests = f.Value
		}
	}
	if interests == "" {
		t.Fatalf("没解析到「Interests and fetishes」这一行: %+v", p.Profile)
	}
	t.Logf("name=%q 影片=%d 订阅=%v 资料行=%d", p.Name, *p.VideoCount, p.SubscriberCount, len(p.Profile))
	t.Logf("Interests and fetishes = %s", interests)
}
