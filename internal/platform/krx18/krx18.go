// Package krx18 是 krx18.com（韩国成人影片站，DooPlay 主题的 WordPress）的适配器。
//
// 它和另外几家不一样：站上**没有艺人主页**——实测整站首页只有 /movies、/genre、/release
// 三种链接（62 + 46 + 5 条），一条 actor/pornstar 都没有，所以「订阅艺人 → 回填影片」
// 那条路在这里走不通。影片是靠**影片页地址**进来的（见 httpapi.handleImportVideo），
// 进来之后照常走 /api/videos/{id}/playback 在线播放。
//
// 站点前面是 Cloudflare 的 managed challenge，只放行浏览器的 TLS 指纹
// （Go 自己的 ClientHello 一律 403，实测见 platform.NewBrowserHTTPClient）。
//
// 播放链路是四跳（2026-11-04 实测，冷启动合计 4.4~5.9s）：
//
//	GET  /movies/{slug}                     1.4~1.9s  影片页：标题/封面/简介/日期/分类都在 HTML + JSON-LD 里
//	POST /wp-admin/admin-ajax.php            0.7~1.4s  action=doo_player_ajax&post={id}&nume=2 → {"embed_url":…}
//	GET  {embed_url}（cdn.loadvid.com）      0.8~1.0s  window.LoadVidConfig = {videoHash, videoToken}
//	POST {loadvid}/videos/resolve-token      0.8~1.2s  拿 token+hash 换回 m3u8 **原文**（不是地址）
//
// 最后一步只给内容不给地址，这正是站内要自己现做一条清单的原因
// （见 platform.SitePlaylistProvider 与 httpapi.handleSitePlaylist）。
//
// m3u8 里是 MPEG-TS 分片（实测 557 段 / 3608.1 秒 / 合计 1.40 GB / 平均 2.5 MB 一段 /
// 每段 6~8 秒），分片地址伪装成 .png，但内容首字节是 0x47（TS 同步字节）；
// **分片不要 Referer、不要特定 UA、CORS 是 \\***，所以只有清单那一侧要过 Cloudflare，
// 分片照走通用 /api/stream 代理。
package krx18

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
)

const (
	defaultBase = "https://krx18.com"

	// userAgent 必须和 platform 里那条 HelloChrome_Auto 成对：指纹是 Chrome、UA 写别的
	// 等于自报假冒，Cloudflare 直接 403。
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"

	// playlistTTL 是「清单 + 分片地址」的缓存时长。
	//
	// 分片地址里既没有签名也没有过期时间：实测同一条分片地址隔两天再取照样 200。
	// 换清单这一步要 4~11 秒（页面 → 播放器 → embed → token 四跳，再拿首段探分辨率），
	// 所以缓存久一点很值——播放器每次起播都会来要一次清单。
	// 30 分钟是「省掉重复的 5 秒」和「万一上游换了存储路径，最多旧 30 分钟」之间的折中：
	// 真换了路径，hls.js 会分片全 502，刷新一次（等 TTL 过）就好。
	playlistTTL = 30 * time.Minute

	// probeBytes 是探测分辨率时从第一段里取的字节数。
	// ffprobe 读到第一个 SPS 就够：实测 1920x1080 H.264 只要 1 MB（一段 2.5 MB 的 40%）。
	probeBytes = 1 << 20
)

// playerServers 是「播放器选项」的尝试顺序（nume 就是页面上的 Server 编号）。
//
// Server 2 = cdn.loadvid.com，新片都在它上面，纯 HTTP 就能跑通，排第一；
// Server 1/3 是 playkrx18.site 的 jwplayer 和 mov18plus.cloud，另外还有老片留下的
// streamango / openload——那些站早就没了，只能当兜底挨个试一遍。
var playerServers = []string{"2", "1", "3"}

// loadvidHost 是目前唯一跑得通的播放器域名（后缀匹配，播放页在 cdn.loadvid.com）。
const loadvidHost = "loadvid.com"

