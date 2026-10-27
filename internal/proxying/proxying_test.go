package proxying

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mario/myporn/internal/config"
	"github.com/mario/myporn/internal/platform"
)

// fakeSettings 模拟设置表；缺的键当空串，和 store.GetSetting 的行为一致。
type fakeSettings map[string]string

func (f fakeSettings) GetSetting(key string) (string, error) { return f[key], nil }

// 优先级必须是「设置 > 环境变量」，而且平台专用 > 全局：
// 界面上能改的东西得压过启动参数，否则用户改完没反应只会以为坏了。
func TestResolverPrecedence(t *testing.T) {
	cfg := &config.Config{
		GlobalProxy: "http://env-global:8080",
		PlatformProxy: map[string]string{
			"instagram": "socks5://env-ig:1080",
			"pornhub":   "http://env-ph:8080",
		},
	}

	// 1) 设置里的平台专用代理最大
	st := fakeSettings{"proxy_instagram": "socks5://db-ig:1080", "global_proxy": "http://db-global:8080"}
	if got := New(st, cfg).For("instagram"); got != "socks5://db-ig:1080" {
		t.Errorf("平台专用代理优先，得到 %q", got)
	}

	// 2) 没配平台专用 → 设置里的全局代理
	st = fakeSettings{"global_proxy": "http://db-global:8080"}
	if got := New(st, cfg).For("instagram"); got != "http://db-global:8080" {
		t.Errorf("应回落到设置里的全局代理，得到 %q", got)
	}

	// 3) 设置里啥都没配 → 环境变量的平台专用
	if got := New(fakeSettings{}, cfg).For("instagram"); got != "socks5://env-ig:1080" {
		t.Errorf("应回落到环境变量的平台代理，得到 %q", got)
	}

	// 4) 环境变量也没平台专用 → 环境变量全局
	if got := New(fakeSettings{}, cfg).For("beeg"); got != "http://env-global:8080" {
		t.Errorf("应回落到环境变量全局代理，得到 %q", got)
	}

	// 5) 全空 = 直连
	if got := New(fakeSettings{}, &config.Config{}).For("instagram"); got != "" {
		t.Errorf("什么都没配应当直连，得到 %q", got)
	}
}

// settings 存的是空串时也要当「没配」，不然会把空代理当成有效值。
func TestResolverIgnoresBlankAndNormalizes(t *testing.T) {
	st := fakeSettings{"proxy_instagram": "   ", "global_proxy": " http://db-global:8080 "}
	r := New(st, &config.Config{})
	if got := r.For("Instagram"); got != "http://db-global:8080" {
		t.Errorf("空白的平台代理应被忽略且去掉首尾空格，得到 %q", got)
	}
	// twitter 与 x 是同一个平台（艺人链接里两种写法都有）
	if got := r.For("twitter"); got != "http://db-global:8080" {
		t.Errorf("twitter 应归一化成 x，得到 %q", got)
	}
}

// 关键行为：改设置后**下一个请求**就走新代理，不用重启、不用重建客户端。
// 这里用一个真代理服务验证：请求先落到代理，把代理清空后请求直接打到源站。
func TestDynamicClientFollowsSettings(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("from-origin"))
	}))
	defer origin.Close()

	var proxied int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxied++
		w.Write([]byte("from-proxy"))
	}))
	defer proxy.Close()

	st := fakeSettings{"proxy_instagram": proxy.URL}
	r := New(st, &config.Config{})
	client, err := platform.NewDynamicHTTPClient(func() string { return r.For("instagram") }, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	body := func() string {
		resp, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 32)
		n, _ := resp.Body.Read(buf)
		return string(buf[:n])
	}

	if got := body(); got != "from-proxy" {
		t.Fatalf("配了代理就该走代理，得到 %q", got)
	}
	if proxied != 1 {
		t.Fatalf("代理应该被命中 1 次，实际 %d", proxied)
	}

	// 用户在设置里把代理清空（模拟换回直连）
	st["proxy_instagram"] = ""
	if got := body(); got != "from-origin" {
		t.Fatalf("清空代理之后应当直连，得到 %q", got)
	}
}
