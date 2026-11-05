package httpapi

// 站内整片地址：GET /api/{platform}/file.mp4?v=<平台侧 id>
//
// 和 /api/{platform}/playlist.m3u8 是同一类东西（见 platform.SitePlaylistProvider）：
// 上游不给一条能直接取的地址，得由适配器现算。krx18 的 Server 2 属于这一种——
// 整片是一条支持 Range 的 mp4，但每一片的 URL 都带一个算出来的 token，
// 所以只能由服务端按 Range 逐片取再转发（算法见 platform.FileStreamProvider 与
// docs/M8-KRX18.md §11.4）。

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/JinsFoni/MyPorn/internal/platform"
)

// handleSiteFile 转发整片 mp4（透传 Range，播放器才能拖动进度条）。
func (s *Server) handleSiteFile(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "platform")
	videoID := strings.TrimSpace(r.URL.Query().Get("v"))
	ad, ok := s.reg.Get(key)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "平台未注册")
		return
	}
	fp, ok := ad.(platform.FileStreamProvider)
	if !ok || videoID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "这个平台没有站内整片通道")
		return
	}
	// 适配器是「先把分片取回来再写头」，所以解析阶段的错误还能回一个正经的 JSON；
	// 但一旦开始写字节就没法改状态码了，这里记一笔状态，别把错误再往 body 里塞。
	tw := &trackingWriter{ResponseWriter: w}
	if err := fp.ServeFile(r.Context(), videoID, tw, r); err != nil {
		if tw.wrote {
			s.log.Warn("整片转发中断", "platform", key, "video", videoID, "err", err)
			return
		}
		writeErr(w, http.StatusBadGateway, "resolve_failed", "取播放地址失败: "+err.Error())
	}
}

// trackingWriter 记一下有没有写过响应头（写了之后就不能再写 JSON 错误了）。
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *trackingWriter) Write(p []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(p)
}
