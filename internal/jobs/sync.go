package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

type syncPayload struct {
	SubscriptionID int64  `json:"subscriptionId"`
	Mode           string `json:"mode"` // auto | full | incremental
}

// pageSize 仅用于把 backfillLimit（影片条数）换算成页数上限。
const pageSize = 24

// runSyncJob 是 sync 类型任务的执行入口。
func (r *Runner) runSyncJob(ctx context.Context, j *model.Job, c *Ctl) error {
	var p syncPayload
	if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
		return fmt.Errorf("任务参数损坏")
	}
	return r.syncSubscription(ctx, c, p)
}

// syncSubscription 执行一次艺人同步：
//   - 首次（backfillDone=false）：从断点页继续翻页直到末页（续跑只对「被中断」有意义）
//   - mode=full：从头重扫到末页（已经回填完过的订阅也一样从头，见下面 startPage 的注释）
//   - 其余情况：只抓第 1 页，遇到已入库影片即停止
func (r *Runner) syncSubscription(ctx context.Context, c *Ctl, p syncPayload) error {
	sub, err := r.st.GetSubscription(p.SubscriptionID)
	if err != nil {
		return fmt.Errorf("订阅不存在: %w", err)
	}
	perf, err := r.st.GetPerformer(sub.PerformerID)
	if err != nil {
		return fmt.Errorf("艺人不存在: %w", err)
	}
	ad, ok := r.reg.Get(perf.Platform)
	if !ok {
		return fmt.Errorf("平台未注册: %s", perf.Platform)
	}

	// 1) 刷新艺人资料（失败不阻塞同步）
	if fresh, err := ad.GetPerformer(ctx, perf.PerformerID); err == nil && fresh != nil {
		fresh.Platform = perf.Platform
		fresh.PerformerID = perf.PerformerID
		if fresh.URL == "" {
			fresh.URL = perf.URL
		}
		if _, err := r.st.UpsertPerformer(fresh); err != nil {
			r.log.Warn("更新艺人资料失败", "performer", perf.ID, "err", err)
		}
	} else if err != nil {
		r.log.Warn("抓取艺人资料失败", "performer", perf.PerformerID, "err", err)
	}

	// 1.5) 资料内容改从站外资料源（Pornhub）借：beeg / xHamster 的艺人页
	// 只有名字、头像和站内统计，简介、标签行、三围、社交链接、排名都缺。
	// 名字对不上就不动，失败同样不阻塞同步。
	if latest, err := r.st.GetPerformer(perf.ID); err == nil {
		r.FillProfile(ctx, latest)
	}

	// 2) 头像：平台页面上抠不到时，用站外头像源按名字补一张（失败不影响同步）。
	if latest, err := r.st.GetPerformer(perf.ID); err == nil {
		r.FillAvatar(ctx, latest)
	}

	full := p.Mode == "full" || !sub.BackfillDone
	startPage := 1
	// 「从断点续跑」只对「上次回填被中断」有意义。已经回填完的订阅再点一次「完整同步」，
	// 用户要的是从头再扫一遍（列表中间漏掉的、当时写入失败的那几部只能靠它补）；
	// 而从断点页续会直接落在末页之外——单页创作者（如 mysaaat）上就是「同步完成，新增 0 部」。
	if full && !sub.BackfillDone && sub.BackfillPage > 0 {
		startPage = sub.BackfillPage + 1
	}
	if full && startPage == 1 && sub.BackfillPage > 0 {
		// 从头重扫：先把旧断点清掉，否则这一轮要是在中途被打断，
		// 下次续跑会拿着上一轮的末尾页码，把前面几页整个跳过去。
		if err := r.st.MarkBackfillProgress(sub.ID, 0, nil, false); err != nil {
			r.log.Warn("重置回填断点失败", "subscription", sub.ID, "err", err)
		}
	}

	maxPages := 0
	if sub.BackfillLimit > 0 {
		maxPages = (sub.BackfillLimit + pageSize - 1) / pageSize
	}

	phase := "incremental"
	if full {
		phase = "backfill"
	}

	page := startPage
	totalNew := 0
	// 新片的封面地址要趁「刚解析到、签名还新鲜」的时候抓进缓存，见 covers.go。
	var newCovers []store.VideoThumb
	// 只在增量同步里收集新片 id：首次全量回填动辄几百部，绝不能自动下载。
	var autoIDs []int64
	finished := false
	videosSeen := 0
	// lastPage 是最后一个真正抓成功的页；回填完成时按它记断点，
	// 免得「翻过头的那一页」把续跑点一直往后推（每次同步都白跑两次请求）。
	lastPage := startPage - 1
	totalPages := 0
	if sub.BackfillTotalPages != nil {
		totalPages = *sub.BackfillTotalPages
	}
	// rateLimited 记下「这一轮是被限流叫停的」：限流不是错误，是节流。
	var rateLimited error

	for {
		if err := c.Check(); err != nil {
			return err
		}
		res, err := ad.ListPerformerVideos(ctx, perf.PerformerID, page)
		if err != nil {
			// 翻过末页时 xHamster 回的是 404，不是空页。这是「到头了」，不是出错：
			// 早先一律按「中断」处理，而且续跑点落在第 2 页时更糟——第 1 页之后的
			// 失败原本一律当硬错误，于是回填永远停在「未完成」，每次扫到这位艺人
			// 都从断点重来一遍、再在同一个 404 上停住（单页创作者上是死循环，mysaaat 就是）。
			//
			// 注意这条要排在第 1 页判断之前：续跑点本身就可能落在已经翻完的那一页上。
			// 第 1 页 404 另当别论：那是艺人页本身没了，仍然算失败。
			if page > 1 && errors.Is(err, platform.ErrNotFound) {
				r.log.Info("影片列表翻到末页", "performer", perf.PerformerID, "page", page)
				finished = true
				break
			}
			// 限流（B 站 -799 / 429、xHamster 429）不是「抓错了」，是「该歇会儿」。
			// 已经抓到的页照常入库、断点照常留着，下一轮接着翻；只有在某一页都没抓到的
			// 极端情况下才当失败处理，免得订阅悄无声息地一直不同步。
			if errors.Is(err, platform.ErrRateLimited) {
				rateLimited = err
				r.log.Warn("列表被限流，这一轮先停", "performer", perf.PerformerID, "page", page, "err", err)
				break
			}
			if page == startPage {
				return fmt.Errorf("抓取第 %d 页失败: %w", page, err)
			}
			r.log.Warn("分页抓取中断，保留已抓内容", "page", page, "err", err)
			break
		}
		if res.TotalPages > 0 {
			totalPages = res.TotalPages
		}
		if len(res.Items) == 0 {
			finished = true
			break
		}

		hitKnown := false
		for _, item := range res.Items {
			if err := c.Check(); err != nil {
				return err
			}
			videosSeen++
			item.Platform = perf.Platform
			exists, err := r.st.VideoExists(item.Platform, item.PlatformVideoID)
			if err != nil {
				return err
			}
			if exists {
				if !full {
					hitKnown = true
					break
				}
				continue
			}
			vid, _, err := r.st.UpsertVideo(&item)
			if err != nil {
				r.log.Warn("写入影片失败", "video", item.PlatformVideoID, "err", err)
				continue
			}
			if err := r.st.LinkVideoPerformer(vid, perf.ID); err != nil {
				r.log.Warn("关联影片与艺人失败", "video", vid, "err", err)
			}
			totalNew++
			if item.ThumbURL != "" {
				newCovers = append(newCovers, store.VideoThumb{
					ID: vid, Platform: item.Platform, PlatformVideoID: item.PlatformVideoID, ThumbURL: item.ThumbURL,
				})
			}
			if !full {
				autoIDs = append(autoIDs, vid)
			}
			// platform 是给前端分流用的：B 站有自己的页面，它的新投稿只能刷 B 站墙，
			// 不能顺手把「关注」流也重拉一遍（那边根本不含 B 站）。
			r.hub.Publish("video.new", map[string]any{
				"videoId":     vid,
				"performerId": perf.ID,
				"platform":    item.Platform,
				"title":       item.Title,
			})
		}

		// 增量同步只有一两页，不该显示成「回填」。
		word := "同步"
		if full {
			word = "回填"
		}
		if totalPages > 0 {
			c.Progress(float64(page)/float64(totalPages)*100,
				fmt.Sprintf("%s第 %d/%d 页，新增 %d 部", word, page, totalPages, totalNew), 0, 0, 0)
		} else {
			c.Progress(0, fmt.Sprintf("%s第 %d 页，新增 %d 部", word, page, totalNew), 0, 0, 0)
		}

		r.hub.Publish("sync.progress", map[string]any{
			"subscriptionId": sub.ID,
			"performerId":    perf.ID,
			"page":           page,
			"totalPages":     totalPages,
			"videosFound":    totalNew,
			"phase":          phase,
		})

		if hitKnown {
			finished = true
			break
		}
		if totalPages > 0 && page >= totalPages {
			finished = true
			break
		}
		if !res.HasMore {
			finished = true
			break
		}
		if maxPages > 0 && (page-startPage+1) >= maxPages {
			finished = true
			break
		}

		lastPage = page

		// 回填过程记录断点，进程中断后可续跑。
		if full {
			var tp *int
			if totalPages > 0 {
				tp = &totalPages
			}
			if err := r.st.MarkBackfillProgress(sub.ID, page, tp, false); err != nil {
				r.log.Warn("记录回填断点失败", "subscription", sub.ID, "err", err)
			}
		}
		page++
	}

	// 一页都没抓到的限流不能算「同步完成」：订阅会一直不动却看不出来。
	if rateLimited != nil && lastPage < startPage {
		return fmt.Errorf("抓取第 %d 页失败: %w", startPage, rateLimited)
	}

	if full && finished {
		var tp *int
		if totalPages > 0 {
			tp = &totalPages
		}
		if err := r.st.MarkBackfillProgress(sub.ID, lastPage, tp, true); err != nil {
			return err
		}
	}

	done := fmt.Sprintf("同步完成，新增 %d 部", totalNew)
	if rateLimited != nil {
		done = fmt.Sprintf("同步被限流中断，新增 %d 部（断点已存，稍后自动接着翻）", totalNew)
	}
	c.Progress(100, done, 0, 0, 0)
	r.warmCovers(newCovers)
	r.repairInBackground()
	if len(autoIDs) > 0 {
		r.maybeAutoDownload(autoIDs)
	}
	// 同步完顺手把最新几部的播放地址解析好（后台跑，失败不影响同步），
	// 这样点开影片基本不用等解析。
	r.warmPlayback(perf.Platform, perf.ID)
	return r.st.MarkSynced(sub.ID, r.interval, "")
}

