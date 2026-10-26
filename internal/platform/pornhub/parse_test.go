package pornhub

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mario/myporn/internal/model"
)

func TestParsePerformerURL(t *testing.T) {
	a := &Adapter{base: "https://cn.pornhub.com"}
	cases := map[string]string{
		"https://cn.pornhub.com/model/mini-diva":        "mini-diva",
		"https://cn.pornhub.com/model/mini-diva/videos": "mini-diva",
		"http://www.pornhub.com/pornstar/jane-doe":      "jane-doe",
		"cn.pornhub.com/model/mini-diva":                "mini-diva",
		"https://cn.pornhub.com/model/mini-diva?x=1":    "mini-diva",
	}
	for in, want := range cases {
		got, err := a.ParsePerformerURL(in)
		if err != nil {
			t.Fatalf("%s: 意外错误 %v", in, err)
		}
		if got != want {
			t.Errorf("%s: 期望 %q，得到 %q", in, want, got)
		}
	}
	if _, err := a.ParsePerformerURL("https://example.com/foo"); err == nil {
		t.Error("非艺人 URL 应该报错")
	}
}

func TestParseMasterPlaylist(t *testing.T) {
	body := `#EXTM3U
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=800000,BANDWIDTH=1000000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2"
360p/index.m3u8
#EXT-X-STREAM-INF:AVERAGE-BANDWIDTH=5000000,BANDWIDTH=6000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
1080p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=20000000,RESOLUTION=3840x2160,CODECS="hvc1.2.4.L153.B0"
https://cdn.example.com/4k/index.m3u8
`
	vs := ParseMasterPlaylist(body, "https://cdn.example.com/master.m3u8")
	if len(vs) != 3 {
		t.Fatalf("期望 3 个变体，得到 %d", len(vs))
	}
	// 必须按质量降序：4K 在最前，默认最高画质。
	if vs[0].Height != 2160 {
		t.Errorf("首个变体应为 2160p，得到 %d", vs[0].Height)
	}
	if vs[0].URL != "https://cdn.example.com/4k/index.m3u8" {
		t.Errorf("绝对地址被改写: %s", vs[0].URL)
	}
	if vs[2].URL != "https://cdn.example.com/360p/index.m3u8" {
		t.Errorf("相对地址补全失败: %s", vs[2].URL)
	}
	if vs[1].Bandwidth != 6000000 {
		t.Errorf("码率解析错误: %d", vs[1].Bandwidth)
	}
	// 4K 那条是 HEVC：id 带编码后缀（同分辨率可能有 H.264 / AV1 / HEVC 多条，不能撞值）
	if vs[0].ID() != "2160p.hevc" {
		t.Errorf("变体 ID 错误: %s", vs[0].ID())
	}
	if vs[0].Quality() != "2160p" {
		t.Errorf("Quality 应只看分辨率: %s", vs[0].Quality())
	}
	// H.264 那条保持老 id，前端记住的档位不受影响
	if vs[1].ID() != "1080p" {
		t.Errorf("H.264 变体 ID 应保持 1080p: %s", vs[1].ID())
	}
}

func TestParsePerformerPage(t *testing.T) {
	html := `<html><head>
<script type="application/ld+json">
{"@type":"Person","name":"Mini Diva","image":"https://ci.example.com/av/1.jpg","description":"简介文本"}
</script></head>
<body>
<div class="infoPiece"><span>Videos</span> <span>312</span></div>
<div class="infoPiece"><span>Subscribers</span> <span>45.6K</span></div>
</body></html>`
	p, err := parsePerformerPage([]byte(html))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Mini Diva" {
		t.Errorf("名称错误: %q", p.Name)
	}
	if p.AvatarURL != "https://ci.example.com/av/1.jpg" {
		t.Errorf("头像错误: %q", p.AvatarURL)
	}
	if p.Bio != "简介文本" {
		t.Errorf("简介错误: %q", p.Bio)
	}
	if p.VideoCount == nil || *p.VideoCount != 312 {
		t.Errorf("视频数错误: %v", p.VideoCount)
	}
}

