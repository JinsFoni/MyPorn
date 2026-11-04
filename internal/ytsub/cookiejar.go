package ytsub

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CookieJar 管 yt-dlp 那份 cookies.txt 的「发出去 → 收回来」。
//
// 为什么不能「每次调用写一份临时文件、用完即删」：`--cookies` 在 yt-dlp 里的定义是
// 「Netscape formatted file to read cookies from **and dump cookie jar in**」
// （yt-dlp 2026.10.26 的 options.py 原文；YoutubeDL.close() 里 `save_cookies()` 只认
// `cookiefile` 那个路径）。YouTube 每轮请求都在轮换 `__Secure-1PSIDTS` /
// `__Secure-3PSIDTS`，yt-dlp 把轮换后的 jar 写回**传给它的那个文件**——过去我们
// 把那份文件当场删掉，下一轮又写回库里那份旧快照，于是每一轮都在用已经作废的
// 1PSIDTS 去敲门，YouTube 判定「这份 cookie 已在浏览器里被轮换」，几次之后直接把
// 登录态清空。
//
// 2026-11-04 实测（库里 2026-11-03T12:24Z 存的那份，26 条 youtube 行）：同一份文件跑一次
// `yt-dlp -j`，文件从 4238 字节缩到 2894 字节、26 行掉到 16 行，`SID` / `HSID` / `SSID` /
// `APISID` / `SAPISID` / `LOGIN_INFO` / `__Secure-1PSID` 全被 YouTube 置为过期
// （control.txt 与 persist.txt 的 diff），报错是 `Sign in to confirm you're not a bot`。
// 掉下来的那 10 行本来是「还没被轮换」的证据，被我们每轮丢掉了。
//
// 做法：每轮发一份**私有副本**给 yt-dlp（同时跑的两轮不会把同一个 jar 写花），跑完把
// 副本读回来；内容与发出去的不同就存回设置（save 回调），登录态掉了就 WARN。
// 数据库里那份因此永远是「yt-dlp 最后一次知道的 jar」，不是「用户第一次粘进来的那份」。
type CookieJar struct {
	dir  string
	save func(string) error
	log  *slog.Logger
	// mu 只保护 save + 日志：并发的两轮各收各的副本，先后落库，最后一个说了算。
	mu sync.Mutex
}

// NewCookieJar 建一个把运行期 jar 放在 dir 下的 jar；save 把最新内容写回设置（可为 nil，测试用）。
func NewCookieJar(dir string, save func(string) error, log *slog.Logger) *CookieJar {
	if log == nil {
		log = slog.Default()
	}
	return &CookieJar{dir: dir, save: save, log: log}
}

// Dir 是运行期 jar 的存放目录（日志与测试用）。
func (j *CookieJar) Dir() string {
	if j == nil {
		return ""
	}
	return j.dir
}

// Checkout 给这次调用发一份私有 cookies.txt，返回路径和收尾函数
// （收尾 = 把 yt-dlp 可能写回来的轮换结果存回设置，然后删掉副本）。
//
// content 为空（没配 cookie）返回空路径，调用方就不加 --cookies。
// 写不出来也返回空路径：宁可这一轮不带登录态跑，也别让一次写盘失败毁掉整个请求。
func (j *CookieJar) Checkout(content string) (string, func(), error) {
	if j == nil {
		return "", func() {}, nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", func() {}, nil
	}
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return "", func() {}, err
	}
	j.sweep()
	f, err := os.CreateTemp(j.dir, "cookies-*.txt")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	if _, err := f.WriteString(content + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", func() {}, err
	}
	f.Close()
	// CreateTemp 已经是 0600，这里再钉一次：cookie 是登录凭证。
	if err := os.Chmod(path, 0o600); err != nil {
		os.Remove(path)
		return "", func() {}, err
	}
	return path, func() {
		j.collect(content, path)
		os.Remove(path)
	}, nil
}

// MergeResult 是一轮之后对 jar 的盘点。
type MergeResult struct {
	// Changed 表示 yt-dlp 写回了新内容（YouTube 轮换过 cookie）。
	Changed bool
	// LoginsBefore / LoginsAfter 是登录态 cookie 的条数（只数 youtube 域、未过期的）。
	LoginsBefore int
	LoginsAfter  int
}

