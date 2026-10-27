package imgcache

// 隐私模式要的「小糊图」：把上游那张封面/头像缩到几十像素宽、糊一下、压成 JPEG 再发。
//
// 为什么不在浏览器里用 `filter: blur()`：一张图一个合成层 + 一遍高斯模糊，
// 实测（Chrome trace，DPR 2）比不模糊多出近一半的 raster 时间，滚起来就是掉帧；
// 而浏览器把一张 48×27 的 JPEG 拉到卡片大小，本来就是一坨糊的色块——
// 视觉几乎一样，代价却是「贴一张 1KB 的纹理」，滚动时什么都不用算。
// 顺带还省了带宽：隐私模式下高清封面根本不会下到浏览器。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os/exec"
	"strings"
	"time"

	// 图床给的格式就这三种：pornhub / xhamster 是 jpg，beeg 是 webp。
	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// errUndecodable 表示这坨字节我们解不出来（比如 CDN 返回了 HTML 错误页）。
var errUndecodable = errors.New("图片解不出来")

// blurredSmall 把任意图床图片缩到 width 宽，糊一遍，编码成 JPEG 返回。
//
// 两步是分开做的，因为顺序有意义：先按面积平均缩到几十像素（细节在这一步就没了），
// 再在这个尺寸上做一次盒式模糊（把缩放留下的硬边抹平）。倒过来做——先模糊原图再缩——
// 要在大图上算十几万个采样点，纯属浪费。
func blurredSmall(data []byte, width int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errUndecodable
	}
	return encodeSmall(src, width)
}

// blurAny 是给 HTTP 层与同步任务用的版本：Go 解不了就交给 ffmpeg 转一道再解。
//
// 为什么需要它：pornhub 的封面走 `pix-*.phncdn.com/.../rs:fit:320:180` 这种图床，
// 谈下来给的是 **AVIF**，而 Go 这边（stdlib + golang.org/x/image）没有 AVIF 解码器。
// ffmpeg 有（libdav1d），而且本来就是这个项目的硬依赖——只在这条路径上多起一个进程，
// 生成的小图有缓存，一张图一辈子只解一次。
func (s *Service) blurAny(ctx context.Context, data []byte, width int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		jpegBytes, ferr := s.ffmpegShrink(ctx, data, width)
		if ferr != nil {
			return nil, errUndecodable
		}
		src, err = jpeg.Decode(bytes.NewReader(jpegBytes))
		if err != nil {
			return nil, errUndecodable
		}
	}
	return encodeSmall(src, width)
}

// imgFfmpegSlots 限制同时解码的 ffmpeg 进程数。
var imgFfmpegSlots = make(chan struct{}, 3)

// ffmpegShrink 用 ffmpeg 把任意格式（AVIF/HEIC/…）解出来并缩到 width 宽，输出 JPEG。
// 在这里 ffmpeg 只当解码器用，缩放交给它顺带做掉（flags=area 就是面积平均，
// 跟 scaleArea 一个意思），出来再走一遍 encodeSmall 的盒式模糊。
func (s *Service) ffmpegShrink(ctx context.Context, data []byte, width int) ([]byte, error) {
	bin := s.ffmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// 首屏十几张 AVIF 会一起来，别在弱机上同时开十几个 ffmpeg 抢 CPU。
	select {
	case imgFfmpegSlots <- struct{}{}:
		defer func() { <-imgFfmpegSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-v", "error",
		"-i", "pipe:0",
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2:flags=area", width),
		"-f", "image2", "-vcodec", "mjpeg", "-q:v", "3",
		"pipe:1")
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("ffmpeg 解码失败: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// encodeSmall 把已经解出来的图缩到 width 宽、糊一遍、编码成 JPEG。
func encodeSmall(src image.Image, width int) ([]byte, error) {
	b := src.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errUndecodable
	}
	if width < 8 {
		width = 8
	}
	if width > b.Dx() {
		width = b.Dx()
	}
	height := (width*b.Dy() + b.Dx()/2) / b.Dx()
	if height < 1 {
		height = 1
	}

	small := scaleArea(src, width, height)
	boxBlur(small, 2)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, small, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// scaleArea 按面积平均缩放：每个目标像素取它在原图里覆盖的那块矩形的平均值。
// 对缩小来说这比最近邻好看得多（不会丢一半细节留下锯齿），也不用引入第三方缩放库。
func scaleArea(src image.Image, dstW, dstH int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	for y := 0; y < dstH; y++ {
		y0 := b.Min.Y + y*sh/dstH
		y1 := b.Min.Y + (y+1)*sh/dstH
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dstW; x++ {
			x0 := b.Min.X + x*sw/dstW
			x1 := b.Min.X + (x+1)*sw/dstW
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a uint64
			var n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(xx, yy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(bl / n >> 8)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return dst
}

// boxBlur 就地做可分离的盒式模糊（左右各走一遍，横竖各一次）。
// 在这个尺寸（几十像素）上它相当于零成本，效果就是把缩放的硬边化开。
func boxBlur(img *image.RGBA, radius int) {
	if radius < 1 {
		return
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	span := 2*radius + 1
	tmp := make([]uint8, len(img.Pix))

	// 横向
	for y := 0; y < h; y++ {
		for c := 0; c < 4; c++ {
			var sum int
			for k := -radius; k <= radius; k++ {
				sum += int(img.Pix[img.PixOffset(clamp(k, 0, w-1), y)+c])
			}
			for x := 0; x < w; x++ {
				tmp[y*img.Stride+x*4+c] = uint8(sum / span)
				out := clamp(x-radius, 0, w-1)
				in := clamp(x+radius+1, 0, w-1)
				sum += int(img.Pix[img.PixOffset(in, y)+c]) - int(img.Pix[img.PixOffset(out, y)+c])
			}
		}
	}
	// 纵向
	for x := 0; x < w; x++ {
		for c := 0; c < 4; c++ {
			var sum int
			for k := -radius; k <= radius; k++ {
				sum += int(tmp[clamp(k, 0, h-1)*img.Stride+x*4+c])
			}
			for y := 0; y < h; y++ {
				img.Pix[y*img.Stride+x*4+c] = uint8(sum / span)
				out := clamp(y-radius, 0, h-1)
				in := clamp(y+radius+1, 0, h-1)
				sum += int(tmp[in*img.Stride+x*4+c]) - int(tmp[out*img.Stride+x*4+c])
			}
		}
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
