package library

import "testing"

// TestCoverCrop 裁切区域永远不超过源图（一旦超过就得放大，糊就是这么来的）。
func TestCoverCrop(t *testing.T) {
	cases := []struct {
		name           string
		sw, sh, tw, th int
		wantW, wantH   int
	}{
		// YouTube hqdefault 480×360 → 1000×1500 的海报：只能裁出 240×360
		{"480x360 -> 2:3", 480, 360, 1000, 1500, 240, 360},
		// 1080p 横屏 → 2:3：720×1080，正好是目标的 0.72 倍
		{"1920x1080 -> 2:3", 1920, 1080, 1000, 1500, 720, 1080},
		// 4K 竖屏 → 2:3：整宽保留
		{"2160x3840 -> 2:3", 2160, 3840, 1000, 1500, 2160, 3240},
		// 比例一致的 16:9：整张图
		{"1920x1080 -> 16:9", 1920, 1080, 1920, 1080, 1920, 1080},
		// 竖图裁成 16:9：留下中间一条
		{"2160x3840 -> 16:9", 2160, 3840, 1920, 1080, 2160, 1215},
		// 非法尺寸
		{"零尺寸", 0, 0, 100, 100, 0, 0},
	}
	for _, c := range cases {
		w, h := CoverCrop(c.sw, c.sh, c.tw, c.th)
		if w != c.wantW || h != c.wantH {
			t.Errorf("%s: CoverCrop(%d,%d,%d,%d) = %dx%d，期望 %dx%d",
				c.name, c.sw, c.sh, c.tw, c.th, w, h, c.wantW, c.wantH)
		}
		if c.sw > 0 && (w > c.sw || h > c.sh) {
			t.Errorf("%s: 裁切区域 %dx%d 超过源图 %dx%d（会逼出放大）", c.name, w, h, c.sw, c.sh)
		}
	}
}

// TestFitWithin 只缩不放。
func TestFitWithin(t *testing.T) {
	cases := []struct {
		w, h, maxW, maxH int
		wantW, wantH     int
	}{
		{720, 1080, 1000, 1500, 720, 1080},   // 比目标小：原样
		{2160, 3240, 1000, 1500, 1000, 1500}, // 比目标大：缩到刚好
		{1920, 1080, 1920, 1080, 1920, 1080}, // 相等：原样
		{3840, 2160, 1280, 720, 1280, 720},
		{0, 0, 10, 10, 0, 0},
	}
	for _, c := range cases {
		w, h := FitWithin(c.w, c.h, c.maxW, c.maxH)
		if w != c.wantW || h != c.wantH {
			t.Errorf("FitWithin(%d,%d,%d,%d) = %dx%d，期望 %dx%d",
				c.w, c.h, c.maxW, c.maxH, w, h, c.wantW, c.wantH)
		}
		if c.w > 0 && (w > c.w || h > c.h) {
			t.Errorf("FitWithin(%d,%d) 放大了：%dx%d", c.w, c.h, w, h)
		}
	}
}
