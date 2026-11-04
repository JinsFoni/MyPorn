package krx18

import (
	"net/url"
	"strings"
	"testing"
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
