package ytsub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const jarFull = `# Netscape HTTP Cookie File
.youtube.com	TRUE	/	TRUE	0	SID	aaa
.youtube.com	TRUE	/	TRUE	0	HSID	bbb
.youtube.com	TRUE	/	TRUE	0	SSID	ccc
.youtube.com	TRUE	/	TRUE	0	APISID	ddd
.youtube.com	TRUE	/	TRUE	0	SAPISID	eee
.youtube.com	TRUE	/	TRUE	0	__Secure-1PSID	fff
.youtube.com	TRUE	/	TRUE	0	__Secure-3PSID	ggg
#HttpOnly_.youtube.com	TRUE	/	TRUE	0	LOGIN_INFO	hhh
.youtube.com	TRUE	/	TRUE	0	PREF	iii
`

// jarRotated 是「YouTube 把登录态全清掉、只留访客 cookie」之后的样子
// （2026-11-04 实测：26 行 → 16 行，SID/HSID/SSID/APISID/SAPISID/LOGIN_INFO/__Secure-1PSID 全没）。
const jarRotated = `# Netscape HTTP Cookie File
.youtube.com	TRUE	/	TRUE	0	PREF	iii
.youtube.com	TRUE	/	TRUE	0	GPS	jjj
`

func TestLoginCookieCount(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"满的 8 条", jarFull, 8},
		{"被清掉", jarRotated, 0},
		{"过期的不算", "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1000000000\tSID\taaa\n", 0},
		{"负数时间戳（已过期）", ".youtube.com\tTRUE\t/\tTRUE\t-1\tSID\taaa\n", 0},
		{"别的站点不算", ".instagram.com\tTRUE\t/\tTRUE\t0\tSID\taaa\n.example.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\tbbb\n", 0},
		{"空文件", "", 0},
	}
	for _, c := range cases {
		if got := LoginCookieCount(c.text); got != c.want {
			t.Fatalf("%s: LoginCookieCount = %d，想要 %d", c.name, got, c.want)
		}
	}
}

// TestCookieJarCheckoutIsPrivate 每轮发一份私有副本：两个同时跑的调用不能共用一个文件，
// 否则两边退出时的写回会互相把 jar 写花；收尾要自己删掉。
func TestCookieJarCheckoutIsPrivate(t *testing.T) {
	dir := t.TempDir()
	j := NewCookieJar(filepath.Join(dir, "ytcookies"), nil, nil)

	p1, done1, err := j.Checkout(jarFull)
	if err != nil || p1 == "" {
		t.Fatalf("Checkout() = %q, %v", p1, err)
	}
	p2, done2, err := j.Checkout(jarFull)
	if err != nil || p2 == "" {
		t.Fatalf("Checkout() = %q, %v", p2, err)
	}
	if p1 == p2 {
		t.Fatalf("两次调用拿到同一个文件 %q，并发时会被写花", p1)
	}
	fi, err := os.Stat(p1)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cookie 文件权限 = %o，要 0600", perm)
	}
	if got, _ := os.ReadFile(p1); strings.TrimSpace(string(got)) != strings.TrimSpace(jarFull) {
		t.Fatalf("副本内容不对:\n%s", got)
	}
	done1()
	done2()
	if _, err := os.Stat(p1); !os.IsNotExist(err) {
		t.Fatalf("收尾之后副本应当删掉: %v", err)
	}
	if _, err := os.Stat(p2); !os.IsNotExist(err) {
		t.Fatalf("收尾之后副本应当删掉: %v", err)
	}
}

// TestCookieJarCollectSavesRotation 每轮收尾必须把轮换结果交给 save。
func TestCookieJarCollectSavesRotation(t *testing.T) {
	dir := t.TempDir()
	var saved string
	j := NewCookieJar(filepath.Join(dir, "ytcookies"), func(text string) error { saved = text; return nil }, nil)

	path, done, err := j.Checkout(jarFull)
	if err != nil {
		t.Fatal(err)
	}
	// yt-dlp 没动过 -> 不该存。
	done()
	if saved != "" {
		t.Fatalf("没变化却存了一次: %q", saved)
	}

	path, done, err = j.Checkout(jarFull)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 yt-dlp 把登录态清掉后写回。
	if err := os.WriteFile(path, []byte(jarRotated), 0o600); err != nil {
		t.Fatal(err)
	}
	done()
	if strings.TrimSpace(saved) != strings.TrimSpace(jarRotated) {
		t.Fatalf("存回设置的内容不对:\n%s", saved)
	}
}