func TestParseVideoListPage(t *testing.T) {
	html := `<html><body><ul>
<li class="pcVideoListItem">
  <a href="/view_video.php?viewkey=abc123" title="Title One"><img data-mediumthumb="https://ci.example.com/1.jpg"></a>
  <span class="duration">12:34</span>
  <span class="views">12K views</span>
  <span class="value">88%</span>
  <span class="added">2 days ago</span>
</li>
<li class="pcVideoListItem">
  <a href="/view_video.php?viewkey=def456" title="Title Two"><img data-src="https://ci.example.com/2.jpg"></a>
  <span class="duration">01:02:03</span>
  <span class="added">3 小时前</span>
</li>
</ul>
<div class="pagination"><li><a>1</a></li><li><a>7</a></li></div>
</body></html>`
	page, err := parseVideoListPage([]byte(html), 1, "https://example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("期望 2 条，得到 %d", len(page.Items))
	}
	v := page.Items[0]
	if v.PlatformVideoID != "abc123" || v.Title != "Title One" {
		t.Errorf("首条解析错误: %+v", v)
	}
	if v.ThumbURL != "https://ci.example.com/1.jpg" {
		t.Errorf("缩略图错误: %s", v.ThumbURL)
	}
	if v.DurationSec != 12*60+34 {
		t.Errorf("时长错误: %d", v.DurationSec)
	}
	if v.ViewCount == nil || *v.ViewCount != 12000 {
		t.Errorf("播放量错误: %v", v.ViewCount)
	}
	if v.Rating == nil || *v.Rating != 88 {
		t.Errorf("评分错误: %v", v.Rating)
	}
	if v.PublishedAt == nil || time.Since(*v.PublishedAt) < 47*time.Hour {
		t.Errorf("相对时间解析错误: %v", v.PublishedAt)
	}
	if page.Items[1].DurationSec != 3723 {
		t.Errorf("hh:mm:ss 解析错误: %d", page.Items[1].DurationSec)
	}
	if page.TotalPages != 7 {
		t.Errorf("总页数错误: %d", page.TotalPages)
	}
	if !page.HasMore {
		t.Error("第 1 页应为还有更多")
	}
}

func TestParseVideoPageAndFlashvars(t *testing.T) {
	html := `<html><head>
<script type="application/ld+json">
{"@type":"VideoObject","name":"My Video","description":"desc","duration":"PT10M5S",
 "thumbnailUrl":"https://ci.example.com/t.jpg","uploadDate":"2026-10-01T10:00:00+00:00"}
</script></head><body>
<h1 class="title"><span class="inlineFreeform">My Video</span></h1>
<a href="/video?search=tag1">tag1</a>
<a href="/video?search=tag2">tag2</a>
<script>
var flashvars_12345 = {"mediaDefinitions":[
 {"format":"hls","quality":"auto","videoUrl":"https://cdn.example.com/hls/master.m3u8","defaultQuality":true},
 {"format":"mp4","quality":"720","videoUrl":"https://cdn.example.com/720.mp4"},
 {"format":"mp4","quality":"480","videoUrl":"https://cdn.example.com/480.mp4"}
]};
</script>
</body></html>`
	v, defs, err := parseVideoPage([]byte(html))
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "My Video" {
		t.Errorf("标题错误: %q", v.Title)
	}
	if v.DurationSec != 605 {
		t.Errorf("时长错误: %d", v.DurationSec)
	}
	if len(v.Tags) != 2 {
		t.Errorf("标签数量错误: %v", v.Tags)
	}
	if len(defs) != 3 {
		t.Fatalf("流定义数量错误: %d", len(defs))
	}
	if defs[0].Format != "hls" {
		t.Errorf("格式错误: %s", defs[0].Format)
	}
}

func TestParseVideoPageSkipsBoilerplateDescription(t *testing.T) {
	// cn 站的 meta description 是站点宣传语，不该写进 nfo 的简介
	boiler := `<html><head>
<meta name="description" content="上Pornhub.com观看性爱视频：某标题。我们是全球最牛的色情网站。">
</head><body><h1 class="title"><span class="inlineFreeform">某标题</span></h1></body></html>`
	v, _, err := parseVideoPage([]byte(boiler))
	if err != nil {
		t.Fatal(err)
	}
	if v.Description != "" {
		t.Errorf("站点宣传语不该当作简介: %q", v.Description)
	}

	// 正文里真的有简介时优先用它
	real := `<html><head>
<meta name="description" content="上Pornhub.com观看性爱视频：某标题。">
</head><body><h1 class="title"><span class="inlineFreeform">某标题</span></h1>
<div class="videoDescriptionText">这是作者自己写的简介。</div></body></html>`
	v2, _, err := parseVideoPage([]byte(real))
	if err != nil {
		t.Fatal(err)
	}
	if v2.Description != "这是作者自己写的简介。" {
		t.Errorf("应取正文简介，得到 %q", v2.Description)
	}
}