// describePlayer 把「不支持的 embed 站」说成人话。
//
// 实测（2026-11-04，从 /movies、/genre/eng-sub/、/genre/korea/ 抽 18 部最近的片子）：
// 18 部全是 play.playkrx18.site + mov18plus.cloud，一部 loadvid 都没有；用户给的
// office-wifes-affair-2026 是 Server 2 上有 loadvid 的那一类（三部里只有它放得出来）。
// 两家不支持的都不是「少写了个正则」：
//   - play.playkrx18.site 的播放逻辑在 appv1.js（javascript-obfuscator 混淆）里，
//     接口还带 CF Turnstile，无头浏览器直接被挑战页挡住；
//   - mov18plus.cloud 把 media 字段加密了，解出来的 id 指向 player.abyssplayer.com，
//     实测那页只回一个占位视频（bbb_sunflower…）。
//
// 所以这里如实报「用的是哪个播放器、暂不支持」，别让用户对着「解析失败」猜。
func describePlayer(host string) string {
	if dead, ok := deadEmbedders[host]; ok {
		return dead
	}
	if host == "" {
		return "一个认不出的播放器"
	}
	return host + " 播放器（暂不支持在线播放）"
}

// deadEmbedders 是实测**已停服**的播放器域名，值里写一句人话。
//
// 老片还在用它们：2017 年的《Living with My Cousin》三个 Server 分别指向
// streamango / openload / 空——点开只能看到一个永远转圈的 iframe。
// 认出来就直接告诉用户是什么情况，别抛一个「取播放器页面失败」让人猜。
var deadEmbedders = map[string]string{
	"openload.co":      "openload（2019 年已关站）",
	"oload.tv":         "openload（2019 年已关站）",
	"streamango.com":   "streamango（已关站）",
	"streamcherry.com": "streamcherry（已关站）",
}

// errNoPerformerPages 说明这个站没有艺人主页可订阅。
var errNoPerformerPages = errors.New("krx18 只有影片页，没有艺人主页")

// Adapter 实现 platform.Adapter，另外实现 VideoURLResolver 与 SitePlaylistProvider。
type Adapter struct {
	base    string
	client  *http.Client
	ffprobe string

	mu    sync.Mutex
	cache map[string]*cached
}

// cached 是一条影片解析出来的页面元数据 + 清单 + 媒体信息。
type cached struct {
	at       time.Time
	page     *pageInfo
	playlist []byte
	base     *url.URL
	info     mediaInfo
}

// mediaInfo 是从第一段分片里量出来的真实参数。
//
// 清单本身只写 #EXTINF（时长），不写分辨率也不写码率，而档位标签是拿 height 拼的
// （1080p / 4K），量不出 height 界面上就只能显示「0p」。
type mediaInfo struct {
	DurationSec int
	Width       int
	Height      int
	Codec       string
	Bandwidth   int
}

// New 创建适配器。ffprobe 是探测分辨率用的可执行文件（空串按 PATH 找）。
func New(baseURL, ffprobe string) (*Adapter, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBase
	}
	hc, err := platform.NewBrowserHTTPClient(30 * time.Second)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		base:    strings.TrimSuffix(baseURL, "/"),
		client:  hc,
		ffprobe: ffprobe,
		cache:   map[string]*cached{},
	}, nil
}

func (a *Adapter) Key() string     { return "krx18" }
func (a *Adapter) Name() string    { return "krx18" }
func (a *Adapter) BaseURL() string { return a.base }

// HTTPClient 让 /api/stream 取分片也用同一个客户端。
//
// 分片其实不需要指纹伪装（server7.stream-baby1.top 不带 Referer、不带特指 UA 也是 200，
// 还写着 access-control-allow-origin: *），那为什么还用 uTLS 这一个？
// 量过：同一条 2,695,485 B 的分片交替各取 6 次，uTLS 中位数 1.35s / 3.28s（两轮），
// 原生 crypto/tls 中位数 4.26s / 4.30s，都是 HTTP/2.0——uTLS 不慢，两轮都更快一点。
// 既然不慢，就没必要为分片再留一套客户端（还少一个「分片域名哪天开始挑战」的坑）。

func (a *Adapter) HTTPClient() *http.Client { return a.client }

// IsMovieSite 声明这是影片站（见 platform.MovieSite）：没有艺人主页可订阅。
func (a *Adapter) IsMovieSite() bool { return true }

// ParsePerformerURL 永远报错：站上没有艺人主页（见包注释）。
func (a *Adapter) ParsePerformerURL(raw string) (string, error) { return "", errNoPerformerPages }

