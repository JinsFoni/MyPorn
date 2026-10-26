package library

import (
	"path/filepath"
	"strings"
	"testing"
)

func baseCtx() NameContext {
	return NameContext{
		Title:      "Some  Title: Part/1",
		Date:       "2024-05-06",
		Performer:  "Mini Diva",
		Platform:   "Pornhub",
		ID:         "ph5f2c1",
		Resolution: "2160p",
	}
}

func TestRenderNameDefaultTemplate(t *testing.T) {
	got := RenderName("", baseCtx())
	if got != "Some Title Part 1" {
		t.Fatalf("默认模板应只保留清洗后的标题，得到 %q", got)
	}
}

func TestRenderNameAllTokens(t *testing.T) {
	cases := map[string]string{
		"{date} - {title} [{id}]":             "2024-05-06 - Some Title Part 1 [ph5f2c1]",
		"{performer}/{platform}/{resolution}": "Mini Diva Pornhub 2160p",
		"{发布日期} {艺人}":                         "2024-05-06 Mini Diva",
		"{标题}":                                "Some Title Part 1",
		"未知{foo}":                             "未知",
	}
	for tmpl, want := range cases {
		if got := RenderName(tmpl, baseCtx()); got != want {
			t.Errorf("模板 %q 期望 %q，得到 %q", tmpl, want, got)
		}
	}
}

func TestRenderNameNeverEmpty(t *testing.T) {
	got := RenderName("{title}", NameContext{Title: "///", ID: "abc"})
	if got != "abc" {
		t.Fatalf("标题全非法时应回退到 id，得到 %q", got)
	}
	got = RenderName("{title}", NameContext{})
	if got != "untitled" {
		t.Fatalf("完全空时应回退到 untitled，得到 %q", got)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		`a/b\c:d*e?f"g<h>i|j`:   "a b c d e f g h i j",
		"  multiple   spaces  ": "multiple spaces",
		"...trailing dots...":   "trailing dots",
		"line\nbreak":           "line break",
	}
	for in, want := range cases {
		if got := Sanitize(in, MaxNameRunes); got != want {
			t.Errorf("Sanitize(%q) 期望 %q，得到 %q", in, want, got)
		}
	}
	if got := Sanitize("..", MaxNameRunes); got != "" {
		t.Errorf(".. 应被清空，得到 %q", got)
	}
	long := strings.Repeat("字", MaxNameRunes+50)
	if n := len([]rune(Sanitize(long, MaxNameRunes))); n != MaxNameRunes {
		t.Errorf("超长名称应截断到 %d 个字符，得到 %d", MaxNameRunes, n)
	}
}

func TestBuildMoviePath(t *testing.T) {
	root := t.TempDir()
	p, err := BuildMoviePath(root, "Pornhub", "Mini Diva", "Some Title")
	if err != nil {
		t.Fatalf("构建路径失败: %v", err)
	}
	want := filepath.Join("Pornhub", "Mini Diva", "Some Title")
	if p.RelDir != want {
		t.Fatalf("相对目录期望 %q，得到 %q", want, p.RelDir)
	}
	if !WithinRoot(root, p.AbsDir) {
		t.Fatalf("路径应位于库根内: %s", p.AbsDir)
	}
	if p.VideoName != "Some Title" {
		t.Fatalf("文件名应与目录同名，得到 %q", p.VideoName)
	}
}

func TestBuildMoviePathBlocksTraversal(t *testing.T) {
	root := t.TempDir()
	// 清洗会把 ../ 换成空格，因此不会越界；再校验一次兜底逻辑。
	p, err := BuildMoviePath(root, "../../etc", "..", "../passwd")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !WithinRoot(root, p.AbsDir) {
		t.Fatalf("清洗后仍越界: %s", p.AbsDir)
	}
	if strings.Contains(p.AbsDir, "..") {
		t.Fatalf("路径不应包含 ..：%s", p.AbsDir)
	}
}

func TestWithinRoot(t *testing.T) {
	root := "/media/lib"
	if !WithinRoot(root, "/media/lib/a/b") {
		t.Fatal("子目录应判定在库根内")
	}
	if WithinRoot(root, "/media/libevil/b") {
		t.Fatal("同前缀的兄弟目录不应判定在库根内")
	}
	if WithinRoot(root, "/etc/passwd") {
		t.Fatal("/etc/passwd 不应判定在库根内")
	}
}

func TestCheckSpace(t *testing.T) {
	dir := t.TempDir()
	info := CheckSpace(dir)
	if !info.Exists || !info.Writable {
		t.Fatalf("临时目录应存在且可写: %+v", info)
	}
	if info.TotalBytes == 0 {
		t.Logf("当前平台未报告总容量（可接受）: %+v", info)
	}
	bad := CheckSpace(filepath.Join(dir, "no-such-sub", "deeper"))
	if !bad.Writable {
		t.Fatalf("父目录可写时应允许创建: %+v", bad)
	}
}
