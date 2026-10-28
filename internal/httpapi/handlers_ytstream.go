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
	"sync"
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
	// 这里比播放器开口早一秒多（前端要等这个响应才渲染播放器），正好用来预热。
	go s.warmYtVideo(context.WithoutCancel(r.Context()), row.VideoID, best, src.renditions)
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

	// 换画质 / 直接拉清单时也预热一遍（已经取过的地址在预热缓存里不会重复取）。
	go s.warmYtVideo(context.WithoutCancel(r.Context()), row.VideoID, variant, src.renditions)
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
	s.serveYtSeg(w, r, sess, abs)
}

// serveYtSeg 出一段地址：预热缓存里有就直接给，没有才去 YouTube 取。
//
// 子清单在缓存里存的是原文，这里现改写成会话地址——发号是会话的事，预热只负责字节。
func (s *Server) serveYtSeg(w http.ResponseWriter, r *http.Request, sess *ytstream.Session, abs string) {
	if warm, ok := s.ytStream.WarmFor(sess.VideoID); ok {
		if p, hit := warm.Get(abs); hit {
			if u, err := url.Parse(abs); err == nil && isHLSPlaylist(u, p.ContentType) {
				out := rewritePlaylist(p.Body, u, "youtube", sess.Ref)
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				w.Header().Set("Cache-Control", "private, max-age=30")
				_, _ = w.Write(out)
				return
			}
			s.log.Debug("预热命中", "url", abs, "bytes", len(p.Body))
			w.Header().Set("Content-Type", p.ContentType)
			w.Header().Set("Cache-Control", "private, max-age=600")
			w.Header().Set("Content-Length", strconv.Itoa(len(p.Body)))
			_, _ = w.Write(p.Body)
			return
		}
	}
	s.log.Debug("预热未命中，回源", "url", abs)
	s.proxyMedia(w, r, abs, "youtube", sess.Ref)
}

// warmYtVideo 是「起播预热」：把两条子清单和头两个分片先取回本地。
//
// 本机实测：解析已缓存时点开到出第一帧约 1.6 秒，其中子清单 0.35s、第一个分片 0.9s，
// 都是「浏览器问一句、我们再替它去 YouTube 问一句」的往返。这两步不依赖浏览器，
// 所以提前到「点开弹窗」那一步（比播放器早一秒多）跑，等播放器开口时已经是本地回环。
//
// 取不到就当没预热过（签名过期之类）：不影响播放，只是退回原来的慢路径。
func (s *Server) warmYtVideo(ctx context.Context, videoID string, v hls.StreamVariant, renditions []hls.Rendition) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	warm := s.ytStream.Warm(videoID)

	playlists := []string{v.URL}
	if audio, ok := hls.AudioFor(renditions, v.AudioGroup); ok {
		playlists = append(playlists, audio.URI)
	}

	// 两条子清单并行取（音轨那边小得多，一起发就行）。
	segs := make([][]string, len(playlists))
	var wg sync.WaitGroup
	for i, raw := range playlists {
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			ct, body, err := s.fetchRawMedia(ctx, raw, "youtube")
			if err != nil {
				s.log.Debug("预热失败", "url", raw, "err", err)
				return
			}
			s.log.Debug("预热子清单", "bytes", len(body), "url", raw)
			warm.Put(raw, ct, body)
			segs[i] = firstMediaURLs(raw, body, 2)
		}(i, raw)
	}
	wg.Wait()

	// 每份清单的头两个（init 段 + 首个分片）：没有它们 hls.js 出不了画面。
	var segWg sync.WaitGroup
	for _, list := range segs {
		for _, u := range list {
			segWg.Add(1)
			go func(u string) {
				defer segWg.Done()
				ct, body, err := s.fetchRawMedia(ctx, u, "youtube")
				if err != nil {
					return
				}
				warm.Put(u, ct, body)
			}(u)
		}
	}
	segWg.Wait()
}

// firstMediaURLs 从子清单原文里取前 n 个媒体地址（相对地址按清单地址展开）。
// 只认分片行和 #EXT-X-MAP 的 URI，别的属性行一律不看。
func firstMediaURLs(playlistURL string, body []byte, n int) []string {
	base, err := url.Parse(playlistURL)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var ref string
		if strings.HasPrefix(trimmed, "#") {
			if !strings.HasPrefix(trimmed, "#EXT-X-MAP") {
				continue
			}
			if i := strings.Index(trimmed, `URI="`); i >= 0 {
				rest := trimmed[i+len(`URI="`):]
				if j := strings.Index(rest, `"`); j >= 0 {
					ref = rest[:j]
				}
			}
		} else {
			ref = trimmed
		}
		if ref == "" {
			continue
		}
		abs, err := base.Parse(ref)
		if err != nil {
			continue
		}
		out = append(out, abs.String())
		if len(out) >= n {
			break
		}
	}
	return out
}

// ytStreamSource 是「视频行 + 上游清单解析结果」。
type ytStreamSource struct {
	row        *store.YtVideoRow
	up         ytstream.Upstream
	variants   []hls.StreamVariant
	renditions []hls.Rendition
}

// cachedYtSource 是一条已解析的 master 清单（变体 + 音轨），按视频 id 缓存到签名到期。
//
// 为什么非要缓存这一层：manifest.googlevideo.com 每被请求一次就会重新签一遍子清单地址
// （sig/lsig 每次都不一样）。之前每次请求都重新拉 master，结果「预热取回来的子清单地址」
// 和「播放器手里那份 master 里的地址」是两个不同的字符串，缓存永远命不中，
// 白白多跑一趟——而且看起来就像预热根本没生效。
type cachedYtSource struct {
	masterURL  string
	variants   []hls.StreamVariant
	renditions []hls.Rendition
	expiresAt  time.Time
}

// ytStreamSource 解析一条视频的上游清单并解析出变体/音轨（同一份解析结果复用）。
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

	s.ytMasterMu.Lock()
	cached, hit := s.ytMaster[row.VideoID]
	s.ytMasterMu.Unlock()
	if hit && cached.masterURL == up.MasterURL && time.Now().Before(cached.expiresAt) {
		out.variants, out.renditions = cached.variants, cached.renditions
		return out, nil
	}

	body, base, err := s.fetchPlaylist(ctx, up.MasterURL)
	if err != nil {
		return out, err
	}
	out.variants, out.renditions = hls.ParseMaster(string(body), base.String())
	if len(out.variants) == 0 {
		return out, social.Upstream("YouTube 返回的清单里没有可用的画质", nil)
	}
	s.ytMasterMu.Lock()
	s.ytMaster[row.VideoID] = cachedYtSource{
		masterURL:  up.MasterURL,
		variants:   out.variants,
		renditions: out.renditions,
		expiresAt:  up.ExpiresAt.Add(-time.Minute),
	}
	s.ytMasterMu.Unlock()
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
