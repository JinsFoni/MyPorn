package beeg

import (
	"encoding/json"
	"strings"
	"testing"
)

// tagFixture 是 /tag?slug=MiniDiva 的真实响应（裁掉无关字段）。
const tagFixture = `{
  "check": true,
  "data": [{"td_column":"tg_caption","td_value":"I don’t have telegram, twitter, snapchat and other accounts."}],
  "id": 24341,
  "links": [
    {"lk_url":"onlyfans.com/minidivaonly","lk_text_link":"minidivaonly","type":{"id":10,"lt_name":"OnlyFans"}},
    {"lk_url":"https://x.com/minidivaonly","lk_text_link":"minidivaonly","type":{"id":3,"lt_name":"X (Twitter)"}}
  ],
  "tg_created":"2020-12-30T16:02:07.611909Z",
  "tg_followers": 9066,
  "tg_likes": 2796,
  "tg_name": "Mini Diva",
  "tg_slug": "MiniDiva",
  "tg_verified": false,
  "tg_videos_count": 48,
  "thumbs": [{"id": 809867252018185, "crops": [{"id": 3008}]}]
}`

// fileFixture 是 /tag/videos 里的一条影片（裁掉无关字段）。
const fileFixture = `{
  "file": {"id": 165927441581031, "fl_duration": 483, "fl_height": 1080, "fl_width": 1920, "fl_min_codec": "avc1.640032",
           "data": [{"cd_column": "sf_name", "cd_value": "Teen slut preparing anal for your big dick"}]},
  "fc_facts": [{"fc_created": "2024-03-03T12:39:43.185558Z", "fc_st_views": 5334, "fc_thumbs": [60], "reactions_count": 5}],
  "tags": [
    {"tg_name": "Mini Diva", "tg_slug": "MiniDiva", "is_person": true, "is_owner": false},
    {"tg_name": "Beeg", "tg_slug": "Beeg", "is_person": false, "is_owner": true},
    {"tg_name": "Big {Tits}", "tg_slug": "Big-Tits", "is_person": false, "is_owner": false}
  ]
}`

func TestToVideo(t *testing.T) {
	var it fileItem
	if err := jsonUnmarshal(t, fileFixture, &it); err != nil {
		t.Fatalf("解析影片失败: %v", err)
	}
	v := it.toVideo("https://beeg.com", "https://thumbs.externulls.com")
	if v == nil {
		t.Fatal("影片为空")
	}
	if v.PlatformVideoID != "165927441581031" {
		t.Fatalf("ID=%q", v.PlatformVideoID)
	}
	if v.URL != "https://beeg.com/165927441581031" {
		t.Fatalf("URL=%q", v.URL)
	}
	if v.Title != "Teen slut preparing anal for your big dick" {
		t.Fatalf("标题=%q", v.Title)
	}
	if v.DurationSec != 483 {
		t.Fatalf("时长=%d", v.DurationSec)
	}
	if v.PublishedAt == nil || v.PublishedAt.Year() != 2024 {
		t.Fatalf("发布日=%v", v.PublishedAt)
	}
	if v.ViewCount == nil || *v.ViewCount != 5334 {
		t.Fatalf("播放量=%v", v.ViewCount)
	}
	if v.ThumbURL != "https://thumbs.externulls.com/videos/165927441581031/60.webp?size=480x270" {
		t.Fatalf("缩略图=%q", v.ThumbURL)
	}
	// 出演者只收 is_person；厂牌（is_owner）和题材标签分开走。
	if len(v.Performers) != 1 || v.Performers[0].Name != "Mini Diva" {
		t.Fatalf("出演者=%+v", v.Performers)
	}
	if len(v.Tags) != 1 || v.Tags[0] != "Big Tits" {
		t.Fatalf("标签=%v", v.Tags)
	}
}

func TestTagDetail(t *testing.T) {
	var tg tagDetail
	if err := jsonUnmarshal(t, tagFixture, &tg); err != nil {
		t.Fatalf("解析艺人失败: %v", err)
	}
	if !strings.Contains(tg.Caption(), "telegram") {
		t.Fatalf("简介=%q", tg.Caption())
	}
	want := "https://thumbs.externulls.com/photos/809867252018185/to.webp?crop_id=3008"
	if got := tg.avatarURL("https://thumbs.externulls.com"); got != want {
		t.Fatalf("头像=%q，期望 %q", got, want)
	}
	// 没有 photos 时不该拼出半个地址。
	empty := tagDetail{}
	if got := empty.avatarURL("https://thumbs.externulls.com"); got != "" {
		t.Fatalf("空艺人头像应为空，得到 %q", got)
	}
}

func TestParsePerformerURL(t *testing.T) {
	a := &Adapter{base: defaultBase}
	cases := map[string]string{
		"https://beeg.com/MiniDiva":     "MiniDiva",
		"https://beeg.com/minidiva":     "minidiva",
		"https://www.beeg.com/MiniDiva": "MiniDiva",
		"https://beeg.com/RileyReid/":   "RileyReid",
		"MiniDiva":                      "MiniDiva",
	}
	for raw, want := range cases {
		got, err := a.ParsePerformerURL(raw)
		if err != nil {
			t.Fatalf("%s → 报错: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%s → %q，期望 %q", raw, got, want)
		}
	}
	for _, bad := range []string{
		"https://beeg.com/165927441581031", // 影片 ID
		"https://beeg.com/",
		"https://beeg.com/saved", // 站内路由
		"https://xhamster.com/pornstars/riley-reid",
	} {
		if _, err := a.ParsePerformerURL(bad); err == nil {
			t.Fatalf("%s 本该被拒", bad)
		}
	}
}

func TestFormatCount(t *testing.T) {
	for n, want := range map[int]string{999: "999", 1000: "1K", 9066: "9.1K", 2796: "2.8K", 2500000: "2.5M"} {
		if got := formatCount(n); got != want {
			t.Fatalf("formatCount(%d)=%q，期望 %q", n, got, want)
		}
	}
}

// jsonUnmarshal 是给测试用的小包装，省得每个用例都写 encoding/json 的错误处理。
func jsonUnmarshal(t *testing.T, raw string, out any) error {
	t.Helper()
	return json.Unmarshal([]byte(raw), out)
}

func TestCleanTagName(t *testing.T) {
	for raw, want := range map[string]string{
		"Big {Tits}":         "Big Tits",
		"Horizontal {Media}": "Horizontal Media",
		"4-20 Min":           "4-20 Min",
		"{Tits}":             "Tits",
		"  a  b  ":           "a b",
	} {
		if got := cleanTagName(raw); got != want {
			t.Fatalf("cleanTagName(%q)=%q，期望 %q", raw, got, want)
		}
	}
}
