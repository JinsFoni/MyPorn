package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
//
// 本地库里已有文件时，本地那一路排在第一位并作为默认档：用户明明下过了，
// 再让他等平台 CDN 现成解析一遍没有意义（而且 YouTube 这类没有 adapter 的平台
// 根本解析不出来，本地文件是唯一的播放方式）。
func (s *Server) handlePlayback(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	v, err := s.st.GetVideo(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "影片不存在")
		return
	}

	out := make([]map[string]any, 0, 8)
	defaultID := ""
	proxyURL := ""
	if lf := s.localPlayable(v); lf != nil {
		defaultID = "local"
		proxyURL = localFileURL(v.ID, lf.LibraryID)
		out = append(out, map[string]any{
			"id":        "local",
			"height":    0,
			"width":     0,
			"bandwidth": 0,
			"codec":     "",
			"url":       proxyURL,
			"hls":       false,
			"isDefault": true,
			"local":     true,
			"libraryId": lf.LibraryID,
			"fileName":  filepath.Base(lf.VideoPath),
			"sizeBytes": lf.SizeBytes,
		})
	}

	var variants []model.Variant
	var meta *model.Video
	var rerr error
	// 平台没注册（YouTube）就别去碰 registry：那不是错误，只是没这条线。
	if _, ok := s.reg.Get(v.Platform); ok {
		variants, meta, rerr = s.ensureVariants(r, v, false)
	}
	switch {
	case rerr != nil && errors.Is(rerr, errUnsupportedPlatform):
		// 落到下面统一处理
	case rerr != nil && len(out) == 0:
		writeErr(w, http.StatusBadGateway, "resolve_failed", "解析播放地址失败: "+rerr.Error())
		return
	case rerr != nil:
		s.log.Warn("本地有文件，线上解析失败就先播本地", "video", v.ID, "err", rerr)
	}

	// 既没有本地文件、也没有线上档位，才是真的没得播：只有不注册 adapter 的平台（YouTube）
	// 会走到这里。判断必须带上 len(variants)：线上档位是在下面那个循环里才追加进 out 的，
	// 只看 out 会把「有线上地址、只是还没进 out」错判成「没得播」——
	// M6 之后每条 xHamster 影片都 400 unsupported_platform，就是这么来的。
	if len(out) == 0 && len(variants) == 0 {
		writeErr(w, http.StatusBadRequest, "unsupported_platform", "这个平台没有在线播放通道，先下载到本地")
		return
	}

	// 所有线上播放地址都改走 /api/stream：平台 CDN 会校验 Referer，
	// 浏览器直接请求只会拿到 404，必须由服务端补上 Referer/UA 再转发。
	for i, vv := range variants {
		src := streamURL(v.Platform, vv.URL)
		hls := strings.Contains(vv.URL, ".m3u8")
		// 音画分轨的档位（B 站 1080P+）没法直接代理：一条 URL 只有一个轨。
		// 改成让服务端先合流成 mp4，再把成品当文件发（Range 都支持）。
		if vv.AudioURL != "" {
			src = remuxURL(v.ID, vv.ID())
			hls = false
		}
		first := i == 0
		if first && defaultID == "" {
			defaultID = vv.ID()
			proxyURL = src
		}
		out = append(out, map[string]any{
			"id":        vv.ID(),
			"height":    vv.Height,
			"width":     vv.Width,
			"bandwidth": vv.Bandwidth,
			"codec":     vv.Codec,
			"url":       src,
			"hls":       hls,
			"isDefault": first && defaultID == vv.ID(),
		})
	}

	var expiresAt any
	if _, fetchedAt, err := s.st.GetVariants(id); err == nil && !fetchedAt.IsZero() && len(variants) > 0 {
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

// remuxURL 是「音画分轨」档位的播放地址：服务端合流成 mp4 再下发。
func remuxURL(videoID int64, variantID string) string {
	q := url.Values{}
	q.Set("v", variantID)
	return "/api/videos/" + strconv.FormatInt(videoID, 10) + "/remux?" + q.Encode()
}

// localFileURL 是本地文件的播放地址（支持 Range，浏览器能直接拖动进度条）。
func localFileURL(videoID, libraryID int64) string {
	return "/api/videos/" + strconv.FormatInt(videoID, 10) + "/file?libraryId=" + strconv.FormatInt(libraryID, 10)
}

// localPlayable 找这个影片在本机库里能直接播的文件。
//
// 只认视频扩展名：库里也可能存着照片（社交存图），那种东西喂给 <video> 只会黑屏。
func (s *Server) localPlayable(v *model.Video) *model.LibraryFile {
	files, err := s.st.ListLibraryFilesByVideo(v.ID)
	if err != nil || len(files) == 0 {
		return nil
	}
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f.VideoPath)) {
		case ".mp4", ".m4v", ".webm", ".mkv", ".mov":
			if _, err := os.Stat(f.VideoPath); err == nil {
				return f
			}
		}
	}
	return nil
}

// handleVideoFile 把本地库里的文件发给浏览器。
//
// 走 ServeFile 而不是自己读：Range / If-Range / 206 / Content-Length 它都处理好了，
// 播放器拖动进度条全靠这个。
func (s *Server) handleVideoFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "影片 id 不对")
		return
	}
	libID := int64(queryInt(r, "libraryId", 0))

	var lf *model.LibraryFile
	if libID > 0 {
		lf, err = s.st.GetLibraryFile(id, libID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "这个库里没有这部电影")
			return
		}
	} else if files, err := s.st.ListLibraryFilesByVideo(id); err == nil && len(files) > 0 {
		lf = files[0]
	} else {
		writeErr(w, http.StatusNotFound, "not_found", "本地没有这部电影")
		return
	}

	abs := filepath.Clean(lf.VideoPath)
	lib, err := s.st.GetLibrary(lf.LibraryID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "库不存在")
		return
	}
	// 落库路径理论上都来自我们自己，但这条接口是"按库根放行任意文件"，
	// 万一库根被改小、或者记录被写脏，这里必须拦住，不能变成任意文件读取。
	root := filepath.Clean(lib.RootPath)
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		s.log.Warn("库文件路径越界，拒绝下发", "video", id, "path", abs, "root", root)
		writeErr(w, http.StatusForbidden, "forbidden", "文件不在库目录内")
		return
	}
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		writeErr(w, http.StatusNotFound, "not_found", "文件已不在磁盘上")
		return
	}
	http.ServeFile(w, r, abs)
}
