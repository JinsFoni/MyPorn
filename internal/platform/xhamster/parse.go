package xhamster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/hls"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
)

// xHamster 的页面不是干净的结构：真正的数据全在 `<script>window.initials = {…}</script>`
// 这一大块 JSON 里（服务端渲染时塞进去的初始状态），DOM 只是它的渲染结果。
// 所以这里所有解析都以 window.initials 为准，不去猜 DOM 选择器——
// 站点改版多半只改 CSS 类名，这份 JSON 的字段名反倒稳定得多。

// errNoInitials 表示页面里没有 window.initials（被反爬挡了，或者结构变了）。
var errNoInitials = errors.New("页面里没有 window.initials，可能被风控拦截或站点改版")

// initials 是 window.initials 里我们关心的那几个分量。
// 字段多、层级深，只挑用得上的；认不出来的字段让它空着。
type initials struct {
	InfoComponent       *infoComponent       `json:"infoComponent"`
	AboutMeComponent    *aboutMeComponent    `json:"aboutMeComponent"`
	ChannelInfo         *channelInfo         `json:"channelInfo"`
	PaginationComponent *paginationComponent `json:"paginationComponent"`
	VideoModel          *videoModel          `json:"videoModel"`
	VideoEntity         *videoEntity         `json:"videoEntity"`
	TagsComponent       *tagsComponent       `json:"videoTagsComponent"`
	PlayerSettings      *playerSettings      `json:"xplayerSettings"`
	PlayerSettings2     *playerSettings      `json:"xplayerSettings2"`
	HostMap             map[string]string    `json:"hostMap"`
	DownloadComponent   *downloadComponent   `json:"downloadDropdownComponent"`
	SearchResult        *videoSection        `json:"searchResult"`

	// 影片列表可能挂在几种不同的分区上，按顺序捡第一个非空的。
	TrendingSection  *videoSection `json:"trendingVideoSectionComponent"`
	NewestSection    *videoSection `json:"newestVideoSectionComponent"`
	ExtendedSection  *videoSection `json:"extendedVideoSectionComponent"`
	PublisherSection *videoSection `json:"publisherVideoSectionComponent"`
}

type infoComponent struct {
	PornstarTop struct {
		ViewsCount            int    `json:"viewsCount"`
		VideoCount            int    `json:"videoCount"`
		Name                  string `json:"name"`
		Country               string `json:"country"`
		ThumbURL              string `json:"thumbUrl"`
		Rating                int    `json:"rating"`
		TranslatedCountryName string `json:"translatedCountryName"`
	} `json:"pornstarTop"`
	SubscribeButtonsProps struct {
		SubscribeButtonProps struct {
			ID          int64 `json:"id"`
			Subscribers int   `json:"subscribers"`
		} `json:"subscribeButtonProps"`
	} `json:"subscribeButtonsProps"`
}

type aboutMeComponent struct {
	Text               string      `json:"text"`
	PersonalInfoList   []infoField `json:"personalInfoList"`
	AppearanceInfoList []infoField `json:"appearanceInfoList"`
}

// infoField 是「I am」「From」「Height」这样一行资料。
//
// Value 故意用 json.RawMessage 原样收下，不在解码时定类型：这一列的形态会随站点改版变脸。
// 2026-10 之前全是字符串（"From": "Russian Federation"），之后新增的
// 「Interests and fetishes」一行是对象数组：
//
//	{"label": "Interests and fetishes", "value": [{"id":1119,"slug":"gentle-sex","name":"Gentle sex",…}, …]}
//
// 直接写 Value string 的后果不是「少显示一行」，而是整个 json.Unmarshal 报
// `cannot unmarshal array into Go struct field … of type string` —— 整页解析失败，
// 于是这类资料更「丰富」的艺人一个都加不进来（mysaaat 就是这样挂的）。
// 现在取值时再宽松地挑文本（见 text），认不出来的形态最多是这一行空着。
type infoField struct {
	Label string          `json:"label"`
	Value json.RawMessage `json:"value"`
}

