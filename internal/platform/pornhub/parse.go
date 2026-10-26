package pornhub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
)

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// ---------- 通用小工具 ----------

func pickText(doc *goquery.Document, selectors ...string) string {
	for _, s := range selectors {
		if t := strings.TrimSpace(doc.Find(s).First().Text()); t != "" {
			return t
		}
	}
	return ""
}

func pickAttr(doc *goquery.Document, attr string, selectors ...string) string {
	for _, s := range selectors {
		n := doc.Find(s).First()
		if v, ok := n.Attr(attr); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func attrAny(n *goquery.Selection, names ...string) string {
	for _, a := range names {
		if v, ok := n.Attr(a); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// jsonLDBlocks 取出页面中所有 application/ld+json 对象。
func jsonLDBlocks(doc *goquery.Document) []map[string]any {
	var out []map[string]any
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		raw := strings.TrimSpace(s.Text())
		if raw == "" {
			return
		}
		var one map[string]any
		if err := json.Unmarshal([]byte(raw), &one); err == nil {
			out = append(out, one)
			return
		}
		var many []map[string]any
		if err := json.Unmarshal([]byte(raw), &many); err == nil {
			out = append(out, many...)
		}
	})
	return out
}

func ldType(m map[string]any) string {
	switch v := m["@type"].(type) {
	case string:
		return v
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

func ldString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case map[string]any:
			if u, ok := v["url"].(string); ok && u != "" {
				return u
			}
		case []any:
			if len(v) > 0 {
				if s, ok := v[0].(string); ok && s != "" {
					return s
				}
				if mm, ok := v[0].(map[string]any); ok {
					if u, ok := mm["url"].(string); ok && u != "" {
						return u
					}
				}
			}
		}
	}
	return ""
}

var reNum = regexp.MustCompile(`[\d.,]+`)

// countValue 取「标签: 数字」这种文案里冒号后半段的数字。
// 不能直接拿整串喂 parseCount：英文 "Subscribers: 1,430,831" 里的 b 会被当成 billion 的倍率。
func countValue(text string) *int {
	cut := text
	if i := strings.LastIndexAny(text, ":："); i >= 0 {
		cut = strings.TrimSpace(strings.TrimLeft(text[i:], ":： "))
	}
	return parseCount(cut)
}

func parseCount(text string) *int {
	m := reNum.FindString(strings.ReplaceAll(text, " ", ""))
	if m == "" {
		return nil
	}
	m = strings.ReplaceAll(m, ",", "")
	mult := 1.0
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "k") || strings.Contains(text, "千"):
		mult = 1e3
	case strings.Contains(lower, "m") || strings.Contains(text, "万"):
		mult = 1e6
		if strings.Contains(text, "万") {
			mult = 1e4
		}
	case strings.Contains(lower, "b"):
		mult = 1e9
	}
	f, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return nil
	}
	n := int(f * mult)
	return &n
}

// isBoilerplateDescription 判断是不是站点自动生成的 SEO 简介。
func isBoilerplateDescription(s string) bool {
	if s == "" {
		return true
	}
	return strings.Contains(s, "观看性爱视频") && strings.Contains(s, "Pornhub")
}

// parseDurationText 支持 "12:34" / "1:02:03" / "PT12M34S"。
func parseDurationText(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasPrefix(s, "PT") || strings.HasPrefix(s, "P") {
		var hours, minutes, seconds int
		if m := regexp.MustCompile(`(\d+)H`).FindStringSubmatch(s); m != nil {
			hours, _ = strconv.Atoi(m[1])
		}
		if m := regexp.MustCompile(`(\d+)M`).FindStringSubmatch(s); m != nil {
			minutes, _ = strconv.Atoi(m[1])
		}
		if m := regexp.MustCompile(`(\d+)S`).FindStringSubmatch(s); m != nil {
			seconds, _ = strconv.Atoi(m[1])
		}
		return hours*3600 + minutes*60 + seconds
	}
	parts := strings.Split(s, ":")
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

var reRelDate = regexp.MustCompile(`(\d+)\s*(second|minute|hour|day|week|month|year|秒|分钟|小时|天|周|月|年)`)

// parseRelativeDate 把 "2 days ago" / "2 天前" 转成绝对时间（近似值）。
func parseRelativeDate(s string, nowT time.Time) *time.Time {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return nil
	}
	if strings.Contains(s, "just now") || strings.Contains(s, "刚刚") || strings.Contains(s, "just") {
		t := nowT
		return &t
	}
	m := reRelDate.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	n, _ := strconv.Atoi(m[1])
	var d time.Duration
	switch m[2] {
	case "second", "秒":
		d = time.Duration(n) * time.Second
	case "minute", "分钟":
		d = time.Duration(n) * time.Minute
	case "hour", "小时":
		d = time.Duration(n) * time.Hour
	case "day", "天":
		d = time.Duration(n) * 24 * time.Hour
	case "week", "周":
		d = time.Duration(n) * 7 * 24 * time.Hour
	case "month", "月":
		d = time.Duration(n) * 30 * 24 * time.Hour
	case "year", "年":
		d = time.Duration(n) * 365 * 24 * time.Hour
	default:
		return nil
	}
	t := nowT.Add(-d)
	// 站点偶尔给出「56年前」这类假时间（推广位），早于站点存在的日期毫无意义，按未知处理。
	if t.Before(time.Date(2006, 1, 1, 0, 0, 0, 0, time.UTC)) {
		return nil
	}
	return &t
}

