package ytsub

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// StreamInfo 是「在线播放」要的那点东西：一把 HLS master 清单的地址。
//
// 为什么用 HLS 而不是 DASH 直链：YouTube 的 DASH 是音视频分离的两条流，
// 浏览器里一个 <video> 塞不进去；HLS 的清单里直接把音轨列成 rendition、
// 每把变体带一个独立的子清单，hls.js 能自己拼，画质也能一路到 4K。
// 实测（2026.10.26 的 yt-dlp）每条视频都有这份清单，地址在 formats[] 的 manifest_url 上。
type StreamInfo struct {
	VideoID     string
	Title       string
	DurationSec int
	MasterURL   string
	// Tracks 是 DASH 那条线上的「单独一条画面」：4K 走它，理由见 DashTrack。
	Tracks []DashTrack
	// ExpiresAt 是清单地址里的 expire=（签名寿命，实测 6 小时）。
	// 到点后所有分片都会 403，所以会话要跟着这个时间收摊，不能当永久地址缓存。
	ExpiresAt time.Time
}

// DashTrack 是一条「只有画面」的 DASH 直链（加上一条共用的音轨），
// 服务端用 ffmpeg -c copy 合成一份 mp4 再给浏览器原生播。
//
// 为什么 4K 非它不可（2026-10-29 实测，视频 YFsMnJEbV1U / 321 秒）：
//   - HLS 里 2160p 只有 VP9 一档，28.2 Mbps（1.06 GB）——实测单连接 3.5~8.4 MB/s，
//     这条流要 3.5 MB/s 才勉强跟得上，缓冲永远攒不起来，40 秒内卡 13 次；
//   - DASH 里有 AV1 2160p：13.5 Mbps（540 MB），只要一半带宽；
//   - 而且走原生 <video> 就绕开了 MSE：这台机器的 VideoToolbox 在 MSE 上放 VP9 4K
//     会每 5~30 秒崩一次（PIPELINE_ERROR_DECODE），同一份字节用原生播放却零错误。
type DashTrack struct {
	Height      int    // 画面高度（2160 = 4K）
	Codecs      string // av01.0.13M.08 这类 codecs 串
	Bandwidth   int64  // bps
	VideoURL    string
	AudioURL    string // 共用一条音轨（AAC / m4a）
	AudioCodecs string
	// VideoBytes / AudioBytes 是两条流的字节数（yt-dlp 的 filesize/clen）。
	// 有了它才能按字节分段并行下载（单连接被 CDN 限速，见 library.fetchRanges）。
	VideoBytes int64
	AudioBytes int64
}

// streamProbe 只取 -j 输出里用得上的几个字段。
type streamProbe struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Duration *float64       `json:"duration"`
	Formats  []streamFormat `json:"formats"`
}

// streamFormat 是 formats[] 里的一行（只留这里用得上的字段）。
type streamFormat struct {
	FormatID    string  `json:"format_id"`
	Protocol    string  `json:"protocol"`
	ManifestURL string  `json:"manifest_url"`
	Height      int     `json:"height"`
	Ext         string  `json:"ext"`
	VCodec      string  `json:"vcodec"`
	ACodec      string  `json:"acodec"`
	Tbr         float64 `json:"tbr"` // 总码率 kbps（画面那条只有画面）
	Filesize    int64   `json:"filesize"`
	FilesizeEst int64   `json:"filesize_approx"`
	AbR         float64 `json:"abr"` // 音轨码率 kbps
	URL         string  `json:"url"`
}

// size 是 yt-dlp 给的字节数：优先 filesize（clen），没有就退估算值。
func (f streamFormat) size() int64 {
	if f.Filesize > 0 {
		return f.Filesize
	}
	return f.FilesizeEst
}

// fastPlayerClient 是快车道只问的那一个客户端。
//
// yt-dlp 默认会并发问 tv samsung 和 visionos 两家播放器 API，再把两家结果并起来
// （2026-11-01 实测一次解析的阶段耗时：网页 1.00 s、tv samsung 0.71 s、
// visionos 0.54 s、播放器 JS 0.93 s、deno 解 n 挑战 0.52 s、m3u8 0.78 s，合计 4.5 s）。
// 而 visionos 一家就把这里要的东西给全了：HLS 的 manifest_url（628/233 那批 m3u8）、
// AV1 2160p 的 https 直链（401）、AAC 音轨（140）。
//
// 只问一家的实测（同一条视频、同一时段）：visionos 2.42~3.18 s，默认两家 4.5 s；
// 换来的是「点开到出画面」少等 1.5 秒左右。会员专属 / 分区限制 / 儿童视频对某些客户端
// 不可见，所以拿不到清单时退回默认客户端集合再问一次（见 worthDefaultClients）。
const fastPlayerClient = "visionos"

