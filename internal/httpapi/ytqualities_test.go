package httpapi

import (
	"testing"

	"github.com/JinsFoni/MyPorn/internal/hls"
	"github.com/JinsFoni/MyPorn/internal/ytstream"
)

// 只出现在 AV1 直链里的档位必须出现在菜单里。
//
// 2026-11-03 现场（3BaMT8c93lA，4K 片）：配了 cookies 之后 yt-dlp 改用带登录态的客户端，
// HLS 清单最高只到 1080p，2160/1440 只在 `protocol=https` 的 AV1 直链里。这份输入就是
// 当时的真实形状——只看清单的话菜单里没有 4K，用户看到的是「YouTube 不能播 4K 了」。
func TestYtQualitiesIncludesTrackOnlyHeights(t *testing.T) {
	variants := []hls.StreamVariant{
		{Height: 1080, Bandwidth: 5678000, Codecs: "avc1.64002A"},
		{Height: 720, Bandwidth: 3289000, Codecs: "avc1.640020"},
	}
	tracks := []ytstream.Track{
		{Height: 2160, Bandwidth: 11505000, Codecs: "av01.0.13M.08", VideoURL: "https://x/v"},
		{Height: 1440, Bandwidth: 5120000, Codecs: "av01.0.12M.08", VideoURL: "https://x/v2"},
	}
	got := ytQualities(variants, tracks)
	byHeight := map[int]ytQualityDTO{}
	for _, q := range got {
		byHeight[q.Height] = q
	}
	if len(got) != 4 {
		t.Fatalf("应有 1080/720/2160/1440 四档，得到 %+v", got)
	}
	q4k, ok := byHeight[2160]
	if !ok {
		t.Fatal("2160p 没出现在菜单里")
	}
	if !q4k.Dash {
		t.Fatal("2160p 必须标成 dash（它只有 AV1 直链这条路）")
	}
	if q4k.Bandwidth != 11505000 {
		t.Fatalf("2160p 的码率要报 AV1 那条（11.5 Mbps），得到 %d", q4k.Bandwidth)
	}
	if q4k.Codecs != "av01.0.13M.08" {
		t.Fatalf("2160p 的编码要报 av01，得到 %q", q4k.Codecs)
	}
	if byHeight[1080].Dash {
		t.Fatal("1080p 有 HLS 清单，不该标 dash")
	}
	// 菜单顺序：高的在前
	if got[0].Height != 2160 {
		t.Fatalf("菜单第一项应是 2160p，得到 %d", got[0].Height)
	}
}

// 清单里带 2160 时（无 cookie 的 visionos 那套客户端），仍要用 AV1 那条的码率/编码报。
func TestYtQualitiesPrefersTrackForHLS2160(t *testing.T) {
	variants := []hls.StreamVariant{{Height: 2160, Bandwidth: 28220650, Codecs: "vp09.00.51.08"}}
	tracks := []ytstream.Track{{Height: 2160, Bandwidth: 13461975, Codecs: "av01.0.13M.08", VideoURL: "https://x/v"}}
	got := ytQualities(variants, tracks)
	if len(got) != 1 {
		t.Fatalf("应只有一档，得到 %+v", got)
	}
	if got[0].Bandwidth != 13461975 || !got[0].Dash {
		t.Fatalf("2160p 应报 AV1 13.46 Mbps 且标 dash，得到 %+v", got[0])
	}
}
