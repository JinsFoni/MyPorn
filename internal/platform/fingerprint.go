package platform

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/cookiejar"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// browserTransport 是「装成浏览器」的 HTTP/2 传输层，过 Cloudflare 用。
//
// 为什么需要它：krx18.com 前面挂着 Cloudflare 的 managed challenge，判的不是
// 请求头而是 **TLS 指纹**——Go 自己那套 ClientHello 一进门就 403：
//
//	curl（Go 同款指纹）  -> HTTP/2 403, cf-mitigated: challenge, 5,631 B 的「Just a moment...」
//	curl --http1.1       -> 301（重定向到同一个被 challenge 的地址）
//	带全套 sec-ch-ua / sec-fetch-* 头也一样 403：头写得再像也没用
//	utls Chrome_Auto     -> 200, 161,478 B, 1.02s（同一台机器、同一个出口 IP）
//	utls Firefox_120     -> 200, 1.70s；utls Safari_Auto -> 200, 0.98s
//
// 也量过 Cloudflare 放行的是「新指纹」而不是「任何非 Go 指纹」：同样是 curl_cffi，
// chrome131 被 403、chrome133a/chrome136 放行（safari180、firefox135 也放行）。
//
// 用的是 utls 的 **Chrome** ClientHello：**UA 必须跟着一起伪装**——指纹是 Chrome、
// UA 却写别的，等于自报假冒。适配器里那句 UA 和这里的 HelloChrome_Auto 是一对，别单独改。
//
// 刻意不支持代理：HTTP/2 的 DialTLSContext 要自己管隧道，而「换了出口 IP 之后
// 指纹/信誉还是不是同一套」没法在这台机器上验证，写一条测不到的分支不如不写。
// 这个站点在本机直连就是 200，需要代理时再说。
func browserTransport() *http2.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			raw, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				raw.Close()
				return nil, err
			}
			// ALPN 由 ClientHello 自带（Chrome 的 h2,http/1.1）；握完手若没谈成 h2，
			// 这里的 Transport 只认 h2，会明确报错而不是悄悄降级成 h1。
			conn := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
			if err := conn.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			return conn, nil
		},
		ReadIdleTimeout: 60 * time.Second,
	}
}

// NewBrowserHTTPClient 建一个伪装浏览器指纹的客户端（带 cookie jar）。
//
// 只有躲在 Cloudflare 后面的站点需要它；其余平台照用 NewHTTPClient ——
// utls 握手比 crypto/tls 慢一点，能不用就不用。
func NewBrowserHTTPClient(timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: browserTransport(), Timeout: timeout, Jar: jar}, nil
}
