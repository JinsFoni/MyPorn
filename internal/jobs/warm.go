package jobs

import (
	"context"
	"time"

	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// warmCount 每次同步预热多少部最新影片：够覆盖「打开就点最新几部」的用法，
// 又不会因为预热把上游请求打得太多。
const warmCount = 6

// warmPlayback 在后台把该艺人最新几部影片的播放地址解析好并落库（5 分钟内有效）。
// 解析失败只记日志：这只是「加速」，不该影响同步结果。
func (r *Runner) warmPlayback(platformKey string, performerID int64) {
	ad, ok := r.reg.Get(platformKey)
	if !ok {
		return
	}
	quick, ok := ad.(platform.PlaybackResolver)
	if !ok {
		return
	}
	items, _, err := r.st.ListFeed(store.FeedQuery{PerformerID: performerID, Limit: warmCount})
	if err != nil || len(items) == 0 {
		return
	}
	go func() {
		// 预热是尽力而为：给个短一点的超时，别在后台堆请求。
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		for _, v := range items {
			if ctx.Err() != nil {
				return
			}
			// 已经有新鲜缓存的跳过。
			if vs, at, err := r.st.GetVariants(v.ID); err == nil && len(vs) > 0 && time.Since(at) < 4*time.Minute {
				continue
			}
			meta, vs, err := quick.ResolvePlayback(ctx, v.PlatformVideoID)
			if err != nil || len(vs) == 0 {
				continue
			}
			if err := r.st.SaveVariants(v.ID, vs); err != nil {
				r.log.Warn("预热播放地址保存失败", "video", v.ID, "err", err)
			}
			// 顺手把标签等只在详情页才有的信息补上。
			if meta != nil && len(meta.Tags) > 0 {
				if err := r.st.FillVideoMeta(v.ID, meta); err != nil {
					r.log.Warn("预热元数据保存失败", "video", v.ID, "err", err)
				}
			}
		}
		r.log.Info("已预热最新影片播放地址", "performer", performerID, "count", len(items))
	}()
}
