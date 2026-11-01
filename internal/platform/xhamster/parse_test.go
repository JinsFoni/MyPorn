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
	// 只取 URL 末尾那段：同一部片子的「带 slug」和「裸 hash」两种地址要折成同一个 ID，
	// 否则同一部片子会在库里存两条、关注页上出现两张一样的卡片（实测见过 586 行）。
	if v.PlatformVideoID != "xhddtuU" {
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

// videoIDFromURL 只留 URL 末尾那一段。
//
// 这些形状都是从真实数据里抄的：库里 2026-10-31 那次同步插进来的 585 行裸 hash
// 全是第一种（只有 hash）第二种（带 slug）的重复品，两边必须折成同一个 ID。
func TestVideoIDFromURL(t *testing.T) {
	cases := map[string]string{
		// 两种写法 → 同一个 ID
		"https://xhamster.com/videos/they-had-a-naughty-threesome-and-played-snowballs-with-a-condom-xhW3Ivw": "xhW3Ivw",
		"https://xhamster.com/videos/xhW3Ivw": "xhW3Ivw",
		// 老式数字 ID 也一样
		"https://xhamster.com/videos/sarah-vandella-gets-banged-by-a-18yo-perv-during-her-massage-14840346": "14840346",
		"https://xhamster.com/videos/14840346": "14840346",
		// 相对的 pageURL、查询串、结尾斜杠
		"/videos/girlsway-roleplay-xhddtuU":            "xhddtuU",
		"https://xhamster.com/videos/xhddtuU?plan=foo": "xhddtuU",
		"https://xhamster.com/videos/xhddtuU/":         "xhddtuU",
		// 认不出的形状原样留着：别把「标题以年份结尾」当成 ID
		"https://xhamster.com/videos/best-of-2021": "best-of-2021",
		// 竖版短视频不是影片页：不走「取末尾那段」，免得和同名长片撞 ID（列表页目前也不收 shorts）
		"https://xhamster.com/shorts/naughty-threesome-played-snowballs-condom-xh5DH60": "https://xhamster.com/shorts/naughty-threesome-played-snowballs-condom-xh5DH60",
		"": "",
	}
	for raw, want := range cases {
		if got := videoIDFromURL(raw); got != want {
			t.Fatalf("%s\n  → %q，期望 %q", raw, got, want)
		}
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

// 2026-10 起 xHamster 给「Interests and fetishes」这一行的 value 换成了对象数组
// （[{id,slug,name,type,popular,url}, …]），而老代码把 value 定成 string：
// 一行的形态变了，json.Unmarshal 直接报错，整页解析失败 ——
// 表现是「这类资料更丰富的艺人一个都加不进来」（mysaaat 就是这么挂的）。
// 这条测试盯的是「一行的形态变了，不该把整页拖下水」。
func TestParseInitialsToleratesListInfoValues(t *testing.T) {
	const fixture = `<script>window.initials={"aboutMeComponent":{"text":"Hi","personalInfoList":[` +
		`{"label":"From","value":"Russian Federation"},` +
		`{"label":"Interests and fetishes","value":[` +
		`{"id":1119,"slug":"gentle-sex","name":"Gentle sex"},` +
		`{"id":527,"slug":"blowjob","name":"Blowjob"},` +
		`{"id":956,"slug":"porn","name":"Porn"},` +
		`{"id":1120,"slug":"role-playing","name":"Role playing"},` +
		`{"id":1130,"slug":"video-games","name":"Video games"},` +
		`{"id":7,"slug":"anime","name":"Anime"},` +
		`{"id":8,"slug":"cosplay","name":"Cosplay"},` +
		`{"id":9,"slug":"massage","name":"Massage"}]},` +
		`{"label":"Age","value":27},` +
		`{"label":"Nothing","value":null}` +
		`],"appearanceInfoList":[{"label":"Height","value":"167 cm (5 ft 65 in)"}]},` +
		`"channelInfo":{"id":1,"type":"creator","slug":"mysaaat"}};</script>`

	iv, err := parseInitials([]byte(fixture))
	if err != nil {
		t.Fatalf("一行的 value 是对象数组时不该让整页解析失败: %v", err)
	}
	p := performerFrom(iv, "mysaaat")
	got := map[string]string{}
	for _, f := range p.Profile {
		got[f.Label] = f.Value
	}
	// 字符串形态照旧
	if got["From"] != "Russian Federation" {
		t.Fatalf("From=%q", got["From"])
	}
	// 对象数组：取 name、用「、」连、超过 6 项截断并带总数
	const wantInterests = "Gentle sex、Blowjob、Porn、Role playing、Video games、Anime 等 8 项"
	if got["Interests and fetishes"] != wantInterests {
		t.Fatalf("Interests and fetishes=%q，期望 %q", got["Interests and fetishes"], wantInterests)
	}
	// 数字
	if got["Age"] != "27" {
		t.Fatalf("Age=%q", got["Age"])
	}
	// null：跳过这一行，不留空行
	if _, ok := got["Nothing"]; ok {
		t.Fatalf("null 值不该出现在资料行里: %v", p.Profile)
	}
	// 剩下的解析照旧（身高仍被 ParseBody 抽走）
	if p.Body.HeightCm != 167 {
		t.Fatalf("身高=%d", p.Body.HeightCm)
	}
	if p.Name != "mysaaat" {
		t.Fatalf("名字=%q", p.Name)
	}
}

// 列表必须按「最新」取：默认序是 sort=rated，第 1 页最上面是老片，
// 而增量同步遇到已入库影片就停，于是新片永远发现不了（2026-10 实测 mysaaat 漏了最新 4 部）。
func TestPerformerPathUsesNewestSort(t *testing.T) {
	cases := map[int]string{
		1:  "/pornstars/riley-reid/newest",
		2:  "/pornstars/riley-reid/newest/2",
		34: "/pornstars/riley-reid/newest/34",
	}
	for page, want := range cases {
		if got := performerPath("riley-reid", page); got != want {
			t.Fatalf("第 %d 页 → %q，期望 %q", page, got, want)
		}
	}
	// 资料里存的仍然是不带排序的主页地址
	if got := performerURLPath("riley-reid"); got != "/pornstars/riley-reid" {
		t.Fatalf("主页地址 = %q", got)
	}
}

// 带 /newest 的页面里没有 newestVideoSectionComponent，整页只有 trending 那一个大分区
// （2026-10 实测：mysaaat 42 条、riley-reid 每页 46 条），解析不能因此变成空。
func TestParseNewestSortedListing(t *testing.T) {
	const fixture = `<script>window.initials={"sort":"added","isDefaultOrder":false,` +
		`"infoComponent":{"pornstarTop":{"name":"mysaaat","videoCount":42}},` +
		`"trendingVideoSectionComponent":{"videoListProps":{"videoThumbProps":[` +
		`{"id":1792929031,"duration":1276,"created":1792929031,"title":"The student paid","pageURL":"https://xhamster.com/videos/the-student-xh1"},` +
		`{"id":1786716606,"duration":687,"created":1786716606,"title":"The tutoring","pageURL":"https://xhamster.com/videos/the-tutoring-xh2"}` +
		`]}}};</script>`

	iv, err := parseInitials([]byte(fixture))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	section := iv.videoSection()
	if section == nil {
		t.Fatal("没认出影片列表")
	}
	items := section.items()
	if len(items) != 2 {
		t.Fatalf("拿到 %d 条，期望 2", len(items))
	}
	// 第一条就是最新的那部
	if items[0].Created != 1792929031 || items[1].Created >= items[0].Created {
		t.Fatalf("不是按发布时间倒序: %d, %d", items[0].Created, items[1].Created)
	}
	if got := videoFromThumb(items[0], defaultBase); got == nil || got.PlatformVideoID == "" {
		t.Fatal("第一条没能转成影片")
	}
}

// sources 会以数组形状出现（2026-11 实测），写成结构体会让整页解析失败：
// 封面、标签、时长全拿不到。这里钉住两种形状都能解。
func TestDownloadSourcesShapes(t *testing.T) {
	obj := &downloadComponent{Sources: []byte(`{"mp4":{"720p":"https://a/720.mp4","480p":"https://a/480.mp4"}}`)}
	got := obj.mp4Sources()
	if len(got) != 2 || got["720p"] != "https://a/720.mp4" {
		t.Fatalf("对象形状解错了：%#v", got)
	}

	arr := &downloadComponent{Sources: []byte(`[{"mp4":{"360p":"https://a/360.mp4"}},{"mp4":{"240p":"https://a/240.mp4"}}]`)}
	got = arr.mp4Sources()
	if len(got) != 2 || got["360p"] != "https://a/360.mp4" || got["240p"] != "https://a/240.mp4" {
		t.Fatalf("数组形状解错了：%#v", got)
	}

	if got := (&downloadComponent{}).mp4Sources(); got != nil {
		t.Fatalf("空 sources 应为 nil：%#v", got)
	}
	if got := (&downloadComponent{Sources: []byte(`"nonsense"`)}).mp4Sources(); got != nil {
		t.Fatalf("认不出的形状应为 nil：%#v", got)
	}
}
