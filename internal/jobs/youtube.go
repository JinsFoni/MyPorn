package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JinsFoni/MyPorn/internal/library"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/social"
	"github.com/JinsFoni/MyPorn/internal/store"
	"github.com/JinsFoni/MyPorn/internal/ytsub"
)

// YouTube 订阅任务（M6），两条：
//   - yt_sync：抓一个频道（或全部启用的频道）的新视频。mode=auto 走 RSS（1 次请求、15 条），
//     mode=deep 走 yt-dlp 深度回填（默认 200 条）。两条通道写同一张表，谁有哪个字段谁补哪个。
//   - yt_download：用 yt-dlp 下一条视频，AV1 视频 + 最高音质音频合成单文件 mp4，再走常规入库。
//
// 抓取并发固定 1：YouTube 也是按 IP 计限流，并发只会更快撞墙（同 M5 的 social_sync）。

type ytSyncPayload struct {
	ChannelID int64  `json:"channelId"` // 0 = 全部启用的频道
	Mode      string `json:"mode"`      // auto | deep
}

const (
	ytModeAuto = "auto"
	ytModeDeep = "deep"
)

// ytBackfillDefault 是深度回填的默认条数（实测 200 条 5.8 秒，see M6 §11.3）。
const ytBackfillDefault = 200

// EnqueueYtSync 投递一次频道同步；同一频道已有排队/在跑的任务就不重复投。
func (r *Runner) EnqueueYtSync(channelRowID int64, mode string) (int64, error) {
	if mode != ytModeDeep {
		mode = ytModeAuto
	}
	if channelRowID > 0 {
		if active, err := r.st.HasActiveJobForTarget(model.JobTypeYtSync, channelRowID); err == nil && active {
			return 0, nil
		}
	}
	return r.st.EnqueueTarget(model.JobTypeYtSync, channelRowID,
		ytSyncPayload{ChannelID: channelRowID, Mode: mode}, 8)
}

// EnqueueYtDownload 投递一次视频下载；同一视频在同一个库里已有任务就不重复投。
//
// 这里先把 videos 那一行建出来，任务就挂在影片 id 上而不是 yt_videos 的行 id：
// 下载页是通用页面（按 target_id 查标题、缩略图、本地文件），两边 id 空间不同，
// 用错会把别的影片的信息贴到这条任务上。
func (r *Runner) EnqueueYtDownload(ytVideoID, libraryID int64, overwrite bool) (int64, error) {
	row, err := r.st.GetYtVideo(ytVideoID)
	if err != nil {
		return 0, fmt.Errorf("视频不存在: %w", err)
	}
	videoID, err := r.st.EnsureVideoForYt(row)
	if err != nil {
		return 0, fmt.Errorf("写入影片索引失败: %w", err)
	}
	if active, err := r.st.HasActiveJobForTarget(model.JobTypeYtDownload, videoID); err == nil && active {
		return 0, nil
	}
	return r.st.EnqueueTarget(model.JobTypeYtDownload, videoID,
		ytDownloadPayload{YtVideoID: ytVideoID, VideoID: videoID, LibraryID: libraryID, Overwrite: overwrite}, 5)
}

// runYtSyncJob 是 yt_sync 的执行入口。
func (r *Runner) runYtSyncJob(ctx context.Context, j *model.Job, c *Ctl) error {
	var p ytSyncPayload
	if j.Payload != "" {
		if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
			return fmt.Errorf("任务参数损坏")
		}
	}
	if r.ytDlp == nil {
		return fmt.Errorf("YouTube 未启用")
	}

	var channels []*store.YtChannel
	if p.ChannelID > 0 {
		ch, err := r.st.GetYtChannel(p.ChannelID)
		if err != nil {
			return fmt.Errorf("频道不存在: %w", err)
		}
		channels = []*store.YtChannel{ch}
	} else {
		list, err := r.st.ListYtChannels(true)
		if err != nil {
			return err
		}
		channels = list
	}
	if len(channels) == 0 {
		c.Progress(100, "没有需要同步的频道", 0, 0, 0)
		return nil
	}

	failed := 0
	for i, ch := range channels {
		if err := c.Check(); err != nil {
			return err
		}
		label := fmt.Sprintf("%d/%d", i+1, len(channels))
		verb := "抓取"
		if p.Mode == ytModeDeep {
			verb = "深度回填"
		}
		c.Progress(float64(i)/float64(len(channels))*100,
			fmt.Sprintf("%s %s（%s）", verb, ch.Title, label), 0, 0, 0)

		var err error
		if p.Mode == ytModeDeep {
			err = r.ytBackfill(ctx, c, ch)
		} else {
			err = r.ytIncrement(ctx, c, ch)
		}
		if err != nil {
			failed++
			r.recordYtError(ch.ID, err)
			// 限流 / 被拦会持续影响后面的频道，直接收工，别把配额浪费在必然失败的请求上。
			if k := social.KindOf(err); k == social.KindRateLimited || k == social.KindBlocked {
				c.Progress(100, fmt.Sprintf("%s 失败：%v", ch.Title, err), 0, 0, 0)
				return nil
			}
			continue
		}
		r.recordYtError(ch.ID, nil)
	}

	r.autoFillYtMeta()
	if failed > 0 {
		c.Progress(100, fmt.Sprintf("同步完成，%d 个频道失败", failed), 0, 0, 0)
		return nil
	}
	c.Progress(100, fmt.Sprintf("同步完成，共 %d 个频道", len(channels)), 0, 0, 0)
	return nil
}