// collect 读回这一轮的副本；和发出去的内容不同就存回设置。
//
// 失败的那一轮也要收：yt-dlp 在**报错退出时也会** dump cookie jar，
// 2026-11-04 实测「报 Sign in to confirm you're not a bot 的那一轮」正是把
// SID / LOGIN_INFO 写成过期的那一轮——不收回来的话，界面永远显示「已配置」，
// 看不出它已经死了。
func (j *CookieJar) collect(sent, path string) MergeResult {
	raw, err := os.ReadFile(path)
	if err != nil {
		return MergeResult{}
	}
	got := strings.TrimSpace(string(raw))
	if got == "" || got == sent {
		return MergeResult{}
	}
	if !looksLikeCookieJar(raw) {
		j.log.Warn("yt-dlp 回写的 cookies.txt 不像一份完整的 jar（可能被写断），这次不采用", "path", path)
		return MergeResult{}
	}
	before, after := LoginCookieCount(sent), LoginCookieCount(got)
	res := MergeResult{Changed: true, LoginsBefore: before, LoginsAfter: after}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.save != nil {
		if err := j.save(got); err != nil {
			j.log.Warn("把 yt-dlp 轮换后的 Cookie 存回设置失败：下一轮还是旧快照", "err", err)
			return res
		}
	}
	if after < before {
		j.log.Warn("YouTube 轮换了 Cookie 并清掉了登录态：这份很快就不管用了，去设置里重导",
			"before", before, "after", after)
	} else {
		j.log.Debug("已存回 yt-dlp 轮换后的 Cookie", "logins", after, "bytes", len(got))
	}
	return res
}

// sweep 顺手清掉崩溃留下的副本（正常路径会自己删）。
func (j *CookieJar) sweep() {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return
	}
	cutoff := nowFunc().Add(-24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "cookies-") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(j.dir, e.Name()))
		}
	}
}

// looksLikeCookieJar 判断回写的内容是不是一份完整的 Netscape jar：
// 至少一行数据、每行 7 列、且以换行结尾（写断的文件通常没有结尾换行）。
func looksLikeCookieJar(raw []byte) bool {
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return false
	}
	rows := 0
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(strings.Split(line, "\t")) != 7 {
			return false
		}
		rows++
	}
	return rows > 0
}

// nowFunc 便于测试里固定「现在」。
var nowFunc = time.Now

// loginCookieNames 是「这份 cookie 还登录着 YouTube」的依据。
//
// 2026-11-04 实测那份作废的 jar 里这 8 个名字掉了 7 个，所以「一个都不剩」
// 就等于登录态没了，界面上不该再说「已配置」。
var loginCookieNames = map[string]bool{
	"SID":            true,
	"HSID":           true,
	"SSID":           true,
	"APISID":         true,
	"SAPISID":        true,
	"LOGIN_INFO":     true,
	"__Secure-1PSID": true,
	"__Secure-3PSID": true,
}

// LoginCookieCount 数一份 Netscape cookies.txt 里还剩几条未过期的 YouTube 登录态 cookie。
//
// 只看 youtube 域：别的站点（用户可能把整个浏览器的 cookie 一起导进来）不算。
// 过期时间是第 5 列，0 表示会话 cookie（导出时很常见），负数是已经过期。
func LoginCookieCount(text string) int {
	n := 0
	now := nowFunc().Unix()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 7 {
			continue
		}
		domain := strings.TrimPrefix(strings.ToLower(cols[0]), ".")
		if domain != "youtube.com" && !strings.HasSuffix(domain, ".youtube.com") {
			continue
		}
		if !loginCookieNames[cols[5]] {
			continue
		}
		if exp, err := strconv.ParseInt(cols[4], 10, 64); err == nil {
			if exp < 0 || (exp > 0 && exp <= now) {
				continue
			}
		}
		n++
	}
	return n
}

// essentialLoginNames 是「YouTube 认不认这份登录态」的两根支柱。
//
// 2026-11-04 实测作废的那一轮：`APISID` / `HSID` / `LOGIN_INFO` / `SAPISID` / `SID` /
// `SIDCC` / `SSID` / `__Secure-1P*` 全被置成过期，只剩一个 `__Secure-3PSID` 的空壳——
// 于是「还剩 1 条登录 cookie」这种数字看着还行，其实已经登录不上了。所以判断死活不看总数，
// 只看这两个：少了任何一个，请求就会被当成未登录（报「请确认你不是机器人」）。
var essentialLoginNames = []string{"SID", "LOGIN_INFO"}

// LoginAlive 回答「这份 cookies.txt 还能当登录态用吗」。
func LoginAlive(text string) bool {
	have := map[string]bool{}
	now := nowFunc().Unix()
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 7 {
			continue
		}
		domain := strings.TrimPrefix(strings.ToLower(cols[0]), ".")
		if domain != "youtube.com" && !strings.HasSuffix(domain, ".youtube.com") {
			continue
		}
		if exp, err := strconv.ParseInt(cols[4], 10, 64); err == nil {
			if exp < 0 || (exp > 0 && exp <= now) {
				continue
			}
		}
		have[cols[5]] = true
	}
	for _, name := range essentialLoginNames {
		if !have[name] {
			return false
		}
	}
	return true
}
