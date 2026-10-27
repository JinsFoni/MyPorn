package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSavedPhotoFiles(t *testing.T) {
	dir := t.TempDir()
	files := []string{"02.jpg", "01.jpg", "03.PNG", "post.json", "04.jpg.part", "x.jpg", "1.jpg"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 子目录不该被当成照片
	if err := os.Mkdir(filepath.Join(dir, "05.jpg"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := savedPhotoFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"01.jpg", "02.jpg", "03.PNG"}
	if len(got) != len(want) {
		t.Fatalf("得到 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("得到 %v，期望 %v", got, want)
		}
	}
}

func TestSavedPhotoFilesMissingDir(t *testing.T) {
	if _, err := savedPhotoFiles(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("目录不存在时应当报错，让调用方决定怎么兜底")
	}
}

func TestPostIDFromURL(t *testing.T) {
	cases := map[string]string{
		"https://x.com/NASA/status/2115129428158087498": "2115129428158087498",
		"https://twitter.com/nasa/status/123?s=20":      "123",
		"https://www.instagram.com/p/Cx1y2z3AbCd/":      "Cx1y2z3AbCd",
		"https://www.instagram.com/nasa/p/Cx1y2z3AbCd/": "Cx1y2z3AbCd",
		"https://x.com/nasa":                            "nasa",
		"":                                              "",
	}
	for in, want := range cases {
		if got := postIDFromURL(in); got != want {
			t.Errorf("postIDFromURL(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestTail(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"2115129428158087498", 8, "58087498"},
		{"abc", 8, "abc"},
		{"", 4, ""},
		{"老帖id", 2, "id"}, // 按 rune 数，不是字节数
	}
	for _, c := range cases {
		if got := tail(c.in, c.n); got != c.want {
			t.Errorf("tail(%q, %d) = %q，期望 %q", c.in, c.n, got, c.want)
		}
	}
}
