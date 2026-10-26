package platform

import (
	"context"
	"testing"
)

func TestImageHostPlatform(t *testing.T) {
	cases := []struct{ url, fallback, want string }{
		// beeg 的图床归 beeg（跨站头像要用它自己的代理配置）
		{"https://thumbs.externulls.com/photos/1/to.webp", "pornhub", "beeg"},
		{"https://externulls.com/photos/1/to.webp", "pornhub", "beeg"},
		{"https://ci.phncdn.com/videos/1.jpg", "pornhub", "pornhub"},
		{"://bad url", "pornhub", "pornhub"},
	}
	for _, c := range cases {
		if got := ImageHostPlatform(c.url, c.fallback); got != c.want {
			t.Errorf("ImageHostPlatform(%q) = %q, 期望 %q", c.url, got, c.want)
		}
	}
}

func TestAvatarRegistryNilSafe(t *testing.T) {
	var reg *AvatarRegistry
	reg.Register(nil)
	if got := reg.All(); got != nil {
		t.Fatalf("nil registry 应当返回空，得到 %v", got)
	}
	reg = NewAvatarRegistry()
	reg.Register(&fakeProvider{})
	if len(reg.All()) != 1 {
		t.Fatalf("注册失败: %v", reg.All())
	}
}

type fakeProvider struct{}

func (fakeProvider) Key() string { return "fake" }

func (fakeProvider) ResolveAvatar(_ context.Context, _ string) (string, error) { return "", nil }
