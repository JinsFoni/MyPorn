package jobs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JinsFoni/MyPorn/internal/library"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
)

// 图片规格（FR-28）：poster 竖版 2:3，thumb/fanart 16:9。
//
// 这是**上限**，不是保证：底图不够大时按底图能给的最大尺寸出图，绝不放大（见 library.ResizeImage）。
const (
	posterW, posterH = 1000, 1500
	thumbW, thumbH   = 1280, 720
	fanartW, fanartH = 1920, 1080
)

type artworkResult struct {
	poster   string
	thumb    string
	fanart   string
	warnings []string
}

// artSource 一张候选底图（视频自己的一帧 / 站方缩略图 / 艺人头像）。
type artSource struct {
	kind string // frame | thumb | avatar
	path string
	w, h int
}

// widthFor 这张底图在目标比例下不放大能给出的宽度（像素）。
func (s artSource) widthFor(tw, th int) int {
	cw, ch := library.CoverCrop(s.w, s.h, tw, th)
	ow, _ := library.FitWithin(cw, ch, tw, th)
	return ow
}

// areaFor 这张底图在目标尺寸里能画出多少像素（不放大）。
func (s artSource) areaFor(tw, th int) int {
	cw, ch := library.CoverCrop(s.w, s.h, tw, th)
	ow, oh := library.FitWithin(cw, ch, tw, th)
	return ow * oh
}

// pickSource 选底图，两步：
//
//  1. 先过「够清楚」的门槛：不放大能给出的宽度 ≥ minW 的候选里，按 pref 顺序取第一个。
//     顺序是刻意的——站方封面是**设计过的图**（片名、构图都在上面），只要它够大就继续用它，
//     哪怕视频帧更高清：实测把 4K 帧当海报，经常是「某个人在低头看纸」这种跟片名毫无关系的随机瞬间。
//  2. 一个都过不了门槛时，取不放大能画得最大的那张。这时候官方图已经小到没救
//     （YouTube 只有 `hqdefault` 480×360、pornhub 的 `rs:fit:320:180` 拉不到大图），
//     换源拍一帧，比放大 4 倍清楚得多。
func pickSource(sources []artSource, tw, th, minW int, pref ...string) *artSource {
	pick := func(kind string) *artSource {
		for i := range sources {
			if sources[i].kind == kind {
				return &sources[i]
			}
		}
		return nil
	}
	for _, kind := range pref {
		if s := pick(kind); s != nil && s.widthFor(tw, th) >= minW {
			return s
		}
	}
	var best *artSource
	bestArea := -1
	for i := range sources {
		if a := sources[i].areaFor(tw, th); a > bestArea {
			best, bestArea = &sources[i], a
		}
	}
	return best
}

// frameAt 取帧位置：片头 1/4 处（避开黑场和片头字幕），短视频取中段。
func frameAt(durSec int) float64 {
	switch {
	case durSec <= 0:
		return 1
	case durSec <= 4:
		return float64(durSec) / 2
	default:
		return float64(durSec) / 4
	}
}

