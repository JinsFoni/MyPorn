package krx18

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// pageInfo 是影片页能抠出来的全部元数据。
type pageInfo struct {
	Slug        string
	URL         string
	PostID      string
	Title       string
	Thumb       string
	Description string
	Country     string
	Tags        []string
	PublishedAt *time.Time
	ViewCount   *int
}

// video 把页面元数据转成一条 videos 行。
//
// 时长不在这里：影片页上根本没有时长（实测 itemprop=duration 和「N 分钟」都不存在），
// 只能从清单的 #EXTINF 累加出来，见 Adapter.resolve。
func (p *pageInfo) video(a *Adapter) *model.Video {
	return &model.Video{
		Platform:        a.Key(),
		PlatformVideoID: p.Slug,
		Title:           p.Title,
		URL:             p.URL,
		ThumbURL:        p.Thumb,
		Description:     p.Description,
		Tags:            p.Tags,
		PublishedAt:     p.PublishedAt,
		ViewCount:       p.ViewCount,
	}
}

var viewCountRe = regexp.MustCompile(`^([\d,\.]+)\s*Views?$`)

// parsePage 从影片页 HTML 里抠元数据。
//
// 页面是 DooPlay 主题：标题在 <h1>，简介在 <div itemprop="description">，
// 分类在 <div class="sgeneros"> 那一排 rel="tag" 链接里，播放量在 #playernotice 的
// data-text（形如 "151301 Views"），发布日期只出现在 JSON-LD 的 datePublished 上
// （可见的那句 "Aug. 30, 2026" 在 span.date 里，格式多一种写法就崩，不做首选）。
func parsePage(htmlText, pageURL, slug string) (*pageInfo, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlText))
	if err != nil {
		return nil, err
	}
	p := &pageInfo{URL: pageURL, Slug: slug}

	p.Title = strings.TrimSpace(doc.Find("h1").First().Text())
	if p.Title == "" {
		return nil, errors.New("影片页里没找到片名（<h1>）")
	}
	p.Thumb = strings.TrimSpace(doc.Find(`img[itemprop="image"]`).First().AttrOr("src", ""))
	if p.Thumb == "" {
		p.Thumb = strings.TrimSpace(doc.Find(`meta[property="og:image"]`).First().AttrOr("content", ""))
	}
	p.Description = strings.TrimSpace(doc.Find(`div[itemprop="description"]`).First().Text())
	p.Country = strings.TrimSpace(doc.Find("span.country").First().Text())
	doc.Find("div.sgeneros a").Each(func(_ int, s *goquery.Selection) {
		if t := strings.TrimSpace(s.Text()); t != "" {
			p.Tags = append(p.Tags, t)
		}
	})

	p.PostID = strings.TrimSpace(doc.Find("meta#dooplay-ajax-counter").First().AttrOr("data-postid", ""))
	if p.PostID == "" {
		if m := postIDRe.FindStringSubmatch(htmlText); m != nil {
			p.PostID = m[1]
		}
	}

	if txt, ok := doc.Find("#playernotice").First().Attr("data-text"); ok {
		p.ViewCount = parseViews(txt)
	}
	if ld := parseMovieJSONLD(htmlText); ld != nil {
		if p.Description == "" {
			p.Description = strings.TrimSpace(ld.Description)
		}
		if p.Thumb == "" {
			p.Thumb = strings.TrimSpace(ld.Image)
		}
		if p.Title == "" {
			p.Title = strings.TrimSpace(ld.Name)
		}
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(ld.DatePublished)); err == nil {
			utc := t.UTC()
			p.PublishedAt = &utc
		}
	}
	return p, nil
}

var postIDRe = regexp.MustCompile(`data-post='(\d+)'`)

func parseViews(raw string) *int {
	m := viewCountRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return nil
	}
	n, err := strconv.Atoi(strings.NewReplacer(",", "", ".", "").Replace(m[1]))
	if err != nil {
		return nil
	}
	return &n
}

// movieLD 是页面 JSON-LD 里那个 @type=Movie 的对象（本站可靠的发布日期来源）。
type movieLD struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	DatePublished string `json:"datePublished"`
	Image         string `json:"image"`
}