// TestCookieJarRejectsTornWrite 写断的 jar（没有结尾换行 / 列数不对）不能存回设置，
// 否则一次并发写坏就把还能用的登录态覆盖成半截文件。
func TestCookieJarRejectsTornWrite(t *testing.T) {
	dir := t.TempDir()
	var saved string
	j := NewCookieJar(filepath.Join(dir, "ytcookies"), func(text string) error { saved = text; return nil }, nil)

	path, done, err := j.Checkout(jarFull)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(".youtube.com\tTRUE\t/\tTRUE\t0\tSID\taaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	done()
	if saved != "" {
		t.Fatalf("写断的文件不该被采用: %q", saved)
	}
	if res := j.collect(jarFull, path); res.Changed {
		t.Fatalf("collect 结果 = %+v，想要不改", res)
	}
}

func TestCookieJarNilSafe(t *testing.T) {
	var j *CookieJar
	if p, done, err := j.Checkout(jarFull); p != "" || err != nil {
		t.Fatalf("nil jar 的 Checkout() = %q, %v", p, err)
	} else {
		done()
	}
	if j.Dir() != "" {
		t.Fatalf("nil jar 的 Dir() 应当是空串")
	}
}

// TestResolveStreamPersistsRotatedCookies 是这件事的端到端钉子：
// 假 yt-dlp 在跑的时候把 cookie 文件改掉（真 yt-dlp 就是这么把轮换后的 jar 写回来的），
// 跑完之后那份改动必须被存回设置，而且下一轮用的是**存回去的那份**，不是库里的旧快照。
func TestResolveStreamPersistsRotatedCookies(t *testing.T) {
	dir := t.TempDir()
	// 假 yt-dlp：找到 --cookies 指向的文件，追加一行（模拟轮换），再吐一份最小 JSON。
	script := "#!/bin/sh\n" +
		"prev=\"\"\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--cookies\" ]; then printf '.youtube.com\\tTRUE\\t/\\tTRUE\\t0\\t__Secure-1PSIDTS\\trotated\\n' >> \"$a\"; fi\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"cat <<'JSON'\n" +
		`{"id":"vid1","title":"x","duration":10,` +
		`"formats":[{"format_id":"hls","protocol":"m3u8_native","height":1080,` +
		`"manifest_url":"https://manifest.googlevideo.com/api/manifest/hls_variant/expire/4070908800/id/x/index.m3u8"}]}` + "\n" +
		"JSON\n"
	bin := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	jarPath := filepath.Join(dir, "data", "ytcookies")
	store := jarFull
	jar := NewCookieJar(jarPath, func(text string) error { store = text; return nil }, nil)
	y := NewYtDlp(func() string { return bin }, nil, func() string { return store }, nil, nil)
	y.SetCookieJar(jar)

	if _, err := y.ResolveStream(context.Background(), "vid1"); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(store, "__Secure-1PSIDTS\trotated") {
		t.Fatalf("yt-dlp 写回的轮换结果没存回设置（下一轮又会拿旧快照去敲门）:\n%s", store)
	}
	// 副本必须收干净：留一堆 cookie 文件在磁盘上等于把登录态摊在盘里。
	if left, _ := os.ReadDir(jarPath); len(left) != 0 {
		t.Fatalf("跑完还剩 %d 个副本文件", len(left))
	}

	// 第二轮：文件被重写过（同内容 + 一行），内容必须是存回去的那份。
	if _, err := y.ResolveStream(context.Background(), "vid1"); err != nil {
		t.Fatalf("第二轮解析失败: %v", err)
	}
	if n := strings.Count(store, "__Secure-1PSIDTS\trotated"); n != 2 {
		t.Fatalf("第二轮应当基于上一轮存回的那份（应出现 2 行），实际 %d 行:\n%s", n, store)
	}
}

// TestLoginAlive 「还剩 1 条空壳」不能算活着：2026-11-04 实测那次 YouTube 只留下
// `__Secure-3PSID`，SID / LOGIN_INFO 都清掉了——照样是未登录。
func TestLoginAlive(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"满的", jarFull, true},
		{"被清掉", jarRotated, false},
		{"只剩空壳", ".youtube.com\tTRUE\t/\tTRUE\t0\t__Secure-3PSID\tggg\n", false},
		{"只有 SID 没有 LOGIN_INFO", ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\taaa\n", false},
		{"SID 过期了", ".youtube.com\tTRUE\t/\tTRUE\t1000000000\tSID\taaa\n#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\thhh\n", false},
	}
	for _, c := range cases {
		if got := LoginAlive(c.text); got != c.want {
			t.Fatalf("%s: LoginAlive = %v，想要 %v", c.name, got, c.want)
		}
	}
}