// ytMetaAutoLimit 是「同步完顺手补一次」的上限。
//
// 一轮 RSS 增量最多进 15 条/频道，40 条足够把这次新增的缺口清掉；更大的积压（比如刚点过
// 深度回填，200 行全缺发布时间）不在同步里顺手跑——那是十几分钟的观看页请求，
// 留给墙上的「补齐」按钮，用户点了才跑。顺带也避免了「永远补不上的行（已删除/私享）」
// 在每轮同步里被反复重抓：积压超过这个数就不再自动跑。
const ytMetaAutoLimit = 40

// autoFillYtMeta 同步完检查一次缺口，积压不大的话直接排一个 yt_meta。
//
// 为什么需要：RSS 那条通道**不带时长**（yt-dlp flat 才带），所以每轮增量同步之后，
// 新进来的那批行就有日期没时长、卡片上光秃秃的。补一次是每条一次 GET（实测 1.5 秒），
// 十几条也就十几秒，比让用户自己发现「有的卡没有时长」再点补齐强。
func (r *Runner) autoFillYtMeta() {
	gaps, err := r.st.CountYtVideosMissingMeta()
	if err != nil {
		r.log.Warn("统计缺元信息的视频失败", "err", err)
		return
	}
	if gaps.Rows == 0 || gaps.Rows > ytMetaAutoLimit {
		return
	}
	if _, err := r.EnqueueYtMeta(ytMetaAutoLimit, false); err != nil {
		r.log.Warn("排队补元信息失败", "err", err)
	}
}

// ytIncrement 走 RSS：1 次请求、最近 15 条、有绝对时间。
func (r *Runner) ytIncrement(ctx context.Context, c *Ctl, ch *store.YtChannel) error {
	client := r.httpClient("youtube")
	feed, videos, err := ytsub.FetchFeed(ctx, client, r.ytThrottle, ch.ChannelID)
	if err != nil {
		return err
	}
	fresh, err := r.st.UpsertYtVideos(ch.ID, toYtInputs(videos))
	if err != nil {
		return err
	}
	// RSS 里带了频道标题，频道改名时顺手更新。
	if feed.Title != "" && feed.Title != ch.Title {
		if _, _, err := r.st.UpsertYtChannel(&store.YtChannel{
			ChannelID: ch.ChannelID, Handle: ch.Handle, Title: feed.Title,
			AvatarURL: ch.AvatarURL, ProfileURL: ch.ProfileURL, Enabled: ch.Enabled,
		}); err != nil {
			r.log.Warn("更新频道资料失败", "channel", ch.ID, "err", err)
		}
	}
	if err := r.st.TouchYtChannelSynced(ch.ID, time.Now()); err != nil {
		r.log.Warn("记录频道同步时间失败", "channel", ch.ID, "err", err)
	}
	r.publishYtNew(c, ch, len(fresh))
	return nil
}

