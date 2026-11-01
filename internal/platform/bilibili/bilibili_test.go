package bilibili

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/JinsFoni/MyPorn/internal/platform"
)

// newTestAdapter 把一个假上游接上来：adapter 的 api 字段指到 httptest，
// 限速器换成不限速、退避清零，这样用例跑起来是毫秒级的。
func newTestAdapter(t *testing.T, h http.HandlerFunc) (*Adapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	a, err := New("https://www.bilibili.com", nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.api = srv.URL
	a.app = srv.URL
	a.lim = rate.NewLimiter(rate.Inf, 1)
	a.listLim = rate.NewLimiter(rate.Inf, 1)
	old := retryBackoff
	retryBackoff = []time.Duration{0, 0}
	t.Cleanup(func() { retryBackoff = old })
	return a, srv
}

func TestParsePerformerURL(t *testing.T) {
	a, _ := New("https://www.bilibili.com", nil, nil)
	cases := []struct {
		in   string
		want string
	}{
		{"https://space.bilibili.com/946974", "946974"},
		{"https://space.bilibili.com/946974/video?spm_id_from=333.999.0.0", "946974"},
		{"https://space.bilibili.com/946974/?spm=1", "946974"},
		{"http://m.bilibili.com/space/946974", "946974"},
		{"space.bilibili.com/946974", "946974"},
		{"  946974  ", "946974"},
		{"https://www.bilibili.com/video/BV1xx411c7mD", ""},
		{"https://example.com/space/1", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, err := a.ParsePerformerURL(c.in)
		if c.want == "" {
			if err == nil {
				t.Errorf("ParsePerformerURL(%q) = %q，期望报错", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePerformerURL(%q) 报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePerformerURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseLength(t *testing.T) {
	cases := map[string]int{"24:52": 1492, "1:02:05": 3725, "0:07": 7, "": 0, "abc": 0}
	for in, want := range cases {
		if got := parseLength(in); got != want {
			t.Errorf("parseLength(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestThumbURL(t *testing.T) {
	got := thumbURL("http://i0.hdslb.com/bfs/archive/abc.jpg")
	want := "https://i0.hdslb.com/bfs/archive/abc.jpg@960w_540h_1c.webp"
	if got != want {
		t.Errorf("thumbURL = %q, want %q", got, want)
	}
	// 已经带 @ 后缀的不再叠一层（叠了 B 站图床会 404）。
	raw := "https://i0.hdslb.com/bfs/archive/abc.jpg@480w_270h_1c.webp"
	if got := thumbURL(raw); got != raw {
		t.Errorf("thumbURL 重复加后缀: %q", got)
	}
	if got := thumbURL(""); got != "" {
		t.Errorf("thumbURL(\"\") = %q", got)
	}
}

func TestGetPerformer(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x/web-interface/card" || r.URL.Query().Get("mid") != "946974" {
			t.Errorf("意外的请求: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"code":0,"message":"0","data":{
			"card":{"mid":"946974","name":"影视飓风","face":"https://i0.hdslb.com/bfs/face/x.jpg",
			        "sign":"无限进步","sex":"男","level_info":{"current_level":6}},
			"follower":18839136,"archive_count":951}}`)
	})
	p, err := a.GetPerformer(context.Background(), "946974")
	if err != nil {
		t.Fatalf("GetPerformer: %v", err)
	}
	if p.Name != "影视飓风" || p.PerformerID != "946974" || p.Platform != "bilibili" {
		t.Errorf("资料字段不对: %+v", p)
	}
	if p.Bio != "无限进步" {
		t.Errorf("签名没落到简介: %q", p.Bio)
	}
	if p.VideoCount == nil || *p.VideoCount != 951 {
		t.Errorf("投稿数不对: %v", p.VideoCount)
	}
	if p.SubscriberCount == nil || *p.SubscriberCount != 18839136 {
		t.Errorf("粉丝数不对: %v", p.SubscriberCount)
	}
	if want := "https://www.bilibili.com/space/946974"; p.URL != want {
		t.Errorf("主页地址 = %q, want %q", p.URL, want)
	}
	if len(p.Profile) != 2 {
		t.Errorf("资料行 = %+v，期望性别与等级两行", p.Profile)
	}
}

func TestListPerformerVideos(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path == "/x/web-interface/nav" {
			fmt.Fprint(w, `{"code":0,"data":{"wbi_img":{
				"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
				"sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`)
			return
		}
		// 翻页走的是 wbi 签名那条（见 wbi.go）：路径和签名都要对。
		if r.URL.Path != "/x/space/wbi/arc/search" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		if q.Get("wts") == "" || len(q.Get("w_rid")) != 32 {
			t.Errorf("签名参数缺失: %s", r.URL.RawQuery)
		} else if want := wbiSignatureOf(q, testMixinKey); q.Get("w_rid") != want {
			t.Errorf("w_rid 校验不通过: got %s want %s（query=%s）", q.Get("w_rid"), want, r.URL.RawQuery)
		}
		if q.Get("mid") != "946974" || q.Get("pn") != "2" || q.Get("order") != "pubdate" {
			t.Errorf("分页参数不对: %s", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"code":0,"message":"0","data":{
			"page":{"pn":2,"ps":50,"count":951},
			"list":{"vlist":[
				{"bvid":"BV1xx411c7mD","title":"第一支","pic":"http://i0.hdslb.com/a.jpg","length":"24:52",
				 "created":1700000000,"play":1234,"description":"简介","tname":"影视"},
				{"bvid":"","title":"合集条目（没有 bvid，应当跳过）"},
				{"bvid":"BV1yy411c7mE","title":"第二支","length":"1:02:05","created":1690000000}
			]}}}`)
	})
	page, err := a.ListPerformerVideos(context.Background(), "946974", 2)
	if err != nil {
		t.Fatalf("ListPerformerVideos: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("条目数 = %d，期望 2（合集那条要被跳过）", len(page.Items))
	}
	first := page.Items[0]
	if first.PlatformVideoID != "BV1xx411c7mD" || first.Title != "第一支" {
		t.Errorf("第一条不对: %+v", first)
	}
	if first.DurationSec != 1492 {
		t.Errorf("时长 = %d, want 1492", first.DurationSec)
	}
	if first.URL != "https://www.bilibili.com/video/BV1xx411c7mD" {
		t.Errorf("影片地址 = %q", first.URL)
	}
	if !strings.Contains(first.ThumbURL, "@960w") {
		t.Errorf("封面没换成缩略图: %q", first.ThumbURL)
	}
	if first.PublishedAt == nil || first.PublishedAt.Unix() != 1700000000 {
		t.Errorf("发布时间不对: %v", first.PublishedAt)
	}
	if first.ViewCount == nil || *first.ViewCount != 1234 {
		t.Errorf("播放数不对: %v", first.ViewCount)
	}
	// count=951、ps=50 → 20 页；第 2 页还有下一页。
	if page.TotalPages != 20 || !page.HasMore {
		t.Errorf("分页信息不对: total=%d hasMore=%v", page.TotalPages, page.HasMore)
	}
}

func TestRateLimitMapping(t *testing.T) {
	var hits int32
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		fmt.Fprint(w, `{"code":-799,"message":"请求过于频繁，请稍后再试","ttl":1}`)
	})
	_, err := a.GetPerformer(context.Background(), "946974")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("期望 ErrRateLimited，得到 %v", err)
	}
	// 三次都失败（首次 + 两次退避重试）才放弃。
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("请求次数 = %d, want 3", got)
	}

	// HTTP 412 是同一套风控的另一种脸，也归到 ErrRateLimited。
	var hits412 int32
	b, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits412, 1)
		w.WriteHeader(http.StatusPreconditionFailed)
	})
	if _, err := b.GetPerformer(context.Background(), "946974"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("412 期望 ErrRateLimited，得到 %v", err)
	}
}

func TestRateLimitRecovers(t *testing.T) {
	var n int32
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			fmt.Fprint(w, `{"code":-799,"message":"too frequent"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"card":{"mid":"1","name":"恢复了的 UP 主"}}}`)
	})
	p, err := a.GetPerformer(context.Background(), "1")
	if err != nil {
		t.Fatalf("第二次应当成功: %v", err)
	}
	if p.Name != "恢复了的 UP 主" {
		t.Errorf("名字 = %q", p.Name)
	}
}

func TestNotFoundMapping(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":-404,"message":"啥都木有"}`)
	})
	_, err := a.GetPerformer(context.Background(), "946974")
	if !errors.Is(err, platform.ErrNotFound) {
		t.Fatalf("期望 platform.ErrNotFound，得到 %v", err)
	}
}

func TestCookieHeaderSent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Cookie")
		fmt.Fprint(w, `{"code":0,"data":{"card":{"mid":"1","name":"n"}}}`)
	}))
	defer srv.Close()
	a, err := New("https://www.bilibili.com", nil, func() string { return "SESSDATA=abc" })
	if err != nil {
		t.Fatal(err)
	}
	a.api = srv.URL
	a.lim = rate.NewLimiter(rate.Inf, 1)
	if _, err := a.GetPerformer(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if got != "SESSDATA=abc" {
		t.Errorf("Cookie 头 = %q", got)
	}
}

// viewJSON / dashJSON / durlJSON 是三份真实形状的响应（字段名照抄实测返回）。
const viewJSON = `{"code":0,"data":{
	"bvid":"BV1xx411c7mD","aid":1,"title":"测试影片","pic":"https://i0.hdslb.com/bfs/archive/p.jpg",
	"desc":"简介","pubdate":1700000000,"duration":286,"tname":"影视",
	"owner":{"mid":946974,"name":"影视飓风","face":"f.jpg"},
	"stat":{"view":999},"pages":[{"cid":123456,"page":1,"part":"P1","duration":286}]}}`

// 未登录的真实形态：dash 只有 360/480，durl 给到 720。
const dashJSON = `{"code":0,"data":{"quality":64,"timelength":286000,
	"dash":{"duration":286,
		"video":[
			{"id":16,"baseUrl":"https://xy.pcdn.edge.mountaintoys.cn/v16.m4s?x=1","backupUrl":["https://upos-sz-estgcos.bilivideo.com/v16.m4s?x=1"],"bandwidth":200000,"codecs":"avc1.64001e","width":640,"height":360,"frameRate":"30"},
			{"id":32,"baseUrl":"https://xy.pcdn.edge.mountaintoys.cn/v32.m4s?x=1","backupUrl":["https://upos-sz-estgcos.bilivideo.com/v32.m4s?x=1"],"bandwidth":500000,"codecs":"avc1.64001f","width":854,"height":480,"frameRate":"30"},
			{"id":64,"baseUrl":"https://xy.pcdn.edge.mountaintoys.cn/v64.m4s?x=1","backupUrl":["https://upos-sz-estgcos.bilivideo.com/v64.m4s?x=1"],"bandwidth":1500000,"codecs":"av01.0.08M.08","width":1280,"height":720,"frameRate":"30"}
		],
		"audio":[
			{"id":30216,"baseUrl":"https://xy.pcdn.edge.mountaintoys.cn/a1.m4s?x=1","backupUrl":["https://upos-sz-estgcos.bilivideo.com/a1.m4s?x=1"],"bandwidth":64000,"codecs":"mp4a.40.2"},
			{"id":30280,"baseUrl":"https://xy.pcdn.edge.mountaintoys.cn/a2.m4s?x=1","backupUrl":["https://upos-sz-estgcos.bilivideo.com/a2.m4s?x=1"],"bandwidth":192000,"codecs":"mp4a.40.2"}
		]}}}`

const durlJSON = `{"code":0,"data":{"quality":64,"format":"mp4720","timelength":286000,
	"accept_quality":[64,32,16],"accept_description":["高清 720P","清晰 480P","流畅 360P"],
	"durl":[{"order":1,"length":286000,"size":25000000,"url":"https://xy.pcdn.edge.mountaintoys.cn/d.mp4?x=1",
	         "backup_url":["https://upos-sz-estgcos.bilivideo.com/d.mp4?x=1"]}]}}`

// fakeBili 按路径分派 view / playurl（fnval 区分 dash 与 durl）。
func fakeBili(t *testing.T, dash, durl string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/view":
			fmt.Fprint(w, viewJSON)
		case "/x/player/playurl":
			if r.URL.Query().Get("fnval") == "0" {
				fmt.Fprint(w, durl)
				return
			}
			fmt.Fprint(w, dash)
		default:
			t.Errorf("意外的请求: %s", r.URL.Path)
		}
	}
}

func TestResolveStreamsPreferDurl(t *testing.T) {
	a, _ := newTestAdapter(t, fakeBili(t, dashJSON, durlJSON))
	vs, err := a.ResolveStreams(context.Background(), "BV1xx411c7mD")
	if err != nil {
		t.Fatalf("ResolveStreams: %v", err)
	}
	// 720 是 durl 那一档：同分辨率的 DASH（av1 720）要被丢掉，不能出现两条 720p。
	if len(vs) != 3 {
		t.Fatalf("档位数 = %d，期望 3（720 durl + 480 dash + 360 dash）: %+v", len(vs), vs)
	}
	if vs[0].Height != 720 || vs[0].AudioURL != "" {
		t.Errorf("第一档应当是 durl 720P 且自带声音: %+v", vs[0])
	}
	if vs[0].Codec != "avc" {
		t.Errorf("durl 编码应当是 avc，得到 %q", vs[0].Codec)
	}
	// 体积预估用的码率：25MB / 286s ≈ 700kbps（必须有值，否则下载预估是 0）。
	if vs[0].Bandwidth < 600000 || vs[0].Bandwidth > 800000 {
		t.Errorf("durl 码率 = %d，期望约 700000", vs[0].Bandwidth)
	}
	// 下面的档位是 DASH：必须带音轨地址，码率含音频。
	for _, v := range vs[1:] {
		if v.AudioURL == "" {
			t.Errorf("DASH 档位缺音轨地址: %+v", v)
		}
	}
	if vs[1].Height != 480 {
		t.Fatalf("第二档 height = %d, want 480", vs[1].Height)
	}
	if want := 500000 + 192000; vs[1].Bandwidth != want {
		t.Errorf("DASH 码率 = %d, want %d（视频 + 最高音轨）", vs[1].Bandwidth, want)
	}
	// 音轨取最高那条（30280）。
	if !strings.Contains(vs[1].AudioURL, "a2.m4s") {
		t.Errorf("音轨不是最高档: %q", vs[1].AudioURL)
	}
	// 地址要挑官方 CDN，别用 PCDN（实测慢 7 倍）。
	if !strings.Contains(vs[1].URL, "upos-sz-estgcos.bilivideo.com") {
		t.Errorf("没挑官方 CDN: %q", vs[1].URL)
	}
}

func TestResolveStreamsDashOnly(t *testing.T) {
	// 有的投稿只给 DASH（没有 durl）：这时 720P 的 DASH 档必须留下来。
	a, _ := newTestAdapter(t, fakeBili(t, dashJSON, `{"code":0,"data":{}}`))
	vs, err := a.ResolveStreams(context.Background(), "BV1xx411c7mD")
	if err != nil {
		t.Fatalf("ResolveStreams: %v", err)
	}
	if len(vs) != 3 {
		t.Fatalf("档位数 = %d, want 3: %+v", len(vs), vs)
	}
	if vs[0].Height != 720 || vs[0].AudioURL == "" {
		t.Errorf("第一档应当是 DASH 720p（带音轨）: %+v", vs[0])
	}
	if vs[0].ID() != "720p.av1" {
		t.Errorf("720 那一档的 id = %q, want 720p.av1（码率为 1.5M 比 480 高，且 AV1 优先）", vs[0].ID())
	}
}

func TestResolveStreamsFailBoth(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/web-interface/view" {
			fmt.Fprint(w, viewJSON)
			return
		}
		fmt.Fprint(w, `{"code":-404,"message":"no playurl"}`)
	})
	if _, err := a.ResolveStreams(context.Background(), "BV1xx411c7mD"); err == nil {
		t.Fatal("两份都拿不到时应当报错")
	}
}