func (a *Adapter) GetPerformer(context.Context, string) (*model.Performer, error) {
	return nil, errNoPerformerPages
}

func (a *Adapter) ListPerformerVideos(context.Context, string, int) (*platform.Page, error) {
	return nil, errNoPerformerPages
}

func (a *Adapter) SearchPerformers(context.Context, string, int) ([]*model.Performer, bool, error) {
	return nil, false, errNoPerformerPages
}

// ParseVideoURL 从影片页地址里取出 slug：/movies/{slug} 或 /movies/{slug}/。
func (a *Adapter) ParseVideoURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("不是合法地址: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	want := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(a.base, "https://"), "http://"))
	if host != want && host != "www."+want {
		return "", fmt.Errorf("不是 %s 的地址", a.Name())
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "movies" || parts[1] == "" {
		return "", fmt.Errorf("不是影片页地址（应形如 %s/movies/<片名>-<年份>）", a.base)
	}
	return parts[1], nil
}

// GetVideo 只取影片页的元数据（标题、封面、简介、分类、日期、播放量）。
func (a *Adapter) GetVideo(ctx context.Context, videoID string) (*model.Video, error) {
	page, err := a.fetchPage(ctx, videoID)
	if err != nil {
		return nil, err
	}
	return page.video(a), nil
}

// ResolveStreams 解析播放清单，返回这一档（站上只有一档，1080p H.264 ≈ 3 Mbps）。
func (a *Adapter) ResolveStreams(ctx context.Context, videoID string) ([]model.Variant, error) {
	e, err := a.resolve(ctx, videoID)
	if err != nil {
		return nil, err
	}
	return []model.Variant{e.variant()}, nil
}

// ResolvePlayback 一次把元信息和清单都带回来（影片页只取一次）。
func (a *Adapter) ResolvePlayback(ctx context.Context, videoID string) (*model.Video, []model.Variant, error) {
	e, err := a.resolve(ctx, videoID)
	if err != nil {
		return nil, nil, err
	}
	v := e.page.video(a)
	v.DurationSec = e.info.DurationSec
	return v, []model.Variant{e.variant()}, nil
}

// PlaylistPath 是这条片子的站内清单地址（见 httpapi.handleSitePlaylist）。
func (a *Adapter) PlaylistPath(videoID string) string {
	return "/api/" + a.Key() + "/playlist.m3u8?v=" + url.QueryEscape(videoID)
}

// SitePlaylist 返回清单原文与基准地址。
func (a *Adapter) SitePlaylist(ctx context.Context, videoID string) ([]byte, *url.URL, error) {
	e, err := a.resolve(ctx, videoID)
	if err != nil {
		return nil, nil, err
	}
	return e.playlist, e.base, nil
}

// ---------- 解析链路 ----------

func (e *cached) variant() model.Variant {
	// url 留空：清单是站内现做的，由 httpapi.handlePlayback 换成 SitePlaylistProvider 的地址。
	return model.Variant{
		Height:          e.info.Height,
		Width:           e.info.Width,
		Bandwidth:       e.info.Bandwidth,
		Codec:           e.info.Codec,
		ApproxBandwidth: true,
	}
}

func (a *Adapter) resolve(ctx context.Context, slug string) (*cached, error) {
	if a.ffprobe == "" {
		a.ffprobe = "ffprobe"
	}
	if c, ok := a.fromCache(slug); ok {
		return c, nil
	}
	page, err := a.fetchPage(ctx, slug)
	if err != nil {
		return nil, err
	}
	playlist, playlistBase, err := a.fetchPlaylist(ctx, page.PostID, slug)
	if err != nil {
		return nil, err
	}
	segs, dur := parseMediaPlaylist(string(playlist), playlistBase)
	if len(segs) == 0 {
		return nil, errors.New("清单里没有分片")
	}
	info, err := a.probeMedia(ctx, segs[0].URL, segs[0].Dur)
	if err != nil {
		return nil, err
	}
	info.DurationSec = int(dur + 0.5)
	c := &cached{at: time.Now(), page: page, playlist: playlist, base: playlistBase, info: info}
	a.toCache(slug, c)
	return c, nil
}

func (a *Adapter) fromCache(slug string) (*cached, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.cache[slug]
	if !ok || time.Since(c.at) > playlistTTL {
		return nil, false
	}
	return c, true
}

func (a *Adapter) toCache(slug string, c *cached) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cache[slug] = c
}