// text 把一行的 value 压成给人看的一串字。
func (f infoField) text() string { return looseText(f.Value) }

// looseText 把任意形态的 JSON 值压成一行文本：
//   - 字符串原样；数字/布尔转字符串
//   - 对象取 name（退而求其次 title / label / text）
//   - 数组逐项压平后用「、」连起来，太多项就截断（兴趣标签能有十几二十个，
//     全塞进资料区那一行会把版式撑爆）
func looseText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return strings.TrimSpace(s)
		}
	case '{':
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			for _, key := range []string{"name", "title", "label", "text"} {
				if v, ok := obj[key]; ok {
					if s := looseText(v); s != "" {
						return s
					}
				}
			}
		}
	case '[':
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return ""
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			if s := looseText(it); s != "" {
				out = append(out, s)
			}
		}
		return joinCapped(out, maxInfoItems)
	default:
		// 数字 / true / false
		var v any
		if json.Unmarshal(raw, &v) == nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return ""
}

// maxInfoItems 是一行资料里最多列几项（兴趣标签这种长列表只留前几项 + 计数）。
const maxInfoItems = 6

// joinCapped 用「、」连接，超过 n 项时截断并补上总数，如「A、B、C 等 14 项」。
func joinCapped(items []string, n int) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= n {
		return strings.Join(items, "、")
	}
	return fmt.Sprintf("%s 等 %d 项", strings.Join(items[:n], "、"), len(items))
}

type channelInfo struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	Slug string `json:"slug"`
}

type paginationComponent struct {
	CurrentPageNumber int `json:"currentPageNumber"`
	LastPageNumber    int `json:"lastPageNumber"`
}

type videoSection struct {
	VideoListProps struct {
		VideoThumbProps []videoThumb `json:"videoThumbProps"`
	} `json:"videoListProps"`
}

// items 取分区里的影片（分区不存在就是空）。
func (s *videoSection) items() []videoThumb {
	if s == nil {
		return nil
	}
	return s.VideoListProps.VideoThumbProps
}

// videoThumb 是列表页里的一条影片，字段名和站点一致。
type videoThumb struct {
	ID       int64  `json:"id"`
	Duration int    `json:"duration"`
	Created  int64  `json:"created"`
	Title    string `json:"title"`
	PageURL  string `json:"pageURL"`
	ThumbURL string `json:"thumbURL"`
	ImageURL string `json:"imageURL"`
	Views    int    `json:"views"`
}

type videoModel struct {
	ID          int64  `json:"id"`
	IDHashSlug  string `json:"idHashSlug"`
	Duration    int    `json:"duration"`
	Title       string `json:"title"`
	PageURL     string `json:"pageURL"`
	Created     int64  `json:"created"`
	ThumbURL    string `json:"thumbURL"`
	PreviewURL  string `json:"previewThumbURL"`
	TrailerURL  string `json:"trailerURL"`
	Description string `json:"description"`
	Views       int    `json:"views"`
	Rating      struct {
		Value float64 `json:"value"`
	} `json:"rating"`
	Resolution []int `json:"resolution"`
}

type videoEntity struct {
	Description string `json:"description"`
	Views       int    `json:"views"`
	DateAgo     string `json:"dateAgo"`
	Orientation string `json:"orientation"`
}

type tagsComponent struct {
	Tags []struct {
		Name       string `json:"name"`
		Slug       string `json:"slug"`
		URL        string `json:"url"`
		IsCategory bool   `json:"isCategory"`
		IsPornstar bool   `json:"isPornstar"`
		IsChannel  bool   `json:"isChannel"`
		IsBrand    bool   `json:"isBrand"`
	} `json:"tags"`
}