func TestResolvePlaybackReturnsMeta(t *testing.T) {
	a, _ := newTestAdapter(t, fakeBili(t, dashJSON, durlJSON))
	v, vs, err := a.ResolvePlayback(context.Background(), "BV1xx411c7mD")
	if err != nil {
		t.Fatalf("ResolvePlayback: %v", err)
	}
	if v == nil || v.Title != "测试影片" || v.DurationSec != 286 {
		t.Fatalf("元数据不对: %+v", v)
	}
	if len(v.Performers) != 1 || v.Performers[0].Name != "影视飓风" {
		t.Errorf("UP 主没带上: %+v", v.Performers)
	}
	if len(vs) == 0 {
		t.Error("没有拿到画质")
	}
}

func TestPickMediaURL(t *testing.T) {
	// PCDN 优先跳官方 CDN
	got := pickMediaURL("https://x.edge.mountaintoys.cn/a.m4s",
		[]string{"https://upos-sz-mirrorcos.bilivideo.com/a.m4s"})
	if !strings.Contains(got, "bilivideo.com") {
		t.Errorf("没换到官方 CDN: %q", got)
	}
	// 这条是 2026-10 的真 bug：PCDN 的域名也以 bilivideo.cn 结尾，
	// 早先的「后缀命中就算官方源」会把主地址（PCDN）当成官方源直接返回。
	got = pickMediaURL("https://cn-hncs-cm-03-08.bilivideo.com:8082/a.m4s",
		[]string{"https://upos-sz-mirrorcoso1.bilivideo.com/a.m4s"})
	if !strings.Contains(got, "upos-sz-mirrorcoso1") {
		t.Errorf("带 8082 端口的 PCDN 主地址应当让位给官方源: %q", got)
	}
	got = pickMediaURL("https://xy183x234x98x139xy.mcdn.bilivideo.cn:8082/a.m4s",
		[]string{"https://cn-hncs-cm-03-08.bilivideo.com/a.m4s"})
	if !strings.Contains(got, "cn-hncs-cm-03-08") {
		t.Errorf("mcdn 主地址应当让位给官方源: %q", got)
	}
	// 没有官方 CDN 就退回主地址（PCDN 慢但能放）
	got = pickMediaURL("https://x.mcdn.bilivideo.cn/a.m4s", []string{"https://y.edge.mountaintoys.cn/a.m4s"})
	if !strings.Contains(got, "mcdn.bilivideo.cn") {
		t.Errorf("只剩 PCDN 时主地址该被选中: %q", got)
	}
	got = pickMediaURL("https://y.edge.mountaintoys.cn/a.m4s", nil)
	if !strings.Contains(got, "mountaintoys") {
		t.Errorf("只剩 PCDN 时也应当给一条: %q", got)
	}
	// 官方源之间保持原有顺序（主地址优先）
	if got := pickMediaURL("https://upos-sz-estghw.bilivideo.com/a.m4s",
		[]string{"https://upos-sz-mirrorcos.bilivideo.com/a.m4s"}); !strings.Contains(got, "estghw") {
		t.Errorf("官方主地址应当优先: %q", got)
	}
	if got := pickMediaURL("", nil); got != "" {
		t.Errorf("空地址应当返回空串: %q", got)
	}
}

