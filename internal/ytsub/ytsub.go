// Package ytsub 是 YouTube 订阅的抓取侧：频道解析、RSS 增量、以及给 yt-dlp 的封装。
//
// 为什么不复用 internal/social 的 Adapter：那边的契约是「博主 → 帖子 → 照片」，
// 一等公民是帖子；YouTube 的一等公民是「频道 → 视频」，而且视频还要交给 yt-dlp 下载、
// 再进本地库。硬套 Adapter 只会让两个包互相污染。
// 复用的是失败语义（internal/social 的 Error / KindOf）与限速器（social.Throttle）——
// 「等一会儿」「换 IP」「升级解析器」这三种脸在界面上必须长得一样。
//
// 两条通道的实测差异（docs/M6-YOUTUBE.md §11）：
//   - RSS：免登录、1 次请求、只给最近 15 条，**有时间没时长**；
//   - yt-dlp flat：能一次列 200 条（5.8 秒）、**有时长没时间**。
//
// 所以两边写同一张表，谁有哪个字段谁就补哪个。
package ytsub

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// BaseURL 便于测试替换。
const BaseURL = "https://www.youtube.com"

// feedURL 按频道 id 取 RSS。
func feedURL(channelID string) string {
	return BaseURL + "/feeds/videos.xml?channel_id=" + channelID
}

// Channel 是一个频道的基本资料。
type Channel struct {
	ChannelID  string
	Handle     string // 不带 @，可能为空
	Title      string
	AvatarURL  string
	ProfileURL string
}

// Video 是一条视频。两条通道都会填这个结构，缺的字段留零值。
type Video struct {
	VideoID      string
	Title        string
	Description  string
	ThumbnailURL string
	PublishedAt  time.Time // 零值 = 不知道（flat 模式）
	DateSource   string    // rss / ytdlp / ""
	Views        int64
	DurationSec  int
	IsLive       bool
	// MembersOnly = 会员抢先看（`availability=subscriber_only`）：加入该频道会员才能看，
	// 我们抓不到也不抓（用户 2026-11-02 的要求）。两条通道都能认出来：
	// flat 列表给 `availability`，观看页给 `BADGE_STYLE_TYPE_MEMBERS_ONLY` 徽章
	// 或 `"offerId":"sponsors_only_video"`（见 watch.go）。RSS 认不出——它压根不列这类视频
	// （实测 2gtrgAKIH88 发布于 14:13、比 feed 里最新几条都新，却不在那 15 条里）。
	MembersOnly bool
}

// ThumbURL 拼封面地址。YouTube 的封面是纯 URL 规则、不看接口返回：
// 卡片用 mqdefault（320×180，16:9 无黑边），大图/海报用 maxresdefault。
func ThumbURL(videoID, name string) string {
	if name == "" {
		name = "mqdefault"
	}
	return "https://i.ytimg.com/vi/" + videoID + "/" + name + ".jpg"
}

// ---- 频道引用解析 ----

// RefKind 是贴进来的东西属于哪一类。
type RefKind int

const (
	RefChannelID RefKind = iota // UC…
	RefHandle                   // @handle
	RefLegacy                   // /c/xxx、/user/xxx 这类老式自定义地址
	RefVideo                    // 单条视频链接，要反查它的频道
)

// ChannelRef 是解析出来的引用，还没联网。
type ChannelRef struct {
	Kind      RefKind
	ChannelID string
	Handle    string
	URL       string // RefLegacy / RefVideo 时用来抓页面
	VideoID   string
}

var (
	ucRe     = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	handleRe = regexp.MustCompile(`^@?([A-Za-z0-9._-]{2,60})$`)
)