type playerSettings struct {
	// sources 里混着两种形状：standard 是「编码 → 清晰度数组」，hls 是「编码 → 单个对象」，
	// 所以按 RawMessage 存下来，用哪个解哪个（见 standardSources）。
	Sources map[string]json.RawMessage `json:"sources"`
}

// standardSources 取 sources.standard（编码 → 清晰度列表）。
// hls 那一组是单档 media 清单，没有分辨率信息，播放用不上，这里不取。
func (ps *playerSettings) standardSources() map[string][]sourceEntry {
	if ps == nil {
		return nil
	}
	raw, ok := ps.Sources["standard"]
	if !ok {
		return nil
	}
	var out map[string][]sourceEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// sourceEntry 是 sources.standard.<编码> 里的一个清晰度。
// url 是密文（见 decrypt.go），quality=auto 那条才是「最高画质」的入口。
type sourceEntry struct {
	URL      string `json:"url"`
	Fallback string `json:"fallback"`
	Quality  string `json:"quality"`
	Label    string `json:"label"`
}

// downloadComponent 是播放页「下载」下拉框的数据，只用来兜底拿明文 MP4 直链。
//
// sources 的形状会变脸：多数页面是 `{"mp4": {"720p": "…"}}`，但有些页面给的是**数组**
// （2026-11 实测 video 1147 / 1192 就是这样），写成具体结构体的话 json.Unmarshal 直接报
// `cannot unmarshal array into Go struct field …`，**整页解析全挂**——连封面、标签、时长
// 一起拿不到，那种影片点开就是解析失败。所以这里原样收下，用的时候按两种形状分别解。
type downloadComponent struct {
	Sources json.RawMessage `json:"sources"`
}

// mp4Sources 取 sources 里的 mp4 档位表（"720p" → 直链）。
// 认不出的形状就当没有：下载直链只是兜底，缺了不影响播放与元数据。
func (dc *downloadComponent) mp4Sources() map[string]string {
	if dc == nil || len(dc.Sources) == 0 {
		return nil
	}
	var obj struct {
		MP4 map[string]string `json:"mp4"`
	}
	if err := json.Unmarshal(dc.Sources, &obj); err == nil && len(obj.MP4) > 0 {
		return obj.MP4
	}
	// 数组形状：[{"mp4": {…}, …}, …]——把每项里的 mp4 合并起来
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(dc.Sources, &arr); err == nil {
		out := map[string]string{}
		for _, item := range arr {
			raw, ok := item["mp4"]
			if !ok {
				continue
			}
			var m map[string]string
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			for k, v := range m {
				out[k] = v
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// initialsJSON 从页面里抠出 window.initials 的 JSON 文本。
// 不能按行或按括号数硬切，字符串里会有花括号和转义引号，所以老老实实扫一遍。
func initialsJSON(body []byte) ([]byte, error) {
	const marker = "window.initials="
	s := string(body)
	i := strings.Index(s, marker)
	if i < 0 {
		return nil, errNoInitials
	}
	s = s[i+len(marker):]
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t') {
		s = s[1:]
	}
	if len(s) == 0 || s[0] != '{' {
		return nil, errNoInitials
	}
	depth, inStr, esc := 0, false, false
	for j := 0; j < len(s); j++ {
		c := s[j]
		switch {
		case esc:
			esc = false
		case c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return []byte(s[:j+1]), nil
			}
		}
	}
	return nil, errNoInitials
}

func parseInitials(body []byte) (*initials, error) {
	raw, err := initialsJSON(body)
	if err != nil {
		return nil, err
	}
	var v initials
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("解析 window.initials 失败: %w", err)
	}
	return &v, nil
}

// videoSection 按优先级挑出「这位艺人的影片列表」那一块。
// 艺人主页第 1 页把整页列表放在 trendingVideoSectionComponent 里（名字叫 trending，
// 内容其实是该艺人的片单，第 2 页起也一样），别的分区是推荐位，不能混进来。
func (v *initials) videoSection() *videoSection {
	for _, s := range []*videoSection{
		v.TrendingSection, v.NewestSection, v.ExtendedSection, v.PublisherSection,
	} {
		if len(s.items()) > 0 {
			return s
		}
	}
	return nil
}

// performerFrom 把 window.initials 转成 model.Performer。
func performerFrom(v *initials, slug string) *model.Performer {
	p := &model.Performer{Name: slug, URL: ""}
	if ci := v.ChannelInfo; ci != nil && ci.Slug != "" {
		slug = ci.Slug
	}
	if info := v.InfoComponent; info != nil {
		top := info.PornstarTop
		if top.Name != "" {
			p.Name = top.Name
		}
		p.AvatarURL = top.ThumbURL
		if top.VideoCount > 0 {
			n := top.VideoCount
			p.VideoCount = &n
		}
		if subs := info.SubscribeButtonsProps.SubscribeButtonProps.Subscribers; subs > 0 {
			n := subs
			p.SubscriberCount = &n
		}
	}
	if cm := v.AboutMeComponent; cm != nil {
		// 简介里带 HTML 实体（个人自述里 "I&#039;m Alice" 这种），原样显示会露馅。
		p.Bio = strings.TrimSpace(html.UnescapeString(cm.Text))
		rows := make([]model.ProfileField, 0, len(cm.PersonalInfoList)+len(cm.AppearanceInfoList))
		for _, f := range append(append([]infoField{}, cm.PersonalInfoList...), cm.AppearanceInfoList...) {
			label := strings.TrimSpace(f.Label)
			value := f.text()
			if label == "" || value == "" {
				continue
			}
			rows = append(rows, model.ProfileField{Label: label, Value: value})
		}
		// 身高/三围从资料行里挑出来单独展示，剩下的照原样留在资料区块。
		p.Body, p.Profile = model.ParseBody(rows)
	}
	return p
}

// videoFromThumb 把列表页的一条转成 model.Video。
func videoFromThumb(t videoThumb, base string) *model.Video {
	v := &model.Video{
		PlatformVideoID: videoIDFromURL(t.PageURL),
		Title:           strings.TrimSpace(t.Title),
		URL:             t.PageURL,
		ThumbURL:        platform.BestThumb(t.ImageURL, t.ThumbURL),
		DurationSec:     t.Duration,
	}
	if v.PlatformVideoID == "" && t.ID != 0 {
		v.PlatformVideoID = fmt.Sprintf("%d", t.ID)
	}
	if v.URL == "" && v.PlatformVideoID != "" {
		v.URL = base + "/videos/" + v.PlatformVideoID
	}
	if t.Created > 0 {
		ts := time.Unix(t.Created, 0)
		v.PublishedAt = &ts
	}
	if t.Views > 0 {
		n := t.Views
		v.ViewCount = &n
	}
	return v
}

// videoIDFromURL 从 /videos/<slug>-xhHASH 里取出「slug-xhHASH」这段当影片 ID。
// 只留 hash（xhHASH）也能用：站点会 301 到带 slug 的规范地址，
// 但自己存着整段更稳，省一次跳转。
func videoIDFromURL(pageURL string) string {
	u := strings.TrimSpace(pageURL)
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "/videos/"); i >= 0 {
		u = u[i+len("/videos/"):]
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return strings.Trim(u, "/")
}

// videoFrom 把影片详情页的 window.initials 转成 model.Video。
func videoFrom(v *initials, id, base string) *model.Video {
	vm := v.VideoModel
	if vm == nil {
		return nil
	}
	out := &model.Video{
		PlatformVideoID: id,
		Title:           strings.TrimSpace(vm.Title),
		URL:             firstNonEmpty(vm.PageURL, base+"/videos/"+id),
		ThumbURL:        platform.BestThumb(vm.ThumbURL),
		PreviewURL:      firstNonEmpty(vm.TrailerURL, vm.PreviewURL),
		DurationSec:     vm.Duration,
		Description:     strings.TrimSpace(firstNonEmpty(vm.Description, entityDescription(v))),
	}
	if vm.Created > 0 {
		ts := time.Unix(vm.Created, 0)
		out.PublishedAt = &ts
	}
	if views := firstPositive(vm.Views, entityViews(v)); views > 0 {
		n := views
		out.ViewCount = &n
	}
	if vm.Rating.Value > 0 {
		r := vm.Rating.Value
		out.Rating = &r
	}
	if tc := v.TagsComponent; tc != nil {
		for _, t := range tc.Tags {
			// 频道/品牌/艺人不是标签；分类和自由标签才是。
			if t.IsChannel || t.IsBrand || t.IsPornstar {
				continue
			}
			if name := strings.TrimSpace(t.Name); name != "" {
				out.Tags = append(out.Tags, name)
			}
		}
	}
	return out
}

func entityDescription(v *initials) string {
	if v.VideoEntity == nil {
		return ""
	}
	return strings.TrimSpace(v.VideoEntity.Description)
}

func entityViews(v *initials) int {
	if v.VideoEntity == nil {
		return 0
	}
	return v.VideoEntity.Views
}

func firstNonEmpty(vals ...string) string {
	for _, s := range vals {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func firstPositive(vals ...int) int {
	for _, n := range vals {
		if n > 0 {
			return n
		}
	}
	return 0
}

// 编译期检查：Adapter 满足 platform.Adapter。
var _ platform.Adapter = (*Adapter)(nil)

// 编译期检查：Adapter 还能顺手带回详情元数据。
var _ platform.PlaybackResolver = (*Adapter)(nil)

// masterURLs 从 window.initials 里取出各编码的 master 清单模板地址。
func masterURLs(v *initials) map[string]string {
	out := map[string]string{}
	ps := v.PlayerSettings
	if ps == nil {
		ps = v.PlayerSettings2
	}
	if ps == nil {
		return out
	}
	for codec, entries := range ps.standardSources() {
		for _, e := range entries {
			// quality=auto 是「最高画质」那条；带具体档位的是单档直链，交给下载兜底用。
			if strings.EqualFold(strings.TrimSpace(e.Quality), "auto") {
				if plain, err := decryptSource(e.URL); err == nil {
					out[codec] = applyHostOverride(plain, v.HostMap, "standard."+codec+".auto.prime")
					break
				}
			}
		}
	}
	return out
}

// applyHostOverride 把明文地址里的主机名首段换成 hostMap 指定的节点。
// 站点自己的 hostMap 就是干这个的：standard.h264.1080p.prime → "video7"，
// 而密文里解出来的常是 video-h 这种中间域名，直连不通。
func applyHostOverride(raw string, hostMap map[string]string, key string) string {
	if len(hostMap) == 0 || raw == "" {
		return raw
	}
	host, ok := hostMap[key]
	if !ok || host == "" {
		return raw
	}
	i := strings.Index(raw, "://")
	if i < 0 {
		return raw
	}
	rest := raw[i+3:]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return raw
	}
	hostPart, path := rest[:slash], rest[slash:]
	// 去掉端口再换首段，换完保留原端口。
	port := ""
	if c := strings.LastIndexByte(hostPart, ':'); c >= 0 {
		port = hostPart[c:]
		hostPart = hostPart[:c]
	}
	labels := strings.Split(hostPart, ".")
	if len(labels) < 2 {
		return raw
	}
	labels[0] = host
	return raw[:i+3] + strings.Join(labels, ".") + port + path
}

// parseMaster 解析 master 清单，并在清单没写 CODECS 时按来源编码补上。
func parseMaster(content, baseURL, codec string) []model.Variant {
	vs := hls.ParseMasterPlaylist(content, baseURL)
	for i := range vs {
		if vs[i].Codec == "" {
			vs[i].Codec = codec
		}
	}
	return vs
}
