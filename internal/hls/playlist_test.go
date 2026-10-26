package hls

import (
	"strings"
	"testing"
)

const mediaFixture = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:12
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:4.000000,
seg-12.ts
#EXTINF:4.000000,
seg-13.ts
#EXTINF:2.500000,
https://cdn.example.com/abs/seg-14.ts
#EXT-X-ENDLIST
`

const masterFixture = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
360p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
1080p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=12000000,RESOLUTION=3840x2160,CODECS="hvc1.1.6.L150,mp4a.40.2"
2160p/index.m3u8
`

func TestParseMediaPlaylist(t *testing.T) {
	pl, err := ParseMediaPlaylist(mediaFixture, "https://cdn.example.com/hls/index.m3u8")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(pl.Segments) != 3 {
		t.Fatalf("分片数期望 3，得到 %d", len(pl.Segments))
	}
	if pl.Segments[0].URI != "https://cdn.example.com/hls/seg-12.ts" {
		t.Errorf("相对地址未补全: %s", pl.Segments[0].URI)
	}
	if pl.Segments[2].URI != "https://cdn.example.com/abs/seg-14.ts" {
		t.Errorf("绝对地址被改写: %s", pl.Segments[2].URI)
	}
	if pl.MediaSequence != 12 || pl.Segments[0].Seq != 12 || pl.Segments[2].Seq != 14 {
		t.Errorf("序号错误: media=%d first=%d last=%d", pl.MediaSequence, pl.Segments[0].Seq, pl.Segments[2].Seq)
	}
	if got := pl.TotalDuration; got < 10.4 || got > 10.6 {
		t.Errorf("总时长期望 10.5，得到 %v", got)
	}
	if !pl.EndList {
		t.Error("应识别 EXT-X-ENDLIST")
	}
}

func TestParseMediaPlaylistEncryptedAndMap(t *testing.T) {
	src := `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="key.bin"
#EXT-X-MAP:URI="init.mp4"
#EXTINF:4.0,
f1.m4s
`
	pl, err := ParseMediaPlaylist(src, "https://cdn.example.com/a/index.m3u8")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !pl.Encrypted {
		t.Error("应识别加密")
	}
	if pl.InitURI != "https://cdn.example.com/a/init.mp4" {
		t.Errorf("EXT-X-MAP 解析错误: %s", pl.InitURI)
	}
}

func TestParseMediaPlaylistRejectsEmpty(t *testing.T) {
	if _, err := ParseMediaPlaylist("#EXTM3U\n#EXT-X-ENDLIST\n", "https://x/y.m3u8"); err == nil {
		t.Fatal("没有分片时应报错")
	}
}

func TestIsMasterAndBestChild(t *testing.T) {
	if !IsMaster(masterFixture) {
		t.Fatal("应识别为 master")
	}
	if IsMaster(mediaFixture) {
		t.Fatal("media 不应被判为 master")
	}
	got := BestChildURL(masterFixture, "https://cdn.example.com/hls/master.m3u8")
	want := "https://cdn.example.com/hls/2160p/index.m3u8"
	if got != want {
		t.Fatalf("最高质量子清单期望 %q，得到 %q", want, got)
	}
}

func TestParseAttrList(t *testing.T) {
	attrs := ParseAttrList(`BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"`)
	if attrs["BANDWIDTH"] != "5000000" {
		t.Errorf("BANDWIDTH 解析错误: %q", attrs["BANDWIDTH"])
	}
	if attrs["CODECS"] != "avc1.640028,mp4a.40.2" {
		t.Errorf("带逗号的引号值被截断: %q", attrs["CODECS"])
	}
}

func TestResolveURL(t *testing.T) {
	cases := [][3]string{
		{"https://a.com/x/y.m3u8", "z.ts", "https://a.com/x/z.ts"},
		{"https://a.com/x/y.m3u8", "/root/z.ts", "https://a.com/root/z.ts"},
		{"https://a.com/x/y.m3u8", "https://b.com/z.ts", "https://b.com/z.ts"},
	}
	for _, c := range cases {
		if got := ResolveURL(c[0], c[1]); got != c[2] {
			t.Errorf("ResolveURL(%q,%q) 期望 %q，得到 %q", c[0], c[1], c[2], got)
		}
	}
}

func TestResolveURLInheritsSignature(t *testing.T) {
	// CDN 把签名放在 master 的查询串里，子清单是相对路径
	base := "https://hv-h.phncdn.com/hls/videos/1/1080P.mp4/master.m3u8?h=abc%2Fdef&e=1792966694&f=1"
	got := ResolveURL(base, "1080P.mp4/index-v1-a1.m3u8")
	want := "https://hv-h.phncdn.com/hls/videos/1/1080P.mp4/1080P.mp4/index-v1-a1.m3u8?h=abc%2Fdef&e=1792966694&f=1"
	if got != want {
		t.Fatalf("相对引用应继承签名\n得到 %s\n期望 %s", got, want)
	}
	// 子地址自带查询串时不要覆盖
	got = ResolveURL(base, "seg.ts?x=1")
	if !strings.HasSuffix(got, "seg.ts?x=1") {
		t.Fatalf("自带查询串不应被覆盖: %s", got)
	}
	// base 没有查询串时保持 RFC 行为
	if got := ResolveURL("https://a.com/x/master.m3u8", "b/seg.ts"); got != "https://a.com/x/b/seg.ts" {
		t.Fatalf("普通相对解析错误: %s", got)
	}
}