// ParseChannelRef 把用户贴进来的字符串归一成引用。不联网、不校验存在性。
func ParseChannelRef(raw string) (*ChannelRef, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, social.NotFound("没有拿到链接或频道 id")
	}
	// 裸 id / 裸 handle
	if ucRe.MatchString(s) {
		return &ChannelRef{Kind: RefChannelID, ChannelID: s}, nil
	}
	if strings.HasPrefix(s, "@") {
		if m := handleRe.FindStringSubmatch(s); m != nil {
			return &ChannelRef{Kind: RefHandle, Handle: strings.ToLower(m[1])}, nil
		}
		return nil, social.NotFound("看起来不是有效的频道 @名")
	}
	if !strings.Contains(s, "youtube.com") && !strings.Contains(s, "youtu.be") {
		// 允许直接贴 handle（不带 @）
		if m := handleRe.FindStringSubmatch(s); m != nil && !strings.ContainsAny(s, "/:. ") {
			return &ChannelRef{Kind: RefHandle, Handle: strings.ToLower(m[1])}, nil
		}
		return nil, social.NotFound("这不是 YouTube 的链接或频道 id")
	}

	u := s
	if !strings.HasPrefix(u, "http") {
		u = "https://" + u
	}
	path := u
	if i := strings.Index(u, "://"); i >= 0 {
		path = u[i+3:]
	}
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	host := path
	if i := strings.Index(path, "/"); i >= 0 {
		host, path = path[:i], path[i:]
	} else {
		path = "/"
	}

	// 单条视频：/watch?v=ID 或 youtu.be/ID
	if strings.HasSuffix(host, "youtu.be") {
		id := strings.Trim(path, "/")
		if id != "" {
			return &ChannelRef{Kind: RefVideo, VideoID: id, URL: "https://www.youtube.com/watch?v=" + id}, nil
		}
	}
	if q := queryValue(u, "v"); q != "" && strings.Contains(host, "youtube.com") {
		return &ChannelRef{Kind: RefVideo, VideoID: q, URL: "https://www.youtube.com/watch?v=" + q}, nil
	}

	switch {
	case strings.HasPrefix(path, "/channel/"):
		id := strings.Trim(strings.TrimPrefix(path, "/channel/"), "/")
		if ucRe.MatchString(id) {
			return &ChannelRef{Kind: RefChannelID, ChannelID: id}, nil
		}
		return nil, social.NotFound("频道地址里的 id 看起来不对")
	case strings.HasPrefix(path, "/@"):
		seg := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
		handle := strings.TrimPrefix(seg, "@")
		if !handleRe.MatchString(seg) {
			return nil, social.NotFound("看起来不是有效的频道 @名")
		}
		return &ChannelRef{Kind: RefHandle, Handle: strings.ToLower(handle), URL: BaseURL + "/@" + handle}, nil
	case strings.HasPrefix(path, "/c/"), strings.HasPrefix(path, "/user/"):
		// /c/xxx、/user/xxx：老式地址，抓页面让它自己跳转到 @handle
		segs := strings.Split(strings.Trim(path, "/"), "/")
		if len(segs) < 2 || segs[1] == "" {
			return nil, social.NotFound("这个老式频道地址不完整")
		}
		return &ChannelRef{
			Kind:   RefLegacy,
			Handle: strings.ToLower(segs[1]),
			URL:    BaseURL + "/" + segs[0] + "/" + segs[1],
		}, nil
	}
	return nil, social.NotFound("没认出这个 YouTube 地址的形态")
}