func TestIsPCDNURL(t *testing.T) {
	// 官方源不能被误伤：它们的协议、端口、路径都和 PCDN 一样，只有主机名不同。
	official := []string{
		"https://upos-sz-mirrorcoso1.bilivideo.com/upgcxcode/a.m4s?e=1",
		"https://cn-hncs-cm-03-08.bilivideo.com/upgcxcode/a.m4s",
		"https://upos-hz-mirrorakam.akamaized.net/upgcxcode/a.m4s",
	}
	for _, u := range official {
		if isPCDNURL(u) {
			t.Errorf("官方源被判成了 PCDN: %s", u)
		}
	}
	pcdn := []string{
		"https://xy183x234x98x139xy.mcdn.bilivideo.cn:8082/v1/resource/upgcxcode/a.m4s",
		"https://b-baacc04b083cltm107bs0ym3d1a.edge.mountaintoys.cn:4483/a.m4s",
		"https://x.mcdn.bilivideo.cn/a.m4s", // 没写端口也要认出来
	}
	for _, u := range pcdn {
		if !isPCDNURL(u) {
			t.Errorf("PCDN 没认出来: %s", u)
		}
	}
	if isPCDNURL("这不是地址") {
		t.Error("解析不了的串不该算 PCDN")
	}
}

func TestNormalizeUnrelatedJSON(t *testing.T) {
	// data 为 null（例如未登录调空间列表第 2 页）要给出明确错误，而不是静默返回空列表。
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"message":"0","data":null}`)
	})
	if _, err := a.GetPerformer(context.Background(), "1"); err == nil {
		t.Fatal("data 为 null 时应当报错")
	}
	var raw json.RawMessage
	_ = raw.UnmarshalJSON
}

func TestListPerformerVideosPage1UsesApp(t *testing.T) {
	var sign, ts string
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/x/v2/space/archive/cursor" {
			t.Errorf("第 1 页应当打 App 接口，实际打了 %s", r.URL.Path)
			return
		}
		q := r.URL.Query()
		if q.Get("vmid") != "946974" || q.Get("order") != "pubdate" || q.Get("ps") != "20" {
			t.Errorf("App 接口参数不对: %s", r.URL.RawQuery)
		}
		sign, ts = q.Get("sign"), q.Get("ts")
		fmt.Fprint(w, `{"code":0,"message":"0","data":{"count":939,"has_next":true,"next_cursor":117060323580501,
			"item":[
				{"bvid":"BV1xrnz6gEVD","title":"折1万次，会坏吗","cover":"http://i0.hdslb.com/c.jpg",
				 "duration":1492,"ctime":1792584000,"play":12345,"tname":"数码","goto":"av"},
				{"bvid":"BV1pgc000001","title":"番剧条目","goto":"pgc","duration":1440},
				{"bvid":"BV1pGnn67EbB","title":"第二支","duration":2682,"ctime":1792400400,"goto":"av"}
			]}}`)
	})
	page, err := a.ListPerformerVideos(context.Background(), "946974", 1)
	if err != nil {
		t.Fatalf("ListPerformerVideos: %v", err)
	}
	if sign == "" || ts == "" {
		t.Errorf("签名参数没带上: sign=%q ts=%q", sign, ts)
	}
	// 番剧（goto=pgc）要被过滤掉，只剩两条投稿。
	if len(page.Items) != 2 {
		t.Fatalf("条目数 = %d，期望 2: %+v", len(page.Items), page.Items)
	}
	first := page.Items[0]
	if first.PlatformVideoID != "BV1xrnz6gEVD" || first.DurationSec != 1492 {
		t.Errorf("第一条不对: %+v", first)
	}
	if len(first.Tags) != 1 || first.Tags[0] != "数码" {
		t.Errorf("分区没当标签: %+v", first.Tags)
	}
	// 939 条 / 每页 20 = 47 页。
	if page.TotalPages != 47 || !page.HasMore {
		t.Errorf("分页信息不对: total=%d hasMore=%v", page.TotalPages, page.HasMore)
	}
}

func TestListPage1FallsBackToWeb(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/v2/space/archive/cursor" {
			// 签名过期 / 接口改版：明确报错，让适配器退回 web 列表。
			fmt.Fprint(w, `{"code":-400,"message":"请求错误"}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"page":{"pn":1,"ps":20,"count":40},
			"list":{"vlist":[{"bvid":"BV1web000001","title":"web 列表回来的","length":"3:20"}]}}}`)
	})
	page, err := a.ListPerformerVideos(context.Background(), "946974", 1)
	if err != nil {
		t.Fatalf("ListPerformerVideos: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].PlatformVideoID != "BV1web000001" {
		t.Fatalf("没退回 web 列表: %+v", page.Items)
	}
}