// writeArtwork 生成 poster/thumb/fanart。任一失败只记警告，不阻塞下载任务。
//
// 底图有三类来源，选法见 pickSource：站方封面（并换成这条片子能给的最大尺寸）、
// 视频自己的一帧、艺人头像。2026-11-02 之前是固定优先级 + 硬放大：
// YouTube 的 `hqdefault` 480×360 直接吹成 1000×1500 的海报，糊到看不出人。
func (r *Runner) writeArtwork(ctx context.Context, video *model.Video, videoPath, dir string, durSec int) artworkResult {
	res := artworkResult{}
	var sources []artSource

	// ① 视频帧（临时文件跟 .thumb.src 一样用点开头，扫库时不会当成图片）
	frameSrc := filepath.Join(dir, ".frame.src.jpg")
	if videoPath != "" {
		if err := r.enc.ExtractFrame(ctx, videoPath, frameAt(durSec), frameSrc); err != nil {
			r.log.Warn("取视频帧当底图失败", "video", video.ID, "err", err)
		} else if w, h, err := r.enc.ImageInfo(ctx, frameSrc); err != nil || w <= 0 {
			r.log.Warn("读视频帧尺寸失败", "video", video.ID, "err", err)
		} else {
			sources = append(sources, artSource{kind: "frame", path: frameSrc, w: w, h: h})
			r.log.Debug("底图：视频帧", "video", video.ID, "size", fmt.Sprintf("%dx%d", w, h))
		}
	}
	defer os.Remove(frameSrc)

	// ② 站方缩略图（先试大图，失败再退回库里存的那个 URL）
	thumbSrc := filepath.Join(dir, ".thumb.src")
	if u := strings.TrimSpace(video.ThumbURL); u != "" {
		client := r.httpClient(video.Platform)
		for _, cand := range biggerThumbVariants(video.Platform, u) {
			if err := fetchImage(ctx, client, cand, refererFor(cand), thumbSrc); err != nil {
				r.log.Debug("缩略图拉取失败", "video", video.ID, "url", cand, "err", err)
				continue
			}
			w, h, err := r.enc.ImageInfo(ctx, thumbSrc)
			if err != nil || w <= 0 {
				r.log.Debug("缩略图尺寸读不出", "video", video.ID, "url", cand, "err", err)
				continue
			}
			sources = append(sources, artSource{kind: "thumb", path: thumbSrc, w: w, h: h})
			r.log.Debug("底图：站方缩略图", "video", video.ID, "url", cand, "size", fmt.Sprintf("%dx%d", w, h))
			break
		}
	}
	defer os.Remove(thumbSrc)

	// ③ 艺人头像（可能来自别站（beeg），客户端（即代理配置）要跟着图床走）
	avatarSrc := filepath.Join(dir, ".avatar.src")
	if len(video.Performers) > 0 && video.Performers[0].ID > 0 {
		if p, err := r.st.GetPerformer(video.Performers[0].ID); err == nil && strings.TrimSpace(p.AvatarURL) != "" {
			avatarClient := r.httpClient(platform.ImageHostPlatform(p.AvatarURL, video.Platform))
			if err := fetchImage(ctx, avatarClient, p.AvatarURL, refererFor(p.AvatarURL), avatarSrc); err != nil {
				r.log.Debug("艺人头像拉取失败", "performer", p.ID, "err", err)
			} else if w, h, err := r.enc.ImageInfo(ctx, avatarSrc); err != nil || w <= 0 {
				r.log.Debug("艺人头像尺寸读不出", "performer", p.ID, "err", err)
			} else {
				sources = append(sources, artSource{kind: "avatar", path: avatarSrc, w: w, h: h})
				r.log.Debug("底图：艺人头像", "performer", p.ID, "size", fmt.Sprintf("%dx%d", w, h))
			}
		}
	}
	defer os.Remove(avatarSrc)

	// 三张图各自挑底图。门槛按「实际显示多大」定：fanart 铺满详情页背景要 960 宽，
	// thumb 是卡片上的 16:9 小图 640 够，poster 在 Emby 里就 300–500px 宽、400 够。
	// 过了门槛就按官方封面优先——实测同一部片子，官方封面（片名/构图都在上面）当背景
	// 比随手拍的一帧好看太多，哪怕帧的像素更多。
	write := func(dst string, tw, th, minW int, pref ...string) string {
		src := pickSource(sources, tw, th, minW, pref...)
		if src == nil {
			return ""
		}
		if err := r.enc.ResizeImage(ctx, src.path, dst, tw, th); err != nil {
			r.log.Warn("生成图片失败", "file", filepath.Base(dst), "from", src.kind, "err", err)
			res.warnings = append(res.warnings, filepath.Base(dst)+" 生成失败")
			return ""
		}
		return dst
	}
	// 竖屏片子（B 站竖版、YouTube Shorts 那种 9:16）反过来：它的一帧本来就是竖的，
	// 裁 2:3 几乎不浪费，而且这块料是满画质的；站方封面却是 16:9 的横图（等于只截了中间一条）。
	posterPref := []string{"avatar", "thumb", "frame"}
	for _, s := range sources {
		if s.kind == "frame" && s.h > s.w {
			posterPref = []string{"avatar", "frame", "thumb"}
			break
		}
	}

	res.fanart = write(filepath.Join(dir, "fanart.jpg"), fanartW, fanartH, fanartW/2, "thumb", "frame", "avatar")
	res.thumb = write(filepath.Join(dir, "thumb.jpg"), thumbW, thumbH, thumbW/2, "thumb", "frame", "avatar")

	// 站方封面**本身就是竖版海报**时（krx18 的 5:7），poster 整张照搬、不裁不放大。
	// 2026-11-05 用户报「krx18 视频下载时的 poster 使用竖版封面图」：krx18 只给 353×500，
	// 过不了 pickSource 那道「不放大能给 400 宽」的门槛，于是被随手拍的一帧顶掉——实测
	// 3355 的 poster.jpg 是一间黑屋子里的两个人，跟片名毫无关系。这类封面是设计过的竖图
	// （韩文片名压在画面上），按下 2:3 裁会从两侧各切掉十来个像素，所以干脆不裁。
	posterDst := filepath.Join(dir, "poster.jpg")
	if cov := portraitCover(sources); cov != nil {
		if err := r.enc.FitImage(ctx, cov.path, posterDst, posterW, posterH); err != nil {
			r.log.Warn("竖版封面出图失败，退回常规海报", "video", video.ID, "err", err)
			res.poster = write(posterDst, posterW, posterH, posterW*2/5, posterPref...)
		} else {
			res.poster = posterDst
			r.log.Debug("底图：竖版站方封面（不裁切）", "video", video.ID, "size", fmt.Sprintf("%dx%d", cov.w, cov.h))
		}
	} else {
		res.poster = write(posterDst, posterW, posterH, posterW*2/5, posterPref...)
	}

	if res.poster == "" && len(res.warnings) == 0 {
		res.warnings = append(res.warnings, "没有可用图片")
	}
	return res
}

