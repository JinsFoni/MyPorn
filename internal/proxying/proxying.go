// Package proxying 把「设置里配的代理」变成可随时重读的解析器。
//
// 为什么需要它：HTTP 客户端的代理通常在建 Transport 时就定死了，改设置要重启进程——
// 而代理恰恰是最需要「改完立刻验证」的配置（换一个出口 IP 就能决定 IG 的抓取是通还是被拦）。
// 这里给的是「每次真正发请求时再问一遍」的解析器：
//
//	http.Transport.Proxy = func(req) { return proxying.Parse(r.For("instagram")) }
//
// 于是 set 完设置，下一个请求就走新代理，不用重启、不用清缓存。
package proxying

import (
	"net/url"
	"strings"

	"github.com/JinsFoni/MyPorn/internal/config"
)

// Settings 只需要读设置，不依赖整个 store（测试里给个 map 就行）。
type Settings interface {
	GetSetting(key string) (string, error)
}

// Resolver 按平台解析代理地址。
type Resolver struct {
	st  Settings
	cfg *config.Config
}

// New 构造解析器。st / cfg 都可为 nil（测试或极简启动）。
func New(st Settings, cfg *config.Config) *Resolver {
	return &Resolver{st: st, cfg: cfg}
}

// For 返回某个平台该用的代理地址，空串表示直连。
//
// 优先级（越靠前越优先）：
//  1. 设置里的平台专用代理（`proxy_<platform>`）——用户在界面里填的就是它；
//  2. 设置里的全局代理（`global_proxy`）；
//  3. 环境变量里的平台代理（`PLATFORM_PROXY_<PLATFORM>`）；
//  4. 环境变量里的全局代理（`GLOBAL_PROXY`）。
//
// 设置优先于环境变量：界面上能改的东西必须比启动参数更有话语权，否则用户改了没反应，
// 只会以为坏了。
func (r *Resolver) For(platform string) string {
	if r == nil {
		return ""
	}
	key := NormalizePlatform(platform)
	if r.st != nil {
		if v := r.setting("proxy_" + key); v != "" {
			return v
		}
		if v := r.setting("global_proxy"); v != "" {
			return v
		}
	}
	if r.cfg != nil {
		if v := strings.TrimSpace(r.cfg.PlatformProxyAt(key)); v != "" {
			return v
		}
		if v := strings.TrimSpace(r.cfg.GlobalProxy); v != "" {
			return v
		}
	}
	return ""
}

// Func 返回一个只吃平台名的函数，给 imgcache 之类的老接口用。
func (r *Resolver) Func() func(string) string {
	return func(platform string) string { return r.For(platform) }
}

// Parse 把代理地址转成 http.Transport.Proxy 需要的三个返回值。
// 地址非法时返回错误，由 Transport 自己把请求失败掉（比静默直连安全：用户配错了要能看见）。
func (r *Resolver) Parse(platform string) (*url.URL, error) {
	raw := r.For(platform)
	if raw == "" {
		return nil, nil
	}
	return url.Parse(raw)
}

func (r *Resolver) setting(key string) string {
	v, err := r.st.GetSetting(key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// NormalizePlatform 把调用方写法统一：去空白、转小写、`twitter` 视同 `x`。
func NormalizePlatform(platform string) string {
	p := strings.ToLower(strings.TrimSpace(platform))
	if p == "twitter" {
		return "x"
	}
	return p
}