// ytBackfill 走 yt-dlp：一次列 N 条，补上 RSS 给不了的时长。
//
// 会员抢先看（`availability=subscriber_only`）在这里被丢掉，库里已有的同 id 行一并删掉：
// 这类视频要加入频道会员才能看，抓进来的卡片点开必然「解析失败」，用户 2026-11-02 要求不要。
// 30 天后它们转公开，下一轮同步看见 availability 不再是 subscriber_only，就会正常入库。
func (r *Runner) ytBackfill(ctx context.Context, c *Ctl, ch *store.YtChannel) error {
	limit := r.settingInt("yt_backfill_limit", ytBackfillDefault)
	c.Progress(c.percent(), fmt.Sprintf("用 yt-dlp 列 %s 的前 %d 条", ch.Title, limit), 0, 0, 0)
	listed, err := r.ytDlp.ListChannel(ctx, ch.ChannelID, limit)
	if err != nil {
		return err
	}
	videos := make([]ytsub.Video, 0, len(listed))
	members := make([]string, 0, 4)
	for _, v := range listed {
		if v.MembersOnly {
			members = append(members, v.VideoID)
			continue
		}
		videos = append(videos, v)
	}
	if len(members) > 0 {
		pruned, derr := r.st.DeleteYtVideosByVideoIDs(members)
		if derr != nil {
			r.log.Warn("删掉会员抢先看的视频失败", "channel", ch.Title, "err", derr)
		}
		r.log.Info("跳过会员抢先看的视频", "channel", ch.Title, "listed", len(members), "removed", pruned)
	}
	fresh, err := r.st.UpsertYtVideos(ch.ID, toYtInputs(videos))
	if err != nil {
		return err
	}
	if err := r.st.TouchYtChannelBackfilled(ch.ID, time.Now()); err != nil {
		r.log.Warn("记录回填时间失败", "channel", ch.ID, "err", err)
	}
	if err := r.st.TouchYtChannelSynced(ch.ID, time.Now()); err != nil {
		r.log.Warn("记录频道同步时间失败", "channel", ch.ID, "err", err)
	}
	r.publishYtNew(c, ch, len(fresh))
	return nil
}

func (r *Runner) publishYtNew(c *Ctl, ch *store.YtChannel, count int) {
	if count <= 0 {
		return
	}
	c.Event("yt.new", map[string]any{
		"channelId": ch.ID,
		"title":     ch.Title,
		"count":     count,
	})
	r.log.Info("抓到新视频", "channel", ch.Title, "count", count)
}

// recordYtError 把失败原因写回频道行，界面上能直接看到；err=nil 表示清空。
func (r *Runner) recordYtError(channelRowID int64, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
		if len(msg) > maxSyncErrorLen {
			msg = msg[:maxSyncErrorLen]
		}
	}
	if err := r.st.SetYtChannelError(channelRowID, msg); err != nil {
		r.log.Warn("记录频道抓取错误失败", "channel", channelRowID, "err", err)
	}
}

func toYtInputs(videos []ytsub.Video) []store.YtVideoInput {
	out := make([]store.YtVideoInput, 0, len(videos))
	for _, v := range videos {
		out = append(out, store.YtVideoInput{
			VideoID:      v.VideoID,
			Title:        v.Title,
			Description:  v.Description,
			ThumbnailURL: v.ThumbnailURL,
			PublishedAt:  v.PublishedAt,
			DateSource:   v.DateSource,
			Views:        v.Views,
			DurationSec:  v.DurationSec,
			IsLive:       v.IsLive,
		})
	}
	return out
}

// ---- 下载 ----

type ytDownloadPayload struct {
	YtVideoID int64 `json:"ytVideoId"` // yt_videos 的行 id（抓取侧）
	VideoID   int64 `json:"videoId"`   // videos 的行 id（入库侧，= 任务 target_id）
	LibraryID int64 `json:"libraryId"`
	Overwrite bool  `json:"overwrite"`
}

