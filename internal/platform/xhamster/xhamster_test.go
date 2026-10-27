package xhamster

import "testing"

// 下面三条密文是 2026-10 从一个真实影片页的 window.initials 里抄出来的，
// 明文用站点自己的 xplayer.js 验过。算法要是哪天换了，这里会先红。
const (
	vectorAV1Cipher = "062d6ba1b8250fca73765fbfa543c68379dc3fac039a9363b17e7a32800d1f65646dd709456e345237cd39240677d827cd9c68fdbb2c413f7043722355ab36468d3d418a3a18d10d6503c873bb520705c8ec5f684db1e213ce3cb90139456f0e575032a349191bd20eee7ce44821e25da5c9af0a6d45b201a50cd0056db48a1a597e00ceb7c14c260ee971817e282a6270f07150964bb5f4a3a619bc403be33345bb25fa4ddedf31657ad0b75949558176cead5669258b619048490184d19c7602eee13951330a73c8a0e3f93c79b830a1802b9753f073f67efbd99ed75ed8335c7cdd05548c1bd58f19eb3c0393c344351e14e9e9a9f7093b"
	vectorAV1Plain  = "https://video-h.xhcdn.com/key=mpg7tSQWPbthmBiiiaDp8g,end=1793124000/data=2602:f3f2:c2:4894:565f:b5e:7e1a:49c0-dvp/referer=/media=hls4/multi=256x144:144p:,426x240:240p:,854x480:480p:,1280x720:720p:,1920x1080:1080p:/026/498/007/_TPL_.av1.mp4.m3u8"

	vectorH264Cipher = "0489a74e8ba27d6f491cfbc992be69ef0fddb9f40ac25efedcc3055ec0ecc37ac2ffd6c94e6be23af7d80900f379f7ccd91669d7a5498bb86ca4e57c729c162b526a1eefc71d7f36d44d0da5376faffe30ed501d0ae2832936a5628f4df3fa4611326328adaa0e00b01656550e63faa45bb0bb0e044cab6d1fa8b87dc821808014e1ad532ffef06c650afd0411f5533c0e0732d386b8d310998b8624c8ac41ac8f9fb6a7f1152f7c1d2fb2dbe130649a6a6d32b4f7fac36dc62ba3e56f2fb255b00166fcb902172e3ae34d26c963aca59f066508f0915a3022c3db788b32ff9d9ca520d5cc7d2598a3ea36bf4842b1389f8ab6be4d"
	vectorH264Plain  = "https://video-h.xhcdn.com/key=ANUthcD8gq22Kl0HpiBqJQ,end=1793124000/data=2602:f3f2:c2:4894:565f:b5e:7e1a:49c0-dvp/referer=/media=hls4/multi=256x144:144p,426x240:240p,854x480:480p,1280x720:720p,1920x1080:1080p/026/498/007/_TPL_.h264.mp4.m3u8"

	vector720Cipher = "07e982bf43eeaf5c2a3763cd70e0af28f5eda8b64fe6d4bdbdf04177b92074adff979474a47af8cd007bbe252ea70024cf60c00324a6e4a21813269895fa1e2b285548b8c10c1742fb47f15f4417ecd5fb3a67bec11c60ea60f1f7ecf0c32b69369211916ec991309c670bb1e51cda8ecfd66753307b4b95e2677bc0f4905b00be3cb8e8ce472c95d579b0bd0991bb1cd0a9876f59a59302472c85bec98b0b4c"
	vector720Plain  = "https://video-h.xhcdn.com/key=3bJRWIlCEuUr9gLevB6h4g,end=1793124000,limit=3/data=2602:f3f2:c2:4894:565f:b5e:7e1a:49c0-dvp/speed=0/026/498/007/720p.h264.mp4"
)

func TestDecryptSource(t *testing.T) {
	cases := []struct {
		name    string
		cipher  string
		want    string
		wantErr bool
	}{
		{name: "av1 master", cipher: vectorAV1Cipher, want: vectorAV1Plain},
		{name: "h264 master", cipher: vectorH264Cipher, want: vectorH264Plain},
		{name: "h264 720p 直链", cipher: vector720Cipher, want: vector720Plain},
		{name: "空串", cipher: "", wantErr: true},
		{name: "不是十六进制", cipher: "zzzzzzzzzzzzzzzz", wantErr: true},
		{name: "奇数长度", cipher: "0123456789abc", wantErr: true},
		{name: "未知算法", cipher: "0909a74e8ba27d6f491cfbc992be69ef0fddb9f4", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decryptSource(c.cipher)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("解密失败: %v", err)
			}
			if got != c.want {
				t.Fatalf("明文不符\n得到: %s\n期望: %s", got, c.want)
			}
		})
	}
}

func TestApplyHostOverride(t *testing.T) {
	hostMap := map[string]string{"standard.h264.auto.prime": "video7"}
	in := "https://video-h.xhcdn.com/key=abc,end=123/media=hls4/_TPL_.h264.mp4.m3u8"
	want := "https://video7.xhcdn.com/key=abc,end=123/media=hls4/_TPL_.h264.mp4.m3u8"
	if got := applyHostOverride(in, hostMap, "standard.h264.auto.prime"); got != want {
		t.Fatalf("换主机失败\n得到: %s\n期望: %s", got, want)
	}
	// hostMap 里没有这个键就原样返回（别把地址改坏）。
	if got := applyHostOverride(in, hostMap, "standard.av1.auto.prime"); got != in {
		t.Fatalf("没有映射时不该改动地址，得到 %s", got)
	}
}

func TestHeightOf(t *testing.T) {
	for raw, want := range map[string]int{"720p": 720, "1080P": 1080, "144p": 144, "": 0, "auto": 0} {
		if got := heightOf(raw); got != want {
			t.Fatalf("heightOf(%q)=%d，期望 %d", raw, got, want)
		}
	}
}

func TestSlugOf(t *testing.T) {
	for raw, want := range map[string]string{
		"Riley Reid":    "riley-reid",
		"  Mini Diva ":  "mini-diva",
		"Lana Rhoades":  "lana-rhoades",
		"Abella Danger": "abella-danger",
		"!!!":           "",
	} {
		if got := slugOf(raw); got != want {
			t.Fatalf("slugOf(%q)=%q，期望 %q", raw, got, want)
		}
	}
}
