package jobs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/JinsFoni/MyPorn/internal/library"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
)

// 图片规格（FR-28）：poster 竖版 2:3，thumb/fanart 16:9。
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

// writeArtwork 生成 poster/thumb/fanart。任一失败只记警告，不阻塞下载任务。
func (r *Runner) writeArtwork(ctx context.Context, video *model.Video, dir string) artworkResult {
	res := artworkResult{}
	client := r.httpClient(video.Platform)

	thumbSrc := filepath.Join(dir, ".thumb.src")
	hasThumb := false
	if u := strings.TrimSpace(video.ThumbURL); u != "" {
		if err := fetchImage(ctx, client, u, refererFor(u), thumbSrc); err != nil {
			res.warnings = append(res.warnings, "缩略图下载失败")
			r.log.Warn("下载缩略图失败", "video", video.ID, "err", err)
		} else {
			hasThumb = true
		}
	}

	avatarSrc := filepath.Join(dir, ".avatar.src")
	hasAvatar := false
	if len(video.Performers) > 0 && video.Performers[0].ID > 0 {
		if p, err := r.st.GetPerformer(video.Performers[0].ID); err == nil && strings.TrimSpace(p.AvatarURL) != "" {
			// 头像可能来自别站（beeg），客户端（即代理配置）要跟着图床走。
			avatarClient := r.httpClient(platform.ImageHostPlatform(p.AvatarURL, video.Platform))
			if err := fetchImage(ctx, avatarClient, p.AvatarURL, refererFor(p.AvatarURL), avatarSrc); err != nil {
				r.log.Warn("下载艺人头像失败", "performer", p.ID, "err", err)
			} else {
				hasAvatar = true
			}
		}
	}
	defer os.Remove(thumbSrc)
	defer os.Remove(avatarSrc)

	if hasThumb {
		if p := filepath.Join(dir, "fanart.jpg"); r.enc.ResizeImage(ctx, thumbSrc, p, fanartW, fanartH) == nil {
			res.fanart = p
		} else {
			res.warnings = append(res.warnings, "fanart 生成失败")
		}
		if p := filepath.Join(dir, "thumb.jpg"); r.enc.ResizeImage(ctx, thumbSrc, p, thumbW, thumbH) == nil {
			res.thumb = p
		} else {
			res.warnings = append(res.warnings, "thumb 生成失败")
		}
	}

	// poster 优先用艺人头像（更接近竖版海报），否则从 16:9 主图居中裁切。
	posterSrc := ""
	switch {
	case hasAvatar:
		posterSrc = avatarSrc
	case hasThumb:
		posterSrc = thumbSrc
	}
	if posterSrc != "" {
		p := filepath.Join(dir, "poster.jpg")
		if err := r.enc.ResizeImage(ctx, posterSrc, p, posterW, posterH); err == nil {
			res.poster = p
		} else {
			res.warnings = append(res.warnings, "poster 生成失败")
		}
	}
	if res.poster == "" && res.warnings == nil {
		res.warnings = append(res.warnings, "没有可用图片")
	}
	return res
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