func queryValue(raw, key string) string {
	i := strings.Index(raw, "?")
	if i < 0 {
		return ""
	}
	for _, kv := range strings.Split(raw[i+1:], "&") {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return ""
}

// ---- RSS ----

type atomFeed struct {
	Title     string `xml:"title"`
	ChannelID string `xml:"http://www.youtube.com/xml/schemas/2015 channelId"`
	Author    struct {
		Name string `xml:"name"`
		URI  string `xml:"uri"`
	} `xml:"author"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	VideoID   string `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
	Title     string `xml:"title"`
	Published string `xml:"published"`
	Group     struct {
		Title       string `xml:"title"`
		Description string `xml:"description"`
		Thumbnail   struct {
			URL string `xml:"url,attr"`
		} `xml:"thumbnail"`
		Community struct {
			Statistics struct {
				Views int64 `xml:"views,attr"`
			} `xml:"statistics"`
		} `xml:"community"`
	} `xml:"http://search.yahoo.com/mrss/ group"`
}

// ParseFeed 解析 RSS。返回频道资料与最近 15 条视频（RSS 本身的全部内容）。
func ParseFeed(body []byte) (*Channel, []Video, error) {
	var f atomFeed
	if err := xml.Unmarshal(body, &f); err != nil {
		return nil, nil, social.StaleParser("YouTube RSS 解析失败：" + err.Error())
	}
	if f.ChannelID == "" && len(f.Entries) == 0 {
		return nil, nil, social.StaleParser("YouTube RSS 里没有频道信息，格式可能变了")
	}
	// RSS 里的 <yt:channelId> 是去掉 UC 前缀的裸 id（实测：UCq-… 在 feed 里是 q-…），
	// 而频道页、播放列表地址用的都是带 UC 的完整 id，这里统一补回去。
	cid := f.ChannelID
	if cid != "" && !ucRe.MatchString(cid) && len(cid) == 22 {
		cid = "UC" + cid
	}
	ch := &Channel{
		ChannelID:  cid,
		Title:      strings.TrimSpace(f.Title),
		ProfileURL: BaseURL + "/channel/" + cid,
	}
	videos := make([]Video, 0, len(f.Entries))
	for _, e := range f.Entries {
		if e.VideoID == "" {
			continue
		}
		v := Video{
			VideoID:      e.VideoID,
			Title:        firstNonEmpty(e.Group.Title, e.Title),
			Description:  e.Group.Description,
			ThumbnailURL: firstNonEmpty(e.Group.Thumbnail.URL, ThumbURL(e.VideoID, "hqdefault")),
			Views:        e.Group.Community.Statistics.Views,
			DateSource:   "rss",
		}
		if t, err := time.Parse(time.RFC3339, e.Published); err == nil {
			v.PublishedAt = t
		}
		videos = append(videos, v)
	}
	return ch, videos, nil
}

// FetchFeed 取一个频道的 RSS。
func FetchFeed(ctx context.Context, client *http.Client, th *social.Throttle, channelID string) (*Channel, []Video, error) {
	if th != nil {
		if err := th.Wait(ctx); err != nil {
			return nil, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL(channelID), nil)
	if err != nil {
		return nil, nil, social.Upstream("构造请求失败", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, social.Upstream("请求 YouTube RSS 失败", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, social.Upstream("读取 YouTube RSS 失败", err)
	}
	if err := httpErr(resp, body, th); err != nil {
		return nil, nil, err
	}
	ch, videos, err := ParseFeed(body)
	if err != nil {
		return nil, nil, err
	}
	if len(videos) == 0 {
		return nil, nil, social.NotFound("这个频道没有视频（或只有直播/会员内容）")
	}
	return ch, videos, nil
}

// ---- 页面解析（频道 id）----

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

var (
	externalIDRe = regexp.MustCompile(`"externalId":"(UC[A-Za-z0-9_-]{22})"`)
	canonicalRe  = regexp.MustCompile(`<link rel="canonical" href="https://www\.youtube\.com/channel/(UC[A-Za-z0-9_-]{22})"`)
	videoChanRe  = regexp.MustCompile(`"channelId":"(UC[A-Za-z0-9_-]{22})"`)
	ogTitleRe    = regexp.MustCompile(`<meta property="og:title" content="([^"]*)"`)
	ogImageRe    = regexp.MustCompile(`<meta property="og:image" content="([^"]*)"`)
)

// ResolveChannel 把任意形态的引用解析成一个真实频道（联网）。
//
// 频道页 HTML 里同时存在很多别的 `"channelId":"UC…"`（推荐位），
// 所以只认 `"externalId"` 与 canonical 两个位置——这是 M6 §11.2 记下的坑。
func ResolveChannel(ctx context.Context, client *http.Client, th *social.Throttle, raw string) (*Channel, error) {
	ref, err := ParseChannelRef(raw)
	if err != nil {
		return nil, err
	}
	if ref.Kind == RefChannelID {
		ch := &Channel{ChannelID: ref.ChannelID, ProfileURL: BaseURL + "/channel/" + ref.ChannelID}
		// 顺手用 RSS 确认存在并补上标题（1 次请求，且 RSS 是唯一稳定给出标题的地方）
		feed, _, err := FetchFeed(ctx, client, th, ref.ChannelID)
		if err != nil {
			return nil, err
		}
		ch.Title = feed.Title
		// RSS 里没有头像，再抓一次频道页拿 og:image（拿不到就先用首字母占位，不算失败）
		if body, err := getPage(ctx, client, th, ch.ProfileURL); err == nil {
			if m := ogImageRe.FindSubmatch(body); m != nil {
				ch.AvatarURL = htmlUnescape(string(m[1]))
			}
			if ch.Title == "" {
				if m := ogTitleRe.FindSubmatch(body); m != nil {
					ch.Title = htmlUnescape(string(m[1]))
				}
			}
		}
		return ch, nil
	}

	pageURL := ref.URL
	if pageURL == "" {
		if ref.Kind == RefHandle {
			pageURL = BaseURL + "/@" + ref.Handle
		}
	}
	if pageURL == "" {
		return nil, social.NotFound("没有可抓取的地址")
	}
	body, err := getPage(ctx, client, th, pageURL)
	if err != nil {
		return nil, err
	}

	ch := &Channel{Handle: ref.Handle}
	if ref.Kind == RefVideo {
		// 视频页的 og:title / og:image 是**这条视频**的，不是频道的——只能拿它的 channelId，
		// 然后回过头再抓一次频道页，否则订阅进来的频道名会是一串视频标题。
		if m := videoChanRe.FindSubmatch(body); m != nil {
			ch.ChannelID = string(m[1])
		}
		if ch.ChannelID == "" {
			return nil, social.NotFound("这条视频里读不出所属频道")
		}
		if body, err = getPage(ctx, client, th, BaseURL+"/channel/"+ch.ChannelID); err != nil {
			return nil, err
		}
	} else {
		if m := externalIDRe.FindSubmatch(body); m != nil {
			ch.ChannelID = string(m[1])
		} else if m := canonicalRe.FindSubmatch(body); m != nil {
			ch.ChannelID = string(m[1])
		}
		if ch.ChannelID == "" {
			return nil, social.StaleParser("频道页里没找到频道 id（YouTube 可能改了页面结构）")
		}
	}
	if m := ogTitleRe.FindSubmatch(body); m != nil {
		ch.Title = htmlUnescape(string(m[1]))
	}
	if m := ogImageRe.FindSubmatch(body); m != nil {
		ch.AvatarURL = htmlUnescape(string(m[1]))
	}
	ch.ProfileURL = BaseURL + "/channel/" + ch.ChannelID
	return ch, nil
}

func getPage(ctx context.Context, client *http.Client, th *social.Throttle, url string) ([]byte, error) {
	if th != nil {
		if err := th.Wait(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, social.Upstream("构造请求失败", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, social.Upstream("请求 YouTube 页面失败", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, social.Upstream("读取 YouTube 页面失败", err)
	}
	if err := httpErr(resp, body, th); err != nil {
		return nil, err
	}
	return body, nil
}

// httpErr 把上游状态码翻成「等一会儿 / 换 IP / 升级解析器」三种脸。
func httpErr(resp *http.Response, body []byte, th *social.Throttle) error {
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return social.NotFound("频道不存在或已注销")
	case resp.StatusCode == http.StatusTooManyRequests:
		wait := retryAfter(resp, 15*time.Minute)
		until := time.Now().Add(wait)
		if th != nil {
			th.CoolUntil(until)
		}
		return social.RateLimited(fmt.Sprintf("YouTube 限流，%s 之后自动恢复", until.Format("15:04")), until)
	case resp.StatusCode == http.StatusForbidden:
		return social.Blocked("YouTube 拒绝了这次请求（多半是这个出口 IP 被要求人机验证）：去设置里给 YouTube 换个代理")
	case resp.StatusCode >= 400:
		return social.Upstream(fmt.Sprintf("YouTube 返回 %d", resp.StatusCode), nil)
	}
	return nil
}

func retryAfter(resp *http.Response, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return fallback
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 || secs > 3600 {
		return fallback
	}
	return time.Duration(secs) * time.Second
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	return r.Replace(s)
}