// ---------- 艺人页 ----------

// profileDropLabels 是资料区块里**不收**的行：
//   - 「会勾起我性欲的是 / 会浇灭我性欲的是」——用户明确不要，这两项也不适合入库；
//   - 「视频播放量 / 主页浏览量 / 已看视频」——平台统计，不是个人资料，
//     页头已经有本地影片数与订阅数了，没必要重复。
//
// 中英文都匹配（站点按语言返回不同文案），只做包含匹配，改文案时不必精确对齐。
var profileDropLabels = []string{
	"会勾起我性欲", "会浇灭我性欲", "播放量", "浏览量", "已看视频",
	"turn on", "turn off", "video views", "profile views", "videos watched",
}

func dropProfileLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	if l == "" {
		return true
	}
	for _, bad := range profileDropLabels {
		if strings.Contains(l, bad) {
			return true
		}
	}
	return false
}

// parseProfileInfo 抓资料区块（.infoPiece）里的「标签: 值」行，保持站点顺序。
// 站点字段按艺人不同（有人有罩杯、眼睛颜色，有人没有），所以不做白名单，
// 只丢 profileDropLabels 里那几行。
func parseProfileInfo(doc *goquery.Document) []model.ProfileField {
	pieces := doc.Find(".js-infoText .infoPiece")
	if pieces.Length() == 0 {
		pieces = doc.Find(".infoPiece")
	}
	out := make([]model.ProfileField, 0, pieces.Length())
	pieces.Each(func(_ int, s *goquery.Selection) {
		label := strings.TrimSpace(s.Find("span").First().Text())
		label = strings.TrimRight(label, ":： ") // 站点写成「性别:」
		value := strings.TrimSpace(s.Find(".smallInfo").First().Text())
		if value == "" {
			value = strings.TrimSpace(s.Find("span").Last().Text())
		}
		if value == "" || value == label || dropProfileLabel(label) {
			return
		}
		out = append(out, model.ProfileField{Label: label, Value: value})
	})
	return out
}

// socialHostLabels 把外链的域名翻成人看的名字，用于站点没给标签时的兜底。
var socialHostLabels = map[string]string{
	"twitter.com": "Twitter", "x.com": "X",
	"instagram.com": "Instagram", "onlyfans.com": "OnlyFans",
	"fansly.com": "Fansly", "tiktok.com": "TikTok",
	"youtube.com": "YouTube", "youtu.be": "YouTube",
	"twitch.tv": "Twitch", "linktr.ee": "Linktree",
	"snapchat.com": "Snapchat", "reddit.com": "Reddit",
	"t.me": "Telegram", "telegram.me": "Telegram",
	"facebook.com": "Facebook", "fanhub.com": "Fanhub",
	"manyvids.com": "ManyVids", "chaturbate.com": "Chaturbate",
	"pornhub.com": "Pornhub",
}

func socialHostLabel(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if name, ok := socialHostLabels[host]; ok {
		return name
	}
	// 未知域名：取主体部分（linktr.ee → Linktr）。
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		base := parts[len(parts)-2]
		if base != "" {
			return strings.ToUpper(base[:1]) + base[1:]
		}
	}
	return host
}