// runYtDownloadJob 是 yt_download 的执行入口。
//
// 与普通 download 任务的区别：那边要先 pickVariant 拿 HLS 清单（必须有 platform.Adapter），
// YouTube 没有 adapter，产物由 yt-dlp 直接给；但下完之后的入库四件套完全一样
// （probe → 命名 → nfo/海报 → SaveLibraryFile），所以影片能进「关注」流、能被 Emby 刮到。
func (r *Runner) runYtDownloadJob(ctx context.Context, j *model.Job, c *Ctl) error {
	var p ytDownloadPayload
	if err := decodePayload(j.Payload, &p); err != nil {
		return fmt.Errorf("任务参数损坏")
	}
	if r.ytDlp == nil {
		return fmt.Errorf("YouTube 未启用")
	}
	row, err := r.st.GetYtVideo(p.YtVideoID)
	if err != nil {
		return fmt.Errorf("视频不存在: %w", err)
	}
	lib, err := r.resolveLibrary(p.LibraryID)
	if err != nil {
		return err
	}
	// 先在 videos 表里落一行（platform='youtube'），后续入库与「关注」流都认它。
	videoID, err := r.st.EnsureVideoForYt(row)
	if err != nil {
		return fmt.Errorf("写入影片索引失败: %w", err)
	}
	video, err := r.st.GetVideo(videoID)
	if err != nil {
		return fmt.Errorf("读取影片失败: %w", err)
	}

	// 幂等：同一影片在这个库里已有文件就跳过（除非显式覆盖）。
	existing, existingErr := r.st.GetLibraryFile(video.ID, lib.ID)
	if existingErr == nil && !p.Overwrite {
		if fileExists(existing.VideoPath) {
			c.Progress(100, "本地已存在，跳过", 0, 0, 0)
			return nil
		}
		_ = r.st.DeleteLibraryFile(video.ID, lib.ID)
	}

	space := library.CheckSpace(lib.RootPath)
	if !space.Writable {
		return fmt.Errorf("库目录不可写: %s (%s)", lib.RootPath, space.Message)
	}

	workDir := filepath.Join(r.cfg.DataDir, "tmp", fmt.Sprintf("yt-%d-%d", video.ID, lib.ID))
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}

	c.Progress(1, "正在解析流…", 0, 0, 0)
	var lastPct float64
	// yt-dlp 是个几十秒到几十分钟的子进程，中间没人去查取消/暂停标志，
	// 用户点了取消就得等它下完——所以下载期间单独起一个轮询。
	dlCtx, dlCancel := context.WithCancel(ctx)
	defer dlCancel()
	stopPoll := make(chan struct{})
	var pollWG sync.WaitGroup
	pollWG.Add(1)
	go func() {
		defer pollWG.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopPoll:
				return
			case <-t.C:
				if cerr := c.Check(); cerr != nil {
					dlCancel()
					return
				}
			}
		}
	}()
	srcPath, err := r.ytDlp.Download(dlCtx, ytsub.DownloadRequest{
		VideoID: row.VideoID,
		WorkDir: workDir,
		OnProgress: func(p ytsub.Progress) {
			// 下载占 1%–90%，后面留给 probe / 海报 / nfo。
			pct := 1 + p.Percent*0.89
			if pct < lastPct {
				// yt-dlp 先下视频轨再下音频轨，进度会回跳；不要让它把进度条拽回去。
				pct = lastPct
			}
			lastPct = pct
			c.Progress(pct, "下载中", int64(p.Speed), p.Done, p.Total)
		},
	})
	close(stopPoll)
	pollWG.Wait()
	if err != nil {
		// 是我们自己掐的（取消/暂停）就把真实原因传上去，别报成 yt-dlp 失败。
		if cerr := c.Check(); cerr != nil {
			return cerr
		}
		return err
	}

	// 平铺列表（yt-dlp --flat-playlist）不带简介，nfo 的 plot 就只剩标题。
	// 下载完顺手补一次详情（约 4 秒）：既拿到简介，也把缺的发布时间写回 yt_videos，
	// 这样文件名里的日期和墙上的「时间未知」都能顺手修好。
	if strings.TrimSpace(row.Description) == "" || row.PublishedAt == nil {
		if vi, verr := r.ytDlp.VideoInfo(ctx, row.VideoID); verr != nil {
			r.log.Warn("补全视频详情失败（继续）", "video", row.VideoID, "err", verr)
		} else {
			if strings.TrimSpace(video.Description) == "" && strings.TrimSpace(vi.Description) != "" {
				video.Description = vi.Description
			}
			if row.PublishedAt == nil && !vi.PublishedAt.IsZero() {
				pub := vi.PublishedAt
				row.PublishedAt = &pub
				if err := r.st.FillYtVideoMeta(row.ID, &pub, vi.DateSource, 0, 0); err != nil {
					r.log.Warn("回写发布时间失败", "video", row.ID, "err", err)
				}
			}
		}
	}

	info, perr := r.enc.Probe(ctx, srcPath)
	if perr != nil {
		r.log.Warn("读取媒体信息失败（继续）", "video", video.ID, "err", perr)
	}
	if info.DurationSec == 0 {
		info.DurationSec = row.DurationSec
	}

	tmpl := r.namingTemplate(lib)
	nameCtx := library.NameContext{
		Title:     video.Title,
		Performer: row.Channel.Title,
		Platform:  "YouTube",
		ID:        row.VideoID,
	}
	if row.PublishedAt != nil {
		nameCtx.Date = row.PublishedAt.Format("2006-01-02")
	}
	if info.Height > 0 {
		nameCtx.Resolution = fmt.Sprintf("%dp", info.Height)
	}
	movieName := library.RenderName(tmpl, nameCtx)
	mp, err := library.BuildMoviePath(lib.RootPath, "YouTube", row.Channel.Title, movieName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(mp.AbsDir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	c.Progress(92, "落盘", 0, 0, 0)
	finalName := mp.VideoName + filepath.Ext(srcPath)
	finalPath := filepath.Join(mp.AbsDir, finalName)
	if err := moveFile(srcPath, finalPath); err != nil {
		return fmt.Errorf("移动文件失败: %w", err)
	}

	c.Progress(96, "写入 Emby 元数据", 0, info.SizeBytes, info.SizeBytes)
	art := r.writeArtwork(ctx, video, finalPath, mp.AbsDir, info.DurationSec)
	nfoPath := filepath.Join(mp.AbsDir, mp.VideoName+".nfo")
	if err := writeNFOFile(nfoPath, video, "YouTube", nameCtx.Resolution, info); err != nil {
		r.log.Warn("写入 nfo 失败", "video", video.ID, "err", err)
		art.warnings = append(art.warnings, "nfo: "+err.Error())
	}

	lf := &model.LibraryFile{
		VideoID:     video.ID,
		LibraryID:   lib.ID,
		RelDir:      mp.RelDir,
		VideoName:   finalName,
		VideoPath:   finalPath,
		NFOPath:     nfoPath,
		PosterPath:  art.poster,
		ThumbPath:   art.thumb,
		FanartPath:  art.fanart,
		SizeBytes:   info.SizeBytes,
		Width:       info.Width,
		Height:      info.Height,
		DurationSec: info.DurationSec,
	}
	if err := r.st.SaveLibraryFile(lf); err != nil {
		return fmt.Errorf("写入库索引失败: %w", err)
	}
	// 下完了就算看过了，别让它在墙上继续顶着未看角标。
	if err := r.st.MarkYtVideosSeen([]int64{row.ID}); err != nil {
		r.log.Warn("标记已看失败", "yt", row.ID, "err", err)
	}
	_ = os.RemoveAll(workDir)

	msg := fmt.Sprintf("已完成 %s（%s）", mp.VideoName, library.HumanBytes(info.SizeBytes))
	if len(art.warnings) > 0 {
		msg += "；图片问题：" + strings.Join(art.warnings, "、")
	}
	c.Progress(100, msg, 0, info.SizeBytes, info.SizeBytes)
	c.Event("download.done", map[string]any{
		"videoId":   video.ID,
		"libraryId": lib.ID,
		"path":      finalPath,
		"sizeBytes": info.SizeBytes,
	})
	r.log.Info("YouTube 下载完成", "video", row.VideoID, "path", finalPath, "size", info.SizeBytes)
	return nil
}

// moveFile 优先 rename（同盘瞬时），跨盘时退回复制。
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

// ---- yt_meta：给缺元信息（发布时间 / 时长 / 播放量）的视频补一遍 ----

// ytMetaDefault 是一次补多少条。
//
// 一条一次普通 GET（实测 1.29MB 响应、1.5 秒），所以 500 条上限 ≈ 12 分钟；
// 排在后台、可中断，跑完墙上的「时间未知」和「没有时长」就没了。默认给足，让用户点一次就清干净。
const ytMetaDefault = 500

type ytMetaPayload struct {
	Limit int `json:"limit"`
	// Force 是「用户明确点了补齐」：忽略 meta_tries 的记账，把补过 3 次还缺的那些也重试一遍。
	// 自动补（同步完顺手跑的那次）不带它。
	Force bool `json:"force"`
}

// EnqueueYtMeta 投递一次「补元信息」；已经有在跑的就不重复投。
func (r *Runner) EnqueueYtMeta(limit int, force bool) (int64, error) {
	if limit <= 0 {
		limit = ytMetaDefault
	}
	if active, err := r.st.HasActiveJobForTarget(model.JobTypeYtMeta, 0); err == nil && active {
		return 0, nil
	}
	return r.st.Enqueue(model.JobTypeYtMeta, ytMetaPayload{Limit: limit, Force: force}, 9)
}

// runYtMetaJob 补元信息：**先按频道批量补时长 → 再用 RSS 批量补日期 → 剩下的逐条看观看页**。
//
// 三遍的原因（第一遍 + 第三遍见 M6 §14.24，第二遍 2026-11-06 加）：
//   - 观看页那条路（一次 GET、1.29MB、1.5 秒）只在这台机器「匿名也能拿到播放器数据」时好用。
//     现在从出口 13.158.71.17 匿名抓 `/watch?v=` 只剩登录墙：1.25~1.34MB 的页面里
//     **零个 lengthSeconds、零个 publishDate**，`playabilityStatus` 是 `LOGIN_REQUIRED`。
//     于是每轮同步都「补完 0 条，3 条这次没拿到」，而墙上那条提示条永远消不掉。
//   - 同一时刻频道页的 yt-dlp flat 列表照样带时长：7 个频道各拉 60 条，
//     1.0~2.4 秒一次、60/60 条都有 `duration`。一次请求补一个频道里所有缺时长的行。
//
// 所以：flat 能补的时长先一次性补掉；日期走 RSS（一次请求 20KB、带绝对时间，覆盖最新 15 条）；
// 剩下的（短视频不在 `/videos` 列表里、超过列表深度的老条目、RSS 那 15 条之外的）才逐条看
// 观看页，观看页被登录墙挡住就整批换 yt-dlp 真解析（实测 4.6 秒/条）。
// 补不到的记账，到 store.YtMetaMaxTries 次就不再算「缺」，提示条也就不会再永远挂着。
func (r *Runner) runYtMetaJob(ctx context.Context, j *model.Job, c *Ctl) error {
	var p ytMetaPayload
	if j.Payload != "" {
		if err := json.Unmarshal([]byte(j.Payload), &p); err != nil {
			return fmt.Errorf("任务参数损坏")
		}
	}
	if p.Force {
		// 用户要求重试：把「补过几次」清零，之前被判「补不到」的行也重新排队。
		if err := r.st.ResetYtMetaTries(); err != nil {
			r.log.Warn("清零补元信息记账失败", "err", err)
		}
	}
	rows, err := r.st.ListYtVideosMissingMeta(p.Limit, p.Force)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		c.Progress(100, "没有缺元信息的视频", 0, 0, 0)
		return nil
	}

	// 第一遍：flat 列表批量补时长。
	filled, left := 0, rows
	if r.ytDlp != nil {
		var n int
		n, left = r.ytFillDurationsFromList(ctx, c, rows)
		filled += n
	}

	// 第二遍：RSS 批量补日期（一次请求覆盖一个频道最新 15 条）。
	var dated int
	if len(left) > 0 {
		dated, left = r.ytFillDatesFromFeed(ctx, c, left)
		filled += dated
	}

	// 第三遍：前两遍都补不到的那些，逐条看观看页。
	client := r.httpClient("youtube")
	failed, pruned, blocked := 0, 0, ""
	for i, row := range left {
		if err := c.Check(); err != nil {
			return err
		}
		c.Progress(float64(i)/float64(len(left))*100,
			fmt.Sprintf("看观看页 %d/%d（已补 %d，跳过 %d）", i+1, len(left), filled, failed), 0, 0, 0)

		info, err := ytsub.WatchMeta(ctx, client, r.ytThrottle, row.VideoID)
		if err != nil {
			// 限流 / 登录墙：后面的必然也失败，收工。这些行（含这一条）一次记满次数——
			// 挡住的不是这一条，而是这条路；换一份 Cookie 或用户点补齐都会清零重来。
			if k := social.KindOf(err); k == social.KindRateLimited || k == social.KindBlocked {
				// 这条路被挡住了（登录墙 / 限流）。有 yt-dlp 就整批换过去：它走自己的客户端
				// 和 Cookie，观看页的登录墙挡不住它（实测 2026-11-06 一次性解析 4.6 秒/条）。
				// 换不了才按老办法把剩下的都记满次数收工。
				if r.ytDlp != nil && r.ytDlp.Available() {
					rest := left[i:]
					n, p2, f2 := r.ytMetaViaYtDlp(ctx, c, rest)
					filled += n
					pruned += p2
					failed += f2
					r.log.Info("观看页被挡，剩余改走 yt-dlp 补齐",
						"rows", len(rest), "filled", n, "failed", f2, "reason", err.Error())
					break
				}
				rest := make([]int64, 0, len(left)-i)
				for _, r2 := range left[i:] {
					rest = append(rest, r2.ID)
				}
				if berr := r.st.BumpYtMetaTries(rest, store.YtMetaMaxTries); berr != nil {
					r.log.Warn("记补元信息次数失败", "err", berr)
				}
				blocked = err.Error()
				break
			}
			failed++
			if berr := r.st.BumpYtMetaTries([]int64{row.ID}, 1); berr != nil {
				r.log.Warn("记补元信息次数失败", "ytVideo", row.ID, "err", berr)
			}
			continue
		}
		if info.MembersOnly {
			// 这条是会员抢先看：库里不该有它（观看页的徽章是权威判据，flat 列表那次可能
			// 还没带上 availability）。删掉，下一轮同步它转公开了再进来。
			if _, derr := r.st.DeleteYtVideosByVideoIDs([]string{row.VideoID}); derr != nil {
				r.log.Warn("删掉会员抢先看的视频失败", "ytVideo", row.ID, "err", derr)
			} else {
				pruned++
			}
			continue
		}
		var published *time.Time
		if !info.PublishedAt.IsZero() {
			published = &info.PublishedAt
		}
		if err := r.st.FillYtVideoMeta(row.ID, published, info.DateSource, info.DurationSec, info.Views); err != nil {
			r.log.Warn("写回元信息失败", "ytVideo", row.ID, "err", err)
			failed++
			if berr := r.st.BumpYtMetaTries([]int64{row.ID}, 1); berr != nil {
				r.log.Warn("记补元信息次数失败", "ytVideo", row.ID, "err", berr)
			}
			continue
		}
		if published == nil && strings.TrimSpace(row.PublishedAt) == "" {
			// 只补到时长、没补到日期（观看页偶尔只剩播放器数据）：FillYtVideoMeta 会把
			// meta_tries 清零，所以这里自己记一次账，否则这一条会在每轮同步里被反复重抓。
			failed++
			if berr := r.st.BumpYtMetaTries([]int64{row.ID}, 1); berr != nil {
				r.log.Warn("记补元信息次数失败", "ytVideo", row.ID, "err", berr)
			}
			continue
		}
		filled++
	}
	msg := fmt.Sprintf("补完 %d 条", filled)
	if pruned > 0 {
		msg += fmt.Sprintf("，删掉 %d 条会员抢先看的", pruned)
	}
	if failed > 0 {
		// 单条失败多半是偶发（实测重跑一次就补上了），所以不说成「已删除」。
		msg += fmt.Sprintf("，%d 条这次没拿到（可能只是这次请求失败，可以再点一次）", failed)
	}
	if blocked != "" {
		msg += fmt.Sprintf("；剩下 %d 条拿不到：%s", len(rows)-filled-pruned-failed, blocked)
	}
	// 收尾这句要强制落库：这条任务常常一两秒就结束，普通 Progress 会落在 800ms 节流里被丢掉。
	c.FlushProgress(100, msg)
	r.log.Info("补元信息完成", "filled", filled, "failed", failed, "membersOnly", pruned,
		"leftOver", len(rows)-filled-pruned, "blocked", blocked)
	return nil
}

