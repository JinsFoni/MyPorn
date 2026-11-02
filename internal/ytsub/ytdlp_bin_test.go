package ytsub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 配置里的绝对路径**不可执行**时必须当成「没找到」。
//
// 2026-11-02 真踩：/tmp/ytvenv/bin/yt-dlp 被 macOS 定时 tmp 清理删掉，只剩同目录的
// python 符号链接；Bin() 当时把配置的路径原样返回，于是 /api/yt/health 报
// found=true、version=""，界面显示「版本新鲜」，点开任何视频都是 502
// fork/exec … no such file or directory。这三条把当时的三种坏形状都钉住：
// 文件不存在 / 路径是目录 / 文件在但没有可执行位。
func TestBinRejectsUnusableConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	notExec := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\necho 2026.10.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
	}{
		{"文件不存在", filepath.Join(dir, "nope", "yt-dlp")},
		{"路径是目录", dir},
		{"没有可执行位", notExec},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			y := NewYtDlp(func() string { return c.path }, nil, nil, nil, nil)
			if got := y.Bin(); got != "" {
				t.Fatalf("Bin() = %q，不可执行的路径必须当没找到", got)
			}
			info := y.Version(context.Background())
			if info.Found {
				t.Fatalf("Found = true，界面会显示成「版本新鲜」")
			}
			if info.Path != c.path {
				t.Fatalf("Path = %q，界面上要能看到自己填过哪个路径", info.Path)
			}
			if info.Error == "" {
				t.Fatal("Error 为空，用户看不出为什么不能用")
			}
		})
	}
}

// 可执行位齐了的文件要照常被认下来（不然上面那条会误伤正常配置）。
func TestBinAcceptsExecutableConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "yt-dlp")
	script := "#!/bin/sh\necho 2026.10.26\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	y := NewYtDlp(func() string { return bin }, nil, nil, nil, nil)
	if got := y.Bin(); got != bin {
		t.Fatalf("Bin() = %q，想要 %q", got, bin)
	}
	info := y.Version(context.Background())
	if !info.Found || info.Version != "2026.10.26" {
		t.Fatalf("Version() = %+v，想要 found + 2026.10.26", info)
	}
	if info.Error != "" {
		t.Fatalf("Error = %q，正常配置不该有错误文案", info.Error)
	}
}

// 填「命令名」而不是路径时要走 PATH（默认配置 `YTDLP_PATH` 缺省就是 "yt-dlp"）。
//
// 拿命令名去 os.Stat 只会得到相对路径 "./yt-dlp"，于是默认配置会被判成「找不到」——
// 这条是 2026-11-02 改 Bin() 时自己撞出来的：本机 PATH 里有 brew 的 yt-dlp，
// 不写环境变量启动，健康检查却报 found=false。
func TestBinResolvesBareCommandNameFromPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	y := NewYtDlp(func() string { return "sh" }, nil, nil, nil, nil)
	if got := y.Bin(); got != "/bin/sh" && got != "/usr/bin/sh" {
		t.Fatalf("Bin() = %q，命令名要走 PATH 解析", got)
	}

	missing := NewYtDlp(func() string { return "yt-dlp-not-installed-xyz" }, nil, nil, nil, nil)
	if got := missing.Bin(); got != "" {
		t.Fatalf("Bin() = %q，PATH 里没有的命令名必须是空串", got)
	}
	if info := missing.Version(context.Background()); info.Found || info.Error == "" {
		t.Fatalf("Version() = %+v，想要 found=false + 错误文案", info)
	}
}

