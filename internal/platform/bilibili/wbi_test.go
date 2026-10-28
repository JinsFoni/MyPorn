package bilibili

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这两串是 nav 接口真实给过的图片地址（也是 B 站官方 WBI 示例里那一对），
// 重排出来的 mixin key 应该是 ea1db124af3c7062474693fa704f4ff8。
const (
	testImgURL    = "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png"
	testSubURL    = "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"
	testMixinKey  = "ea1db124af3c7062474693fa704f4ff8"
	testNavBody   = `{"code":0,"message":"0","data":{"wbi_img":{"img_url":"` + testImgURL + `","sub_url":"` + testSubURL + `"}}}`
	testSignWTS   = int64(1700000000)
	testSignRid   = "29d06ef2393f3b5a1a5ec31774aa438e"
	testSignQuery = "mid=946974&order=pubdate&order_avoided=true&platform=web&pn=2&ps=20&web_location=1550101"
)

// wbiSignatureOf 按服务端收到的参数重算一遍 w_rid（先把 w_rid 自己摘掉）。
func wbiSignatureOf(q url.Values, mixinKey string) string {
	rest := make(url.Values, len(q))
	for k, v := range q {
		if k == "w_rid" {
			continue
		}
		rest[k] = v
	}
	return signWBI(rest, mixinKey, time.Unix(parseInt64(rest.Get("wts")), 0)).Get("w_rid")
}

func parseInt64(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

func TestWbiMixinKey(t *testing.T) {
	if got := wbiMixinKey(testImgURL, testSubURL); got != testMixinKey {
		t.Fatalf("mixin key = %q, want %q", got, testMixinKey)
	}
	// 取不到完整的 64 位就明说，不要拼出一把假 key 去签名。
	if got := wbiMixinKey("https://i0.hdslb.com/bfs/wbi/short.png", testSubURL); got != "" {
		t.Errorf("残缺的图片名应该返回空，得到 %q", got)
	}
	if got := wbiKeyOf("https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png"); got != "7cd084941338484aae1ad9425b84077c" {
		t.Errorf("wbiKeyOf = %q", got)
	}
}

func TestSignWBI(t *testing.T) {
	q := url.Values{
		"mid": {"946974"}, "order": {"pubdate"}, "order_avoided": {"true"},
		"platform": {"web"}, "pn": {"2"}, "ps": {"20"}, "web_location": {"1550101"},
	}
	if got := encodeWBI(q); got != testSignQuery {
		t.Fatalf("签名串 = %q, want %q", got, testSignQuery)
	}
	signed := signWBI(q, testMixinKey, time.Unix(testSignWTS, 0))
	if got := signed.Get("wts"); got != "1700000000" {
		t.Errorf("wts = %q", got)
	}
	if got := signed.Get("w_rid"); got != testSignRid {
		t.Errorf("w_rid = %q, want %q", got, testSignRid)
	}
	// 原表不能被改（重试时要拿同一份参数重签）。
	if _, ok := q["w_rid"]; ok {
		t.Error("signWBI 改了调用方的参数表")
	}
}

// 签名被拒（-352）→ 换 key 重试一次 → 还拒就退回未签名版那条慢路。
func TestSpacePageFallsBackWhenWbiRejected(t *testing.T) {
	var navHits, wbiHits, plainHits int32
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			atomic.AddInt32(&navHits, 1)
			fmt.Fprint(w, testNavBody)
		case "/x/space/wbi/arc/search":
			atomic.AddInt32(&wbiHits, 1)
			fmt.Fprint(w, `{"code":-352,"message":"风控校验失败"}`)
		case "/x/space/arc/search":
			atomic.AddInt32(&plainHits, 1)
			fmt.Fprint(w, `{"code":0,"data":{"page":{"pn":2,"ps":20,"count":40},
				"list":{"vlist":[{"bvid":"BV1fallback","title":"兜底页"}]}}}`)
		default:
			t.Errorf("没预料到的路径: %s", r.URL.Path)
		}
	})
	page, err := a.ListPerformerVideos(context.Background(), "946974", 2)
	if err != nil {
		t.Fatalf("ListPerformerVideos: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].PlatformVideoID != "BV1fallback" {
		t.Fatalf("兜底页没生效: %+v", page.Items)
	}
	// 一次原始 + 换 key 重试一次 = 两次签名请求。
	if got := atomic.LoadInt32(&wbiHits); got != 2 {
		t.Errorf("签名路径请求 %d 次，期望 2 次（含换 key 重试）", got)
	}
	if got := atomic.LoadInt32(&plainHits); got != 1 {
		t.Errorf("未签名路径请求 %d 次，期望 1 次", got)
	}
	// nav 只在第一次取 key 时打，重取 key 会再打一次，共两次。
	if got := atomic.LoadInt32(&navHits); got != 2 {
		t.Errorf("nav 请求 %d 次，期望 2 次", got)
	}
}

// 签名正常时不碰未签名那条路。
func TestSpacePagePrefersWbi(t *testing.T) {
	var plainHits int32
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x/web-interface/nav":
			fmt.Fprint(w, testNavBody)
		case "/x/space/wbi/arc/search":
			if !strings.HasPrefix(r.URL.RawQuery, "mid=946974") && !strings.Contains(r.URL.RawQuery, "mid=946974") {
				t.Errorf("参数不对: %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"code":0,"data":{"page":{"pn":3,"ps":20,"count":41},
				"list":{"vlist":[{"bvid":"BV1wbi","title":"签名页"}]}}}`)
		case "/x/space/arc/search":
			atomic.AddInt32(&plainHits, 1)
			fmt.Fprint(w, `{"code":-799,"message":"请求过于频繁"}`)
		}
	})
	// 连打两页：第二页要复用缓存里的 mixin key，不该每次都去问 nav。
	for _, pn := range []int{2, 3} {
		page, err := a.ListPerformerVideos(context.Background(), "946974", pn)
		if err != nil {
			t.Fatalf("第 %d 页: %v", pn, err)
		}
		if len(page.Items) != 1 || page.Items[0].PlatformVideoID != "BV1wbi" {
			t.Fatalf("第 %d 页内容不对: %+v", pn, page.Items)
		}
	}
	if got := atomic.LoadInt32(&plainHits); got != 0 {
		t.Errorf("签名正常还去打了 %d 次未签名接口", got)
	}
	if a.wbiKey != testMixinKey {
		t.Errorf("mixin key 没缓存住: %q", a.wbiKey)
	}
}

// nav 未登录时回 -101：LoginName 要能认出「没登录」而不是报错。
func TestLoginName(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"登录了", `{"code":0,"data":{"isLogin":true,"uname":"月白喵丶"}}`, "月白喵丶"},
		{"没登录", `{"code":-101,"message":"账号未登录","data":{"isLogin":false}}`, ""},
		{"code 0 但 isLogin=false", `{"code":0,"data":{"isLogin":false}}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/x/web-interface/nav" {
					t.Errorf("路径不对: %s", r.URL.Path)
				}
				fmt.Fprint(w, c.body)
			})
			got, err := a.LoginName(context.Background())
			if err != nil {
				t.Fatalf("LoginName: %v", err)
			}
			if got != c.want {
				t.Errorf("LoginName = %q, want %q", got, c.want)
			}
		})
	}
}