// ytFillDatesFromFeed 用 RSS 批量补发布时间，返回还没补上的行。
//
// RSS 一次请求约 20KB、带**绝对时间**（`<published>`）；观看页那条路每条 1.3MB，还可能撞
// 登录墙。刚深度回填过的频道正好是「前 15 条全缺日期」（2026-11-06 实测：频道 horizon 的
// 15 条就是这样，RSS 那 15 条恰好一条不少地带着日期），一次请求就能清掉一大半。
// RSS 只覆盖最新 15 条，更老的（列表深度之外的老条目）仍旧交给观看页那一遍。
func (r *Runner) ytFillDatesFromFeed(ctx context.Context, c *Ctl, rows []store.YtVideoLite) (filled int, left []store.YtVideoLite) {
	byChannel := map[int64][]store.YtVideoLite{}
	order := make([]int64, 0, 4)
	for _, row := range rows {
		if _, ok := byChannel[row.ChannelID]; !ok {
			order = append(order, row.ChannelID)
		}
		byChannel[row.ChannelID] = append(byChannel[row.ChannelID], row)
	}
	client := r.httpClient("youtube")
	for i, chID := range order {
		if err := c.Check(); err != nil {
			for _, rest := range order[i:] {
				left = append(left, byChannel[rest]...)
			}
			return filled, left
		}
		ch, err := r.st.GetYtChannel(chID)
		if err != nil {
			left = append(left, byChannel[chID]...)
			continue
		}
		c.Progress(c.percent(), fmt.Sprintf("用 RSS 补 %s 的发布时间", ch.Title), 0, 0, 0)
		_, videos, err := ytsub.FetchFeed(ctx, client, r.ytThrottle, ch.ChannelID)
		if err != nil {
			r.log.Warn("拉 RSS 补日期失败，这些行改走观看页",
				"channel", ch.Title, "rows", len(byChannel[chID]), "err", err)
			left = append(left, byChannel[chID]...)
			continue
		}
		byID := make(map[string]ytsub.Video, len(videos))
		for _, v := range videos {
			byID[v.VideoID] = v
		}
		for _, row := range byChannel[chID] {
			v, ok := byID[row.VideoID]
			if !ok || v.PublishedAt.IsZero() {
				// 不在最新 15 条里（比 RSS 还老），交给观看页
				left = append(left, row)
				continue
			}
			pub := v.PublishedAt
			if err := r.st.FillYtVideoMeta(row.ID, &pub, v.DateSource, 0, v.Views); err != nil {
				r.log.Warn("写回发布时间失败", "ytVideo", row.ID, "err", err)
				left = append(left, row)
				continue
			}
			filled++
		}
	}
	return filled, left
}

