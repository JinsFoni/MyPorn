package ytsub

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mario/myporn/internal/social"
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
	// ExpiresAt 是清单地址里的 expire=（签名寿命，实测 6 小时）。
	// 到点后所有分片都会 403，所以会话要跟着这个时间收摊，不能当永久地址缓存。
	ExpiresAt time.Time
}

// streamProbe 只取 -j 输出里用得上的几个字段。
type streamProbe struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Duration *float64 `json:"duration"`
	Formats  []struct {
		FormatID    string `json:"format_id"`
		Protocol    string `json:"protocol"`
		ManifestURL string `json:"manifest_url"`
		Height      int    `json:"height"`
	} `json:"formats"`
}

// ResolveStream 解析一条视频的 HLS master 地址。
//
// 特意不带 -f：这里要的是清单，不是某一档格式；带上格式串反而会让 yt-dlp
// 只留被选中的那把，manifest_url 也就跟着没了。
func (y *YtDlp) ResolveStream(ctx context.Context, videoID string) (StreamInfo, error) {
	bin := y.Bin()
	if bin == "" {
		return StreamInfo{}, social.StaleParser("没有找到 yt-dlp：去设置 → YouTube 里填路径，或先装一个（版本越新越好）")
	}
	args := []string{"-j", "--no-playlist", "--no-warnings", "--socket-timeout", "20"}
	args = append(args, y.commonArgs()...)
	args = append(args, BaseURL+"/watch?v="+videoID)

	stdout, stderr, err := y.run(ctx, bin, args...)
	if err != nil {
		return StreamInfo{}, y.mapError(err, stderr)
	}
	var p streamProbe
	if err := json.Unmarshal([]byte(stdout), &p); err != nil {
		return StreamInfo{}, social.StaleParser("yt-dlp 的视频信息解析失败（版本可能过期）：" + err.Error())
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
	if info.MasterURL == "" {
		return StreamInfo{}, social.Upstream("这条视频没有可用的 HLS 清单（直播/会员专属/分区限制时会出现）", nil)
	}
	if info.ExpiresAt.IsZero() {
		// 读不到 expire= 就按 30 分钟保守估算：宁可早一点重新解析，
		// 也不要拿着可能已经作废的签名地址去放分片。
		info.ExpiresAt = time.Now().Add(30 * time.Minute)
	}
	return info, nil
}

// expireFromURL 从 googlevideo 的清单地址里取寿命（Unix 秒）。
//
// 两处都要看：googlevideo 的清单把 expire 写在**路径**里（/api/manifest/hls_variant/expire/1793159341/…），
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