func TestParseMediaDefinitionsWithoutFlashvars(t *testing.T) {
	body := []byte(`{"x":1,"mediaDefinitions":[{"format":"mp4","quality":"1080","videoUrl":"https://a/1.mp4"}]}`)
	defs := parseMediaDefinitions(body)
	if len(defs) != 1 || defs[0].QualityText() != "1080" {
		t.Fatalf("回退解析失败: %+v", defs)
	}
}

func TestParseQualityToHeight(t *testing.T) {
	cases := map[string]int{"720": 720, "1080p": 1080, "2160": 2160, "auto": 0, "": 0}
	for in, want := range cases {
		if got := parseQualityToHeight(in); got != want {
			t.Errorf("%q: 期望 %d 得到 %d", in, want, got)
		}
	}
}

func TestParseDurationText(t *testing.T) {
	cases := map[string]int{
		"12:34": 754, "1:02:03": 3723, "PT10M5S": 605, "PT1H2M3S": 3723, "": 0,
	}
	for in, want := range cases {
		if got := parseDurationText(in); got != want {
			t.Errorf("%q: 期望 %d 得到 %d", in, want, got)
		}
	}
}

func TestParseCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"12K views", 12000},
		{"1.5M views", 1500000},
		{"1,234", 1234},
		{"45.6K", 45600},
	}
	for _, c := range cases {
		got := parseCount(c.in)
		if got == nil || *got != c.want {
			t.Errorf("%q: 期望 %d 得到 %v", c.in, c.want, got)
		}
	}
}

func TestVariantSortAndDefault(t *testing.T) {
	vs := []model.Variant{
		{Height: 720, Bandwidth: 1000},
		{Height: 1080, Bandwidth: 2000},
		{Height: 1080, Bandwidth: 5000},
	}
	model.SortVariantsDesc(vs)
	if vs[0].Height != 1080 || vs[0].Bandwidth != 5000 {
		t.Errorf("排序错误: %+v", vs)
	}
}

// mediaDefinitions 里含嵌套空数组（"segmentFormats":{} / "quality":[]），
// 早期用非贪婪正则会在第一个 ']' 处截断，导致整段解不出来。
func TestParseMediaDefinitionsWithNestedArrays(t *testing.T) {
	html := `<html><body><script>
	var flashvars_x = {"video_id":"1","mediaDefinitions":[
	  {"group":1,"height":1080,"width":1920,"format":"hls","quality":"1080","videoUrl":"https:\/\/hv-h.phncdn.com\/1080\/master.m3u8?h=a","segmentFormats":{"audio":"ts_aac","video":"mpeg2_ts"},"cookies":[]},
	  {"group":1,"height":2160,"width":3840,"format":"mp4","quality":[],"videoUrl":"https:\/\/cn.pornhub.com\/video\/get_media?s=abc","segmentFormats":{"audio":"aac","video":"mp4"},"cookies":[],"remote":true},
	  {"group":1,"height":720,"width":1280,"format":"hls","quality":"720","videoUrl":"https:\/\/hv-h.phncdn.com\/720\/master.m3u8?h=b","cookies":[]}
	]};
	</script></body></html>`

	defs := parseMediaDefinitions([]byte(html))
	if len(defs) != 3 {
		t.Fatalf("应解析出 3 条流定义，得到 %d: %+v", len(defs), defs)
	}
	if defs[0].Height != 1080 || defs[0].Format != "hls" {
		t.Errorf("第一条不对: %+v", defs[0])
	}
	if !strings.Contains(defs[0].VideoURL, "hv-h.phncdn.com/1080/master.m3u8") {
		t.Errorf("转义斜杠未反转义: %q", defs[0].VideoURL)
	}
	if defs[1].Height != 2160 || !strings.Contains(defs[1].VideoURL, "get_media") {
		t.Errorf("4K 直链条目丢失: %+v", defs[1])
	}
}

func TestParseVideoListItemFillsURL(t *testing.T) {
	html := `<ul><li class="pcVideoListItem" data-video-vkey="6798a78e9c631">
	  <a href="/view_video.php?viewkey=6798a78e9c631" title="标题 A"><img data-mediumthumb="https://ci.phncdn.com/a.jpg"></a>
	  <span class="duration">12:34</span>
	</li></ul>`
	page, err := parseVideoListPage([]byte(html), 1, "https://cn.pornhub.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("应解析出 1 条，得到 %d", len(page.Items))
	}
	got := page.Items[0]
	if got.URL != "https://cn.pornhub.com/view_video.php?viewkey=6798a78e9c631" {
		t.Errorf("列表项缺少可回溯的原始地址: %q", got.URL)
	}
	if got.DurationSec != 754 {
		t.Errorf("时长解析错误: %d", got.DurationSec)
	}
}

