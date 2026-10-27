package httpapi

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os/exec"
	"testing"
)

func TestSmallWidthParam(t *testing.T) {
	cases := map[string]int{
		"48":   48,
		" 64 ": 64,
		"8":    8,
		"256":  256,
		"":     0,
		"7":    0, // 太小，糊了也没意义
		"512":  0, // 太大，那就不是「小图」了
		"abc":  0,
		"-48":  0,
	}
	for in, want := range cases {
		if got := smallWidthParam(in); got != want {
			t.Errorf("smallWidthParam(%q) = %d, 期望 %d", in, got, want)
		}
	}
}

// testImage 造一张 400×300 的 PNG：左半红、右半蓝，中间一条白线。
// 有硬边界才验得出「缩下去以后还在原来的位置」。
func testImage(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			switch {
			case x == 199 || x == 200:
				img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			case x < 200:
				img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
			default:
				img.Set(x, y, color.RGBA{R: 30, G: 30, B: 200, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("造测试图失败：%v", err)
	}
	return buf.Bytes()
}

func TestBlurredSmallShrinksAndKeepsLayout(t *testing.T) {
	src := testImage(t)
	out, err := blurredSmall(src, 48)
	if err != nil {
		t.Fatalf("blurredSmall: %v", err)
	}
	if len(out) >= len(src) {
		t.Errorf("小图 %d 字节，不比原图 %d 字节小", len(out), len(src))
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("输出的不是合法 JPEG：%v", err)
	}
	if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != 48 || h != 36 {
		t.Errorf("尺寸 = %dx%d，期望 48x36（保持 4:3）", w, h)
	}
	// 左右两半的主要颜色不能颠倒，也不能糊成一色
	left := img.At(4, 18).(color.YCbCr)
	right := img.At(43, 18).(color.YCbCr)
	if left.Y == 0 && right.Y == 0 {
		t.Fatal("整张图是黑的")
	}
	// Cb（蓝色差）在右半部分应该明显更高
	lb := avgCb(img, 0, 20)
	rb := avgCb(img, 28, 48)
	if rb <= lb {
		t.Errorf("右半 Cb=%.1f 不大于左半 Cb=%.1f，红蓝可能颠倒了", rb, lb)
	}
}

// avgCb 求某一列区间内的平均 Cb，用来判断哪边是蓝的。
func avgCb(img image.Image, x0, x1 int) float64 {
	var sum, n float64
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := x0; x < x1; x++ {
			sum += float64(img.At(x, y).(color.YCbCr).Cb)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

func TestBlurredSmallRejectsGarbage(t *testing.T) {
	if _, err := blurredSmall([]byte("<html>403 Forbidden</html>"), 48); err == nil {
		t.Fatal("解不出来的字节应当报错（调用方据此发空白，绝不能漏原图）")
	}
}

func TestBlurredSmallCapsWidthToSource(t *testing.T) {
	// 源图只有 20 宽：要它缩到 48 就别说 48，超出源宽的放大没有意义
	out, err := blurredSmall(testImage(t), 48)
	if err != nil {
		t.Fatalf("blurredSmall: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 48 {
		t.Fatalf("宽 = %d，期望 48", img.Bounds().Dx())
	}

	small := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			small.Set(x, y, color.RGBA{R: 10, G: 200, B: 10, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, small); err != nil {
		t.Fatal(err)
	}
	out2, err := blurredSmall(buf.Bytes(), 48)
	if err != nil {
		t.Fatalf("blurredSmall: %v", err)
	}
	img2, err := jpeg.Decode(bytes.NewReader(out2))
	if err != nil {
		t.Fatal(err)
	}
	if got := img2.Bounds().Dx(); got != 20 {
		t.Errorf("源图只有 20 宽时输出宽 = %d，期望 20（不做放大）", got)
	}
}

// 隐私模式这条路在 HTTP 层用的是 blurredSmallAny：Go 解不了就找 ffmpeg。
func TestBlurredSmallAnyGoPath(t *testing.T) {
	s := &Server{}
	out, err := s.blurredSmallAny(context.Background(), testImage(t), 48)
	if err != nil {
		t.Fatalf("blurredSmallAny: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Dx(); got != 48 {
		t.Errorf("宽 = %d，期望 48", got)
	}
}

// 彻底解不出来时必须报错，让上层发空白占位——绝不能退化成「把原图发出去」。
func TestBlurredSmallAnyRejectsGarbage(t *testing.T) {
	s := &Server{}
	if _, err := s.blurredSmallAny(context.Background(), []byte("<html>403 Forbidden</html>"), 48); err == nil {
		t.Fatal("垃圾字节应当报错")
	}
}

// ffmpeg 兜底：pornhub 的 pix 图床发的是 AVIF，Go 这边解不了，只能靠它。
// 这里喂 PNG 验通路（AVIF 编码器不一定装了），够说明「ffmpeg 能接住这条路径」。
func TestFFmpegShrink(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("这台机器没装 ffmpeg，跳过兜底通路测试")
	}
	s := &Server{}
	out, err := s.ffmpegShrink(context.Background(), testImage(t), 48)
	if err != nil {
		t.Fatalf("ffmpegShrink: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("ffmpeg 输出的不是合法 JPEG：%v", err)
	}
	if got := img.Bounds().Dx(); got != 48 {
		t.Errorf("宽 = %d，期望 48", got)
	}
}