// maybeAutoDownload 在「同步到新片后自动下载」开关打开时，把新片投进下载队列。
// 默认关闭；关闭时下载只能手动触发（详情页/卡片上的下载按钮）。
func (r *Runner) maybeAutoDownload(videoIDs []int64) {
	on, err := r.st.GetSetting("auto_download_new")
	if err != nil || on != "1" {
		return
	}
	lib, err := r.st.DefaultLibrary()
	if err != nil {
		r.log.Warn("自动下载跳过：没有可用的默认本地库", "err", err)
		return
	}
	queued := 0
	for _, id := range videoIDs {
		// 已经有本地文件，或已经在队列里跑着，就别重复投递。
		if files, err := r.st.ListLibraryFilesByVideo(id); err == nil && len(files) > 0 {
			continue
		}
		if active, err := r.st.HasActiveJobForTarget(model.JobTypeDownload, id); err == nil && active {
			continue
		}
		if _, err := r.EnqueueDownload(id, lib.ID, false); err != nil {
			r.log.Warn("自动下载投递失败", "video", id, "err", err)
			continue
		}
		queued++
	}
	if queued > 0 {
		r.log.Info("已按设置自动下载同步到的新片", "count", queued, "library", lib.Name)
	}
}

// EnqueueSync 供 HTTP 层调用：把同步任务入队。
func (r *Runner) EnqueueSync(subscriptionID int64, mode string) (int64, error) {
	return r.st.Enqueue(model.JobTypeSync, syncPayload{SubscriptionID: subscriptionID, Mode: mode}, priorityFor(mode))
}

func priorityFor(mode string) int {
	// 数字越小越先执行：用户手动触发的优先进队。
	switch mode {
	case "full", "incremental":
		return 10
	default:
		return 100
	}
}