func TestParseVideoListPageSkipsPromotedItems(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<html><body><ul>`)
	// 5 条正常条目（属于 mini-diva）
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, `<li class="pcVideoListItem" data-video-vkey="ok%d">
			<a href="/view_video.php?viewkey=ok%d" title="正常 %d"></a>
			<a href="/model/mini-diva/videos">Mini Diva</a>
			<a href="/model/mini-diva"><img src="https://ci.example.com/t%d.jpg"></a>
			<span class="duration">10:00</span><span class="added">%d天前</span></li>`, i, i, i, i, i+1)
	}
	// 推广位：别家艺人 + 假时间
	b.WriteString(`<li class="pcVideoListItem" data-video-vkey="ad1">
		<a href="/view_video.php?viewkey=ad1" title="广告片"></a>
		<a href="/model/somebody-else">Somebody</a>
		<span class="duration">12:00</span><span class="added">56年前</span></li>`)
	// 无发布时间的推广位
	b.WriteString(`<li class="pcVideoListItem" data-video-vkey="ad2">
		<a href="/view_video.php?viewkey=ad2" title="广告片2"></a>
		<span class="duration">08:00</span></li>`)
	b.WriteString(`</ul></body></html>`)

	pg, err := parseVideoListPageFor([]byte(b.String()), 1, "https://cn.pornhub.com", "mini-diva")
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.Items) != 5 {
		ids := make([]string, 0, len(pg.Items))
		for _, v := range pg.Items {
			ids = append(ids, v.PlatformVideoID)
		}
		t.Fatalf("应过滤掉 2 条推广位，得到 %d 条: %v", len(pg.Items), ids)
	}
	for _, v := range pg.Items {
		if v.PublishedAt == nil {
			t.Fatalf("条目 %s 缺少发布时间", v.PlatformVideoID)
		}
	}
}

func TestParseVideoListPageKeepsUndatedWhenMostAreUndated(t *testing.T) {
	// 没有一条带日期时不能全丢（可能是页面结构变了）
	body := `<html><body><ul>
		<li class="pcVideoListItem" data-video-vkey="a"><a href="/view_video.php?viewkey=a" title="A"></a></li>
		<li class="pcVideoListItem" data-video-vkey="b"><a href="/view_video.php?viewkey=b" title="B"></a></li>
	</ul></body></html>`
	pg, err := parseVideoListPage([]byte(body), 1, "https://cn.pornhub.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.Items) != 2 {
		t.Fatalf("日期全缺时不该过滤，得到 %d 条", len(pg.Items))
	}
}

func TestParseRelativeDateRejectsImplausible(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	if got := parseRelativeDate("56年前", now); got != nil {
		t.Fatalf("离谱日期应为 nil，得到 %v", got)
	}
	if got := parseRelativeDate("3天前", now); got == nil {
		t.Fatal("正常日期不该被丢弃")
	}
}

// 快路径不拉 HLS 清单，码率只能用直链文件名猜；合并时这个「猜的」标记不能丢。
func TestMergeVariantsKeepsApproxFlagWithoutHLSBandwidth(t *testing.T) {
	in := []model.Variant{
		{Height: 1080, URL: "https://cdn.invalid/hls/1080/master.m3u8"},                                         // HLS def，无码率
		{Height: 1080, URL: "https://cdn.invalid/1080P_4000K_x.mp4", Bandwidth: 4000000, ApproxBandwidth: true}, // 直链
	}
	out := mergeVariants(in, map[int]int{})
	if len(out) != 1 {
		t.Fatalf("应按清晰度去重成 1 条，得到 %d", len(out))
	}
	if out[0].Bandwidth != 4000000 {
		t.Fatalf("应沿用直链的码率，得到 %d", out[0].Bandwidth)
	}
	if !out[0].ApproxBandwidth {
		t.Fatal("「码率是猜的」标记丢了，下载体积预估会高估")
	}

	// 拿到真实 HLS 码率时就该把标记清掉。
	out2 := mergeVariants(in, map[int]int{1080: 1353004})
	if out2[0].Bandwidth != 1353004 || out2[0].ApproxBandwidth {
		t.Fatalf("应以 HLS 真实码率覆盖，得到 %d approx=%v", out2[0].Bandwidth, out2[0].ApproxBandwidth)
	}
}