// ResolveStream 解析一条视频的 HLS master 地址。
//
// 特意不带 -f：这里要的是清单，不是某一档格式；带上格式串反而会让 yt-dlp
// 只留被选中的那把，manifest_url 也就跟着没了。
func (y *YtDlp) ResolveStream(ctx context.Context, videoID string) (StreamInfo, error) {
	bin := y.Bin()
	if bin == "" {
		return StreamInfo{}, social.StaleParser("没有找到 yt-dlp：去设置 → YouTube 里填路径，或先装一个（版本越新越好）")
	}
	info, stderr, err := y.resolveStreamWith(ctx, bin, videoID, fastPlayerClient)
	if err == nil && info.MasterURL != "" {
		return info.finish(videoID), nil
	}
	if err != nil && !worthDefaultClients(stderr) {
		// 已经知道这条视频放不了（会员/私有/删除），再等 4.5 秒重问一遍没有意义
		return StreamInfo{}, y.mapError(err, stderr)
	}
	y.log.Debug("快车道没拿到 HLS 清单，退回默认客户端重问一次",
		"videoId", videoID, "master", info.MasterURL != "", "err", err)
	info, stderr, err = y.resolveStreamWith(ctx, bin, videoID, "")
	if err != nil {
		return StreamInfo{}, y.mapError(err, stderr)
	}
	if info.MasterURL == "" {
		return StreamInfo{}, social.Upstream("这条视频没有可用的 HLS 清单（直播/会员专属/分区限制时会出现）", nil)
	}
	return info.finish(videoID), nil
}

// resolveStreamWith 跑一次 `yt-dlp -j`，client 为空就用 yt-dlp 的默认客户端集合。
func (y *YtDlp) resolveStreamWith(ctx context.Context, bin, videoID, client string) (StreamInfo, string, error) {
	// 不带 --no-warnings：yt-dlp 关于「这份 cookie 已经在浏览器里被轮换」的提示是 WARNING，
	// 被压掉之后只剩一句含糊的 `Sign in to confirm you're not a bot`，用户就会去查网络
	//（2026-11-03 就是这么绕了半天的）。多出来的噪音由 mapError 只认 ERROR 行来兜。
	args := []string{"-j", "--no-playlist", "--socket-timeout", "20"}
	args = append(args, y.commonArgs()...)
	// 登录态：这条是「播放」的解析，年龄限制/要人机验证的片子不带 cookie 连清单都拿不到
	//（2026-11-03 的现场，见 addCookies 的注释）。
	args, dropCookies, cerr := y.addCookies(args)
	defer dropCookies()
	if cerr != nil {
		y.log.Debug("写临时 Cookie 失败，这条照旧不带 cookie 跑", "err", cerr)
	}
	if client != "" {
		args = append(args, "--extractor-args", "youtube:player_client="+client)
	}
	args = append(args, BaseURL+"/watch?v="+videoID)

	started := time.Now()
	stdout, stderr, err := y.run(ctx, bin, args...)
	// 解析耗时是「点开到出画面」里最长的一段（实测冷启动 13~16 秒），
	// 想优化起播就得先能看见它到底花在哪一次调用上——所以这行用 Debug 常开着不吵。
	y.log.Debug("yt-dlp 解析完成", "videoId", videoID, "client", clientName(client),
		"cookies", strings.TrimSpace(y.cookieText()) != "",
		"elapsed", time.Since(started).Round(time.Millisecond).String(), "err", err)
	if err != nil {
		return StreamInfo{}, stderr, err
	}
	var p streamProbe
	if err := json.Unmarshal([]byte(stdout), &p); err != nil {
		return StreamInfo{}, stderr, social.StaleParser("yt-dlp 的视频信息解析失败（版本可能过期）：" + err.Error())
	}
	info := StreamInfo{VideoID: videoID, Title: p.Title}
	if p.Duration != nil {
		info.DurationSec = int(*p.Duration + 0.5)
	}
	for _, f := range p.Formats {
		if strings.TrimSpace(f.ManifestURL) == "" {
			continue
		}
		info.MasterURL = f.ManifestURL
		if exp := expireFromURL(f.ManifestURL); !exp.IsZero() {
			info.ExpiresAt = exp
		}
		break
	}
	info.Tracks = dashTracks(p.Formats)
	return info, stderr, nil
}