func parseMovieJSONLD(htmlText string) *movieLD {
	for _, block := range ldJSONRe.FindAllStringSubmatch(htmlText, -1) {
		var doc struct {
			Graph []json.RawMessage `json:"@graph"`
		}
		if err := json.Unmarshal([]byte(block[1]), &doc); err != nil {
			continue
		}
		nodes := doc.Graph
		if len(nodes) == 0 {
			nodes = []json.RawMessage{[]byte(block[1])}
		}
		for _, raw := range nodes {
			var probe struct {
				Type string `json:"@type"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil || probe.Type != "Movie" {
				continue
			}
			var m movieLD
			if err := json.Unmarshal(raw, &m); err != nil {
				continue
			}
			return &m
		}
	}
	return nil
}

var ldJSONRe = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

// iframeSrcRe 抠 <iframe src="…"> 里的地址。
var iframeSrcRe = regexp.MustCompile(`(?i)<iframe[^>]+src=["']([^"']+)["']`)

// embedSrcFrom 从 doo_player_ajax 的 embed_url 里取地址。
//
// 两种形状实测都有：新片回裸地址（"https://cdn.loadvid.com/videos/play/…"），
// 老片回一整段 iframe HTML（"<iframe src=\"https://openload.co/embed/…\">"）。
func embedSrcFrom(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "<") {
		if m := iframeSrcRe.FindStringSubmatch(raw); m != nil {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	return raw
}

// hostOf 取小写主机名（去掉 www.），认「哪个 embed 站」用。
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// ---------- loadvid 播放器配置 ----------

type loadvidConfig struct {
	hash  string
	token string
}

var (
	videoHashRe  = regexp.MustCompile(`videoHash:\s*'([^']+)'`)
	videoTokenRe = regexp.MustCompile(`videoToken:\s*'([^']+)'`)
)

// parseLoadvidConfig 抠 window.LoadVidConfig 里的 videoHash / videoToken。
// 两个值配一对去 POST /videos/resolve-token 换清单，缺一个都换不出来。
func parseLoadvidConfig(embedHTML string) (*loadvidConfig, error) {
	h := videoHashRe.FindStringSubmatch(embedHTML)
	t := videoTokenRe.FindStringSubmatch(embedHTML)
	if h == nil || t == nil {
		return nil, errors.New("播放器页里没找到 videoHash / videoToken")
	}
	return &loadvidConfig{hash: h[1], token: t[1]}, nil
}

// ---------- 清单 ----------

// segRef 是清单里的一段：地址 + 它自己的 #EXTINF 时长（码率估算要用）。
type segRef struct {
	URL *url.URL
	Dur float64
}

// parseMediaPlaylist 拆出分片（地址已绝对化）和总时长。
//
// 只处理媒体清单：主清单（#EXT-X-STREAM-INF）由 highestChildPlaylist 先挑出一条子清单。
func parseMediaPlaylist(text string, base *url.URL) ([]segRef, float64) {
	var (
		segs []segRef
		dur  float64
		next float64
	)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			v := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.Index(v, ","); i >= 0 {
				v = v[:i]
			}
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				next = f
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		ref, err := url.Parse(line)
		if err != nil {
			continue
		}
		segs = append(segs, segRef{URL: base.ResolveReference(ref), Dur: next})
		dur += next
		next = 0
	}
	return segs, dur
}

// highestChildPlaylist 在主清单里挑 BANDWIDTH 最大的那条子清单（不是主清单就返回 nil）。
//
// 只认紧跟 #EXT-X-STREAM-INF 的那一行：媒体清单里每一条分片都是裸地址，
// 不卡这个条件就会把最后一段分片当子清单返回。
func highestChildPlaylist(text string, base *url.URL) *url.URL {
	var (
		best    *url.URL
		bestB   = -1
		pending bool
		curB    int
	)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			pending, curB = true, 0
			for _, part := range strings.Split(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"), ",") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(part), "BANDWIDTH="); ok {
					curB, _ = strconv.Atoi(v)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "#") || !pending {
			continue
		}
		pending = false
		ref, err := url.Parse(line)
		if err != nil {
			continue
		}
		if curB > bestB {
			best, bestB = base.ResolveReference(ref), curB
		}
	}
	return best
}
