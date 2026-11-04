package krx18

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/platform"
)

// fixturePage 是影片页的骨架（2026-11-04 从那台机器上抓到的真页面里摘出来的形状，
// 只留下解析用得上的节点），改站改版时先在这里对。
const fixturePage = `<html><head>
<meta property="og:image" content="https://cdnupload.com/wp-content/uploads/2026/08/office-wifes-affair-2026-af33.jpg"/>
<script type="application/ld+json">{"@context": "https://schema.org","@graph": [
{"@type": "Organization","@id": "https://krx18.com/#organization","name": "Watch Erotic Adult Movies 18+ Online Free"},
{"@type": "Movie","@id": "https://krx18.com/movies/office-wifes-affair-2026/#video","name": "Office Wife’s Affair","description": "SEO 版简介","url": "https://krx18.com/movies/office-wifes-affair-2026/","datePublished": "2026-08-30T17:15:15+07:00","image": "https://cdnupload.com/poster.jpg"}
]}</script>
</head><body>
<div class="sheader"><div class="poster"><img itemprop="image" src="https://cdnupload.com/wp-content/uploads/2026/08/poster.jpg" alt="Office Wife’s Affair"></div>
<div class="data"><h1>Office Wife&#8217;s Affair</h1>
<div class="extra"><span class='date' itemprop='dateCreated'>Aug. 30, 2026</span><span class='country'>Korea</span>
<div itemprop="description" class="wp-content"><p>The cute female employee Mijin is in a precarious situation.</p></div></div>
<div class="sgeneros"> <a href="https://krx18.com/genre/eng-sub/" rel="tag">Eng Sub</a><a href="https://krx18.com/genre/korea/" rel="tag">Korea</a> </div>
</div></div>
<div class='dooplay_player'><div id='dooplay_player_big_content'></div></div>
<span id='playernotice' data-text='151301 Views'>151301 Views</span>
<div id='playeroptions'><ul id='playeroptionsul' class='ajax_mode'><li id='player-option-1' class='dooplay_player_option' data-type='movie' data-post='88258' data-nume='1'><span class='title'>Server 1</span></li><li id='player-option-2' class='dooplay_player_option' data-type='movie' data-post='88258' data-nume='2'><span class='title'>Server 2</span><span class='server'>loadvid.com</span></li></ul></div>
<meta id='dooplay-ajax-counter' data-postid='88258'/>
</body></html>`

func TestParsePage(t *testing.T) {
	p, err := parsePage(fixturePage, "https://krx18.com/movies/office-wifes-affair-2026", "office-wifes-affair-2026")
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if p.Title != "Office Wife’s Affair" {
		t.Errorf("标题 = %q", p.Title)
	}
	if p.PostID != "88258" {
		t.Errorf("播放器编号 = %q，想要 88258", p.PostID)
	}
	if p.Thumb != "https://cdnupload.com/wp-content/uploads/2026/08/poster.jpg" {
		t.Errorf("封面 = %q", p.Thumb)
	}
	if p.Description != "The cute female employee Mijin is in a precarious situation." {
		t.Errorf("简介 = %q（应该是页面上那段剧情，不是 JSON-LD 里的 SEO 文案）", p.Description)
	}
	if p.Country != "Korea" {
		t.Errorf("国家 = %q", p.Country)
	}
	if len(p.Tags) != 2 || p.Tags[0] != "Eng Sub" || p.Tags[1] != "Korea" {
		t.Errorf("分类 = %v", p.Tags)
	}
	if p.ViewCount == nil || *p.ViewCount != 151301 {
		t.Errorf("播放量 = %v", p.ViewCount)
	}
	// 发布日期只信 JSON-LD：可见那句「Aug. 30, 2026」带着句点，格式一改就崩。
	// JSON-LD 里是 +07:00，取 UTC 之后是 8 月 30 日 10:15。
	if p.PublishedAt == nil || p.PublishedAt.UTC().Format("2006-01-02 15:04") != "2026-08-30 10:15" {
		t.Errorf("发布日期 = %v", p.PublishedAt)
	}
}