// ytMetaViaYtDlp 是观看页被登录墙挡住时的换路：逐条让 yt-dlp 真解析一次。
//
// 比观看页贵（实测 4.6 秒/条 vs 一个 GET），但它是自己那套客户端 + Cookie，
// YouTube 让匿名观看页返回 LOGIN_REQUIRED 的时候它照样能拿到 upload_date。
func (r *Runner) ytMetaViaYtDlp(ctx context.Context, c *Ctl, rows []store.YtVideoLite) (filled, pruned, failed int) {
	for i, row := range rows {
		if err := c.Check(); err != nil {
			return filled, pruned, failed
		}
		c.Progress(c.percent(), fmt.Sprintf("换 yt-dlp 补齐 %d/%d", i+1, len(rows)), 0, 0, 0)
		vi, err := r.ytDlp.VideoInfo(ctx, row.VideoID)
		if err != nil {
			r.log.Warn("yt-dlp 补元信息失败", "ytVideo", row.VideoID, "err", err)
			failed++
			if berr := r.st.BumpYtMetaTries([]int64{row.ID}, 1); berr != nil {
				r.log.Warn("记补元信息次数失败", "ytVideo", row.ID, "err", berr)
			}
			continue
		}
		if vi.MembersOnly {
			if _, derr := r.st.DeleteYtVideosByVideoIDs([]string{row.VideoID}); derr != nil {
				r.log.Warn("删掉会员抢先看的视频失败", "ytVideo", row.VideoID, "err", derr)
				failed++
			} else {
				pruned++
			}
			continue
		}
		var published *time.Time
		if !vi.PublishedAt.IsZero() {
			published = &vi.PublishedAt
		}
		if err := r.st.FillYtVideoMeta(row.ID, published, vi.DateSource, vi.DurationSec, vi.Views); err != nil {
			r.log.Warn("写回元信息失败", "ytVideo", row.ID, "err", err)
			failed++
			if berr := r.st.BumpYtMetaTries([]int64{row.ID}, 1); berr != nil {
				r.log.Warn("记补元信息次数失败", "ytVideo", row.ID, "err", berr)
			}
			continue
		}
		filled++
	}
	return filled, pruned, failed
}