// parseSocialLinks 抓艺人主页右侧的站外链接列表（官方网址 / Twitter / Instagram…）。
// 只认 .socialList：顶部那个「关于我的更多信息」按钮是引流位，不算社交账号。
func parseSocialLinks(doc *goquery.Document) []model.SocialLink {
	out := make([]model.SocialLink, 0, 4)
	seen := map[string]bool{}
	doc.Find(".socialList li a").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		href = strings.TrimSpace(href)
		if href == "" || seen[href] {
			return
		}
		// 只收 http(s)：资料来自上游页面，别把 javascript: / data: 之类的带进前端。
		lower := strings.ToLower(href)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return
		}
		label := strings.TrimSpace(s.Find(".socialText").First().Text())
		if label == "" {
			label = socialHostLabel(href)
		}
		if label == "" {
			return
		}
		seen[href] = true
		out = append(out, model.SocialLink{Label: label, URL: href})
	})
	return out
}

// aboutText 抓「简介」正文。区块里第一个 div 是标题（"简介 Mini Diva"），正文是后面的 div。
func aboutText(doc *goquery.Document) string {
	s := doc.Find(".aboutMeSection").First()
	if s.Length() == 0 {
		return ""
	}
	var out string
	s.Children().EachWithBreak(func(_ int, c *goquery.Selection) bool {
		if c.HasClass("title") {
			return true
		}
		if t := strings.TrimSpace(c.Text()); t != "" {
			out = t
			return false
		}
		return true
	})
	return out
}

func parsePerformerPage(body []byte) (*model.Performer, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	p := &model.Performer{}

	for _, ld := range jsonLDBlocks(doc) {
		if t := ldType(ld); t == "Person" || t == "ProfilePage" {
			if p.Name == "" {
				p.Name = ldString(ld, "name", "headline")
			}
			if p.AvatarURL == "" {
				p.AvatarURL = ldString(ld, "image", "thumbnailUrl")
			}
			if p.Bio == "" {
				p.Bio = ldString(ld, "description")
			}
		}
	}

	if p.Name == "" {
		p.Name = pickText(doc, ".nameSubscribe h1", "h1[itemprop=name]", ".title h1", "h1")
	}
	if p.AvatarURL == "" {
		p.AvatarURL = pickAttr(doc, "data-src",
			".avatarWrapper img", ".tooltipTrig img", "img[src*='/av/']", "img[alt*='avatar']")
	}
	if p.AvatarURL == "" {
		p.AvatarURL = pickAttr(doc, "src", ".avatarWrapper img", "img[src*='/av/']")
	}
	if p.Bio == "" {
		p.Bio = pickText(doc, ".aboutMeText", "[itemprop=description]")
	}
	if p.Bio == "" {
		p.Bio = aboutText(doc) // 页面上的「简介」区块（ld+json 里通常没有 description）
	}

	// 资料区块与站外链接：整组随艺人资料一起存，页面按顺序渲染。
	p.Profile = parseProfileInfo(doc)
	p.Social = parseSocialLinks(doc)

	// 头部读数：模特资料页顶部只有三块（模特排名 / 视频播放量 / 订阅者），
	// 都用 .infoBox 承载，`data-title` 里是完整数字（"订阅者: 1,430,831"），
	// 比页面显示的 "1.4M" 精确得多。**资料页没有「影片总数」**，所以 video_count 可能一直为空。
	doc.Find(".infoBox").Each(func(_ int, s *goquery.Selection) {
		raw, _ := s.Attr("data-title")
		label := strings.ToLower(raw)
		if label == "" {
			label = strings.ToLower(s.Find(".title").First().Text())
		}
		if p.SubscriberCount == nil &&
			(strings.Contains(label, "订阅") || strings.Contains(label, "subscriber")) {
			p.SubscriberCount = countValue(raw)
			if p.SubscriberCount == nil {
				p.SubscriberCount = parseCount(s.Find(".big").First().Text())
			}
		}
	})

	// 计数：从详情区块里按标签文字就近取数。
	// 只认资料区块（.infoPiece / .info）——早先这里还带了 `li`，结果把影片列表卡片
	// 里的「播放量」当成了影片总数（48949 这种数字就是这么来的）。
	// 同一个坑还有一处：「视频播放量: 561,906,285」也含「视频」二字，必须先被
	// dropProfileLabel 挡掉，否则会把播放量当成影片总数。
	doc.Find(".infoPiece, .info").Each(func(_ int, s *goquery.Selection) {
		txt := strings.ToLower(strings.TrimSpace(s.Text()))
		if len(txt) > 120 || dropProfileLabel(txt) {
			return
		}
		switch {
		case strings.Contains(txt, "subscriber") || strings.Contains(txt, "订阅"):
			if p.SubscriberCount == nil {
				p.SubscriberCount = parseCount(s.Find("span").Last().Text())
				if p.SubscriberCount == nil {
					p.SubscriberCount = parseCount(txt)
				}
			}
		case strings.Contains(txt, "video") || strings.Contains(txt, "视频"):
			if p.VideoCount == nil {
				p.VideoCount = parseCount(s.Find("span").Last().Text())
				if p.VideoCount == nil {
					p.VideoCount = parseCount(txt)
				}
			}
		}
	})

	if p.Name == "" {
		return nil, errors.New("未能解析艺人信息（页面结构可能已变更）")
	}
	return p, nil
}

