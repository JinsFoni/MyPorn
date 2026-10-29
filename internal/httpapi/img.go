package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

// placeholderPNG 是 1x1 透明 PNG，图片代理失败时返回，避免前端出现破图。
var placeholderPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

// handleImage 是图片代理的 HTTP 薄壳：参数解析 + 发字节，抓取/缓存/缩图都在
// internal/imgcache（同步任务也要用它预热封面）。
//
// 两个可选参数：
//   - w=<px>  显示宽度，交给图床自己缩放（只有 beeg 的图床认）
//   - s=<px>  隐私模式要的小糊图：在本地缩到 s 像素宽、糊一遍再发
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw := strings.TrimSpace(q.Get("u"))
	fallback := strings.TrimSpace(q.Get("f"))
	platformKey := q.Get("p")

	if smallWidth := smallWidthParam(q.Get("s")); smallWidth > 0 {
		data, err := s.images.Small(r.Context(), platformKey, raw, smallWidth)
		if err != nil && fallback != "" {
			data, err = s.images.Small(r.Context(), platformKey, fallback, smallWidth)
		}
		if err != nil {
			// 隐私模式的失败方式只能是「看不见」：发空白，绝不退回原图。
			s.log.Warn("隐私模式小图生成失败，发空白占位", "url", raw, "err", err)
			servePlaceholder(w)
			return
		}
		serveBytes(w, data)
		return
	}

	data, ok := s.images.Bytes(r.Context(), platformKey, raw, q.Get("w"))
	if !ok && fallback != "" {
		// `f=` 是后端自己说的兜底地址（见 dto.go 的 imgProxyChain），不是任意用户输入：
		// 换一档尺寸名再抓一次，抓不到才发空白。
		data, ok = s.images.Bytes(r.Context(), platformKey, fallback, q.Get("w"))
	}
	if !ok {
		servePlaceholder(w)
		return
	}
	serveBytes(w, data)
}

// smallWidthParam 解析 s= 参数：只收 8–256，别的都当没写。
func smallWidthParam(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 8 || n > 256 {
		return 0
	}
	return n
}

func servePlaceholder(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	w.Write(placeholderPNG)
}

func serveBytes(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
