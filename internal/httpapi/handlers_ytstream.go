package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/hls"
	"github.com/mario/myporn/internal/muxcache"
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
//
// Progressive = 这一档不走 HLS/MSE，而是服务端合流成 mp4 给 <video> 原生播。
// 目前只有 2160p 会为 true（AV1 13.5 Mbps 对比 HLS 里唯一那档 VP9 28.2 Mbps，
// 另外还绕开了 MSE 的 4K 解码崩溃，见 ytstream.Track 的注释）。
// 报上来的 Bandwidth/Codecs 也是**实际要播的那条**（AV1），不是 HLS 清单里的 VP9——
// 菜单上写着 28.2 Mbps 却只下 13.5 Mbps，用户会以为哪里算错了。
type ytQualityDTO struct {
	Height      int    `json:"height"`
	Label       string `json:"label"`
	Bandwidth   int    `json:"bandwidth"`
	Codecs      string `json:"codecs"`
	Progressive bool   `json:"progressive"`
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
		// 上游这会儿解析不出来（最常见：YouTube 的 IP 级风控，几分钟内 yt-dlp 拿不到地址），
		// 但已经合好的 4K 成品还在盘上——那跟上游一点关系都没有，别陪它一起失败。
		if q := s.cachedYtQualities(row.VideoID); len(q) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"videoId": row.VideoID, "title": row.Title, "durationSec": row.DurationSec,
				"src": "", "qualities": q, "default": q[0].Height, "expiresAt": "",
				"cachedOnly": true,
			})
			return
		}
		writeSocialErr(w, err)
		return
	}
	qualities := ytQualities(src.variants, src.tracks)
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

// cachedYtQualities 把盘上已经合好的成品折成画质菜单（按高度降序）。
//
// 只用来兜「上游解析失败」那一种情况：菜单里只剩这些原生播放的档，HLS 那条路没有地址。
// 码率/编码不报（成品就是成品，菜单里只需要档位名），前端照旧按 progressive 走 /file。
func (s *Server) cachedYtQualities(videoID string) []ytQualityDTO {
	if s.mux == nil {
		return nil
	}
	prefix := "yt-" + videoID + "-"
	out := []ytQualityDTO{}
	for _, key := range s.mux.KeysWithPrefix(prefix) {
		raw := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "p")
		h, err := strconv.Atoi(raw)
		if err != nil || h <= 0 {
			continue
		}
		out = append(out, ytQualityDTO{Height: h, Label: strconv.Itoa(h) + "p", Progressive: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Height > out[j].Height })
	return out
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
			s.writePrefetched(w, sess, abs, p)
			return
		}
		// 预热正在取同一条地址：等它，别自己再向 YouTube 要一份。
		// 起播时这条连接就是「第一段分片」，两路并发只会把同一根出口带宽掰成两半。
		if p, hit := warm.Wait(r.Context(), abs); hit {
			s.writePrefetched(w, sess, abs, p)
			return
		}
	}
	s.log.Debug("预热未命中，回源", "url", abs)
	s.proxyMedia(w, r, abs, "youtube", sess.Ref)
}

// writePrefetched 把预热取回来的字节发出去（子清单要现改写成会话地址，分片原样发）。
func (s *Server) writePrefetched(w http.ResponseWriter, sess *ytstream.Session, abs string, p ytstream.Prefetched) {
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
}

// ytWarmEnabled 是排查开关：MYPORN_YT_WARM=0 时整个起播预热关掉。
//
// 理由很实际：预热把「子清单 + 头两个分片」提到浏览器开口之前取，4K 出问题时
// 很难分清是「我们预热取回来的字节」还是「浏览器自己那条路径」的锅。
// 关掉再跑一遍就能二分（实测 6 次加载里 2 次失败、关掉预热 3 次失败，
// 两路都会失败 → 不是预热的锅，这才排到无头浏览器的解码上去）。
var ytWarmEnabled = os.Getenv("MYPORN_YT_WARM") != "0"

