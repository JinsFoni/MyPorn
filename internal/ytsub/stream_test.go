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
   "manifest_url":"https://manifest.googlevideo.com/api/manifest/hls_variant/expire/1793159341/id/953462b85216579e/index.m3u8"}
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
	// expire=1793159341 → 2026-10-27T…（签名寿命要跟着地址走，会话靠它过期）
	if got := info.ExpiresAt.Unix(); got != 1793159341 {
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
	if got := expireFromURL("https://manifest.googlevideo.com/api/manifest/hls_variant/expire/1793159341/id/abc/index.m3u8"); got.Unix() != 1793159341 {
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
