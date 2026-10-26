package library

import (
	"encoding/xml"
	"testing"
	"time"
)

func TestRenderNFO(t *testing.T) {
	date := time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC)
	body, err := RenderNFO(MovieMeta{
		Title:         "Some Title",
		OriginalTitle: "Some Title",
		Plot:          "剧情 & <描述>",
		Premiered:     &date,
		RuntimeMin:    12,
		Genres:        []string{"Amateur", "HD"},
		Tags:          []string{"solo", "hd", "HD"},
		Studio:        "Pornhub",
		Actors:        []Actor{{Name: "Mini Diva", Role: "Actor"}},
		UniqueIDs:     []UniqueID{{Type: "pornhub", Value: "ph5f2c1", Default: true}},
		Website:       "https://example.com/view_video.php?viewkey=ph5f2c1",
		Rating:        Rating01(92),
	})
	if err != nil {
		t.Fatalf("生成 nfo 失败: %v", err)
	}
	text := string(body)
	if !contains(text, `<?xml version="1.0" encoding="UTF-8"?>`) && !contains(text, `<?xml version="1.0" encoding="utf-8"?>`) {
		t.Errorf("缺少 XML 声明: %s", text)
	}
	for _, want := range []string{
		"<title>Some Title</title>",
		"<premiered>2024-05-06</premiered>",
		"<year>2024</year>",
		"<runtime>12</runtime>",
		"<studio>Pornhub</studio>",
		"<name>Mini Diva</name>",
		`<uniqueid type="pornhub" default="true">ph5f2c1</uniqueid>`,
		"剧情 &amp; &lt;描述&gt;",
		"<rating>9.2</rating>",
	} {
		if !contains(text, want) {
			t.Errorf("nfo 缺少片段 %q\n%s", want, text)
		}
	}
	// tags 去重（大小写不敏感）
	if n := countOf(text, "<tag>hd</tag>") + countOf(text, "<tag>HD</tag>"); n != 1 {
		t.Errorf("tag 应去重，出现 %d 次", n)
	}
	// 必须是合法 XML
	var holder struct {
		XMLName xml.Name `xml:"movie"`
	}
	if err := xml.Unmarshal(body, &holder); err != nil {
		t.Fatalf("nfo 不是合法 XML: %v", err)
	}
}

func TestRating01(t *testing.T) {
	cases := map[float64]float64{92: 9.2, 8.5: 8.5, 0: 0, 120: 10}
	for in, want := range cases {
		if got := Rating01(in); got != want {
			t.Errorf("Rating01(%v) 期望 %v，得到 %v", in, want, got)
		}
	}
}

func contains(s, sub string) bool { return countOf(s, sub) > 0 }

func countOf(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}

func TestCleanPlot(t *testing.T) {
	seo := "上Pornhub.com观看性爱视频：某标题。我们是全球最牛的色情网站。"
	if got := CleanPlot(seo, "某标题"); got != "某标题" {
		t.Errorf("站点宣传语应被丢弃，得到 %q", got)
	}
	if got := CleanPlot("作者写的简介", "某标题"); got != "作者写的简介" {
		t.Errorf("正常简介不应被改写，得到 %q", got)
	}
	if got := CleanPlot("  ", "某标题"); got != "某标题" {
		t.Errorf("空简介应退回标题，得到 %q", got)
	}
}