// ---------- 艺人影片列表 ----------

func parseVideoListPage(body []byte, page int, base string) (*platform.Page, error) {
	return parseVideoListPageFor(body, page, base, "")
}

// parseVideoListPageFor 与 parseVideoListPage 相同，但会剔除「不是这位艺人」的推广位：
// 艺人页顶部常混进别家片子的广告条目，它们的发布时间还常是「56年前」这种假值。
func parseVideoListPageFor(body []byte, page int, base, wantPerformer string) (*platform.Page, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	out := &platform.Page{Page: page}
	nowT := time.Now().UTC()

	var entries []listEntry

	doc.Find("li.pcVideoListItem, li[data-video-vkey], div.videoBox, li.videoBox").Each(func(_ int, s *goquery.Selection) {
		v, ok := parseVideoListItem(s, nowT)
		if !ok {
			return
		}
		handle, has := modelHandleOf(s)
		entries = append(entries, listEntry{video: v, handle: handle, hasLink: has})
	})

	// 艺人页会插进别家片子的推广位：它们往往既没有指向该艺人的链接，发布时间也是
	// 「56年前」这类假值。只要页面上确实存在「指向该艺人」的条目，就以它为准，
	// 其余一律当推广位丢掉；整页都没有艺人链接时（页面结构可能变了）退回按日期判断。
	withWant, dated := 0, 0
	for _, e := range entries {
		if e.video.PublishedAt != nil {
			dated++
		}
		if e.hasLink && wantPerformer != "" && sameHandle(e.handle, wantPerformer) {
			withWant++
		}
	}

	for _, e := range entries {
		switch {
		case wantPerformer != "" && withWant > 0:
			if !e.hasLink || !sameHandle(e.handle, wantPerformer) {
				continue
			}
		case e.video.PublishedAt == nil && len(entries) >= 5 && dated*3 >= len(entries)*2:
			// 大部分条目都有正常日期时，没有日期的多半是推广位。
			continue
		}
		v := e.video
		if v.URL == "" && base != "" {
			v.URL = strings.TrimRight(base, "/") + "/view_video.php?viewkey=" + url.QueryEscape(v.PlatformVideoID)
		}
		out.Items = append(out.Items, *v)
	}
	out.TotalPages = detectTotalPages(doc)
	if out.TotalPages > 0 {
		out.HasMore = page < out.TotalPages
	} else {
		out.HasMore = len(out.Items) > 0
	}
	return out, nil
}

var reViewkey = regexp.MustCompile(`[?&]viewkey=([A-Za-z0-9]+)`)

var reModelHref = regexp.MustCompile(`/model/([^/?"#]+)`)

// modelHandleOf 取列表项上标注的艺人 handle（第一个 /model/ 链接）。
func modelHandleOf(s *goquery.Selection) (string, bool) {
	handle := ""
	found := false
	s.Find(`a[href*="/model/"]`).EachWithBreak(func(_ int, a *goquery.Selection) bool {
		href, _ := a.Attr("href")
		m := reModelHref.FindStringSubmatch(href)
		if m == nil {
			return true
		}
		handle, found = m[1], true
		return false
	})
	return handle, found
}

// sameHandle 宽松比较艺人 handle（忽略大小写与结尾的 /videos 之类）。
func sameHandle(a, b string) bool {
	return strings.EqualFold(strings.Trim(a, "/"), strings.Trim(b, "/"))
}

