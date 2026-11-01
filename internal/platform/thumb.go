package platform

import (
	"regexp"
	"strconv"
	"strings"
)

// thumbTransformRe 匹配图床 URL 里那段缩放参数，例如 xHamster 的
// `…/b(2),s(w:16,h:9),webp/024/130/167/v2/16x9.213.webp` 里的 `s(w:16,h:9)`。
// 目前只有 xHamster 的 ic-vt-nss.xhcdn.com / thumb-nss.xhcdn.com 用这种写法。
var thumbTransformRe = regexp.MustCompile(`s\(w:(\d+),h:(\d+)\)`)

// placeholderWidth 是「真人能当封面用」的下限宽度。
// 站点给的两档正常封面是 526 与 1280 宽（竖屏那几个是 350/405 宽），占位糊图只有 16 宽，
// 中间是一片空白，所以 200 这个界两头都不挨着，不会误伤。
const placeholderWidth = 200

// PlaceholderThumb 判断一个封面地址是不是站点给的「模糊占位图」（LQIP）。
//
// 为什么需要判断：xHamster 偶尔把这个塞进 `imageURL`（正常应该是 1280x720）——
// 2026-10-31 那轮抓取，Hot Pearl 与 mysaaat 两个订阅下 676 条影片就都成了
// `b(2),s(w:16,h:9),webp/…`：16 像素宽的糊图被铺到 266×150 的卡片上，整张封面就是一团糊。
// 它的体积也说明了问题——原样取回来只有 796 字节。
//
// 只认 `s(w:X,h:Y)` 这段变换里的宽度，不去猜「是不是糊的」：`b(2)` 那种模糊滤镜本身
// 不该作为判据（别的图床也可能带），宽度小于 200 已经是铁证。
func PlaceholderThumb(raw string) bool {
	m := thumbTransformRe.FindStringSubmatch(raw)
	if m == nil {
		return false
	}
	w, err := strconv.Atoi(m[1])
	if err != nil {
		return false
	}
	return w > 0 && w < placeholderWidth
}

// BestThumb 在若干候选封面里挑一张能铺满卡片的：跳过占位糊图，按传入顺序取第一张合格的。
// 只剩占位图时退回它 —— 糊的总比空着强，封面修复任务会再拿详情页把地址换掉。
func BestThumb(candidates ...string) string {
	var first string
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if first == "" {
			first = c
		}
		if !PlaceholderThumb(c) {
			return c
		}
	}
	return first
}
