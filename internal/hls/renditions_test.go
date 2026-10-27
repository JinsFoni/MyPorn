package hls

import "testing"

// 这份清单是 YouTube 真实返回的 master 的缩小版：两条音轨（Opus/AAC）、
// 几档分辨率、H.264 与 VP9 并存。它比成人站的清单多一层「音轨单独列」的结构，
// 正是这层结构决定改写后有没有声音。
const ytMasterFixture = `#EXTM3U
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:URI="https://manifest.googlevideo.com/a233/index.m3u8",TYPE=AUDIO,GROUP-ID="233",NAME="Default",DEFAULT=YES,AUTOSELECT=YES
#EXT-X-MEDIA:URI="https://manifest.googlevideo.com/a234/index.m3u8",TYPE=AUDIO,GROUP-ID="234",NAME="Default",DEFAULT=YES,AUTOSELECT=YES
#EXT-X-STREAM-INF:BANDWIDTH=95759,CODECS="avc1.4D4015,mp4a.40.5",RESOLUTION=426x240,AUDIO="233"
https://manifest.googlevideo.com/v229/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=876853,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1920x1080,AUDIO="234"
https://manifest.googlevideo.com/v270/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=655763,CODECS="vp09.00.40.08,mp4a.40.2",RESOLUTION=1920x1080,AUDIO="234"
https://manifest.googlevideo.com/v614/index.m3u8
`

func TestParseMasterKeepsRenditions(t *testing.T) {
	variants, renditions := ParseMaster(ytMasterFixture, "https://manifest.googlevideo.com/master.m3u8")
	if len(variants) != 3 {
		t.Fatalf("变体数 = %d，想要 3", len(variants))
	}
	if len(renditions) != 2 {
		t.Fatalf("音轨数 = %d，想要 2", len(renditions))
	}
	if renditions[1].GroupID != "234" || !renditions[1].Default {
		t.Fatalf("第二条音轨解析错了: %+v", renditions[1])
	}
	if renditions[1].URI != "https://manifest.googlevideo.com/a234/index.m3u8" {
		t.Fatalf("音轨地址没补全: %s", renditions[1].URI)
	}
	if variants[1].AudioGroup != "234" || variants[1].Width != 1920 || variants[1].Height != 1080 {
		t.Fatalf("变体解析错了: %+v", variants[1])
	}
	if variants[1].Codecs != "avc1.640028,mp4a.40.2" {
		t.Fatalf("CODECS 应保留原文: %q", variants[1].Codecs)
	}
}

func TestPickVariant(t *testing.T) {
	variants, _ := ParseMaster(ytMasterFixture, "")
	// 不设上限：同高度取码率高的那把（H.264 876k > VP9 655k）。
	best, ok := PickVariant(variants, 0)
	if !ok || best.Height != 1080 || best.Bandwidth != 876853 {
		t.Fatalf("最高画质选错了: %+v ok=%v", best, ok)
	}
	// 限到 240：只剩一条。
	capped, ok := PickVariant(variants, 240)
	if !ok || capped.Height != 240 {
		t.Fatalf("限高选错了: %+v ok=%v", capped, ok)
	}
	// 限到 120：一条都没有，调用方要能识别。
	if _, ok := PickVariant(variants, 120); ok {
		t.Fatal("限到 120 时不该选出变体")
	}
}

func TestAudioFor(t *testing.T) {
	_, renditions := ParseMaster(ytMasterFixture, "")
	audio, ok := AudioFor(renditions, "234")
	if !ok || audio.GroupID != "234" {
		t.Fatalf("音轨没找到: %+v ok=%v", audio, ok)
	}
	if _, ok := AudioFor(renditions, "999"); ok {
		t.Fatal("不存在的组不该返回音轨")
	}
}