// listEntry 是艺人页列表里的一条原始条目。
type listEntry struct {
	video   *model.Video
	handle  string // 条目上标注的艺人 handle（没有则为空）
	hasLink bool
}

func parseVideoListItem(s *goquery.Selection, nowT time.Time) (*model.Video, bool) {
	vkey := attrAny(s, "data-video-vkey")
	href := attrAny(s, "href")
	if vkey == "" && href != "" {
		if m := reViewkey.FindStringSubmatch(href); m != nil {
			vkey = m[1]
		}
	}
	link := s.Find("a").First()
	if vkey == "" {
		h := attrAny(link, "href")
		if m := reViewkey.FindStringSubmatch(h); m != nil {
			vkey = m[1]
		}
	}
	if vkey == "" {
		return nil, false
	}

	v := &model.Video{PlatformVideoID: vkey}

	// 标题：优先 link title 属性，其次 .title a 文本。
	v.Title = attrAny(link, "title")
	if v.Title == "" {
		v.Title = strings.TrimSpace(s.Find(".title a, .title, .videoTitle").First().Text())
	}
	if v.Title == "" {
		v.Title = strings.TrimSpace(link.Text())
	}

	img := s.Find("img").First()
	v.ThumbURL = attrAny(img, "data-mediumthumb", "data-src", "data-original", "src")

	// 预览视频（悬停播放用），部分页面存在。
	v.PreviewURL = attrAny(img, "data-mediabook")
	if v.PreviewURL == "" {
		v.PreviewURL = attrAny(s.Find("video").First(), "src")
	}

	v.DurationSec = parseDurationText(strings.TrimSpace(s.Find(".duration, .videoDuration").First().Text()))
	v.ViewCount = parseCount(strings.TrimSpace(s.Find(".views, .videoViews").First().Text()))

	if r := strings.TrimSpace(s.Find(".value, .ratingValue").First().Text()); r != "" {
		if f, err := strconv.ParseFloat(strings.TrimSuffix(r, "%"), 64); err == nil {
			v.Rating = &f
		}
	}

	added := strings.TrimSpace(s.Find(".added, .videoAdded").First().Text())
	v.PublishedAt = parseRelativeDate(added, nowT)

	return v, true
}

func detectTotalPages(doc *goquery.Document) int {
	max := 0
	doc.Find(".pagination3 li a, .pagination li a, ul.pagination a").Each(func(_ int, s *goquery.Selection) {
		if n, err := strconv.Atoi(strings.TrimSpace(s.Text())); err == nil && n > max {
			max = n
		}
	})
	if max > 0 {
		return max
	}
	txt := strings.ToLower(doc.Text())
	if m := regexp.MustCompile(`page\s*(\d+)\s*of\s*(\d+)`).FindStringSubmatch(txt); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil {
			return n
		}
	}
	return 0
}

// ---------- 视频页 ----------

type mediaDefinition struct {
	Format   string          `json:"format"`
	Quality  json.RawMessage `json:"quality"`
	VideoURL string          `json:"videoUrl"`
	Default  bool            `json:"defaultQuality"`
	Height   int             `json:"height"`
	Width    int             `json:"width"`
	Remote   bool            `json:"remote"`
}

// QualityText 返回可读画质标识。
// 真实页面里同一个字段既可能是 "1080"，也可能是空数组 []（直链条目就是这样），
// 用 string 解码会让整个数组反序列化失败，所以这里放宽成任意 JSON 值。
func (d mediaDefinition) QualityText() string {
	raw := bytes.TrimSpace(d.Quality)
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// jsObjectAt 从 i 处的 '{' 或 '[' 起做括号配平扫描，返回完整的 JSON 片段。
// 不能用 `\{.*?\}` 这类正则：mediaDefinitions 里含嵌套的空数组/对象，
// 非贪婪匹配会在第一个 ']' 处截断，导致整个数组解不出来。
func jsObjectAt(body []byte, i int) []byte {
	if i < 0 || i >= len(body) {
		return nil
	}
	open := body[i]
	if open != '{' && open != '[' {
		return nil
	}
	depth := 0
	inString := false
	escaped := false
	for k := i; k < len(body); k++ {
		c := body[k]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				if (open == '{' && c != '}') || (open == '[' && c != ']') {
					return nil
				}
				return body[i : k+1]
			}
		}
	}
	return nil
}

