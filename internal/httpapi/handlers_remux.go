package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"fmt"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/muxcache"
)

// handleVideoRemux 把「音画分轨」的那一档合流成一份 mp4 再下发。
//
// 只有 DASH 站（目前是 B 站 1080P 以上）的档位会带 AudioURL：那些片源是画面一条、
// 声音一条，浏览器只有一个 <video>，谁也没法自己合。这里做的事跟下载链路里的
// 「两条流 remux 成 mp4」完全一样（ffmpeg -c copy，不转码），只是结果留在缓存目录里
// 当普通文件发——Range、秒开、拖动进度条全都白拿。
//
// 第一次请求要等合流跑完（实测 480P/286 秒的片子 2 秒，1080P 长片十几秒），
// 之后同一个档位直接读盘。前端看到的就是一段普通的加载等待，不需要额外提示。
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

	// find 从库里的档位表取这一档。地址是带签名的、5 分钟就会换一批，
	// 所以「找不到」不是错误，而是「该重新解析了」。
	find := func() (model.Variant, bool) {
		vs, _, gerr := s.st.GetVariants(id)
		if gerr != nil {
			return model.Variant{}, false
		}
		for _, vv := range vs {
			if vv.ID() == variantID && vv.AudioURL != "" {
				return vv, true
			}
		}
		return model.Variant{}, false
	}
	if _, ok := find(); !ok {
		if _, _, rerr := s.ensureVariants(r, v, false); rerr != nil {
			writeErr(w, http.StatusBadGateway, "resolve_failed", "解析播放地址失败: "+rerr.Error())
			return
		}
		if _, ok := find(); !ok {
			writeErr(w, http.StatusNotFound, "not_found",
				"这一档画质已经不可用了（平台的活动地址会过期），刷新页面重新选一次")
			return
		}
	}

	ad, hasAdapter := s.reg.Get(v.Platform)
	// B 站媒体 CDN 不带 Referer 一律 403（实测），合流时两条流都要带上。
	referer := ""
	if hasAdapter {
		referer = strings.TrimSuffix(ad.BaseURL(), "/") + "/"
	}

	started := time.Now()
	path, err := s.mux.Ensure(r.Context(), fmt.Sprintf("%d-%s", id, variantID),
		func(ctx context.Context) (muxcache.Source, error) {
			vv, ok := find()
			if !ok && hasAdapter {
				// 合流跑在后台 context 上，拿到的很可能是早就过期的地址：
				// 重新解析一次并写回库里，再用新地址合流。
				fresh, rerr := ad.ResolveStreams(ctx, v.PlatformVideoID)
				if rerr != nil {
					return muxcache.Source{}, rerr
				}
				model.SortVariantsDesc(fresh)
				if serr := s.st.SaveVariants(id, fresh); serr != nil {
					s.log.Warn("保存清晰度列表失败", "video", id, "err", serr)
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
			}, nil
		})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// 用户自己切走了（或者等不及），不用再回话。
			return
		}
		s.log.Warn("合流失败", "video", id, "variant", variantID, "err", err)
		writeErr(w, http.StatusBadGateway, "mux_failed", "合流失败: "+err.Error())
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		writeErr(w, http.StatusInternalServerError, "internal", "合流产物不见了")
		return
	}
	// 真跑了一次合流才值得记一条：命中缓存也记会把这个日志刷成噪声。
	if el := time.Since(started); el > time.Second {
		s.log.Info("分轨合流完成", "video", id, "variant", variantID,
			"size", st.Size(), "elapsed", el.Round(100*time.Millisecond))
	}
	// 成品是「这一档画质」的完整文件，浏览器可以放心缓存一会儿。
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// ServeFile 负责 Range / If-Range / 206，进度条拖动全靠它。
	http.ServeFile(w, r, path)
}
