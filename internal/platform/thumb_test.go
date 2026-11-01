package platform

import "testing"

func TestPlaceholderThumb(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		// 2026-10-31 那轮真的进了库的那种：16 像素宽的糊图（原图只有 796 字节）
		{"https://ic-vt-nss.xhcdn.com/a/MmI0NTUwZjU3ZGJhZTQwZWQyNDM5MDNjODg5MjYwMDE/b(2),s(w:16,h:9),webp/024/130/167/v2/16x9.213.webp", true},
		// 正常封面：1280x720 / 526x298 / 竖屏 405x720 与 350x620 都不算占位
		{"https://ic-vt-nss.xhcdn.com/a/x/s(w:1280,h:720),webp/024/130/167/v2/2560x1440.213.webp", false},
		{"https://ic-vt-nss.xhcdn.com/a/x/s(w:526,h:298),webp/024/130/167/v2/526x298.213.webp", false},
		{"https://ic-vt-nss.xhcdn.com/a/x/s(w:405,h:720),webp/031/450/896/orig.9.jpg", false},
		{"https://ic-vt-nss.xhcdn.com/a/x/s(w:350,h:620),webp/031/450/896/orig.9.jpg", false},
		// 没有缩放参数的地址（pornhub / beeg / YouTube）一律当正常图
		{"https://pix-fl.phncdn.com/c6371/videos/202609/12/62101845/original.mp4/plain/rs:fit:320:180", false},
		{"https://i4.ytimg.com/vi/s8OtOH_Vfa0/hqdefault.jpg", false},
		{"", false},
	}
	for _, c := range cases {
		if got := PlaceholderThumb(c.url); got != c.want {
			t.Errorf("PlaceholderThumb(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestBestThumb(t *testing.T) {
	lqip := "https://ic-vt-nss.xhcdn.com/a/h/b(2),s(w:16,h:9),webp/024/130/167/v2/16x9.213.webp"
	big := "https://ic-vt-nss.xhcdn.com/a/h/s(w:1280,h:720),webp/024/130/167/v2/2560x1440.213.webp"
	mid := "https://ic-vt-nss.xhcdn.com/a/h/s(w:526,h:298),webp/024/130/167/v2/526x298.213.webp"

	if got := BestThumb(lqip, mid); got != mid {
		t.Errorf("占位图在前时应挑后一张：got %q", got)
	}
	if got := BestThumb(big, mid); got != big {
		t.Errorf("两张都正常时按顺序取第一张：got %q", got)
	}
	if got := BestThumb(lqip, ""); got != lqip {
		t.Errorf("只剩占位图时退回它，别空着：got %q", got)
	}
	if got := BestThumb("", ""); got != "" {
		t.Errorf("全空应返回空串：got %q", got)
	}
}
