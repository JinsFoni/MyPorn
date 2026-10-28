package bilibili

// WBI 签名：这是「空间投稿列表」能不能连打的关键。
//
// B 站从 2023 年起给一批 /x/... 接口加了 wbi 签名（`w_rid` + `wts`），
// 签名材料就是 nav 接口给的两张图片名。浏览器每次请求都带，所以它连翻几十页没事；
// 服务端不带签名去打同一个接口，就被当成「非浏览器流量」——能过一两次，然后 -799 / 412。
//
// 2026-10 实测（同一个 IP、同一份 Cookie，mid=946974，ps=20）：
//
//	未签名 /x/space/arc/search      → 1 页之后 -799「请求过于频繁」，隔 10s 再打就是 412 封禁
//	签名   /x/space/wbi/arc/search  → 连打 15 页、3.3 秒、零失败（第 16 页也一样）
//
// 也就是说：之前那套「1 次 / 20 秒 + 退避 3 小时」其实是在给「没签名」还债。
// 签上之后整库回填（939 部 ≈ 47 页）从「以天计」变成「十几秒」。
//
// 参考实现：bili-sync（Rust）的 crates/bili_sync/src/bilibili/{submission,credential,mod}.rs。
// 用的也是这两件事：nav 拿 img/sub 图片名重排出 mixin key，再把「排好序的查询串 + key」
// 做 md5 得到 w_rid。它没有额外的 dm_* 指纹参数，也不需要真实浏览器 TLS 指纹。

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// mixinKeyEncTab 是 B 站前端里那段固定的重排表（64 项，官方的 WBI 示例里就是它）。
var mixinKeyEncTab = [64]int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38,
	41, 13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36,
	20, 34, 44, 52,
}

// wbiKeyTTL 是 mixin key 的缓存时间。图片名按天轮换，半小时重取一次足够便宜
// （nav 是不设防的接口），也不会在轮换的瞬间因为旧 key 连续失败。
const wbiKeyTTL = 30 * time.Minute

// wbiMixinKey 由 nav 给的两张图片地址算出 32 位 mixin key：
// 两个文件名拼成 64 字符，按 mixinKeyEncTab 重排，取前 32 位。
func wbiMixinKey(imgURL, subURL string) string {
	raw := wbiKeyOf(imgURL) + wbiKeyOf(subURL)
	if len(raw) < 64 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(mixinKeyEncTab))
	for _, i := range mixinKeyEncTab {
		b.WriteByte(raw[i])
	}
	return b.String()[:32]
}

// wbiKeyOf 从 https://i0.hdslb.com/bfs/wbi/<32 位>.png 里抠出那 32 位。
func wbiKeyOf(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	return s
}

// encodeWBI 是签名用的查询串：按键名字典序、值做 URL 转义。
// 注意空格必须编成 %20 而不是 Go 默认的 +，这是 B 站校验的一部分。
func encodeWBI(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(strings.ReplaceAll(url.QueryEscape(q.Get(k)), "+", "%20"))
	}
	return b.String()
}

// signWBI 返回带 wts / w_rid 的新参数表（不改原表，方便调用方复用同一份参数重试）。
func signWBI(q url.Values, mixinKey string, now time.Time) url.Values {
	out := make(url.Values, len(q)+2)
	for k, v := range q {
		out[k] = v
	}
	out.Set("wts", strconv.FormatInt(now.Unix(), 10))
	sum := md5.Sum([]byte(encodeWBI(out) + mixinKey))
	out.Set("w_rid", hex.EncodeToString(sum[:]))
	return out
}

// mixinKey 取（并缓存）当前的 mixin key。nav 接口不设防，用普通限速器即可。
func (a *Adapter) mixinKey(ctx context.Context) (string, error) {
	a.wbiMu.Lock()
	if a.wbiKey != "" && time.Since(a.wbiAt) < wbiKeyTTL {
		key := a.wbiKey
		a.wbiMu.Unlock()
		return key, nil
	}
	a.wbiMu.Unlock()

	var d struct {
		WbiImg struct {
			ImgURL string `json:"img_url"`
			SubURL string `json:"sub_url"`
		} `json:"wbi_img"`
	}
	if err := a.call(ctx, "/x/web-interface/nav", nil, a.lim, a.base+"/", &d); err != nil {
		return "", fmt.Errorf("取 wbi 签名材料失败: %w", err)
	}
	key := wbiMixinKey(d.WbiImg.ImgURL, d.WbiImg.SubURL)
	if key == "" {
		return "", errors.New("B 站 nav 没有给出 wbi 图片名，签名用不了")
	}
	a.wbiMu.Lock()
	a.wbiKey, a.wbiAt = key, time.Now()
	a.wbiMu.Unlock()
	return key, nil
}

// invalidateMixinKey 在签名被拒后强制下次重取。
func (a *Adapter) invalidateMixinKey() {
	a.wbiMu.Lock()
	a.wbiKey, a.wbiAt = "", time.Time{}
	a.wbiMu.Unlock()
}

// callWBI 打一个需要 wbi 签名的接口：签名被拒（-352）就换一把 key 重试一次，
// 还不行就把错误交给上层（空间列表会退回未签名版那条慢路）。
func (a *Adapter) callWBI(ctx context.Context, path string, q url.Values, referer string, out any) error {
	key, err := a.mixinKey(ctx)
	if err != nil {
		return err
	}
	err = a.call(ctx, path, signWBI(q, key, time.Now()), a.wbiLim, referer, out)
	if err == nil || !errors.Is(err, errWbiRejected) {
		return err
	}
	// -352 多半是图片名刚刚轮换，旧 key 失效了。
	a.invalidateMixinKey()
	key, err = a.mixinKey(ctx)
	if err != nil {
		return err
	}
	return a.call(ctx, path, signWBI(q, key, time.Now()), a.wbiLim, referer, out)
}
