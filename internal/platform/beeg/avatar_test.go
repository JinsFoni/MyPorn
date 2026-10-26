package beeg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Mini Diva":        "minidiva",
		"  Mini  Diva ":    "minidiva",
		"Lucía Fernández":  "luciafernandez",
		"Łukasz Østergård": "lukaszostergard",
		"Zoë-Åsa":          "zoeasa",
		"":                 "",
		"!!!":              "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// fakeStore 起一个假的 store.externulls.com，只回 /tag?slug=。
func fakeStore(t *testing.T, status int, body string) *AvatarProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tag" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("slug"); got != "minidiva" {
			t.Errorf("slug 参数 = %q, 期望 minidiva", got)
		}
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "Mozilla/5.0") {
			t.Errorf("UA 没带：%q", ua)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	p := NewAvatarProvider(srv.Client())
	p.storeAPI = srv.URL
	p.thumbBase = "https://thumbs.example.com"
	return p
}

const miniDivaJSON = `{"id":24341,"tg_name":"Mini Diva","tg_slug":"MiniDiva","thumbs":[{"id":809867252018185,"crops":[{"id":3008}]}]}`

func TestResolveAvatarBuildsPhotoURL(t *testing.T) {
	p := fakeStore(t, http.StatusOK, miniDivaJSON)
	got, err := p.ResolveAvatar(context.Background(), "Mini Diva")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := "https://thumbs.example.com/photos/809867252018185/to.webp?crop_id=3008"
	if got != want {
		t.Errorf("头像地址 = %q, 期望 %q", got, want)
	}
}

func TestResolveAvatarWithoutCrop(t *testing.T) {
	p := fakeStore(t, http.StatusOK, `{"tg_name":"Mini Diva","thumbs":[{"id":42}]}`)
	got, err := p.ResolveAvatar(context.Background(), "minidiva")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if want := "https://thumbs.example.com/photos/42/to.webp"; got != want {
		t.Errorf("头像地址 = %q, 期望 %q", got, want)
	}
}

func TestResolveAvatarRejectsNameMismatch(t *testing.T) {
	// 接口按 slug 模糊查，回来的是别人就不能用。
	p := fakeStore(t, http.StatusOK, `{"tg_name":"Mini Divan","thumbs":[{"id":1}]}`)
	if _, err := p.ResolveAvatar(context.Background(), "Mini Diva"); err == nil {
		t.Fatal("名字不匹配应当报错")
	}
}

func TestResolveAvatarHandlesMissAndErrors(t *testing.T) {
	if _, err := fakeStore(t, http.StatusNotFound, "").ResolveAvatar(context.Background(), "Mini Diva"); err == nil {
		t.Fatal("404 应当报错")
	}
	if _, err := fakeStore(t, http.StatusInternalServerError, "").ResolveAvatar(context.Background(), "Mini Diva"); err == nil {
		t.Fatal("500 应当报错")
	}
	if _, err := fakeStore(t, http.StatusOK, `{"tg_name":"Mini Diva","thumbs":[]}`).ResolveAvatar(context.Background(), "Mini Diva"); err == nil {
		t.Fatal("没有头像照片应当报错")
	}
	if _, err := NewAvatarProvider(nil).ResolveAvatar(context.Background(), "!!!"); err == nil {
		t.Fatal("名字压不出 slug 应当报错")
	}
}

// TestLiveResolveAvatar 是可选的真实网络测试（LIVE=1 才跑）。
func TestLiveResolveAvatar(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("设置 LIVE=1 才跑真实网络测试")
	}
	got, err := NewAvatarProvider(nil).ResolveAvatar(context.Background(), "Mini Diva")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(got, "/photos/") || !strings.Contains(got, "to.webp") {
		t.Fatalf("头像地址不对: %q", got)
	}
	t.Logf("Mini Diva 头像: %s", got)
}
