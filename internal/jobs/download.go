package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/download"
	"github.com/JinsFoni/MyPorn/internal/library"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// DownloadPayloadInfo 是投递下载任务时快照下来的参数（导出供 HTTP 层解析展示）。
type DownloadPayloadInfo struct {
	VideoID   int64 `json:"videoId"`
	LibraryID int64 `json:"libraryId"`
	Overwrite bool  `json:"overwrite"`
}

// ParseDownloadPayload 解析任务 payload，供队列页展示所属库等信息。
func ParseDownloadPayload(raw string, dst *DownloadPayloadInfo) error {
	return json.Unmarshal([]byte(raw), dst)
}

// EnqueueDownload 投递一个下载任务（target_id = videoId，便于幂等判重）。
func (r *Runner) EnqueueDownload(videoID, libraryID int64, overwrite bool) (int64, error) {
	return r.st.EnqueueTarget(model.JobTypeDownload, videoID,
		DownloadPayloadInfo{VideoID: videoID, LibraryID: libraryID, Overwrite: overwrite}, 10)
}

// httpClient 按平台代理配置缓存 HTTP 客户端（下载分片与抓图共用）。
func (r *Runner) httpClient(platformKey string) *http.Client {
	r.clientsMu.Lock()
	defer r.clientsMu.Unlock()
	if c, ok := r.clients[platformKey]; ok {
		return c
	}
	// 代理是动态问的：改设置里的代理不用重启，下一个分片就走新出口。
	c, err := platform.NewDynamicHTTPClient(func() string { return r.proxyFor(platformKey) }, 0)
	if err != nil {
		c, _ = platform.NewHTTPClient("", 0)
	}
	// 单分片可能很久，禁用整体超时，靠 ctx 控制生命周期。
	c.Timeout = 0
	r.clients[platformKey] = c
	return c
}

// platformClientFor 优先复用适配器自带的客户端（带页面 cookie jar）：
// HLS 签名与页面会话绑定，换一个没有 sessid 的客户端会直接 410。
// 复用时要复制一份，避免把 30s 的整体超时带进大文件下载。
func (r *Runner) platformClientFor(ad platform.Adapter, platformKey string) *http.Client {
	type clientExposer interface{ HTTPClient() *http.Client }
	if ce, ok := ad.(clientExposer); ok {
		if base := ce.HTTPClient(); base != nil {
			cp := *base
			cp.Timeout = 0
			return &cp
		}
	}
	return r.httpClient(platformKey)
}