// portraitCoverMinW 是「站方竖版封面可以当海报」的最小宽度。
//
// 353×500 是 krx18 唯一给的尺寸（实测它的图床只生成这一档，`-768x1088` / `-1024x1450`
// 这些 WordPress 尺寸一律 404），Emby / 浏览器的海报格子 300–500px 宽，353 够用；
// 再小就到头像级别了，那种不配当海报（会退回常规的「够大就官方图、不够就换一帧」）。
const portraitCoverMinW = 250

// portraitCover 挑出「本身就是竖版海报」的站方封面（竖图、宽 ≥ portraitCoverMinW）。
//
// 只认站方封面（thumb）：视频帧和艺人头像不在此列——头像不是海报，横屏片子的一帧
// 裁成 2:3 才是对的（那正是 ResizeImage 干的事）。
func portraitCover(sources []artSource) *artSource {
	for i := range sources {
		if s := &sources[i]; s.kind == "thumb" && s.h > s.w && s.w >= portraitCoverMinW {
			return s
		}
	}
	return nil
}

// biggerThumbVariants 把库里存的缩略图 URL 换成这条视频能给的更大版本，从大到小排。
//
// 库里存的是列表页用的尺寸（YouTube `hqdefault` 480×360、pornhub `rs:fit:320:180`、
// bilibili `@960w_540h_1c`、xhamster `s(w:1280,h:720)`），拿来撑海报/背景都太小。
// 每个平台的大图 URL 都是改后缀/参数，改完拉失败就算了，最后一定退回原 URL。
func biggerThumbVariants(platformName, rawURL string) []string {
	switch strings.ToLower(platformName) {
	case "youtube":
		// https://i1.ytimg.com/vi/<id>/hqdefault.jpg → maxresdefault(1920×1080) → sddefault(640×480) → hqdefault(480×360)
		if i := strings.LastIndex(rawURL, "/"); i > 0 && strings.HasPrefix(rawURL, "http") {
			base := rawURL[:i+1]
			out := []string{base + "maxresdefault.jpg", base + "sddefault.jpg", rawURL}
			return dedupe(out)
		}
	case "pornhub":
		// .../rs:fit:320:180/vts:315?... → rs:fit:1920:1080
		if out := replaceOnce(rawURL, regexp.MustCompile(`rs:fit:\d+:\d+`), "rs:fit:1920:1080"); out != rawURL {
			return []string{out, rawURL}
		}
	case "bilibili":
		// ...jpg@960w_540h_1c.webp → @1920w_1080h_1c.webp
		if out := replaceOnce(rawURL, regexp.MustCompile(`@\d+w_\d+h`), "@1920w_1080h"); out != rawURL {
			return []string{out, rawURL}
		}
	case "xhamster":
		// .../s(w:1280,h:720),webp/... → s(w:1920,h:1080)
		if out := replaceOnce(rawURL, regexp.MustCompile(`s\(w:\d+,h:\d+\)`), "s(w:1920,h:1080)"); out != rawURL {
			return []string{out, rawURL}
		}
	}
	return []string{rawURL}
}

