package library

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"
	"time"
)

// MovieMeta 是生成 Emby movie nfo 所需的全部信息（FR-27）。
type MovieMeta struct {
	Title         string
	OriginalTitle string
	Plot          string
	Premiered     *time.Time // 发布日期
	RuntimeMin    int        // 分钟
	Genres        []string
	Tags          []string
	Studio        string // = 平台名
	Actors        []Actor
	UniqueIDs     []UniqueID
	Website       string // = 影片原始地址
	Rating        float64
	Maker         string
}

// Actor 对应 nfo 里的 <actor><name/><role/></actor>。
type Actor struct {
	Name string
	Role string
}

// UniqueID 对应 <uniqueid type="pornhub" default="true">xxx</uniqueid>。
type UniqueID struct {
	Type    string
	Value   string
	Default bool
}

// nfo 结构与 Emby/Jellyfin 的 movie 规范对齐；字段顺序不影响刮削。
type nfoMovie struct {
	XMLName       xml.Name    `xml:"movie"`
	Title         string      `xml:"title"`
	OriginalTitle string      `xml:"originaltitle,omitempty"`
	SortTitle     string      `xml:"sorttitle,omitempty"`
	Plot          string      `xml:"plot,omitempty"`
	Premiered     string      `xml:"premiered,omitempty"`
	Year          int         `xml:"year,omitempty"`
	Runtime       int         `xml:"runtime,omitempty"`
	Rating        float64     `xml:"rating,omitempty"`
	Maker         string      `xml:"maker,omitempty"`
	Studio        string      `xml:"studio,omitempty"`
	Website       string      `xml:"website,omitempty"`
	Genres        []string    `xml:"genre"`
	Tags          []string    `xml:"tag"`
	Actors        []nfoActor  `xml:"actor"`
	UniqueIDs     []nfoUnique `xml:"uniqueid"`
}

type nfoActor struct {
	Name  string `xml:"name"`
	Role  string `xml:"role,omitempty"`
	Type  string `xml:"type,omitempty"`
	Order int    `xml:"order,omitempty"`
}

type nfoUnique struct {
	Type    string `xml:"type,attr"`
	Default bool   `xml:"default,attr,omitempty"`
	Value   string `xml:",chardata"`
}

// RenderNFO 输出完整的 nfo 文本（含 XML 声明），供上层直接落盘。
func RenderNFO(m MovieMeta) ([]byte, error) {
	node := nfoMovie{
		Title:         strings.TrimSpace(m.Title),
		OriginalTitle: strings.TrimSpace(m.OriginalTitle),
		SortTitle:     strings.TrimSpace(m.Title),
		Plot:          strings.TrimSpace(m.Plot),
		Runtime:       m.RuntimeMin,
		Rating:        m.Rating,
		Maker:         m.Maker,
		Studio:        m.Studio,
		Website:       m.Website,
		Genres:        dedupe(m.Genres),
		Tags:          dedupe(m.Tags),
	}
	if node.Title == "" {
		node.Title = "Untitled"
		node.SortTitle = ""
	}
	if m.Premiered != nil && !m.Premiered.IsZero() {
		node.Premiered = m.Premiered.Format("2006-01-02")
		node.Year = m.Premiered.Year()
	}
	for i, a := range m.Actors {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		node.Actors = append(node.Actors, nfoActor{
			Name:  name,
			Role:  strings.TrimSpace(a.Role),
			Type:  "Actor",
			Order: i,
		})
	}
	for _, u := range m.UniqueIDs {
		if strings.TrimSpace(u.Value) == "" {
			continue
		}
		node.UniqueIDs = append(node.UniqueIDs, nfoUnique{Type: u.Type, Default: u.Default, Value: u.Value})
	}

	body, err := xml.MarshalIndent(node, "", "  ")
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	sb.WriteString(xml.Header)
	sb.Write(body)
	sb.WriteString("\n")
	return []byte(sb.String()), nil
}

// WriteNFO 把 nfo 写到 w。
func WriteNFO(w io.Writer, m MovieMeta) error {
	b, err := RenderNFO(m)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// Rating01 把 0-100 的评分折算成 Emby 使用的 0-10，并保留一位小数。
func Rating01(r float64) float64 {
	if r <= 0 {
		return 0
	}
	if r > 10 {
		r = r / 10
	}
	if r > 10 {
		r = 10
	}
	v, _ := strconv.ParseFloat(strconv.FormatFloat(r, 'f', 1, 64), 64)
	return v
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

// CleanPlot 清洗简介：站点自动生成的 SEO 文案（如 cn 站的
// 「上Pornhub.com观看性爱视频：…」）对 Emby 没有意义，直接丢掉。
func CleanPlot(description, fallbackTitle string) string {
	d := strings.TrimSpace(description)
	if d == "" || isSEODescription(d) {
		return strings.TrimSpace(fallbackTitle)
	}
	return d
}

// isSEODescription 判断是不是站点自动生成的宣传语。
func isSEODescription(s string) bool {
	return strings.Contains(s, "观看性爱视频") && strings.Contains(s, "Pornhub")
}