// runDownloadJob 执行一次「解析 → 下载 → 封装 → 元数据 → 落库」的完整流程。
func (r *Runner) runDownloadJob(ctx context.Context, j *model.Job, c *Ctl) error {
	var p DownloadPayloadInfo
	if err := decodePayload(j.Payload, &p); err != nil {
		return fmt.Errorf("任务参数损坏")
	}

	video, err := r.st.GetVideo(p.VideoID)
	if err != nil {
		return fmt.Errorf("影片不存在: %w", err)
	}
	lib, err := r.resolveLibrary(p.LibraryID)
	if err != nil {
		return err
	}
	ad, ok := r.reg.Get(video.Platform)
	if !ok {
		return fmt.Errorf("平台未注册: %s", video.Platform)
	}

	// 幂等：同一 videoId + library 已存在则跳过（除非显式覆盖）。
	existing, existingErr := r.st.GetLibraryFile(video.ID, lib.ID)
	if existingErr == nil && !p.Overwrite {
		if fileExists(existing.VideoPath) {
			c.Progress(100, "本地已存在，跳过", 0, 0, 0)
			return nil
		}
		// 索引存在但文件没了：清理脏索引后重新下载。
		_ = r.st.DeleteLibraryFile(video.ID, lib.ID)
	}

	space := library.CheckSpace(lib.RootPath)
	if !space.Writable {
		return fmt.Errorf("库目录不可写: %s (%s)", lib.RootPath, space.Message)
	}

	best, err := r.pickVariant(ctx, video, ad)
	if err != nil {
		return err
	}
	c.Progress(2, fmt.Sprintf("已解析画质 %s", best.ID()), 0, 0, 0)

	// 列表页只给标题/时长/封面，nfo 想要的简介/标签/评分在详情页里，顺手补齐。
	video = r.enrichVideo(ctx, ad, video)

	// 目录与命名
	tmpl := r.namingTemplate(lib)
	nameCtx := library.NameContext{
		Title:      video.Title,
		Performer:  primaryPerformerName(video),
		Platform:   ad.Name(),
		ID:         video.PlatformVideoID,
		Resolution: best.ID(),
	}
	if video.PublishedAt != nil {
		nameCtx.Date = video.PublishedAt.Format("2006-01-02")
	}
	movieName := library.RenderName(tmpl, nameCtx)
	mp, err := library.BuildMoviePath(lib.RootPath, ad.Name(), nameCtx.Performer, movieName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(mp.AbsDir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 起一个看门狗，把「取消/暂停」按钮传导到 ctx。
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := c.Check(); err != nil {
					return
				}
			}
		}
	}()

	// 临时目录固定（不含 jobID），暂停后恢复可复用已下载分片。
	workDir := filepath.Join(r.cfg.DataDir, "tmp", fmt.Sprintf("dl-%d-%d", video.ID, lib.ID))
	binPath := filepath.Join(workDir, "stream.bin")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}

	// CDN 校验的是站点 Referer；影片自身 URL 可能为空（旧数据），一律用平台根地址。
	referer := strings.TrimSuffix(ad.BaseURL(), "/") + "/"
	if referer == "/" && video.URL != "" {
		referer = video.URL
	}
	estimated := estimateBytes(best, video.DurationSec)
	outBase := filepath.Join(mp.AbsDir, mp.VideoName)

	finalPath, err := r.downloadAndMux(ctx, c, video, best, workDir, binPath, outBase, referer, estimated, ad)
	if err != nil {
		return err
	}

	info, perr := r.enc.Probe(ctx, finalPath)
	if perr != nil {
		r.log.Warn("读取媒体信息失败（继续）", "video", video.ID, "err", perr)
	}
	if info.DurationSec == 0 {
		info.DurationSec = video.DurationSec
	}
	if info.Height == 0 {
		info.Height = best.Height
		info.Width = best.Width
	}

	// 元数据（图片失败只记警告，不阻塞任务，见 FR-28）
	c.Progress(96, "写入 Emby 元数据", 0, info.SizeBytes, info.SizeBytes)
	art := r.writeArtwork(ctx, video, finalPath, mp.AbsDir, info.DurationSec)
	nfoPath := filepath.Join(mp.AbsDir, mp.VideoName+".nfo")
	if err := writeNFOFile(nfoPath, video, ad.Name(), best.ID(), info); err != nil {
		r.log.Warn("写入 nfo 失败", "video", video.ID, "err", err)
		art.warnings = append(art.warnings, "nfo: "+err.Error())
	}

	// 落库索引
	lf := &model.LibraryFile{
		VideoID:     video.ID,
		LibraryID:   lib.ID,
		RelDir:      mp.RelDir,
		VideoName:   mp.VideoName,
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

	// 覆盖模式：清掉可能残留的另一种容器格式
	if existingErr == nil && existing.VideoPath != "" && existing.VideoPath != finalPath {
		_ = os.Remove(existing.VideoPath)
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
	r.log.Info("下载完成", "video", video.ID, "path", finalPath, "size", info.SizeBytes)
	return nil
}

// downloadAndMux 优先用自研分段下载器；清单形态不支持时退回 ffmpeg 直拉。
func (r *Runner) downloadAndMux(ctx context.Context, c *Ctl, video *model.Video, best model.Variant,
	workDir, binPath, outBase, referer string, estimated int64, ad platform.Adapter) (string, error) {

	// 音画分轨（B 站 1080P 以上）：画面一条流、声音一条流，交给 ffmpeg 两路输入合流。
	// 这一档不能走自研分段下载器——它按「一条清单」下分片，装不下两条独立的流。
	if best.AudioURL != "" {
		return r.muxFromTwoURLs(ctx, c, video, best, outBase, referer, estimated)
	}

	// 直链 mp4（Pornhub 的 get_media 会回这种）不必分片：直接让 ffmpeg 流式拉取再 -c copy 封装。
	if !strings.Contains(best.URL, ".m3u8") {
		return r.muxFromURL(ctx, c, video, best, outBase, referer, estimated)
	}

	client := r.platformClientFor(ad, video.Platform)
	dl := download.New(client)
	dl.Referer = referer
	if r.cfg.DownloadRateKB > 0 {
		dl.BytePerSec = int64(r.cfg.DownloadRateKB) * 1024
	}

	pl, err := dl.Resolve(ctx, best.URL)
	if err != nil {
		if errors.Is(err, download.ErrUnsupported) {
			r.log.Info("清单需 ffmpeg 处理，转直拉模式", "video", video.ID)
			return r.muxFromURL(ctx, c, video, best, outBase, referer, estimated)
		}
		return "", fmt.Errorf("解析播放清单失败: %w", err)
	}

	err = dl.Run(ctx, pl, filepath.Join(workDir, "seg"), binPath, func(p download.Progress) {
		msg := fmt.Sprintf("下载中 %d/%d 分片", p.DoneSegments, p.TotalSegments)
		if p.SpeedBPS > 0 {
			msg += " · " + library.HumanBytes(p.SpeedBPS) + "/s"
		}
		if p.ETASeconds > 0 {
			msg += fmt.Sprintf(" · 剩余 %s", humanETA(p.ETASeconds))
		}
		// 下载占 0-90%，封装/元数据各占剩余
		c.Progress(p.Percent*0.9, msg, p.SpeedBPS, p.BytesDone, p.BytesTotal)
	})
	if err != nil {
		return "", err
	}

	c.Progress(92, "封装中（ffmpeg -c copy，不转码）", 0, 0, estimated)
	ctxRemux, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	return r.enc.Remux(ctxRemux, binPath, outBase)
}

// muxFromURL 是兜底路径：把 m3u8 地址交给 ffmpeg 直接拉流。
func (r *Runner) muxFromURL(ctx context.Context, c *Ctl, video *model.Video, best model.Variant,
	outBase, referer string, estimated int64) (string, error) {

	ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	return r.enc.RemuxFromURL(ctx, best.URL, referer, ua, outBase, video.DurationSec, func(pct float64, written int64) {
		c.Progress(pct*0.9, fmt.Sprintf("下载中（ffmpeg）%.0f%%", pct), 0, written, estimated)
	})
}

// muxFromTwoURLs 把画面 + 声音两条流拉下来合流成一份文件。
//
// 进度靠 ffmpeg 的 -progress 报（和单条直拉那条路径一样按 0-90% 折算）。
func (r *Runner) muxFromTwoURLs(ctx context.Context, c *Ctl, video *model.Video, best model.Variant,
	outBase, referer string, estimated int64) (string, error) {

	ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	return r.enc.RemuxFromURLs(ctx, best.URL, best.AudioURL, referer, ua, outBase, video.DurationSec,
		func(pct float64, written int64) {
			c.Progress(pct*0.9, fmt.Sprintf("下载中（音画合流）%.0f%%", pct), 0, written, estimated)
		})
}

// pickVariant 取最高画质：优先用 5 分钟内的缓存，否则重新解析。
// 「最高」= 分辨率优先，同分辨率内 AV1 > HEVC > VP9 > H.264（见 model.SortVariantsDesc）。
func (r *Runner) pickVariant(ctx context.Context, video *model.Video, ad platform.Adapter) (model.Variant, error) {
	variants, fetchedAt, err := r.st.GetVariants(video.ID)
	model.SortVariantsDesc(variants)
	if err == nil && len(variants) > 0 && time.Since(fetchedAt) < 5*time.Minute {
		r.logPicked(video, variants[0])
		return variants[0], nil
	}
	fresh, rerr := ad.ResolveStreams(ctx, video.PlatformVideoID)
	if rerr != nil {
		if len(variants) > 0 {
			r.log.Warn("重新解析失败，使用缓存画质列表", "video", video.ID, "err", rerr)
			return variants[0], nil
		}
		return model.Variant{}, fmt.Errorf("解析播放地址失败: %w", rerr)
	}
	if len(fresh) == 0 {
		return model.Variant{}, fmt.Errorf("平台没有返回可用画质")
	}
	model.SortVariantsDesc(fresh)
	if err := r.st.SaveVariants(video.ID, fresh); err != nil {
		r.log.Warn("保存画质列表失败", "video", video.ID, "err", err)
	}
	r.logPicked(video, fresh[0])
	return fresh[0], nil
}

// logPicked 把最终选的档位与编码写进日志：将来某个平台给了 AV1，
// 从日志里能直接看出「这部片子下的是 AV1 还是 H.264」。
func (r *Runner) logPicked(video *model.Video, v model.Variant) {
	codec := model.CodecLabel(v.Codec)
	if codec == "" {
		codec = "编码未知"
	}
	r.log.Info("选定画质", "video", video.ID, "quality", v.Quality(), "codec", codec, "bandwidth", v.Bandwidth)
}

// enrichVideo 在写 nfo 之前用详情页补全简介、标签、评分与封面。
// 失败不致命：元数据缺一点也比整个任务失败好。
func (r *Runner) enrichVideo(ctx context.Context, ad platform.Adapter, v *model.Video) *model.Video {
	if v.Description != "" && len(v.Tags) > 0 && v.Rating != nil {
		return v
	}
	ectx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	detail, err := ad.GetVideo(ectx, v.PlatformVideoID)
	if err != nil || detail == nil {
		if err != nil {
			r.log.Warn("补全影片详情失败", "video", v.ID, "err", err)
		}
		return v
	}
	merged := *v
	if merged.Description == "" {
		merged.Description = detail.Description
	}
	if len(merged.Tags) == 0 {
		merged.Tags = detail.Tags
	}
	if merged.Rating == nil {
		merged.Rating = detail.Rating
	}
	if merged.ThumbURL == "" {
		merged.ThumbURL = detail.ThumbURL
	}
	if merged.PreviewURL == "" {
		merged.PreviewURL = detail.PreviewURL
	}
	if merged.URL == "" {
		merged.URL = detail.URL
	}
	if merged.DurationSec == 0 {
		merged.DurationSec = detail.DurationSec
	}
	if _, _, uerr := r.st.UpsertVideo(&merged); uerr != nil {
		r.log.Warn("回写影片详情失败", "video", v.ID, "err", uerr)
	}
	return &merged
}

func (r *Runner) resolveLibrary(id int64) (*model.Library, error) {
	if id > 0 {
		lib, err := r.st.GetLibrary(id)
		if err != nil {
			return nil, fmt.Errorf("本地库不存在: %w", err)
		}
		return lib, nil
	}
	lib, err := r.st.DefaultLibrary()
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("还没有可用的本地库，请先在设置里创建")
		}
		return nil, err
	}
	return lib, nil
}

