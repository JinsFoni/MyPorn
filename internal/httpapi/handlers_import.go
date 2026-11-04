package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
)

// 影片站（krx18 这类）没有艺人主页，订阅那条路走不通：贴一条**影片页地址**进来，
// 解析、入库、在线播放。两个接口在这里：
//
//	POST /api/videos/import              贴地址 → 入库（顺带解析播放清单）→ 返回影片
//	GET  /api/{platform}/playlist.m3u8   站内现做的 HLS 清单（见 platform.SitePlaylistProvider）

type importVideoReq struct {
	URL string `json:"url"`
}

// handleImportVideo 把「影片页地址」变成库里的一条影片。
//
// 解析走适配器的 ResolvePlayback（页面元数据 + 清单一次取全）：解析不出来就不入库——
// 一条播不了的影片放在关注流里只会让人反复点、反复失败。
//
// 实测代价（krx18，2026-11-04）：整条链 4.4~5.9 秒（页面 1.4~1.9s + 播放器 0.7~1.4s
// + 播放器页 0.8~1.0s + 换清单 0.8~1.2s），所以这一步是「点一下、等几秒」的量级，
// 前端要显示进度而不是假装瞬时完成。
func (s *Server) handleImportVideo(w http.ResponseWriter, r *http.Request) {
	var req importVideoReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写影片地址")
		return
	}
	ad, videoID, err := s.videoAdapterForURL(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unsupported_platform", err.Error())
		return
	}

	var (
		meta     *model.Video
		variants []model.Variant
	)
	if pb, ok := ad.(platform.PlaybackResolver); ok {
		meta, variants, err = pb.ResolvePlayback(r.Context(), videoID)
	} else {
		variants, err = ad.ResolveStreams(r.Context(), videoID)
		if err == nil {
			meta, err = ad.GetVideo(r.Context(), videoID)
		}
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "解析这部影片失败: "+err.Error())
		return
	}
	if meta == nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "解析这部影片失败: 没拿到元数据")
		return
	}
	meta.Platform = ad.Key()
	meta.PlatformVideoID = videoID
	if meta.URL == "" {
		meta.URL = raw
	}
	id, _, err := s.st.UpsertVideo(meta)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if len(variants) > 0 {
		if err := s.st.SaveVariants(id, variants); err != nil {
			s.log.Warn("保存清晰度列表失败", "video", id, "err", err)
		}
	}
	v, err := s.st.GetVideo(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"video": videoDTO(v)})
}

// videoAdapterForURL 找「能按影片页地址认片」的适配器。
func (s *Server) videoAdapterForURL(raw string) (platform.Adapter, string, error) {
	keys := make([]string, 0, 4)
	for _, a := range s.reg.All() {
		vr, ok := a.(platform.VideoURLResolver)
		if !ok {
			continue
		}
		keys = append(keys, a.Name())
		if id, err := vr.ParseVideoURL(raw); err == nil {
			return a, id, nil
		}
	}
	if len(keys) == 0 {
		return nil, "", errUnsupportedPlatform
	}
	return nil, "", fmt.Errorf("无法识别该影片链接（支持：%s）", strings.Join(keys, " / "))
}

// handleSitePlaylist 现做一条 HLS 清单。
//
// 用得上它的平台：清单拿不到地址、只有内容（krx18 的 /videos/resolve-token 就是回
// m3u8 正文，好让浏览器的网络面板里看不到真实存储地址）。这里把清单里的分片地址
// 改写成 /api/stream 代理地址再发出去，其余部分（#EXTINF、#EXT-X-ENDLIST）原样保留。
func (s *Server) handleSitePlaylist(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "platform")
	videoID := strings.TrimSpace(r.URL.Query().Get("v"))
	ad, ok := s.reg.Get(key)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "平台未注册")
		return
	}
	sp, ok := ad.(platform.SitePlaylistProvider)
	if !ok || videoID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "这个平台没有站内清单")
		return
	}
	body, base, err := sp.SitePlaylist(r.Context(), videoID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "取播放清单失败: "+err.Error())
		return
	}
	out := rewritePlaylist(body, base, key, nil)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	// 清单本身只有十几 KB，但里面每条分片地址都指向站内代理：短缓存，别让播放器
	// 每次起播都回来问一遍（适配器那边另有 10 分钟的解析缓存）。
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}