func replaceOnce(s string, re *regexp.Regexp, repl string) string {
	if loc := re.FindStringIndex(s); loc != nil {
		return s[:loc[0]] + repl + s[loc[1]:]
	}
	return s
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// writeNFOFile 生成 Emby 规范 movie nfo（FR-27）。
func writeNFOFile(path string, video *model.Video, platformName, resolution string, info library.MediaInfo) error {
	var actors []library.Actor
	for _, p := range video.Performers {
		actors = append(actors, library.Actor{Name: p.Name, Role: "Actor"})
	}
	runtimeMin := 0
	if info.DurationSec > 0 {
		runtimeMin = (info.DurationSec + 30) / 60
	}
	plot := library.CleanPlot(video.Description, video.Title)
	if resolution != "" {
		plot = strings.TrimSpace(fmt.Sprintf("%s\n\n画质：%s", plot, resolution))
	}

	meta := library.MovieMeta{
		Title:         video.Title,
		OriginalTitle: video.Title,
		Plot:          plot,
		Premiered:     video.PublishedAt,
		RuntimeMin:    runtimeMin,
		Tags:          video.Tags,
		Studio:        platformName,
		Actors:        actors,
		UniqueIDs: []library.UniqueID{
			{Type: video.Platform, Value: video.PlatformVideoID, Default: true},
		},
		Website: video.URL,
		Maker:   platformName,
	}
	if video.Rating != nil {
		meta.Rating = library.Rating01(*video.Rating)
	}

	body, err := library.RenderNFO(meta)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fetchImage 带 Referer/UA 拉取一张图片到本地文件。
func fetchImage(ctx context.Context, client *http.Client, rawURL, referer, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("图片为空")
	}
	return nil
}

// refererOverrides 按域名后缀指定 Referer：beeg 的图床（externulls）反过来拒绝
// 自家根域的 Referer（https://externulls.com/ 会 403），只认 beeg.com。
var refererOverrides = map[string]string{
	"externulls.com": "https://beeg.com/",
}

// refererFor 用图片域名的主域构造 Referer，绕过常见防盗链。
func refererFor(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for suffix, ref := range refererOverrides {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return ref
		}
	}
	parts := strings.Split(u.Host, ".")
	if len(parts) >= 2 {
		return "https://" + strings.Join(parts[len(parts)-2:], ".") + "/"
	}
	return u.Scheme + "://" + u.Host + "/"
}
