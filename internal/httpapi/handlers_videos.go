package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/store"
)

const variantCacheTTL = 5 * time.Minute

func (s *Server) handleListVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	performerID, _ := strconv.ParseInt(q.Get("performerId"), 10, 64)
	items, next, err := s.st.ListFeed(store.FeedQuery{
		PerformerID: performerID,
		Cursor:      q.Get("cursor"),
		Limit:       queryInt(r, "limit", 30),
		Query:       q.Get("q"),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, v := range items {
		out = append(out, videoDTO(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "nextCursor": nullableString(next)})
}

func (s *Server) handleListNewVideos(w http.ResponseWriter, r *http.Request) {
	performerID, _ := strconv.ParseInt(r.URL.Query().Get("performerId"), 10, 64)
	items, next, err := s.st.ListFeed(store.FeedQuery{
		PerformerID: performerID,
		Cursor:      r.URL.Query().Get("cursor"),
		Limit:       queryInt(r, "limit", 30),
		OnlyNew:     true,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, v := range items {
		out = append(out, videoDTO(v))
	}
	total, _ := s.st.CountNewVideos()
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "nextCursor": nullableString(next), "total": total})
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) handleGetVideo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	v, err := s.st.GetVideo(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "影片不存在")
		return
	}
	dto := videoDTO(v)
	dto["description"] = v.Description

	related := []map[string]any{}
	if len(v.Performers) > 0 {
		items, _, err := s.st.ListFeed(store.FeedQuery{PerformerID: v.Performers[0].ID, Limit: 12})
		if err == nil {
			for _, rv := range items {
				if rv.ID == v.ID {
					continue
				}
				related = append(related, videoDTO(rv))
				if len(related) >= 6 {
					break
				}
			}
		}
	}
	dto["related"] = related
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleVideoSeen(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err := s.st.MarkVideoSeen(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ensureVariants 返回（必要时解析并落库）该影片的清晰度列表，按质量降序，缓存 5 分钟。
// needBandwidth=false 时走快路径：不额外查真实码率，起播更快（播放用不到码率）。
// 第二个返回值是解析时顺手拿到的详情页元数据（标签、简介等），命中缓存时为 nil。
func (s *Server) ensureVariants(r *http.Request, v *model.Video, needBandwidth bool) ([]model.Variant, *model.Video, error) {
	variants, fetchedAt, err := s.st.GetVariants(v.ID)
	// 库里只按 (height, bandwidth) 存，编码偏好在这里补上：
	// 缓存命中时也要重排一次，否则「播放用 AV1、下载拿 H.264」两边会对不上。
	model.SortVariantsDesc(variants)
	if err == nil && len(variants) > 0 && time.Since(fetchedAt) < variantCacheTTL {
		if !needBandwidth || hasBandwidth(variants) {
			return variants, nil, nil
		}
	}
	ad, ok := s.reg.Get(v.Platform)
	if !ok {
		return nil, nil, errUnsupportedPlatform
	}
	var fresh []model.Variant
	var meta *model.Video
	var rerr error
	if quick, ok := ad.(platform.PlaybackResolver); ok && !needBandwidth {
		meta, fresh, rerr = quick.ResolvePlayback(r.Context(), v.PlatformVideoID)
	} else {
		fresh, rerr = ad.ResolveStreams(r.Context(), v.PlatformVideoID)
	}
	if rerr != nil {
		if len(variants) > 0 {
			s.log.Warn("播放地址解析失败，先用缓存兜底", "video", v.ID, "err", rerr)
			return variants, nil, nil // 解析失败时降级用旧缓存
		}
		return nil, nil, rerr
	}
	model.SortVariantsDesc(fresh)
	if serr := s.st.SaveVariants(v.ID, fresh); serr != nil {
		s.log.Warn("保存清晰度列表失败", "video", v.ID, "err", serr)
	}
	// 标签这类只在详情页出现的信息，解析播放地址时顺手补齐。
	if meta != nil && len(meta.Tags) > 0 {
		if merr := s.st.FillVideoMeta(v.ID, meta); merr != nil {
			s.log.Warn("补齐影片元数据失败", "video", v.ID, "err", merr)
		}
	}
	return fresh, meta, nil
}

var errUnsupportedPlatform = errors.New("平台未注册")

// hasBandwidth 判断缓存的清晰度里是否已经带了真实码率（体积预估要用）。
func hasBandwidth(vs []model.Variant) bool {
	for _, v := range vs {
		if v.Bandwidth > 0 && !v.ApproxBandwidth {
			return true
		}
	}
	return false
}

// handlePlayback 解析（并缓存 5 分钟）最高画质的播放地址列表。
func (s *Server) handlePlayback(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	v, err := s.st.GetVideo(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "影片不存在")
		return
	}

	variants, meta, err := s.ensureVariants(r, v, false)
	if err != nil {
		if errors.Is(err, errUnsupportedPlatform) {
			writeErr(w, http.StatusBadRequest, "unsupported_platform", "平台未注册")
			return
		}
		writeErr(w, http.StatusBadGateway, "resolve_failed", "解析播放地址失败: "+err.Error())
		return
	}
	_, fetchedAt, _ := s.st.GetVariants(id)
	cacheValid := !fetchedAt.IsZero()

	// 所有播放地址都改走 /api/stream：平台 CDN 会校验 Referer，
	// 浏览器直接请求只会拿到 404，必须由服务端补上 Referer/UA 再转发。
	out := make([]map[string]any, 0, len(variants))
	defaultID := ""
	proxyURL := ""
	for i, vv := range variants {
		if i == 0 {
			defaultID = vv.ID()
		}
		src := streamURL(v.Platform, vv.URL)
		if i == 0 {
			proxyURL = src
		}
		out = append(out, map[string]any{
			"id":        vv.ID(),
			"height":    vv.Height,
			"width":     vv.Width,
			"bandwidth": vv.Bandwidth,
			"codec":     vv.Codec,
			"url":       src,
			"hls":       strings.Contains(vv.URL, ".m3u8"),
			"isDefault": i == 0,
		})
	}
	var expiresAt any
	if cacheValid {
		expiresAt = fetchedAt.Add(variantCacheTTL).Format(time.RFC3339)
	}
	// 标签跟着播放解析一起回来：详情页第一次打开就能显示，不用另抓一次页面。
	tags := v.Tags
	if meta != nil && len(meta.Tags) > 0 {
		tags = meta.Tags
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"videoId":          id,
		"variants":         out,
		"defaultVariantId": nullableString(defaultID),
		"proxyPlaybackUrl": nullableString(proxyURL),
		"expiresAt":        expiresAt,
		"tags":             tags,
	})
}
