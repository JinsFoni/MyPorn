package xhamster

import (
	"strings"
	"testing"
)

// pageFixture 是一个裁到最小的艺人主页：只保留 window.initials 里我们解析的那几块，
// 外面套上一段真实页面里也有的杂项 HTML（脚本、注释、带花括号的字符串）。
const pageFixture = `<!DOCTYPE html><html><head><title>x</title></head><body>
<div id="app"></div>
<script>window.initials={"infoComponent":{"pornstarTop":{"viewsCount":942383357,"videoCount":1528,"name":"Riley Reid","country":"US","thumbUrl":"https://ic-tt-nss.xhcdn.com/a/abc/webp/000/029/214/avatar1.jpg","rating":99,"translatedCountryName":"United States"},"subscribeButtonsProps":{"subscribeButtonProps":{"id":1529,"subscribers":152259}}},"aboutMeComponent":{"text":"Riley Reid is an adult film actress.","showGeneratedNotice":true,"personalInfoList":[{"label":"I am","value":"Female"},{"label":"From","value":"United States"}],"appearanceInfoList":[{"label":"Height","value":"160 cm"}]},"channelInfo":{"id":1529,"type":"pornstar","slug":"riley-reid","personType":"pornstar"},"paginationComponent":{"currentPageNumber":1,"lastPageNumber":34,"pageLinkTemplate":"https://xhamster.com/pornstars/riley-reid/{#}"},"trendingVideoSectionComponent":{"sectionDataBlock":"trending","videoListProps":{"videoThumbProps":[{"id":21989353,"duration":2724,"created":1646902566,"title":"GIRLSWAY - Roleplay With Me","pageURL":"https://xhamster.com/videos/girlsway-roleplay-xhddtuU","thumbURL":"https://ic-vt-nss.xhcdn.com/a/t/s(w:526,h:298),webp/021/989/353/1280x720.c.jpg","imageURL":"https://ic-vt-nss.xhcdn.com/a/t/s(w:1280,h:720),webp/021/989/353/1280x720.c.jpg","views":1100000,"videoType":"video"}]}},"newestVideoSectionComponent":{"videoListProps":{"videoThumbProps":[{"id":999,"duration":60,"title":"不该被选中","pageURL":"https://xhamster.com/videos/other-xhzzz"}]}}};
window.somethingElse={"a":"}"};</script>
</body></html>`

func TestParseInitialsAndPerformer(t *testing.T) {
	iv, err := parseInitials([]byte(pageFixture))
	if err != nil {
		t.Fatalf("解析 window.initials 失败: %v", err)
	}
	p := performerFrom(iv, "riley-reid")
	if p.Name != "Riley Reid" {
		t.Fatalf("名字=%q", p.Name)
	}
	if p.VideoCount == nil || *p.VideoCount != 1528 {
		t.Fatalf("影片数=%v", p.VideoCount)
	}
	if p.SubscriberCount == nil || *p.SubscriberCount != 152259 {
		t.Fatalf("订阅数=%v", p.SubscriberCount)
	}
	if p.AvatarURL == "" || !strings.Contains(p.AvatarURL, "avatar1.jpg") {
		t.Fatalf("头像=%q", p.AvatarURL)
	}
	if !strings.Contains(p.Bio, "adult film actress") {
		t.Fatalf("简介=%q", p.Bio)
	}
	// 身高那一行应当被 ParseBody 抽走，不再重复出现在资料行里。
	if p.Body.HeightCm != 160 {
		t.Fatalf("身高=%d", p.Body.HeightCm)
	}
	if len(p.Profile) != 2 {
		t.Fatalf("资料行=%v（身高不该留在里面）", p.Profile)
	}
}

func TestVideoSectionPrefersTrending(t *testing.T) {
	iv, err := parseInitials([]byte(pageFixture))
	if err != nil {
		t.Fatalf("解析 window.initials 失败: %v", err)
	}
	sec := iv.videoSection()
	if sec == nil {
		t.Fatal("没找到影片列表")
	}
	items := sec.items()
	if len(items) != 1 || items[0].ID != 21989353 {
		t.Fatalf("选错了列表：%+v", items)
	}
}

func TestVideoFromThumb(t *testing.T) {
	thumb := videoThumb{
		ID:       21989353,
		Duration: 2724,
		Created:  1646902566,
		Title:    "GIRLSWAY - Roleplay With Me",
		PageURL:  "https://xhamster.com/videos/girlsway-roleplay-xhddtuU",
		ThumbURL: "https://ic-vt-nss.xhcdn.com/a/t/526x298.webp",
		ImageURL: "https://ic-vt-nss.xhcdn.com/a/t/1280x720.webp",
		Views:    1100000,
	}
	v := videoFromThumb(thumb, "https://xhamster.com")
	if v.PlatformVideoID != "girlsway-roleplay-xhddtuU" {
		t.Fatalf("影片 ID=%q", v.PlatformVideoID)
	}
	// 缩略图优先用 1280x720 那张（比列表用的 526x298 清楚）。
	if !strings.Contains(v.ThumbURL, "1280x720") {
		t.Fatalf("缩略图=%q", v.ThumbURL)
	}
	if v.PublishedAt == nil || v.PublishedAt.Year() != 2022 {
		t.Fatalf("发布日=%v", v.PublishedAt)
	}
	if v.ViewCount == nil || *v.ViewCount != 1100000 {
		t.Fatalf("播放量=%v", v.ViewCount)
	}
}

// 详情页的 sources 有两种形状（standard 是数组，hls 是对象），
// 这里验证只取 standard、并把 quality=auto 那条解出来当 master 模板。
func TestMasterURLsPicksAuto(t *testing.T) {
	json := `{"hostMap":{"standard.av1.auto.prime":"video7"},
		"xplayerSettings":{"sources":{
			"standard":{"av1":[{"quality":"auto","url":"` + vectorAV1Cipher + `"}]},
			"hls":{"av1":{"url":"` + vectorAV1Cipher + `"}}}}}}`
	iv, err := parseInitials([]byte(`<script>window.initials=` + json + `;</script>`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	urls := masterURLs(iv)
	got, ok := urls["av1"]
	if !ok {
		t.Fatalf("没解出 av1 的 master 地址：%v", urls)
	}
	if !strings.HasPrefix(got, "https://video7.xhcdn.com/") {
		t.Fatalf("主机没按 hostMap 替换：%s", got)
	}
	if !strings.Contains(got, "_TPL_.av1.mp4.m3u8") {
		t.Fatalf("不是 master 模板：%s", got)
	}
}

func TestParsePerformerURL(t *testing.T) {
	a := &Adapter{base: defaultBase}
	cases := map[string]string{
		"https://xhamster.com/pornstars/riley-reid":      "riley-reid",
		"https://xhamster.com/pornstars/riley-reid/2":    "riley-reid",
		"https://xhamster.com/creators/mini-diva":        "mini-diva",
		"https://xhamster.desi/pornstars/riley-reid":     "riley-reid",
		"https://cn.xhamster.com/pornstars/riley-reid?v": "riley-reid",
		"riley-reid": "riley-reid",
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
		"https://beeg.com/MiniDiva",
		"https://xhamster.com/videos/girlsway-roleplay-xhddtuU",
		"https://xhamster.com/pornstars",
	} {
		if _, err := a.ParsePerformerURL(bad); err == nil {
			t.Fatalf("%s 本该被拒", bad)
		}
	}
}