// flat 列表里的 `availability`：subscriber_only = 会员抢先看，要如实报出来
// （jobs 的 ytBackfill 据此丢掉不入库）。
//
// 这份 JSON 的形状照 2026-11-02 真跑 `yt-dlp -J --flat-playlist` 的输出剪的：
// 40 条里只有一条 availability="subscriber_only"（2gtrgAKIH88），其余是 null。
func TestListChannelFlagsMembersOnly(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "yt-dlp")
	script := `#!/bin/sh
cat <<'JSON'
{"entries":[
 {"id":"2gtrgAKIH88","title":"会员抢先看的那条","duration":205,"view_count":null,
  "timestamp":null,"availability":"subscriber_only","live_status":null},
 {"id":"iEZ_quzVcOE","title":"公开的那条","duration":601,"view_count":139,
  "timestamp":null,"availability":null,"live_status":null}
]}
JSON
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	y := NewYtDlp(func() string { return bin }, func() string { return "" }, func() string { return "" },
		func() string { return "" }, nil)
	videos, err := y.ListChannel(context.Background(), "UC7h5bqq_rzVxqhLyKtA7JRw", 10)
	if err != nil {
		t.Fatalf("列频道失败: %v", err)
	}
	if len(videos) != 2 {
		t.Fatalf("应拿到 2 条，得到 %d", len(videos))
	}
	if !videos[0].MembersOnly {
		t.Error("availability=subscriber_only 的那条没被标成会员抢先看")
	}
	if videos[0].DurationSec != 205 {
		t.Errorf("会员抢先看的时长也该照收（用户可能想留证据），得到 %d", videos[0].DurationSec)
	}
	if videos[1].MembersOnly {
		t.Error("公开视频被误标成会员抢先看")
	}
}

// 会员抢先看的 yt-dlp 原话要翻成一句人话，别把 stderr 原样丢给用户。
// 实测（2026-11-02，2gtrgAKIH88）：
// `ERROR: [youtube] 2gtrgAKIH88: Join this channel to get access to members-only content
//
//	like this video, and other exclusive perks.`
func TestMapErrorMembersOnly(t *testing.T) {
	y := NewYtDlp(func() string { return "" }, nil, nil, nil, nil)
	stderr := "ERROR: [youtube] 2gtrgAKIH88: Join this channel to get access to members-only content like this video, and other exclusive perks."
	err := y.mapError(errStub{}, stderr)
	msg := err.Error()
	if !strings.Contains(msg, "会员抢先观看") {
		t.Fatalf("错误信息没说是会员抢先看: %q", msg)
	}
	if strings.Contains(msg, "Join this channel") {
		t.Fatalf("把 yt-dlp 的英文原话丢给用户了: %q", msg)
	}
}

// cookies 被浏览器轮换（WARNING）要盖过那句含糊的 `Sign in to confirm you're not a bot`。
//
// 实测原话（2026-11-03，M34TIq4UBA0）：
// `WARNING: [youtube] The provided YouTube account cookies are no longer valid.
//
//	They have likely been rotated in the browser as a security measure.`
//
// 同一次运行的最后一行才是 `ERROR: … Sign in to confirm you're not a bot.`——
// 只按 ERROR 判就会让用户又去查网络，而真正该做的是重新导出一份 cookie。
func TestMapErrorRotatedCookies(t *testing.T) {
	y := NewYtDlp(func() string { return "" }, nil, nil, nil, nil)
	stderr := "WARNING: [youtube] The provided YouTube account cookies are no longer valid. " +
		"They have likely been rotated in the browser as a security measure.\n" +
		"ERROR: [youtube] M34TIq4UBA0: Sign in to confirm you're not a bot.\n"
	msg := y.mapError(errStub{}, stderr).Error()
	if !strings.Contains(msg, "cookies.txt 已经失效") {
		t.Fatalf("应指向「cookies 失效」，得到: %q", msg)
	}
}

// warnings 里的噪音不能把原因判歪：`nsig extraction failed` 这类不是「接口变了」。
func TestMapErrorIgnoresWarningNoise(t *testing.T) {
	y := NewYtDlp(func() string { return "" }, nil, nil, nil, nil)
	stderr := "WARNING: [youtube] nsig extraction failed: Some formats may be missing\n" +
		"ERROR: [youtube] M34TIq4UBA0: Sign in to confirm you're not a bot.\n"
	msg := y.mapError(errStub{}, stderr).Error()
	if !strings.Contains(msg, "人机验证") {
		t.Fatalf("应判成人机验证，得到: %q", msg)
	}
	if strings.Contains(msg, "接口又变了") {
		t.Fatalf("被 nsig 的 warning 带偏成「升级 yt-dlp」了: %q", msg)
	}
}

type errStub struct{}

func (errStub) Error() string { return "exit status 1" }
