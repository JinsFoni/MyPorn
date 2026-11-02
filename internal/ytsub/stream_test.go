package ytsub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 假 yt-dlp：只吐一份 -j。字段照真实输出裁（formats[] 里 manifest_url 是清单地址，
// 带 expire= 的签名寿命），这里要验的是「挑清单 + 读寿命」这段解析。
const fakeStreamYtDlp = `#!/bin/sh
cat <<'JSON'
{"id":"lTRiuFIWV54","title":"1 A.M Study Session","duration":3674,
 "formats":[
  {"format_id":"137","protocol":"https","height":1080},
  {"format_id":"270","protocol":"m3u8_native","height":1080,
   "manifest_url":"https://manifest.googlevideo.com/api/manifest/hls_variant/expire/4070908800/id/953462b85216579e/index.m3u8"}
 ]}
JSON
`

const fakeNoHLSYtDlp = `#!/bin/sh
cat <<'JSON'
{"id":"vid1","title":"无清单","duration":10,"formats":[{"format_id":"18","protocol":"https","height":360}]}
JSON
`

func writeFakeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveStreamPassesCookies 播放解析必须把设置里的 cookies.txt 交给 yt-dlp。
//
// 2026-11-03 的现场：用户在设置里填了 cookie，播放照样被要求人机验证。量下来是**这条命令行
// 里根本没有 --cookies**（只有下载那条带了）——登录态对解析一样是刚需：年龄限制 / 被要求
// 人机验证的片子，不带 cookie 连清单都不给。这条测试钉住「参数里有 --cookies，且文件内容
// 就是库里那份」（不是空文件，也不是下载那条路的临时文件）。
func TestResolveStreamPassesCookies(t *testing.T) {
	dir := t.TempDir()
	cookieText := "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tsecret-value\n"
	// 假 yt-dlp：把收到的参数抄一份，顺手把 --cookies 指的文件也留一份下来。
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + dir + "/args.txt\n" +
		"prev=\"\"\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--cookies\" ]; then cp \"$a\" " + dir + "/cookies.txt; fi\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"cat <<'JSON'\n" +
		`{"id":"vid1","title":"带 cookie","duration":10,` +
		`"formats":[{"format_id":"hls","protocol":"m3u8_native","height":1080,` +
		`"manifest_url":"https://manifest.googlevideo.com/api/manifest/hls_variant/expire/4070908800/id/x/index.m3u8"}]}` + "\n" +
		"JSON\n"
	bin := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	y := NewYtDlp(func() string { return bin }, nil, func() string { return cookieText }, nil, nil)
	if _, err := y.ResolveStream(context.Background(), "vid1"); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--cookies") {
		t.Fatalf("解析命令行里没有 --cookies:\n%s", args)
	}
	got, err := os.ReadFile(filepath.Join(dir, "cookies.txt"))
	if err != nil {
		t.Fatalf("--cookies 指的文件没留下（可能是空路径）: %v", err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(cookieText) {
		t.Fatalf("交给 yt-dlp 的 cookie 内容不对:\n%q", string(got))
	}
}

// 配了 cookies 之后快车道必须换成「支持 cookie 的那些客户端」。
//
// 2026-11-03 实测：yt-dlp 一旦拿到登录态就把 visionos 摘掉
// （`Skipping client "visionos" since it does not support cookies`），快车道只剩几张
// storyboard 图，白跑一趟；而且它的默认集合给的 HLS 最高 1080p、4K 只在 AV1 直链里。
// 换成 tv_samsung,web_safari 一次 6.24 s 就拿到 2160 的 AV1 直链 + 18 条 m3u8。
func TestResolveStreamFastLaneClient(t *testing.T) {
	run := func(cookieText string) string {
		dir := t.TempDir()
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + dir + "/args.txt\n" +
			"cat <<'JSON'\n" +
			`{"id":"vid1","title":"t","duration":10,` +
			`"formats":[{"format_id":"hls","protocol":"m3u8_native","height":1080,` +
			`"manifest_url":"https://manifest.googlevideo.com/api/manifest/hls_variant/expire/4070908800/id/x/index.m3u8"}]}` + "\n" +
			"JSON\n"
		bin := filepath.Join(dir, "yt-dlp")
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		var cookies func() string
		if cookieText != "" {
			cookies = func() string { return cookieText }
		}
		y := NewYtDlp(func() string { return bin }, nil, cookies, nil, nil)
		if _, err := y.ResolveStream(context.Background(), "vid1"); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		args, err := os.ReadFile(filepath.Join(dir, "args.txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(args)
	}

	if got := run(""); !strings.Contains(got, "player_client=visionos") {
		t.Fatalf("没 cookie 时快车道应还是 visionos:\n%s", got)
	}
	got := run("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\tv\n")
	if !strings.Contains(got, "player_client=tv_samsung,web_safari") {
		t.Fatalf("配了 cookie 时快车道要换成 tv_samsung,web_safari:\n%s", got)
	}
	if strings.Contains(got, "player_client=visionos") {
		t.Fatalf("配了 cookie 还去问 visionos（会被 yt-dlp 摘掉、白跑一趟）:\n%s", got)
	}
}

func TestResolveStreamPicksManifest(t *testing.T) {
	path := writeFakeScript(t, fakeStreamYtDlp)
	dlp := NewYtDlp(func() string { return path }, func() string { return "" },
		func() string { return "" }, func() string { return "" }, nil)

	info, err := dlp.ResolveStream(context.Background(), "lTRiuFIWV54")
	if err != nil {
		t.Fatalf("ResolveStream 失败: %v", err)
	}
	if info.MasterURL == "" || !strings.Contains(info.MasterURL, "hls_variant") {
		t.Fatalf("没挑到 HLS master: %q", info.MasterURL)
	}
	if info.DurationSec != 3674 {
		t.Fatalf("时长 = %d，想要 3674", info.DurationSec)
	}
	// expire=4070908800 → 2099-01-01（签名寿命要跟着地址走，会话靠它过期；用远未来，别写会过期的时间戳）
	if got := info.ExpiresAt.Unix(); got != 4070908800 {
		t.Fatalf("expire 没读出来: %d", got)
	}
	if info.ExpiresAt.Before(time.Now()) {
		t.Fatalf("expire 解析成了过去: %v", info.ExpiresAt)
	}
}

func TestResolveStreamWithoutHLS(t *testing.T) {
	path := writeFakeScript(t, fakeNoHLSYtDlp)
	dlp := NewYtDlp(func() string { return path }, func() string { return "" },
		func() string { return "" }, func() string { return "" }, nil)

	if _, err := dlp.ResolveStream(context.Background(), "vid1"); err == nil {
		t.Fatal("没有 manifest_url 时应该报错，而不是给个空地址")
	}
}

func TestExpireFromURL(t *testing.T) {
	// 清单把 expire 放在路径里（真实形状），分片才放在查询串里 —— 两种都得认。
	if got := expireFromURL("https://manifest.googlevideo.com/api/manifest/hls_variant/expire/4070908800/id/abc/index.m3u8"); got.Unix() != 4070908800 {
		t.Fatalf("路径里的 expire 解析成 %v", got)
	}
	if got := expireFromURL("https://x/playlist?expire=100&sig=abc"); got.Unix() != 100 {
		t.Fatalf("expire=100 解析成 %v", got)
	}
	if got := expireFromURL("https://x/playlist"); !got.IsZero() {
		t.Fatalf("没有 expire 时应返回零值，得到 %v", got)
	}
	if got := expireFromURL("https://x/playlist?expire=abc"); !got.IsZero() {
		t.Fatalf("expire 非数字时应返回零值，得到 %v", got)
	}
}

// 4K 走 DASH 那条线的挑法：只认 AV1 + mp4 + https，音轨只认 AAC（m4a）。
// 形状照 2026-10-29 的真实 -j 输出裁（YFsMnJEbV1U）。
func TestResolveStreamPicksAV1Track(t *testing.T) {
	const fake = `#!/bin/sh
cat <<'JSON'
{"id":"YFsMnJEbV1U","title":"4K","duration":321,
 "formats":[
  {"format_id":"628","protocol":"m3u8_native","height":2160,"ext":"mp4","vcodec":"vp09.00.51.08","acodec":"none","tbr":28220.65,
   "manifest_url":"https://manifest.googlevideo.com/api/manifest/hls_variant/expire/4070908800/id/x/index.m3u8"},
  {"format_id":"315","protocol":"https","height":2160,"ext":"webm","vcodec":"vp9","acodec":"none","tbr":26340.159,"url":"https://rr1---x.googlevideo.com/videoplayback?itag=315"},
  {"format_id":"401","protocol":"https","height":2160,"ext":"mp4","vcodec":"av01.0.13M.08","acodec":"none","tbr":13461.975,"url":"https://rr1---x.googlevideo.com/videoplayback?itag=401"},
  {"format_id":"137","protocol":"https","height":1080,"ext":"mp4","vcodec":"avc1.640028","acodec":"none","tbr":6234.34,"url":"https://rr1---x.googlevideo.com/videoplayback?itag=137"},
  {"format_id":"251","protocol":"https","ext":"webm","vcodec":"none","acodec":"opus","abr":99.278,"url":"https://rr1---x.googlevideo.com/videoplayback?itag=251"},
  {"format_id":"140","protocol":"https","ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","abr":129.501,"url":"https://rr1---x.googlevideo.com/videoplayback?itag=140"},
  {"format_id":"139","protocol":"https","ext":"m4a","vcodec":"none","acodec":"mp4a.40.5","abr":48.814,"url":"https://rr1---x.googlevideo.com/videoplayback?itag=139"}
 ]}
JSON
`
	path := writeFakeScript(t, fake)
	dlp := NewYtDlp(func() string { return path }, func() string { return "" },
		func() string { return "" }, func() string { return "" }, nil)

	info, err := dlp.ResolveStream(context.Background(), "YFsMnJEbV1U")
	if err != nil {
		t.Fatalf("ResolveStream 失败: %v", err)
	}
	// VP9（webm）和 H.264 都不该出现在这张表里：前者码率翻倍，后者 4K 没有。
	if len(info.Tracks) != 1 {
		t.Fatalf("应该只挑出 1 条 AV1 直链，得到 %d 条: %+v", len(info.Tracks), info.Tracks)
	}
	tr := info.Tracks[0]
	if tr.Height != 2160 || !strings.Contains(tr.Codecs, "av01") {
		t.Fatalf("挑错了档位: %+v", tr)
	}
	if !strings.Contains(tr.VideoURL, "itag=401") {
		t.Fatalf("画面地址不是 AV1 那条: %q", tr.VideoURL)
	}
	// 音轨取码率最高的 AAC（itag 140 / 129 kbps），不能拿 opus 那条。
	if !strings.Contains(tr.AudioURL, "itag=140") || tr.AudioCodecs != "mp4a.40.2" {
		t.Fatalf("音轨挑错了: %q %q", tr.AudioURL, tr.AudioCodecs)
	}
	if tr.Bandwidth != 13461975 {
		t.Fatalf("码率 = %d，想要 13461975（13.46 Mbps）", tr.Bandwidth)
	}
}

// 没有 AAC 音轨时不要硬凑：上层会退回 HLS（合流只认 mp4 容器里的 AAC）。
func TestDashTracksWithoutAAC(t *testing.T) {
	tracks := dashTracks([]streamFormat{
		{FormatID: "401", Protocol: "https", Height: 2160, Ext: "mp4", VCodec: "av01.0.13M.08", ACodec: "none", Tbr: 13461.975, URL: "https://x/v"},
		{FormatID: "251", Protocol: "https", Ext: "webm", VCodec: "none", ACodec: "opus", AbR: 99.278, URL: "https://x/a"},
	})
	if len(tracks) != 0 {
		t.Fatalf("只有 opus 音轨时不该给出直链，得到 %+v", tracks)
	}
}

// 老版本 yt-dlp 不认 --remote-components：应当自动去掉它重跑，而不是整个功能报错。
func TestRunRetriesWithoutRemoteComponents(t *testing.T) {
	const fake = `#!/bin/sh
for a in "$@"; do
  if [ "$a" = "--remote-components" ]; then
    echo "yt-dlp: error: unrecognized arguments: --remote-components ejs:github" >&2
    exit 2
  fi
done
echo "json output"
`
	path := writeFakeScript(t, fake)
	dlp := NewYtDlp(func() string { return path }, func() string { return "" },
		func() string { return "" }, func() string { return "" }, nil)

	out, _, err := dlp.run(context.Background(), path, dlp.commonArgs()...)
	if err != nil {
		t.Fatalf("应该自动降级重跑成功，得到错误: %v", err)
	}
	if !strings.Contains(out, "json output") {
		t.Fatalf("重跑没拿到输出: %q", out)
	}
	if !dlp.rcBad.Load() {
		t.Fatal("撞过 unrecognized arguments 之后应当记住不再带这个参数")
	}
	if len(dlp.commonArgs()) != 0 {
		t.Fatalf("置位后 commonArgs 不该再带上它: %v", dlp.commonArgs())
	}
}

func TestUnknownRemoteComponents(t *testing.T) {
	if !unknownRemoteComponents("yt-dlp: error: unrecognized arguments: --remote-components ejs:github") {
		t.Fatal("应当认出 unrecognized arguments")
	}
	if unknownRemoteComponents("ERROR: unable to download video data: HTTP Error 403") {
		t.Fatal("403 不该被当成「不认参数」")
	}
	if got := withoutRemoteComponents([]string{"-j", "--remote-components", "ejs:github", "URL"}); len(got) != 2 {
		t.Fatalf("摘除后应剩 2 个参数: %v", got)
	}
}