func (a *Adapter) fetchPage(ctx context.Context, slug string) (*pageInfo, error) {
	pageURL := a.base + "/movies/" + slug
	body, err := a.getText(ctx, pageURL, map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	})
	if err != nil {
		return nil, err
	}
	return parsePage(body, pageURL, slug)
}

// fetchPlaylist 走「影片页 → 播放器选项 → loadvid → 换清单」这四跳。
//
// 播放器不止一个：页面上的「Server 1/2/3」对应不同的 embed 站，老片留下的那几个
// 早就关站了（实测 2017 年的《Living with My Cousin》只有 streamango / openload），
// 所以按 playerServers 的顺序逐个试，谁给出 LoadVidConfig 就用谁。
func (a *Adapter) fetchPlaylist(ctx context.Context, postID, slug string) ([]byte, *url.URL, error) {
	if postID == "" {
		return nil, nil, errors.New("影片页里没找到播放器编号（data-post）")
	}
	var problems []string
	for _, nume := range playerServers {
		embedURL, err := a.playerEmbedURL(ctx, postID, slug, nume)
		if err != nil {
			problems = append(problems, "Server "+nume+"："+err.Error())
			continue
		}
		if embedURL == "" {
			continue
		}
		if host := hostOf(embedURL); !strings.HasSuffix(host, loadvidHost) {
			problems = append(problems, "Server "+nume+" 用的是 "+describePlayer(host))
			continue
		}
		playlist, base, err := a.resolveLoadvid(ctx, embedURL)
		if err != nil {
			problems = append(problems, "Server "+nume+"："+err.Error())
			continue
		}
		return playlist, base, nil
	}
	if len(problems) == 0 {
		return nil, nil, errors.New("影片页上没有可用的播放器")
	}
	return nil, nil, fmt.Errorf("这部片没有能用的播放器（%s）", strings.Join(problems, "；"))
}

