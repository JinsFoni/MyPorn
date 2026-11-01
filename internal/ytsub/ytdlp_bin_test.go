package ytsub

import (
	"context"
	"os"
	"path/filepath"
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
