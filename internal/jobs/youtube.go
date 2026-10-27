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

	"github.com/mario/myporn/internal/library"
	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/social"
	"github.com/mario/myporn/internal/store"
	"github.com/mario/myporn/internal/ytsub"
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

	if failed > 0 {
		c.Progress(100, fmt.Sprintf("同步完成，%d 个频道失败", failed), 0, 0, 0)
		return nil
	}
	c.Progress(100, fmt.Sprintf("同步完成，共 %d 个频道", len(channels)), 0, 0, 0)
	return nil
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
func (r *Runner) ytBackfill(ctx context.Context, c *Ctl, ch *store.YtChannel) error {
	limit := r.settingInt("yt_backfill_limit", ytBackfillDefault)
	c.Progress(c.percent(), fmt.Sprintf("用 yt-dlp 列 %s 的前 %d 条", ch.Title, limit), 0, 0, 0)
	videos, err := r.ytDlp.ListChannel(ctx, ch.ChannelID, limit)
	if err != nil {
		return err
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
	art := r.writeArtwork(ctx, video, mp.AbsDir)
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