// warmYtVideo 是「起播预热」：把两条子清单和头两个分片先取回本地。
//
// 本机实测：解析已缓存时点开到出第一帧约 1.6 秒，其中子清单 0.35s、第一个分片 0.9s，
// 都是「浏览器问一句、我们再替它去 YouTube 问一句」的往返。这两步不依赖浏览器，
// 所以提前到「点开弹窗」那一步（比播放器早一秒多）跑，等播放器开口时已经是本地回环。
//
// 取不到就当没预热过（签名过期之类）：不影响播放，只是退回原来的慢路径。
//
// 这里每一条地址都先 Claim 再取：handleYtStreamInfo 和 handleYtStreamMaster 各会
// 开一轮预热（点开弹窗一次、播放器拉清单又一次），不去重就是同一段分片下载两遍。
func (s *Server) warmYtVideo(ctx context.Context, videoID string, v hls.StreamVariant, renditions []hls.Rendition) {
	if !ytWarmEnabled {
		return
	}
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
			if !warm.Claim(raw) {
				// 另一轮预热已经在取这条清单：等它取回来，再从缓存里读地址列表
				p, hit := warm.Wait(ctx, raw)
				if hit {
					segs[i] = firstMediaURLs(raw, p.Body, 2)
				}
				return
			}
			ct, body, err := s.fetchRawMedia(ctx, raw, "youtube")
			if err != nil {
				s.log.Debug("预热失败", "url", raw, "err", err)
				warm.Settle(raw, "", nil)
				return
			}
			s.log.Debug("预热子清单", "bytes", len(body), "url", raw)
			warm.Settle(raw, ct, body)
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
				if !warm.Claim(u) {
					return
				}
				ct, body, err := s.fetchRawMedia(ctx, u, "youtube")
				if err != nil {
					warm.Settle(u, "", nil)
					return
				}
				warm.Settle(u, ct, body)
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
	tracks     []ytstream.Track
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
	out.tracks = up.Tracks

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
	// 顺手清掉已经过期的（每条几百字节，但看过的视频会一直累积）。
	for id, c := range s.ytMaster {
		if time.Now().After(c.expiresAt) {
			delete(s.ytMaster, id)
		}
	}
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
//
// tracks 里有 AV1 直链的那一档改成「原生播放」：码率与编码都按 AV1 报（见 ytQualityDTO）。
// 只给 2160p 开这个口子——那是唯一一档 HLS 只肯给 VP9 28.2 Mbps 的高度；1440p 及以下
// 的 VP9 码率（14.1 Mbps 起）在实测带宽下还能跟得上，走 HLS 少一次合流的等待。
func ytQualities(variants []hls.StreamVariant, tracks []ytstream.Track) []ytQualityDTO {
	best := map[int]hls.StreamVariant{}
	for _, v := range variants {
		if v.Height <= 0 {
			continue
		}
		if cur, ok := best[v.Height]; !ok || v.Bandwidth > cur.Bandwidth {
			best[v.Height] = v
		}
	}
	trackAt := map[int]ytstream.Track{}
	for _, t := range tracks {
		trackAt[t.Height] = t
	}
	out := make([]ytQualityDTO, 0, len(best))
	for h, v := range best {
		q := ytQualityDTO{
			Height: h, Label: strconv.Itoa(h) + "p",
			Bandwidth: v.Bandwidth, Codecs: hls.PickVideoCodec(v.Codecs),
		}
		if t, ok := trackAt[h]; ok && h >= 2160 {
			q.Bandwidth = int(t.Bandwidth)
			q.Codecs = hls.PickVideoCodec(t.Codecs)
			q.Progressive = true
		}
		out = append(out, q)
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

// handleYtFile 是 4K 的原生播放口：把 DASH 的「AV1 画面 + AAC 音轨」合成一份 mp4 再下发。
//
// 为什么不继续用 HLS/MSE（2026-10-29 实测，视频 YFsMnJEbV1U / 321 秒 / 2160p60）：
//   - YouTube 的 HLS 里 2160p 只有 VP9 一档，28.2 Mbps / 1.06 GB。这条流要 3.5 MB/s 才跟得上，
//     而本机单连接实测 3.5~8.4 MB/s —— 缓冲永远攒不起来，1440×900 窗口里 40 秒卡了 13 次；
//   - 同一台机器的 VideoToolbox 在 MSE 上放 VP9 4K 会每 5~30 秒崩一次
//     （PIPELINE_ERROR_DECODE），同一份字节用原生 <video> 却是零错误；
//   - DASH 里有 AV1 2160p（itag 401）：13.5 Mbps / 540 MB，只要一半带宽，原生播放还能硬解。
//
// 于是走 B 站 4K 那条同样的路：ffmpeg -c copy 落盘缓存（`muxcache`），按普通文件发，
// Range/拖动进度条全白拿；第一次要等整条流下完（540 MB，实测 1~3 分钟），之后秒开。
// `?status=1` 立刻回进度 JSON（同 handleVideoRemux），前端轮询到 ready 再把地址交给 <video>。
func (s *Server) handleYtFile(w http.ResponseWriter, r *http.Request) {
	row, err := s.ytVideoByParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "视频 id 不对")
		return
	}
	height := queryInt(r, "q", 0)
	if height <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "缺少画质参数 q")
		return
	}
	if s.mux == nil {
		writeErr(w, http.StatusInternalServerError, "internal", "合流器不可用（需要 ffmpeg）")
		return
	}

	key := fmt.Sprintf("yt-%s-%dp", row.VideoID, height)
	produce := s.ytFileProducer(row, height)
	if r.URL.Query().Get("status") != "" {
		// 上一次失败了：用户再来要一次就是「重试」——把失败记录和解析缓存都丢掉重来，
		// 否则会拿着 5 分钟内的同一条错误、同一批 403 地址再答一次。
		if s.mux.Snapshot(key).State == muxcache.StateFailed {
			s.mux.Clear(key)
			if s.ytStream != nil {
				s.ytStream.Forget(row.VideoID)
			}
		}
		s.writeRemuxStatus(w, key, produce)
		return
	}

	path, err := s.mux.Ensure(r.Context(), key, produce)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// 失败最常见的原因是「这条 CDN 边不给」，换一次解析会换一批节点，值得自动重试一次。
		// （签名没过期也会 403：实测 sn-qja5m5-5b 这类边同一份地址重试几次都是 403。）
		s.log.Warn("YouTube 合流失败，换一批地址重试", "video", row.VideoID, "height", height, "err", err)
		if s.ytStream != nil {
			s.ytStream.Forget(row.VideoID)
		}
		path, err = s.mux.Ensure(r.Context(), key, produce)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		s.log.Warn("YouTube 合流失败", "video", row.VideoID, "height", height, "err", err)
		if errors.Is(err, context.DeadlineExceeded) {
			writeErr(w, http.StatusGatewayTimeout, "mux_timeout",
				"这一档要整条流下载完才能合流，超过时限没下完（多半是 CDN 卡住了）。稍后重试会重新解析一条线路。")
			return
		}
		writeErr(w, http.StatusBadGateway, "mux_failed", "合流失败: "+err.Error())
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		writeErr(w, http.StatusInternalServerError, "internal", "合流产物不见了")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	// ServeFile 负责 Range / If-Range / 206：拖动进度条靠它。
	http.ServeFile(w, r, path)
}

