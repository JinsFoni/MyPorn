package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/hls"
	"github.com/mario/myporn/internal/social"
	"github.com/mario/myporn/internal/store"
	"github.com/mario/myporn/internal/ytstream"
)

// YouTube 的「站内在线播放」：为什么不能只靠 iframe（M6 补的那一条）。
//
// 实测（2026-10-27）：内嵌播放器对一批版权音乐视频直接回「此视频不能观看」。
// 用一条只有 iframe 的空白页面复现，不经本应用也一样——是 YouTube 对
// 「嵌入 + 本机出口 IP」这个组合的拒播，改参数救不回来。
//
// 于是换成服务端出流：yt-dlp 解析出 HLS master（含全部画质与独立音轨），
// 我们改写清单、代理分片，浏览器只跟本机说话。代价是自己维护一份地址簿
// （ytstream.Session），好处是画质能到 4K、没有广告、隐私模式下照看不误。

// ytQualityDTO 是画质菜单的一项。同一高度只报一条（取码率最高的那把）。
type ytQualityDTO struct {
	Height    int    `json:"height"`
	Label     string `json:"label"`
	Bandwidth int    `json:"bandwidth"`
	Codecs    string `json:"codecs"`
}

// handleYtStreamInfo 打开播放弹窗时调一次：解析上游 + 报可选画质。
//
// 解析要 2~4 秒（跑一次 yt-dlp），所以结果是缓存在 ytstream.Manager 里的，
// 后面拉清单、切画质都不会再碰 YouTube。
func (s *Server) handleYtStreamInfo(w http.ResponseWriter, r *http.Request) {
	row, err := s.ytVideoByParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "视频 id 不对")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	src, err := s.ytStreamSource(ctx, row)
	if err != nil {
		writeSocialErr(w, err)
		return
	}
	qualities := ytQualities(src.variants)
	best, _ := hls.PickVariant(src.variants, 0)
	writeJSON(w, http.StatusOK, map[string]any{
		"videoId":     row.VideoID,
		"title":       row.Title,
		"durationSec": src.up.DurationSec,
		"src":         fmt.Sprintf("/api/yt/videos/%d/stream.m3u8", row.ID),
		"qualities":   qualities,
		"default":     best.Height,
		"expiresAt":   src.up.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// handleYtStreamMaster 出「只有一档画质」的 master 清单。
//
// 为什么不让 hls.js 自己做 ABR：这里要的是「最高画质」而不是「省带宽」——
// 本机/局域网内带宽不是瓶颈，ABR 起步那一两分钟反而是糊的。
// ?q=1080 可以手动降档；不传就是最高。
func (s *Server) handleYtStreamMaster(w http.ResponseWriter, r *http.Request) {
	row, err := s.ytVideoByParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "视频 id 不对")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	src, err := s.ytStreamSource(ctx, row)
	if err != nil {
		writeSocialErr(w, err)
		return
	}

	maxHeight := queryInt(r, "q", 0)
	variant, ok := hls.PickVariant(src.variants, maxHeight)
	if !ok {
		writeErr(w, http.StatusNotFound, "no_variant", "这条视频没有可播的画质")
		return
	}

	sess := s.ytStream.NewSession(row.VideoID, src.up.ExpiresAt)
	body := buildYtMaster(variant, src.renditions, sess.Ref)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, max-age=10")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// handleYtStreamSeg 是播放会话的取物口：子清单与分片都从这里出去。
//
// 地址簿在服务端，所以浏览器拿不到 YouTube 的签名地址，也伪造不出来——
// 号是会话里登记过的才认。
func (s *Server) handleYtStreamSeg(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "地址编号不对")
		return
	}
	if s.ytStream == nil {
		writeErr(w, http.StatusConflict, "no_stream", "没有可用的播放会话")
		return
	}
	sess, ok := s.ytStream.Session(sid)
	if !ok {
		// 会话过期（签名到期/被淘汰）：让前端重新开一次，而不是放一段 404。
		writeErr(w, http.StatusGone, "session_expired", "播放会话已过期，重新打开一次即可")
		return
	}
	abs, ok := sess.URL(n)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "没有这个地址")
		return
	}
	s.proxyMedia(w, r, abs, "youtube", sess.Ref)
}

