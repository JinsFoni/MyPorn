package ytsub

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDownloadHonoursContextCancel 取消任务必须能把 yt-dlp 掐掉。
//
// 这里踩过一个真坑：Download 原先先等读管道的 goroutine、再 cmd.Wait()，
// 而 yt-dlp 调起来的 ffmpeg 等孙进程也继承着这两根管道——只要它们还活着，
// 读取就等不到 EOF，取消要一直挂到下载自然结束（实测 60 秒）。
func TestDownloadHonoursContextCancel(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "yt-dlp")
	script := "#!/bin/sh\ntrap 'exit 130' INT\nsleep 60 &\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	y := NewYtDlp(func() string { return bin }, func() string { return "" }, func() string { return "" }, func() string { return "" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1 * time.Second)
		cancel()
	}()
	start := time.Now()
	_, err := y.Download(ctx, DownloadRequest{VideoID: "x", WorkDir: filepath.Join(dir, "w")})
	if err == nil {
		t.Fatal("取消之后不该返回成功")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("取消响应太慢: %v", took)
	}
}