// ytFileProducer 组装这一档的两条流地址。只在真的要合流时才被调用：
// 签名地址几小时就过期，缓存命中时不该再去问 YouTube。
//
// 过期这件事由 ytstream.Manager 兜着：Upstream() 只在 ExpiresAt−1 分钟之前返回缓存，
// 之后重跑一次 yt-dlp。所以这里拿到的地址一定是新的。
func (s *Server) ytFileProducer(row *store.YtVideoRow, height int) func(context.Context) (muxcache.Source, error) {
	return func(ctx context.Context) (muxcache.Source, error) {
		if s.ytStream == nil {
			return muxcache.Source{}, social.Upstream("没有配置 yt-dlp，站内播放用不了", nil)
		}
		up, err := s.ytStream.Upstream(ctx, row.VideoID)
		if err != nil {
			return muxcache.Source{}, err
		}
		for _, t := range up.Tracks {
			if t.Height != height {
				continue
			}
			if t.VideoURL == "" || t.AudioURL == "" {
				break
			}
			return muxcache.Source{
				VideoURL: t.VideoURL,
				AudioURL: t.AudioURL,
				Referer:  "https://www.youtube.com/",
				UA:       mediaUserAgent,
				TotalSec: up.DurationSec,
			}, nil
		}
		return muxcache.Source{}, fmt.Errorf("这一档（%dp）没有 AV1 直链，只能走 HLS 播放", height)
	}
}