func TestParsePageMissingPlayer(t *testing.T) {
	// 没有播放器编号时不该返回半条数据：拿不到编号就换不出清单，入库等于放一条播不了的片。
	body := `<html><body><h1>Some Movie</h1></body></html>`
	p, err := parsePage(body, "https://krx18.com/movies/some-movie", "some-movie")
	if err != nil {
		t.Fatalf("parsePage: %v", err)
	}
	if p.PostID != "" {
		t.Errorf("播放器编号 = %q，想要空", p.PostID)
	}
}

const fixtureEmbed = `<html><head><meta name="csrf-token" content="abc" /></head><body>
<script>window.LoadVidConfig = {
    videoHash: 'WTcpSHlNDgcTvAUdggcU',
    videoUrl: '',
    videoToken: 'LZjwq8D0Xbq7sN5GMWocDdu02JxArKkudWj8Sxu+19fJ0L1yhP2thkPzsSgFsyePv9Mu',
    isProtected: true,
  };</script></body></html>`

func TestParseLoadvidConfig(t *testing.T) {
	cfg, err := parseLoadvidConfig(fixtureEmbed)
	if err != nil {
		t.Fatalf("parseLoadvidConfig: %v", err)
	}
	if cfg.hash != "WTcpSHlNDgcTvAUdggcU" {
		t.Errorf("hash = %q", cfg.hash)
	}
	// 真值是 464 字符的 base64（实测），这里只留一段：能原样取出来就说明正则没跑偏。
	if want := "LZjwq8D0Xbq7sN5GMWocDdu02JxArKkudWj8Sxu+19fJ0L1yhP2thkPzsSgFsyePv9Mu"; cfg.token != want {
		t.Errorf("token = %q，想要 %q", cfg.token, want)
	}
	if _, err := parseLoadvidConfig("<html>no player</html>"); err == nil {
		t.Error("没有 LoadVidConfig 时应该报错")
	}
}