// finish 补上解析结果的尾巴：寿命读不到就按 30 分钟保守估算。
func (info StreamInfo) finish(videoID string) StreamInfo {
	info.VideoID = videoID
	if info.ExpiresAt.IsZero() {
		// 读不到 expire= 就按 30 分钟保守估算：宁可早一点重新解析，
		// 也不要拿着可能已经作废的签名地址去放分片。
		info.ExpiresAt = time.Now().Add(30 * time.Minute)
	}
	return info
}

// worthDefaultClients 判断「快车道失败」值不值得换默认客户端再问一遍。
//
// 只认「这条视频本来就看不了」的判决词直接放弃（会员专属、私有、已删除、地区限制、
// 人机验证）；其余（yt-dlp 不认这个客户端名、接口返回空、临时 5xx）都再试一次。
func worthDefaultClients(stderr string) bool {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "members-only"), strings.Contains(s, "join this channel"),
		strings.Contains(s, "private video"), strings.Contains(s, "removed by the uploader"),
		strings.Contains(s, "video unavailable"), strings.Contains(s, "is not available in your country"),
		strings.Contains(s, "sign in to confirm"), strings.Contains(s, "not a bot"):
		return false
	}
	return true
}

func clientName(c string) string {
	if c == "" {
		return "默认"
	}
	return c
}

// expireFromURL 从 googlevideo 的清单地址里取寿命（Unix 秒）。
//
// 两处都要看：googlevideo 的清单把 expire 写在**路径**里（/api/manifest/hls_variant/expire/4070908800/…），
// 只有分片地址才把它放进查询串。只认查询串的话会读不到寿命、退化成保守估算，
// 于是每次都得重跑 yt-dlp。
func expireFromURL(raw string) time.Time {
	u, err := url.Parse(raw)
	if err != nil {
		return time.Time{}
	}
	if sec := parseEpoch(u.Query().Get("expire")); sec > 0 {
		return time.Unix(sec, 0)
	}
	parts := strings.Split(u.Path, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "expire" {
			if sec := parseEpoch(parts[i+1]); sec > 0 {
				return time.Unix(sec, 0)
			}
		}
	}
	return time.Time{}
}

func parseEpoch(s string) int64 {
	sec, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || sec <= 0 {
		return 0
	}
	return sec
}

// dashTracks 从 formats[] 里挑出「只有画面」的 mp4 直链，按高度去重（同高度取码率最高那条）。
//
// 只认 AV1 + mp4 + https：AV1 装在 MP4 里能被 VideoToolbox 硬解（实测 4K 60fps 60 秒只掉 2 帧），
// 而 VP9 那条是 webm、26.3 Mbps / 1.06 GB，码率翻倍没有任何好处。
// 音轨只认 AAC（m4a）：要 -c copy 塞进 mp4，opus 那条属于 webm（实测 itag 140：
// mp4a.40.2 / 129 kbps / 5.2 MB，体积可以忽略）。
func dashTracks(formats []streamFormat) []DashTrack {
	audioURL, audioCodecs := "", ""
	bestABR := 0.0
	var audioBytes int64
	for _, f := range formats {
		if !strings.HasPrefix(f.ACodec, "mp4a") || f.VCodec != "none" || f.Ext != "m4a" || f.URL == "" {
			continue
		}
		if f.AbR > bestABR {
			bestABR, audioURL, audioCodecs = f.AbR, f.URL, f.ACodec
			audioBytes = f.size()
		}
	}
	if audioURL == "" {
		return nil // 没有 AAC 音轨就别谈原生播放了，让上层退回 HLS
	}

	byHeight := map[int]DashTrack{}
	for _, f := range formats {
		if !strings.HasPrefix(f.VCodec, "av01") || f.ACodec != "none" || f.Ext != "mp4" {
			continue
		}
		if f.Protocol != "https" || f.URL == "" || f.Height <= 0 {
			continue
		}
		t := DashTrack{
			Height:      f.Height,
			Codecs:      f.VCodec,
			Bandwidth:   int64(f.Tbr * 1000),
			VideoURL:    f.URL,
			AudioURL:    audioURL,
			AudioCodecs: audioCodecs,
			VideoBytes:  f.size(),
			AudioBytes:  audioBytes,
		}
		if prev, ok := byHeight[f.Height]; !ok || t.Bandwidth > prev.Bandwidth {
			byHeight[f.Height] = t
		}
	}
	out := make([]DashTrack, 0, len(byHeight))
	for _, t := range byHeight {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Height > out[j].Height })
	return out
}
