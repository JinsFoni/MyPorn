package xcom

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCandidates(t *testing.T) {
	cases := []struct {
		name string
		want []string
	}{
		{"Mini Diva", []string{"minidiva", "mini_diva"}},
		{"  Mini  Diva ", []string{"minidiva", "mini_diva"}},
		{"Lucía", []string{"lucia"}},
		{"Charming Milana", []string{"charmingmilana", "charming_milana"}},
		// 超过 X 账号名 15 字符上限，猜不出来就别瞎猜
		{"Kendra Sunderland", nil},
		{"!!!", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := handleCandidates(c.name)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("handleCandidates(%q) = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

func TestNameMatches(t *testing.T) {
	cases := []struct {
		display, want string
		ok            bool
	}{
		{"Mini Diva", "Mini Diva", true},
		{"Milana", "Charming Milana", true},       // 显示名只写了名的后半截
		{"Mini Diva | OF", "Mini Diva", true},     // 显示名加了后缀
		{"FANTIASHA WALLACE", "Mini Diva", false}, // 同名账号，但不是这个人
		{"", "Mini Diva", false},
		{"Zoë", "Zoe", true}, // 变音符号折叠后一致
		{"Sweet Jane", "Mini Diva", false},
	}
	for _, c := range cases {
		if got := nameMatches(c.display, c.want); got != c.ok {
			t.Errorf("nameMatches(%q, %q) = %v, 期望 %v", c.display, c.want, got, c.ok)
		}
	}
}

func TestAvatarFrom(t *testing.T) {
	cases := []struct{ in, want, errContains string }{
		{"https://pbs.twimg.com/profile_images/1/abc_400x400.jpg", "https://pbs.twimg.com/profile_images/1/abc_400x400.jpg", ""},
		{"https://pbs.twimg.com/profile_images/1/abc_normal.jpg", "https://pbs.twimg.com/profile_images/1/abc_400x400.jpg", ""},
		{"https://abs.twimg.com/sticky/default_profile_images/default_profile_400x400.png", "", "没有头像"},
		{"https://pbs.twimg.com/default_profile_images/x.png", "", "没传过头像"},
		{"", "", "没有头像地址"},
	}
	for _, c := range cases {
		got, err := avatarFrom(c.in)
		if c.errContains != "" {
			if err == nil || !strings.Contains(err.Error(), c.errContains) {
				t.Errorf("avatarFrom(%q) 错误 = %v, 期望包含 %q", c.in, err, c.errContains)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("avatarFrom(%q) = %q, %v; 期望 %q", c.in, got, err, c.want)
		}
	}
}

// profileHTML 拼一个和 X 真实返回结构相近的个人页。
func profileHTML(display, handle, image string) string {
	return fmt.Sprintf(`<!doctype html><html><head>
<meta property="og:type" content="profile">
<meta property="og:title" content="%s (@%s) on X">
<meta property="og:image" content="%s">
</head><body></body></html>`, display, handle, image)
}

// fakeX 起一个假的 x.com：pages 是 path -> (状态码, HTML)。
func fakeX(t *testing.T, pages map[string]struct {
	status int
	body   string
}) *AvatarProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "Mozilla/5.0") {
			t.Errorf("UA 没带：%q", ua)
		}
		page, ok := pages[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(profileHTML("User Profile Not Found - X | 404 Error", "", "")))
			return
		}
		w.WriteHeader(page.status)
		_, _ = w.Write([]byte(page.body))
	}))
	t.Cleanup(srv.Close)
	p := NewAvatarProvider(srv.Client())
	p.base = srv.URL
	return p
}

type page = struct {
	status int
	body   string
}

const milanaAvatar = "https://pbs.twimg.com/profile_images/1550877283409330176/9geuFBXw_400x400.jpg"

func TestResolveAvatarFallsThroughCandidates(t *testing.T) {
	// 拼一起的那个 handle 是同名路人：显示名对不上，应该继续试下一个候选。
	p := fakeX(t, map[string]page{
		"charmingmilana":  {http.StatusOK, profileHTML("Stark", "CharmingMilana", "https://pbs.twimg.com/profile_images/1/stark_400x400.jpg")},
		"charming_milana": {http.StatusOK, profileHTML("Milana", "Charming_Milana", milanaAvatar)},
	})
	got, err := p.ResolveAvatar(context.Background(), "Charming Milana")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got != milanaAvatar {
		t.Errorf("头像 = %q, 期望 %q", got, milanaAvatar)
	}
}

func TestResolveAvatarRejectsStranger(t *testing.T) {
	p := fakeX(t, map[string]page{
		"minidiva":  {http.StatusOK, profileHTML("FANTIASHA WALLACE", "MINIDIVA", "https://pbs.twimg.com/profile_images/1/fw_400x400.jpg")},
		"mini_diva": {http.StatusOK, profileHTML("Mini Diva", "mini_diva", "https://pbs.twimg.com/profile_images/1/md_400x400.jpg")},
	})
	got, err := p.ResolveAvatar(context.Background(), "Mini Diva")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, "md_400x400") {
		t.Errorf("应跳过同名路人，实际拿到 %q", got)
	}
}

func TestResolveAvatarMisses(t *testing.T) {
	// 两个候选都不存在
	if _, err := fakeX(t, map[string]page{}).ResolveAvatar(context.Background(), "Mini Diva"); err == nil {
		t.Error("账号都不存在时应报错")
	}
	// 账号存在但没传过头像
	p := fakeX(t, map[string]page{
		"minidiva": {http.StatusOK, profileHTML("Mini Diva", "minidiva", "https://abs.twimg.com/sticky/default_profile_images/default_profile_400x400.png")},
	})
	if _, err := p.ResolveAvatar(context.Background(), "Mini Diva"); err == nil {
		t.Error("只有默认头像时应报错")
	}
	// 风控页：200 但没有 og 信息
	p = fakeX(t, map[string]page{
		"minidiva": {http.StatusOK, "<html><body>Something went wrong</body></html>"},
	})
	if _, err := p.ResolveAvatar(context.Background(), "Mini Diva"); err == nil {
		t.Error("页面没有账号信息时应报错")
	}
	// 限流
	p = fakeX(t, map[string]page{
		"minidiva": {http.StatusTooManyRequests, ""},
	})
	if _, err := p.ResolveAvatar(context.Background(), "Mini Diva"); err == nil {
		t.Error("429 时应报错")
	}
	// 名字转不出 handle
	if _, err := NewAvatarProvider(nil).ResolveAvatar(context.Background(), "!!!"); err == nil {
		t.Error("空 slug 时应报错")
	}
}
