package imgcache

// 隐私模式要的「小糊图」：把上游那张封面/头像缩到一百来像素宽、糊一下、编码再发。
//
// 为什么不在浏览器里用 `filter: blur()`：一张图一个合成层 + 一遍高斯模糊，
// 实测（Chrome trace，DPR 2）比不模糊多出近一半的 raster 时间，滚起来就是掉帧；
// 而浏览器把一张 120 宽的小图拉到卡片大小，本来就是一坨糊的色块——
// 代价只是「贴一张小纹理」，滚动时什么都不用算。
// 顺带还省了带宽：隐私模式下高清封面根本不会下到浏览器。
//
// 编码用 **PNG 而不是 JPEG**：Go 的 jpeg 编码器只会输出 4:2:0（色度横向纵向各减半），
// 而这里的小图只有一百来像素宽，色度再减半就等于每个色度像素铺 4 个显示像素，
// 拉到卡片大小后大片平滑区域会析出**色带断层**（此前 48px + q82 就是这个毛病）。
// PNG 是无损 4:4:4，慢着看是平滑的。体积代价可以接受：120 宽的糊图约 7KB，
// 比它替换掉的那张 480 宽封面（40KB+）小得多，一屏二十来张也就多几百 KB。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"math/rand"
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

// blurredSmall 把任意图床图片缩到 width 宽，糊一遍，编码成 PNG 返回。
//
// 两步是分开做的，因为顺序有意义：先按面积平均缩到一百来像素（细节在这一步就没了），
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
		pngBytes, ferr := s.ffmpegDecode(ctx, data, width)
		if ferr != nil {
			return nil, errUndecodable
		}
		src, err = png.Decode(bytes.NewReader(pngBytes))
		if err != nil {
			return nil, errUndecodable
		}
	}
	return encodeSmall(src, width)
}

// imgFfmpegSlots 限制同时解码的 ffmpeg 进程数。
var imgFfmpegSlots = make(chan struct{}, 3)

// ffmpegDecode 用 ffmpeg 把任意格式（AVIF/HEIC/…）解出来，输出**无损 PNG**。
// 在这里 ffmpeg 只当解码器用：顺带按 area 缩到 4×width（够 encodeSmall 再取平均，
// 又不会让管道里塞进一张原始尺寸的大图），**绝不输出 JPEG**——那样等于在缩图前
// 先损一次，色度还会减半，色带断层就是这么叠出来的。
func (s *Service) ffmpegDecode(ctx context.Context, data []byte, width int) ([]byte, error) {
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
		"-vf", fmt.Sprintf("scale=%d:-2:flags=area", 4*width),
		"-f", "image2", "-vcodec", "png",
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

// encodeSmall 把已经解出来的图缩到 width 宽、糊一遍、编码成 PNG。
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
	// 三遍盒式 ≈ 高斯：单遍盒式在半径这么大时会留下方方正正的「台阶」轮廓，
	// 叠三遍（方差相加）就化开了，成本在 120×68 这种尺寸上可以忽略。
	for i := 0; i < smallBlurPasses; i++ {
		boxBlur(small, smallBlurRadius(width))
	}
	dither(small, smallDitherAmp)

	var out bytes.Buffer
	if err := png.Encode(&out, small); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// smallBlurRadius 让小图上的模糊半径跟着宽度走：显示时放大的倍数越大，半径就得越大。
// 宽/18 意味着 48px 用 3、120px 用 7（每遍，共三遍）——换算到 480px 的卡片尺寸
// 等效 σ 约 35px，也就是「不管小图多宽，糊的程度一样」，换分辨率不会改变隐私强度：
// 轮廓还在、姿势还能猜，但看不清任何细节（这正是「挡一眼」要的效果）。
func smallBlurRadius(width int) int {
	if r := (width + 9) / 18; r > 0 {
		return r
	}
	return 1
}

// smallBlurPasses / smallDitherAmp 是「糊得平滑」的两个必要条件：
//   - 单遍盒式模糊的核是个方框，半径一大就能看出轮廓上的方台阶，叠三遍才像高斯；
//   - 8 位通道装不下「跨几百像素才降一级」的缓慢渐变，四倍放大后就是一圈圈色带，
//     每像素加一点零均值噪声（三角分布）把台阶换成细颗粒，人眼看过去就是平滑的。
const (
	smallBlurPasses = 3
	smallDitherAmp  = 1.5
)

// dither 在落 8 位之前给每个像素加一点三角分布的零均值噪声（±amp 上下）。
// 只在 R/G/B 上加，alpha 不动；噪声会在浏览器放大时被插值糊开，看着是细颗粒而不是噪点。
func dither(img *image.RGBA, amp float64) {
	if amp <= 0 {
		return
	}
	rnd := rand.New(rand.NewSource(1)) // 固定种子：同一张图每次生成的结果一致
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			i := img.PixOffset(x, y)
			for c := 0; c < 3; c++ {
				n := (rnd.Float64() + rnd.Float64() - 1) * amp
				v := float64(img.Pix[i+c]) + n
				if v < 0 {
					v = 0
				} else if v > 255 {
					v = 255
				}
				img.Pix[i+c] = uint8(math.Round(v))
			}
		}
	}
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

// boxBlur 就地做可分离的盒式模糊（横向一遍、纵向一遍），求和用整数、落位时四舍五入。
// 在这个尺寸（一百来像素）上它相当于零成本，效果就是把缩放的硬边化开。
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
				tmp[y*img.Stride+x*4+c] = uint8((sum + span/2) / span)
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
				img.Pix[y*img.Stride+x*4+c] = uint8((sum + span/2) / span)
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
