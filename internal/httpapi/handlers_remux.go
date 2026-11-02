package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/muxcache"
)

// handleVideoRemux 把「音画分轨」的那一档合流成一份 mp4 再下发。
//
// 只有 DASH 站（目前是 B 站 1080P 以上）的档位会带 AudioURL：那些片源是画面一条、
// 声音一条，浏览器只有一个 <video>，谁也没法自己合。这里做的事跟下载链路里的
// 「两条流 remux 成 mp4」完全一样（ffmpeg -c copy，不转码），只是结果留在缓存目录里
// 当普通文件发——Range、秒开、拖动进度条全都白拿。
//
// 第一次请求要等合流跑完。时长差得极远，别按一个数量级想：
//
//	480P/286 秒的片子   68 MB   2 秒
//	1080P 长片          88 MB   十几秒
//	4K（AV1）          554 MB   1~2 分钟
//	4K（H.264）       1.77 GB   3~5 分钟
//
// 4K 那两档是「点下去像放不出来」的全部原因——请求头一个字节都不回，浏览器只能干等。
// 所以这个接口多了一条 `?status=1`：立刻回 JSON（状态 + 百分比），合流在后台跑，
// 前端轮询到 ready 再把地址交给 <video>。见 handleRemuxStatus。
func (s *Server) handleVideoRemux(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "影片 id 不对")
		return
	}
	if s.mux == nil {
		writeErr(w, http.StatusInternalServerError, "internal", "合流器不可用（需要 ffmpeg）")
		return
	}
	v, err := s.st.GetVideo(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "影片不存在")
		return
	}
	variantID := strings.TrimSpace(r.URL.Query().Get("v"))
	if variantID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "缺少画质参数 v")
		return
	}

	key := fmt.Sprintf("%d-%s", id, variantID)
	produce := s.remuxProducer(v, variantID)
	if r.URL.Query().Get("status") != "" {
		s.writeRemuxStatus(w, key, produce)
		return
	}

	path, err := s.mux.Ensure(r.Context(), key, produce)
	if err != nil {
		// 用户自己切走了（换档位、退回列表），不用再回话。
		if errors.Is(err, context.Canceled) {
			return
		}
		s.log.Warn("合流失败", "video", id, "variant", variantID, "err", err)
		// 丢掉这一批签名地址：下次重试会重新解析，通常能拿到另一条 CDN。
		s.dropVariants(id)
		if errors.Is(err, context.DeadlineExceeded) {
			writeErr(w, http.StatusGatewayTimeout, "mux_timeout",
				"这一档要整条流下载完才能合流，超过时限没下完（多半是 CDN 卡住了）。稍后重试会自动换一条线路。")
			return
		}
		writeErr(w, http.StatusBadGateway, "mux_failed", "合流失败: "+err.Error())
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		writeErr(w, http.StatusInternalServerError, "internal", "合流产物不见了")
		return
	}
	// 成品是「这一档画质」的完整文件，浏览器可以放心缓存一会儿。
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// ServeFile 负责 Range / If-Range / 206，进度条拖动全靠它。
	http.ServeFile(w, r, path)
}

// handleRemuxStatus 是 `?status=1` 的那条路：开始（或接上）合流，立刻回状态。
//
// 存在的理由就一句话——4K 要等好几分钟，而这个等待以前是「一个不回来的请求」，
// 用户看到的只有转圈，最后往往误判成「放不了」。
//
//	{"state":"merging","percent":62.5,"elapsedSec":48}
//	{"state":"ready"}                      ← 前端这才把 /remux 交给 <video>
//	{"state":"failed","error":"…"}         ← 明确说是哪里坏了
func (s *Server) writeRemuxStatus(w http.ResponseWriter, key string, produce func(context.Context) (muxcache.Source, error)) {
	snap := s.mux.Snapshot(key)
	if snap.State == muxcache.StateIdle {
		s.mux.Begin(key, produce)
		snap = s.mux.Snapshot(key)
	}
	pct := snap.Percent
	out := map[string]any{
		"state":      string(snap.State),
		"percent":    float64(int(pct*10)) / 10,
		"elapsedSec": int(snap.Elapsed.Seconds() + 0.5),
	}
	if snap.State == muxcache.StateFailed && snap.Err != nil {
		out["error"] = snap.Err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// remuxProducer 组装「这一档的两条流地址」。只在真的要合流时才被调用：
// 存下来的地址带签名、几小时就过期，缓存命中时绝不重新解析。
func (s *Server) remuxProducer(v *model.Video, variantID string) func(context.Context) (muxcache.Source, error) {
	// find 从库里的档位表取这一档。地址是带签名的、5 分钟就会换一批，
	// 所以「找不到」不是错误，而是「该重新解析了」。
	find := func() (model.Variant, bool) {
		vs, _, err := s.st.GetVariants(v.ID)
		if err != nil {
			return model.Variant{}, false
		}
		for _, vv := range vs {
			if vv.ID() == variantID && vv.AudioURL != "" {
				return vv, true
			}
		}
		return model.Variant{}, false
	}
	ad, hasAdapter := s.reg.Get(v.Platform)
	// B 站媒体 CDN 不带 Referer 一律 403（实测），合流时两条流都要带上。
	referer := ""
	if hasAdapter {
		referer = strings.TrimSuffix(ad.BaseURL(), "/") + "/"
	}

	return func(ctx context.Context) (muxcache.Source, error) {
		vv, ok := find()
		if !ok && hasAdapter {
			// 合流跑在后台 context 上，拿到的很可能是早就过期的地址：
			// 重新解析一次并写回库里，再用新地址合流。
			fresh, rerr := ad.ResolveStreams(ctx, v.PlatformVideoID)
			if rerr != nil {
				return muxcache.Source{}, rerr
			}
			model.SortVariantsDesc(fresh)
			fresh = model.DedupeVariants(fresh)
			if serr := s.st.SaveVariants(v.ID, fresh); serr != nil {
				s.log.Warn("保存清晰度列表失败", "video", v.ID, "err", serr)
			}
			for _, f := range fresh {
				if f.ID() == variantID && f.AudioURL != "" {
					vv, ok = f, true
					break
				}
			}
		}
		if !ok {
			return muxcache.Source{}, errors.New("找不到这一档画质的地址")
		}
		return muxcache.Source{
			VideoURL: vv.URL,
			AudioURL: vv.AudioURL,
			Referer:  referer,
			UA:       mediaUserAgent,
			TotalSec: v.DurationSec,
		}, nil
	}
}

// dropVariants 清掉这部片子的画质缓存，逼下一次重新解析。
//
// 合流失败最常见的原因是「地址过期 / 那条 CDN 卡住了」，两种情况换一批新地址就能好；
// 但如果继续用库里这一批，用户点几次「重试」都会撞同一堵墙。
func (s *Server) dropVariants(videoID int64) {
	if err := s.st.SaveVariants(videoID, nil); err != nil {
		s.log.Warn("清理画质缓存失败", "video", videoID, "err", err)
	}
}