// jsValueAfterKey 找 "key" 之后紧跟的对象或数组。
func jsValueAfterKey(body []byte, key string) []byte {
	needle := []byte(`"` + key + `"`)
	from := 0
	for {
		idx := bytes.Index(body[from:], needle)
		if idx < 0 {
			return nil
		}
		i := from + idx + len(needle)
		for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
			i++
		}
		if i < len(body) && body[i] == ':' {
			i++
			for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
				i++
			}
			if out := jsObjectAt(body, i); out != nil {
				return out
			}
		}
		from = i
		if from >= len(body) {
			return nil
		}
	}
}

// parseVideoPage 解析视频详情页，返回影片信息与流定义列表。
func parseVideoPage(body []byte) (*model.Video, []mediaDefinition, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	v := &model.Video{}

	for _, ld := range jsonLDBlocks(doc) {
		if t := ldType(ld); t == "VideoObject" || t == "Movie" {
			if v.Title == "" {
				v.Title = ldString(ld, "name", "headline")
			}
			if v.Description == "" {
				v.Description = ldString(ld, "description")
			}
			if v.ThumbURL == "" {
				v.ThumbURL = ldString(ld, "thumbnailUrl", "image")
			}
			if d := ldString(ld, "duration"); d != "" {
				v.DurationSec = parseDurationText(d)
			}
			if up := ldString(ld, "uploadDate", "datePublished"); up != "" {
				if t, err := time.Parse(time.RFC3339, up); err == nil {
					v.PublishedAt = &t
				} else if t, err := time.Parse("2006-01-02", up); err == nil {
					v.PublishedAt = &t
				}
			}
			if v.Rating == nil {
				if agg, ok := ld["aggregateRating"].(map[string]any); ok {
					v.Rating = ldRating(agg)
				}
			}
		}
	}

	if v.Title == "" {
		v.Title = pickText(doc, "h1.title span.inlineFreeform", "h1.title", "h1[class*=title]", "h1")
	}
	if v.ThumbURL == "" {
		v.ThumbURL = pickAttr(doc, "content", `meta[property="og:image"]`)
	}
	if v.DurationSec == 0 {
		v.DurationSec = parseDurationText(pickText(doc, ".duration span", ".videoDuration"))
	}
	if v.Description == "" {
		// 优先正文里的用户简介；没有就退回 meta description，但要跳过站点宣传语
		//（cn 站几乎每部片的 meta 都是「上Pornhub.com观看性爱视频：…」，写进 nfo 只会污染简介）。
		v.Description = pickText(doc, ".videoDescriptionText", ".video-description", "[data-type=description]")
	}
	if v.Description == "" {
		if meta := pickAttr(doc, "content", `meta[name="description"]`); !isBoilerplateDescription(meta) {
			v.Description = meta
		}
	}

	// 标签
	seen := map[string]bool{}
	doc.Find(`a[href*="/video?search="], .tagsWrapper a, .tagList a`).Each(func(_ int, s *goquery.Selection) {
		t := strings.TrimSpace(s.Text())
		if t != "" && !seen[t] && len(v.Tags) < 40 {
			seen[t] = true
			v.Tags = append(v.Tags, t)
		}
	})

	defs := parseMediaDefinitions(body)
	if v.Title == "" && len(defs) == 0 {
		return nil, nil, errors.New("未能解析视频页面（页面结构可能已变更或需要登录）")
	}
	return v, defs, nil
}

func ldRating(m map[string]any) *float64 {
	if s, ok := m["ratingValue"].(string); ok {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return &f
		}
	}
	if f, ok := m["ratingValue"].(float64); ok {
		return &f
	}
	return nil
}

