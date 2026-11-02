package ytsub

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// WatchMeta 从一个视频的观看页里抠出「发布时间 / 播放量 / 时长」。
//
// 为什么不复用 yt-dlp 的 VideoInfo：那条路要起一个 yt-dlp 进程，实测单条 4.1 秒；
// 而深度回填一次就是 200 条，补一遍得十几分钟。观看页是一次普通 GET：
// 实测 1.29MB（gzip 后约 1/5）、1.5 秒，同样的三个字段都在 JSON 里。
// 所以「批量补时间」走这条路，「用户点开某一条」仍走 yt-dlp（顺带把描述也拿全）。
//
// 解析的是 watch 页里的 microformat（`"publishDate":"2025-04-14T04:15:22-07:00"`），
// 老页面有时只有 `uploadDate`，两个都试。取不到就返回错误，不要瞎猜。
var (
	watchPublishRe  = regexp.MustCompile(`"publishDate":"(\d{4}-\d{2}-\d{2}T[^"]+)"`)
	watchUploadRe   = regexp.MustCompile(`"uploadDate":"(\d{4}-\d{2}-\d{2}T[^"]+)"`)
	watchViewsRe    = regexp.MustCompile(`"viewCount":"(\d+)"`)
	watchLengthRe   = regexp.MustCompile(`"lengthSeconds":"(\d+)"`)
	watchItemDateRe = regexp.MustCompile(`itemprop="datePublished" content="([^"]+)"`)
)

// 会员抢先看（`availability=subscriber_only`）在观看页上的两个语言无关的胎记。
//
// 实测 2gtrgAKIH88（2026-11-02，1.26MB 的观看页）：
//
//	"badges":[{"metadataBadgeRenderer":{"icon":{"iconType":"SPONSORSHIP_STAR"},
//	  "style":"BADGE_STYLE_TYPE_MEMBERS_ONLY","label":"会员抢先观看", …}}]
//	"playabilityStatus":{"status":"UNPLAYABLE","reason":"成为此频道的会员即可观看…",
//	  "errorScreen":{… "offerId":"sponsors_only_video"}}
//
// 认徽章是因为它才是这件事本身（会员抢先看），`offerId` 只是播放器的拒播理由
// （顺手也认它，防止某天徽章被折叠掉）。两个都是英文枚举值，不受 `hl` 影响——
// 上面那句 reason 现在是中文，全因为请求带了 `Accept-Language: zh-CN`，不能拿它当判据。
var (
	watchMembersBadgeRe = regexp.MustCompile(`BADGE_STYLE_TYPE_MEMBERS_ONLY`)
	watchMembersOfferRe = regexp.MustCompile(`"offerId":"sponsors_only_video"`)
)

func WatchMeta(ctx context.Context, client *http.Client, th *social.Throttle, videoID string) (Video, error) {
	body, err := getPage(ctx, client, th, BaseURL+"/watch?v="+videoID+"&hl=en")
	if err != nil {
		return Video{}, err
	}
	v, err := parseWatchMeta(string(body))
	if err != nil {
		return Video{}, err
	}
	v.VideoID = videoID
	return v, nil
}

func parseWatchMeta(page string) (Video, error) {
	raw := firstMatch(page, watchPublishRe)
	if raw == "" {
		raw = firstMatch(page, watchUploadRe)
	}
	if raw == "" {
		raw = firstMatch(page, watchItemDateRe)
	}
	v := Video{DateSource: "watch"}
	if raw != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
		if err != nil {
			return Video{}, social.Upstream("发布时间格式不认识："+raw, err)
		}
		v.PublishedAt = t.UTC()
	}
	// viewCount 在 microformat 里常常是 0（没开放），真正的数字在 videoDetails 里，
	// 所以取第一个非 0 的；时长同理（0 当没拿到）。
	if n := firstPositive(page, watchViewsRe); n > 0 {
		v.Views = n
	}
	if n := firstPositive(page, watchLengthRe); n > 0 {
		v.DurationSec = int(n)
	}
	// 只有时间缺、时长在的页面也要收：卡片上那块「时长」是独立补的
	// （RSS 通道不带时长），这里拿不到时间就别连时长一起丢了。
	// 两样都没有才是真正的「没戏」——视频不存在 / 已被删除 / 被登录墙挡住。
	if v.PublishedAt.IsZero() && v.DurationSec == 0 {
		return Video{}, social.NotFound("观看页里没有发布时间也没有时长（视频可能已删除或需要登录）")
	}
	if strings.Contains(page, `"isLiveContent":true`) {
		v.IsLive = true
	}
	v.MembersOnly = watchMembersBadgeRe.MatchString(page) || watchMembersOfferRe.MatchString(page)
	v.Title = watchTitle(page)
	return v, nil
}

func firstMatch(s string, re *regexp.Regexp) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// firstPositive 取第一个能解析成正整数的匹配（跳过 0 与解析失败）。
func firstPositive(s string, re *regexp.Regexp) int64 {
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// watchTitle 取 <title>，只用来在日志里认出是哪条；空了也无所谓。
func watchTitle(page string) string {
	const open, close = "<title>", "</title>"
	i := strings.Index(page, open)
	if i < 0 {
		return ""
	}
	rest := page[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return htmlUnescape(strings.TrimSuffix(strings.TrimSpace(rest[:j]), " - YouTube"))
}