// playerEmbedURL 问一次「第 nume 个播放器」的 embed 地址。
//
// 响应有两种形状，都要认：老片回的是 iframe HTML
// （`<iframe src="https://openload.co/embed/…">`），在跑的 Server 2 回的是裸地址。
func (a *Adapter) playerEmbedURL(ctx context.Context, postID, slug, nume string) (string, error) {
	form := url.Values{
		"action": {"doo_player_ajax"},
		"post":   {postID},
		"nume":   {nume},
		"type":   {"movie"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.base+"/wp-admin/admin-ajax.php", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", a.base+"/movies/"+slug+"/")
	body, err := a.do(req)
	if err != nil {
		return "", fmt.Errorf("取播放器地址失败: %w", err)
	}
	var ajax struct {
		EmbedURL string `json:"embed_url"`
	}
	if err := json.Unmarshal(body, &ajax); err != nil {
		return "", fmt.Errorf("播放器地址响应看不懂: %s", truncate(body, 120))
	}
	return embedSrcFrom(ajax.EmbedURL), nil
}

// resolveLoadvid 在 loadvid 的播放器页里拿 token，再 POST 换回清单正文。
func (a *Adapter) resolveLoadvid(ctx context.Context, embedURL string) ([]byte, *url.URL, error) {
	embed, err := a.getText(ctx, embedURL, map[string]string{"Referer": a.base + "/"})
	if err != nil {
		return nil, nil, fmt.Errorf("取播放器页面失败: %w", err)
	}
	cfg, err := parseLoadvidConfig(embed)
	if err != nil {
		return nil, nil, err
	}

	origin := embedOrigin(embedURL)
	payload, _ := json.Marshal(map[string]string{"token": cfg.token, "hash": cfg.hash})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		origin+"/videos/resolve-token", strings.NewReader(string(payload)))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.apple.mpegurl,*/*")
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", embedURL)
	playlist, err := a.do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("换播放清单失败: %w", err)
	}
	if !strings.Contains(string(playlist), "#EXTM3U") {
		return nil, nil, fmt.Errorf("换回来的不是清单: %s", truncate(playlist, 120))
	}
	base, err := url.Parse(embedURL)
	if err != nil {
		return nil, nil, err
	}
	// 主清单（多档）时挑带宽最高那条子清单再取一次；本站在实测里是单档媒体清单，
	// 这条分支只是防止上游哪天改成多档。
	if child := highestChildPlaylist(string(playlist), base); child != nil {
		body, err := a.getText(ctx, child.String(), map[string]string{"Referer": embedURL})
		if err != nil {
			return nil, nil, err
		}
		if !strings.Contains(body, "#EXTM3U") {
			return nil, nil, errors.New("子清单不是 m3u8")
		}
		return []byte(body), child, nil
	}
	return playlist, base, nil
}

// probeMedia 从第一段分片里量出真实的分辨率、编码与码率。
func (a *Adapter) probeMedia(ctx context.Context, seg *url.URL, segDur float64) (mediaInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, seg.String(), nil)
	if err != nil {
		return mediaInfo{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", probeBytes-1))
	resp, err := a.client.Do(req)
	if err != nil {
		return mediaInfo{}, fmt.Errorf("取第一段分片失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return mediaInfo{}, fmt.Errorf("取第一段分片失败: 上游返回 %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, probeBytes))
	if err != nil {
		return mediaInfo{}, err
	}
	total := int64(len(data))
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			if n, perr := strconv.ParseInt(cr[i+1:], 10, 64); perr == nil && n > 0 {
				total = n
			}
		}
	}

	dir, err := os.MkdirTemp("", "krx18-probe")
	if err != nil {
		return mediaInfo{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "seg.ts")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return mediaInfo{}, err
	}
	out, err := exec.CommandContext(ctx, a.ffprobe,
		"-v", "error", "-f", "mpegts", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,codec_name,bit_rate",
		"-of", "json", path).Output()
	if err != nil {
		return mediaInfo{}, fmt.Errorf("ffprobe 量不出分辨率（%s）: %w", a.ffprobe, err)
	}
	var probed struct {
		Streams []struct {
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			CodecName string `json:"codec_name"`
			BitRate   string `json:"bit_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probed); err != nil {
		return mediaInfo{}, err
	}
	if len(probed.Streams) == 0 || probed.Streams[0].Height == 0 {
		return mediaInfo{}, errors.New("ffprobe 没读出视频流")
	}
	s := probed.Streams[0]
	info := mediaInfo{Width: s.Width, Height: s.Height, Codec: codecTag(s.CodecName)}
	if br, perr := strconv.Atoi(s.BitRate); perr == nil && br > 0 {
		info.Bandwidth = br
	}
	if info.Bandwidth == 0 && segDur > 0 {
		// TS 里没有 bit_rate 标签时按「这一段有多大 / 它播多久」估：整条片子是恒定码率，
		// 单段估算的误差在 1% 以内（实测首段 2,695,485 B / 7.207 s = 2.99 Mbps，
		// 整条片子 ffprobe 量到 3.01 Mbps）。
		info.Bandwidth = int(float64(total) * 8 / segDur)
	}
	return info, nil
}

// ---------- HTTP ----------

func (a *Adapter) getText(ctx context.Context, raw string, hdr map[string]string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	body, err := a.do(req)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (a *Adapter) do(req *http.Request) ([]byte, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		// Cloudflare 的挑战页是 403 + 5.6 KB HTML，正文里写着 "Just a moment..."：
		// 认出来就说人话，别让人对着一个状态码猜。
		if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "Just a moment") {
			return nil, errors.New("被 Cloudflare 拦住了（TLS 指纹不被放行）")
		}
		return nil, fmt.Errorf("上游返回 %d: %s", resp.StatusCode, truncate(body, 120))
	}
	return body, nil
}

func embedOrigin(embedURL string) string {
	u, err := url.Parse(embedURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "https://cdn.loadvid.com"
	}
	return u.Scheme + "://" + u.Host
}

// codecTag 把 ffprobe 的 codec_name 归成 manifest 里那种写法（avc1 / hev1 / av01），
// 后面 model.CodecFamily 认的就是这几种。
func codecTag(name string) string {
	switch strings.ToLower(name) {
	case "h264":
		return "avc1"
	case "hevc":
		return "hev1"
	case "av1":
		return "av01"
	default:
		return name
	}
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