// ytFillDurationsFromList 用 yt-dlp 的 flat 列表批量补时长，返回没补上的行。
//
// 实测（2026-11-04，7 个启用的频道各拉 60 条）：1.0~2.4 秒一次、每条都带 duration，
// 而同一个视频的观看页是 1.3MB 的登录墙。一次请求补一个频道里所有缺时长的行，
// 比逐条 1.5 秒还拿不到东西的观看页省得多，而且**不依赖登录态**。
//
// 拉不到的频道不放弃：那些行原样交给第二遍（观看页）。
func (r *Runner) ytFillDurationsFromList(ctx context.Context, c *Ctl, rows []store.YtVideoLite) (filled int, left []store.YtVideoLite) {
	byChannel := map[int64][]store.YtVideoLite{}
	order := make([]int64, 0, 4)
	for _, row := range rows {
		if _, ok := byChannel[row.ChannelID]; !ok {
			order = append(order, row.ChannelID)
		}
		byChannel[row.ChannelID] = append(byChannel[row.ChannelID], row)
	}
	limit := r.settingInt("yt_backfill_limit", ytBackfillDefault)
	for i, chID := range order {
		if err := c.Check(); err != nil {
			for _, rest := range order[i:] {
				left = append(left, byChannel[rest]...)
			}
			return filled, left
		}
		ch, err := r.st.GetYtChannel(chID)
		if err != nil {
			left = append(left, byChannel[chID]...)
			continue
		}
		c.Progress(c.percent(), fmt.Sprintf("用 flat 列表补 %s 的时长", ch.Title), 0, 0, 0)
		listed, err := r.ytDlp.ListChannel(ctx, ch.ChannelID, limit)
		if err != nil {
			r.log.Warn("拉频道 flat 列表失败，这些行改走观看页",
				"channel", ch.Title, "rows", len(byChannel[chID]), "err", err)
			left = append(left, byChannel[chID]...)
			continue
		}
		byID := make(map[string]ytsub.Video, len(listed))
		for _, v := range listed {
			byID[v.VideoID] = v
		}
		for _, row := range byChannel[chID] {
			v, ok := byID[row.VideoID]
			if !ok || v.DurationSec <= 0 {
				// 短视频不在 `/videos` 列表里（它在 `/shorts`，那个列表连时长字段都没有），
				// 超出列表深度的老条目也在这里——都留给观看页那遍。
				left = append(left, row)
				continue
			}
			if err := r.st.FillYtVideoMeta(row.ID, nil, "", v.DurationSec, v.Views); err != nil {
				r.log.Warn("写回时长失败", "ytVideo", row.ID, "err", err)
				left = append(left, row)
				continue
			}
			// flat 列表**永远不带发布时间**（2026-11-06 实测：频道 horizon 的 15 条
			// 全是 `timestamp: null`，同日再拉一次也是 15/15 为 null）。所以补完时长不等于
			// 补齐了——缺日期的行必须继续交给 RSS / 观看页那两遍。原来这里直接算「补齐」，
			// 于是深度回填进来的那批行永远停在「时间未知」，而且每轮同步都说「没有缺元信息」。
			if strings.TrimSpace(row.PublishedAt) == "" {
				left = append(left, row)
				continue
			}
			filled++
		}
	}
	return filled, left
}