// ytStreamSource 是「视频行 + 上游清单解析结果」。
type ytStreamSource struct {
	row        *store.YtVideoRow
	up         ytstream.Upstream
	variants   []hls.StreamVariant
	renditions []hls.Rendition
}

// ytStreamSource 解析一条视频的上游清单并解析出变体/音轨。
func (s *Server) ytStreamSource(ctx context.Context, row *store.YtVideoRow) (ytStreamSource, error) {
	out := ytStreamSource{row: row}
	if s.ytStream == nil {
		return out, social.Upstream("没有配置 yt-dlp，站内播放用不了", nil)
	}
	up, err := s.ytStream.Upstream(ctx, row.VideoID)
	if err != nil {
		return out, err
	}
	out.up = up
	body, base, err := s.fetchPlaylist(ctx, up.MasterURL)
	if err != nil {
		return out, err
	}
	out.variants, out.renditions = hls.ParseMaster(string(body), base.String())
	if len(out.variants) == 0 {
		return out, social.Upstream("YouTube 返回的清单里没有可用的画质", nil)
	}
	return out, nil
}

// fetchPlaylist 取一份清单原文（走同一个出网客户端：代理设置改完立刻生效）。
func (s *Server) fetchPlaylist(ctx context.Context, raw string) ([]byte, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !streamHostAllowed(u.Hostname()) {
		return nil, nil, social.Upstream("清单地址不合法", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, social.Upstream("清单地址不合法", err)
	}
	req.Header.Set("User-Agent", mediaUserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := s.streamClient("youtube").Do(req)
	if err != nil {
		return nil, nil, social.Upstream("取清单失败: "+err.Error(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, nil, social.Upstream("清单返回 "+strconv.Itoa(resp.StatusCode), nil)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, nil, social.Upstream("读取清单失败", err)
	}
	return data, u, nil
}

// buildYtMaster 拼一份最小可用的 master：一条变体 + 它的音轨。
//
// 原始清单里有二三十条变体（每种编码 × 每档分辨率）和两条音轨，
// 全搬过去只会让 hls.js 在 ABR 里挑来挑去；这里只留「选中的那条」。
func buildYtMaster(v hls.StreamVariant, renditions []hls.Rendition, ref refMapper) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-INDEPENDENT-SEGMENTS\n")

	audioGroup := ""
	if audio, ok := hls.AudioFor(renditions, v.AudioGroup); ok {
		// 组名统一改成 aud：原组名是 YouTube 的 itag（233/234），只是内部编号，
		// 顺手换掉以后这份清单里就不会再带 YouTube 的痕迹。
		audioGroup = "aud"
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=%q,NAME=%q,DEFAULT=YES,AUTOSELECT=YES,URI=%q\n",
			audioGroup, firstNonEmptyStr(audio.Name, "Default"), ref(audio.URI))
	}

	inf := fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d", v.Bandwidth)
	if v.Codecs != "" {
		inf += fmt.Sprintf(",CODECS=%q", v.Codecs)
	}
	if v.Width > 0 && v.Height > 0 {
		inf += fmt.Sprintf(",RESOLUTION=%dx%d", v.Width, v.Height)
	}
	if audioGroup != "" {
		inf += fmt.Sprintf(",AUDIO=%q", audioGroup)
	}
	b.WriteString(inf + "\n")
	b.WriteString(ref(v.URL) + "\n")
	return b.String()
}

// ytQualities 把变体折成画质菜单（同一高度只留码率最高的那把，按高度降序）。
func ytQualities(variants []hls.StreamVariant) []ytQualityDTO {
	best := map[int]hls.StreamVariant{}
	for _, v := range variants {
		if v.Height <= 0 {
			continue
		}
		if cur, ok := best[v.Height]; !ok || v.Bandwidth > cur.Bandwidth {
			best[v.Height] = v
		}
	}
	out := make([]ytQualityDTO, 0, len(best))
	for h, v := range best {
		out = append(out, ytQualityDTO{
			Height: h, Label: strconv.Itoa(h) + "p",
			Bandwidth: v.Bandwidth, Codecs: hls.PickVideoCodec(v.Codecs),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Height > out[j].Height })
	return out
}

// ytVideoByParam 从路由上取视频行。
func (s *Server) ytVideoByParam(r *http.Request) (*store.YtVideoRow, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return nil, err
	}
	return s.st.GetYtVideo(id)
}