// namingTemplate 依次回落到库配置 → 全局设置 → 默认模板。
func (r *Runner) namingTemplate(lib *model.Library) string {
	if t := strings.TrimSpace(lib.NamingTemplate); t != "" {
		return t
	}
	if t, err := r.st.GetSetting("naming_template"); err == nil && strings.TrimSpace(t) != "" {
		return t
	}
	return library.DefaultTemplate
}

func primaryPerformerName(v *model.Video) string {
	if len(v.Performers) > 0 && strings.TrimSpace(v.Performers[0].Name) != "" {
		return v.Performers[0].Name
	}
	return "Unknown"
}

// estimateBytes 用码率 × 时长估算体积（FR-16b 的下载确认框用同一算法）。
func estimateBytes(v model.Variant, durationSec int) int64 {
	return v.EstimateBytes(durationSec)
}

func humanETA(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%d 秒", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%d 分 %d 秒", sec/60, sec%60)
	}
	return fmt.Sprintf("%d 小时 %d 分", sec/3600, sec%3600/60)
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

var errNoPayload = errors.New("任务参数为空")

func decodePayload(s string, dst any) error {
	if strings.TrimSpace(s) == "" || s == "{}" {
		return errNoPayload
	}
	return json.Unmarshal([]byte(s), dst)
}