// parseMediaDefinitions 取页面里的 mediaDefinitions 数组。
// 新版页面把它放在一个转义过的 JS 字符串里（url 写成 https:\/\/…），
// 旧版页面则是 flashvars_<ts> = {...}; 两种都按括号配平扫描后反序列化。
func parseMediaDefinitions(body []byte) []mediaDefinition {
	if raw := jsValueAfterKey(body, "mediaDefinitions"); raw != nil {
		if defs, ok := decodeMediaDefs(raw); ok {
			return defs
		}
	}
	// flashvars_<ts> 里的整段 JSON（含 mediaDefinitions）
	if idx := bytes.Index(body, []byte("flashvars_")); idx >= 0 {
		if eq := bytes.IndexByte(body[idx:], '='); eq >= 0 {
			start := idx + eq + 1
			for start < len(body) && body[start] != '{' {
				start++
			}
			if raw := jsObjectAt(body, start); raw != nil {
				var fv struct {
					MediaDefinitions []mediaDefinition `json:"mediaDefinitions"`
				}
				if err := json.Unmarshal(raw, &fv); err == nil && len(fv.MediaDefinitions) > 0 {
					return fv.MediaDefinitions
				}
			}
		}
	}
	return nil
}

func decodeMediaDefs(raw []byte) ([]mediaDefinition, bool) {
	var defs []mediaDefinition
	if err := json.Unmarshal(raw, &defs); err == nil && len(defs) > 0 {
		return defs, true
	}
	// 某些页面把 JSON 再套一层字符串（"[]" 形式），反转义后再试一次。
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if err := json.Unmarshal([]byte(str), &defs); err == nil && len(defs) > 0 {
			return defs, true
		}
	}
	return nil, false
}

func parseQualityToHeight(q string) int {
	q = strings.TrimSpace(strings.ToLower(q))
	q = strings.TrimSuffix(q, "p")
	n, err := strconv.Atoi(q)
	if err != nil {
		return 0
	}
	return n
}

// ParseMasterPlaylist 解析 HLS master playlist 的变体列表（相对 URI 会按 base 补全）。
func ParseMasterPlaylist(content, baseURL string) []model.Variant {
	lines := strings.Split(content, "\n")
	var out []model.Variant
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			continue
		}
		attrs := parseAttrList(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
		var v model.Variant
		if bw, ok := attrs["BANDWIDTH"]; ok {
			v.Bandwidth, _ = strconv.Atoi(bw)
		}
		if res, ok := attrs["RESOLUTION"]; ok {
			if w, h, ok2 := strings.Cut(res, "x"); ok2 {
				v.Width, _ = strconv.Atoi(w)
				v.Height, _ = strconv.Atoi(h)
			}
		}
		v.Codec = attrs["CODECS"]
		// 下一非注释行是 URI
		for j := i + 1; j < len(lines); j++ {
			uri := strings.TrimSpace(lines[j])
			if uri == "" || strings.HasPrefix(uri, "#") {
				continue
			}
			v.URL = resolveURL(baseURL, uri)
			i = j
			break
		}
		if v.URL != "" {
			out = append(out, v)
		}
	}
	model.SortVariantsDesc(out)
	return out
}

func parseAttrList(s string) map[string]string {
	out := map[string]string{}
	var cur strings.Builder
	var inQuote bool
	parts := []string{}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	for _, p := range parts {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			continue
		}
		out[strings.ToUpper(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return out
}

func resolveURL(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		return base[:i+1] + ref
	}
	return ref
}

// ---------- 搜索页 ----------

func parseSearchResults(body []byte, base string) ([]*model.Performer, bool) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	var out []*model.Performer
	seen := map[string]bool{}
	doc.Find(`a[href*="/model/"], a[href*="/pornstar/"]`).Each(func(_ int, s *goquery.Selection) {
		href := attrAny(s, "href")
		if href == "" {
			return
		}
		slug := ""
		if m := rePerformerPath.FindStringSubmatch(href); m != nil {
			slug = m[1]
		}
		if slug == "" || slug == "search" || seen[slug] {
			return
		}
		name := strings.TrimSpace(s.Text())
		if name == "" {
			name = slug
		}
		if len(name) > 80 {
			return
		}
		var avatar string
		if img := s.Find("img").First(); img.Length() > 0 {
			avatar = attrAny(img, "data-src", "data-mediumthumb", "src")
		}
		if avatar == "" {
			if img := s.Parent().Find("img").First(); img.Length() > 0 {
				avatar = attrAny(img, "data-src", "src")
			}
		}
		seen[slug] = true
		url := href
		if strings.HasPrefix(url, "/") {
			url = base + url
		}
		out = append(out, &model.Performer{
			PerformerID: slug,
			Name:        name,
			URL:         url,
			AvatarURL:   avatar,
		})
	})
	hasMore := doc.Find(`.pagination a[rel=next], .pagination3 a[rel=next]`).Length() > 0
	return out, hasMore
}