// 清单片段照实测那份（557 段、伪装成 .png 的 TS、相对地址与绝对地址混着）。
const fixturePlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:8
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:7.207200,
https://server7.stream-baby1.top/user/708/hls/1/97ce22b2-d391-4423-935d-771092c294d9-1788084169/ZHNKFV1tNf5676e.png
#EXTINF:6.840167,
a60m7AV7i0857a5.png
#EXTINF:8.341667,
https://server7.stream-baby1.top/user/708/hls/1/97ce22b2-d391-4423-935d-771092c294d9-1788084169/sXZXOm7K2b62f6.png
#EXT-X-ENDLIST
`

func TestParseMediaPlaylist(t *testing.T) {
	base, _ := url.Parse("https://server7.stream-baby1.top/user/708/hls/1/97ce22b2-d391-4423-935d-771092c294d9-1788084169/ZHNKFV1tNf5676e.png")
	segs, dur := parseMediaPlaylist(fixturePlaylist, base)
	if len(segs) != 3 {
		t.Fatalf("分片数 = %d，想要 3", len(segs))
	}
	if got := segs[1].URL.String(); got != "https://server7.stream-baby1.top/user/708/hls/1/97ce22b2-d391-4423-935d-771092c294d9-1788084169/a60m7AV7i0857a5.png" {
		t.Errorf("相对地址没拼对: %s", got)
	}
	if d := dur; d < 22.3 || d > 22.5 {
		t.Errorf("总时长 = %.3f，想要 22.39（7.2072+6.840167+8.341667）", d)
	}
	if segs[0].Dur != 7.2072 {
		t.Errorf("首段时长 = %v", segs[0].Dur)
	}
}

// 老片回的就是这种 iframe HTML：2017 年的《Living with My Cousin》Server 1/2 分别指向
// streamango 和 openload，两个站都关了好几年。
func TestEmbedSrcFrom(t *testing.T) {
	cases := map[string]string{
		`https://cdn.loadvid.com/videos/play/WTcpSHlNDgcTvAUdggcU`:                                            "https://cdn.loadvid.com/videos/play/WTcpSHlNDgcTvAUdggcU",
		` <iframe src="https://openload.co/embed/BQhKGCYc4Gw/x.mp4" scrolling="no" frameborder="0"></iframe>`: "https://openload.co/embed/BQhKGCYc4Gw/x.mp4",
		`<iframe src='https://streamango.com/embed/qslcbbrbofslnmkq/' width="100%"></iframe>`:                 "https://streamango.com/embed/qslcbbrbofslnmkq/",
		"":                          "",
		"<div>no iframe here</div>": "",
	}
	for in, want := range cases {
		if got := embedSrcFrom(in); got != want {
			t.Errorf("embedSrcFrom(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.krx18.com/movies/x/":         "krx18.com",
		"https://cdn.loadvid.com/videos/play/abc": "cdn.loadvid.com",
		"": "",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestHighestChildPlaylist(t *testing.T) {
	body := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1280x720
720/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1920x1080
1080/index.m3u8
`
	base, _ := url.Parse("https://cdn.example.com/videos/play/abc")
	got := highestChildPlaylist(body, base)
	if got == nil || got.String() != "https://cdn.example.com/videos/play/1080/index.m3u8" {
		t.Fatalf("挑子清单 = %v，想要带宽最高那条", got)
	}
	// 媒体清单（没有 #EXT-X-STREAM-INF）不该被当成主清单。
	if got := highestChildPlaylist(fixturePlaylist, base); got != nil {
		t.Errorf("媒体清单返回了子清单 %v", got)
	}
}

func TestParseVideoURL(t *testing.T) {
	a := &Adapter{base: "https://krx18.com"}
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"https://krx18.com/movies/office-wifes-affair-2026", "office-wifes-affair-2026", false},
		{"https://krx18.com/movies/office-wifes-affair-2026/", "office-wifes-affair-2026", false},
		{"https://www.krx18.com/movies/abc?x=1", "abc", false},
		{"https://krx18.com/genre/korea/", "", true},
		{"https://krx18.com/", "", true},
		{"https://pornhub.com/view_video.php?viewkey=1", "", true},
		{"not a url", "", true},
	}
	for _, c := range cases {
		got, err := a.ParseVideoURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: 想要报错，却拿到 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: id = %q，想要 %q", c.in, got, c.want)
		}
	}
}

func TestPlaylistPath(t *testing.T) {
	a := &Adapter{base: "https://krx18.com"}
	got := a.PlaylistPath("office-wifes-affair-2026")
	if got != "/api/krx18/playlist.m3u8?v=office-wifes-affair-2026" {
		t.Errorf("清单地址 = %q", got)
	}
	// 播放页里的 hls 判定是「地址里带 .m3u8」，这条路径必须能命中。
	if !strings.Contains(got, ".m3u8") {
		t.Errorf("%q 里没有 .m3u8，播放器不会走 hls.js", got)
	}
}

func TestCodecTag(t *testing.T) {
	for in, want := range map[string]string{"h264": "avc1", "hevc": "hev1", "av1": "av01", "mpeg4": "mpeg4"} {
		if got := codecTag(in); got != want {
			t.Errorf("codecTag(%q) = %q，想要 %q", in, got, want)
		}
	}
}

// fixtureTag 是 tag 归档页的真片段（/tag/do-hee-%eb%8f%84%ed%9d%ac/ 的第 1、2 条 + 分页行），
// 实测那一页共 3 页 92 部、第 1 页 39 部。
const fixtureTag = `<article id="post-89610" class="item movies"><div class="poster"><img src="https://cdnupload.com/wp-content/uploads/2026/09/secret-sex-life-of-a-married-woman-who-has-explosive-orgasms-e7ac.jpg" alt="Secret Sex Life of a Married Woman Who Has Explosive Orgasms"><div class="rating">0</div><div class="mepo"> <span class="quality">HD-ENG</span></div><a href="https://krx18.com/movies/89610-secret-sex-life-of-a-married-woman-who-has-explosive-orgasms-2026/"><div class="see play4"></div></a></div><div class="data"><h3><a href="https://krx18.com/movies/89610-secret-sex-life-of-a-married-woman-who-has-explosive-orgasms-2026/">Secret Sex Life of a Married Woman Who Has Explosive Orgasms</a></h3> <span>Sep. 19, 2026</span></div> <div class="animation-1 dtinfo"> <div class="title"> <h4>Secret Sex Life of a Married Woman Who Has Explosive Orgasms</h4> </div> <div class="metadata"> <span>2026</span> <span>44117 views</span> </div> <div class="texto">A married woman leads a dangerous double life, using her social club activities as an excuse to sneak out and carry on secret affairs with other ...</div> <div class="genres"><div class="mta"><a href="https://krx18.com/genre/eng-sub/" rel="tag">Eng Sub</a><a href="https://krx18.com/genre/korea/" rel="tag">Korea</a></div></div> </div> </article>
<article id="post-89267" class="item movies"><div class="poster"><img src="https://cdnupload.com/wp-content/uploads/2026/09/fortress-of-desire-2026-49c1.jpg" alt="Fortress of Desire"><div class="rating">0</div><div class="mepo"> <span class="quality">HD-ENG</span></div><a href="https://krx18.com/movies/fortress-of-desire-2026/"><div class="see play4"></div></a></div><div class="data"><h3><a href="https://krx18.com/movies/fortress-of-desire-2026/">Fortress of Desire</a></h3> <span>Sep. 13, 2026</span></div> <div class="animation-1 dtinfo"> <div class="title"> <h4>Fortress of Desire</h4> </div> <div class="metadata"> <span>2026</span> <span>51168 views</span> </div> <div class="texto">&#8220;To have that man, I joined hands with his wife&#8221; A deadly adult melodrama thriller about four men and women driven by an unstoppable ...</div> <div class="genres"><div class="mta"><a href="https://krx18.com/genre/eng-sub/" rel="tag">Eng Sub</a><a href="https://krx18.com/genre/korea/" rel="tag">Korea</a></div></div> </div> </article>
<div class="pagination"><span>Page 1 of 3</span><span class="current">1</span><a href='https://krx18.com/tag/do-hee-%EB%8F%84%ED%9D%AC/page/2/' class="inactive">2</a><a href='https://krx18.com/tag/do-hee-%EB%8F%84%ED%9D%AC/page/3/' class="inactive">3</a></div>`

func TestParseTagPage(t *testing.T) {
	page, err := parseTagPage(fixtureTag, "https://krx18.com/tag/do-hee-%EB%8F%84%ED%9D%AC/")
	if err != nil {
		t.Fatalf("parseTagPage 报错: %v", err)
	}
	// u.Path 解过码，slug 就是站点自己的那一份（do-hee-도희），标签接口回的也是它。
	if page.Slug != "do-hee-도희" {
		t.Errorf("slug = %q", page.Slug)
	}
	if page.TotalPages != 3 {
		t.Errorf("总页数 = %d，想要 3（分页行写着 Page 1 of 3）", page.TotalPages)
	}
	if len(page.Items) != 2 {
		t.Fatalf("条数 = %d，想要 2", len(page.Items))
	}
	first := page.Items[0]
	if first.PlatformVideoID != "89610-secret-sex-life-of-a-married-woman-who-has-explosive-orgasms-2026" {
		t.Errorf("slug = %q", first.PlatformVideoID)
	}
	if first.Title == "" || first.ThumbURL == "" || first.URL == "" {
		t.Errorf("标题/封面/地址有空的: %+v", first)
	}
	if first.PublishedAt == nil || first.PublishedAt.Format("2006-01-02") != "2026-09-19" {
		t.Errorf("发布日期 = %v，想要 2026-09-19（页面上是 Sep. 19, 2026）", first.PublishedAt)
	}
	if first.ViewCount == nil || *first.ViewCount != 44117 {
		t.Errorf("播放量 = %v，想要 44117", first.ViewCount)
	}
	if len(first.Tags) != 2 || first.Tags[0] != "Eng Sub" || first.Tags[1] != "Korea" {
		t.Errorf("分类 = %v", first.Tags)
	}
	// 列表页不写时长：留 0，等播放时从清单的 #EXTINF 补。
	if first.DurationSec != 0 {
		t.Errorf("时长 = %d，列表页不写时长，应该留 0", first.DurationSec)
	}
}

func TestParsePerformerURL(t *testing.T) {
	a := &Adapter{base: "https://krx18.com"}
	// 地址栏里是 percent-encoded，u.Path 解过码：返回的是站点自己那份 slug（do-hee-도희），
	// 标签接口回的 slug 字段也是这一份，两边才对得上。
	ok := map[string]string{
		"https://krx18.com/tag/do-hee-%EB%8F%84%ED%9D%AC/":            "do-hee-도희",
		"https://krx18.com/tag/do-hee-%EB%8F%84%ED%9D%AC":             "do-hee-도희",
		"https://www.krx18.com/tag/do-hee-%EB%8F%84%ED%9D%AC/page/2/": "do-hee-도희",
		"https://krx18.com/tag/big-tits/":                             "big-tits",
	}
	for in, want := range ok {
		got, err := a.ParsePerformerURL(in)
		if err != nil || got != want {
			t.Errorf("ParsePerformerURL(%q) = %q, %v；想要 %q", in, got, err, want)
		}
	}
	// 影片页不是订阅源（那是 ParseVideoURL 的活），别让它被误当成一个 tag。
	for _, in := range []string{
		"https://krx18.com/movies/office-wifes-affair-2026/",
		"https://krx18.com/tag/",
		"https://example.com/tag/x/",
	} {
		if got, err := a.ParsePerformerURL(in); err == nil {
			t.Errorf("ParsePerformerURL(%q) 该报错，却给了 %q", in, got)
		}
	}
}

func TestSlugOfMovieURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://krx18.com/movies/fortress-of-desire-2026/": "fortress-of-desire-2026",
		"https://krx18.com/movies/89724-tisay-2016":         "89724-tisay-2016",
		"https://krx18.com/tag/x/":                          "",
		"":                                                  "",
	} {
		if got := slugOfMovieURL(in); got != want {
			t.Errorf("slugOfMovieURL(%q) = %q，想要 %q", in, got, want)
		}
	}
}

// TestAdapterSubscription 用本地 httptest 顶替 krx18，把「订阅一个 tag」这条路走一遍：
// 地址 → slug → 标签接口 → 归档页 → 最后翻过末页时 404 变 ErrNotFound。
//
// 断言的几个点都是真机上踩过的：
//   - slug 是 percent-decoded 的那一份（do-hee-도희），拼回地址时必须重新编码；
//   - 名字/片数走 /wp-json/wp/v2/tags（影片的 posts 接口是空的，标签接口有货）；
//   - 翻过末页 WordPress 回 404，同步任务靠 ErrNotFound 认「到头了」。
func TestAdapterSubscription(t *testing.T) {
	var gotTagURL, gotPageURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/wp-json/wp/v2/tags", func(w http.ResponseWriter, r *http.Request) {
		gotTagURL = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("slug") == "do-hee-도희" {
			// 真机就是编码态（实测 /wp-json/wp/v2/tags?search=do%20hee 回的 slug 是
			// "do-hee-%eb%8f%84%ed%9d%ac"，小写十六进制）。
			_, _ = w.Write([]byte(`[{"id":5247,"count":92,"name":"Do Hee (도희)",` +
				`"slug":"do-hee-%eb%8f%84%ed%9d%ac",` +
				`"link":"https://krx18.com/tag/do-hee-%eb%8f%84%ed%9d%ac/"}]`))
			return
		}
		if r.URL.Query().Get("search") == "do hee" {
			_, _ = w.Write([]byte(`[{"id":5247,"count":92,"name":"Do Hee (도희)",` +
				`"slug":"do-hee-%eb%8f%84%ed%9d%ac","link":""}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/tag/do-hee-도희/", func(w http.ResponseWriter, r *http.Request) {
		gotPageURL = r.URL.EscapedPath()
		_, _ = w.Write([]byte(fixtureTag))
	})
	mux.HandleFunc("/tag/do-hee-도희/page/2/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "<html>nothing here</html>", http.StatusNotFound)
	})
	// 用 TLS 的 httptest：适配器那条 uTLS 传输只讲 HTTP/2，明文 http:// 会报
	// 「http2: unencrypted HTTP/2 not enabled」，所以这里换成测试服务器自己的客户端。
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	a, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.client = srv.Client()

	slug, err := a.ParsePerformerURL(srv.URL + "/tag/do-hee-%EB%8F%84%ED%9D%AC/")
	if err != nil || slug != "do-hee-도희" {
		t.Fatalf("ParsePerformerURL = %q, %v", slug, err)
	}

	p, err := a.GetPerformer(t.Context(), slug)
	if err != nil {
		t.Fatalf("GetPerformer: %v", err)
	}
	if p.Name != "Do Hee (도희)" || p.VideoCount == nil || *p.VideoCount != 92 {
		t.Errorf("艺人 = %+v，想要「Do Hee (도희)」/ 92 部", p)
	}
	// 接口回的 slug 是编码态，入到我们这边的标识必须是解码态：地址栏、入库、拼
	// 归档页地址三处只有一种形态才对得上（见 canonicalTagSlug 的注释）。
	if p.Platform != "krx18" || p.PerformerID != "do-hee-도희" {
		t.Errorf("平台 / 标识 = %q / %q", p.Platform, p.PerformerID)
	}
	if !strings.Contains(gotTagURL, "_fields=") {
		t.Errorf("标签接口的查询串 = %q，_fields 没带上（整行 900 多字节没必要）", gotTagURL)
	}

	page, err := a.ListPerformerVideos(t.Context(), slug, 1)
	if err != nil {
		t.Fatalf("ListPerformerVideos: %v", err)
	}
	if len(page.Items) != 2 || page.TotalPages != 3 || !page.HasMore {
		t.Errorf("第 1 页 = %d 条 / 总页数 %d / 还有更多 %v", len(page.Items), page.TotalPages, page.HasMore)
	}
	if page.Items[0].Platform != "krx18" {
		t.Errorf("入参没写 platform，同步任务会把它当别的站: %+v", page.Items[0])
	}
	// 非 ASCII 的 slug 拼回地址时必须重新编码，否则服务器 404。
	if gotPageURL != "/tag/do-hee-%EB%8F%84%ED%9D%AC/" {
		t.Errorf("请求路径 = %q，想要 /tag/do-hee-%%EB%%8F%%84%%ED%%9D%%AC/", gotPageURL)
	}

	// 最后一页之后的 404 = 「到头了」，必须是 ErrNotFound（同步任务据此收工）。
	if _, err := a.ListPerformerVideos(t.Context(), slug, 2); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("翻过末页的错 = %v，想要 platform.ErrNotFound", err)
	}

	// 标签没了也是 ErrNotFound，不能变成「同步永久失败」。
	if _, err := a.GetPerformer(t.Context(), "gone-tag"); !errors.Is(err, platform.ErrNotFound) {
		t.Errorf("不存在的 tag 的错 = %v，想要 platform.ErrNotFound", err)
	}

	list, more, err := a.SearchPerformers(t.Context(), "do hee", 1)
	if err != nil || len(list) != 1 || more {
		t.Fatalf("SearchPerformers = %d 条 / 还有更多 %v / %v", len(list), more, err)
	}
	if list[0].PerformerID != "do-hee-도희" {
		t.Errorf("候选的标识要归一成解码态（存库/拼地址都用它），实际 %q", list[0].PerformerID)
	}
	if list[0].URL != srv.URL+"/tag/do-hee-%EB%8F%84%ED%9D%AC/" {
		t.Errorf("标签接口没给 link 时要用 a.base 拼一个归档地址（非 ASCII 要编码），实际 %q", list[0].URL)
	}
}

func TestCanonicalTagSlug(t *testing.T) {
	for in, want := range map[string]string{
		"do-hee-%eb%8f%84%ed%9d%ac": "do-hee-도희", // 标签接口回的形态
		"do-hee-도희":                 "do-hee-도희", // 地址栏解出来的形态
		"big-tits":                  "big-tits",  // 纯 ASCII 不受影响
		"":                          "",
	} {
		if got := canonicalTagSlug(in); got != want {
			t.Errorf("canonicalTagSlug(%q) = %q，想要 %q", in, got, want)
		}
	}
}
