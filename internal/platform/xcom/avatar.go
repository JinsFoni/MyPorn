// Package xcom 从 X（Twitter）借艺人头像。
//
// 有些艺人在 beeg 上查得到人、却没有照片（tag 的 thumbs 是空的），
// 但他们在 X 上有主页。X 的个人页在未登录时也会返回带 og:title / og:image
// 的 HTML，不需要 API key：
//
//	<meta property="og:title" content="Milana (@Charming_Milana) on X">
//	<meta property="og:image" content="https://pbs.twimg.com/profile_images/….jpg">
//
// og:image 就是 400x400 的头像。默认头像（没设过头像的账号）走的是
// abs.twimg.com/sticky/default_profile_images，那就当没查到。
//
// 真正的难点是「找对账号」：X 没有公开的按名字搜索接口，只能按艺人名猜 handle
// （去掉空格拼一起、或用下划线连起来），再把页面上的显示名和艺名按词比一遍。
// 对不上就当没查到——宁可不给头像，也不能挂一张陌生人的照片。
package xcom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/JinsFoni/MyPorn/internal/platform"
)

const (
	// baseURL 是 X 的网页域名（个人页 HTML 里就带 og 信息）。
	baseURL = "https://x.com"
	// handleMaxLen 是 X 账号名长度上限，超了就不可能存在。
	handleMaxLen = 15
	// avatarHost 是 X 的头像图床；默认头像在 abs.twimg.com 上，另作处理。
	avatarHost = "pbs.twimg.com"

	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// AvatarProvider 按艺人名在 X 上找同名的账号，拿它的头像。
type AvatarProvider struct {
	client *http.Client
	base   string // 可替换，便于测试
}

// NewAvatarProvider 用给定的 HTTP 客户端（可带代理）构造头像来源。
func NewAvatarProvider(client *http.Client) *AvatarProvider {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &AvatarProvider{client: client, base: baseURL}
}

// Key 实现 platform.AvatarProvider。
func (p *AvatarProvider) Key() string { return "x" }

// ResolveAvatar 依次试几个可能的账号名，第一个「名字对得上」的就算命中。
func (p *AvatarProvider) ResolveAvatar(ctx context.Context, performerName string) (string, error) {
	candidates := handleCandidates(performerName)
	if len(candidates) == 0 {
		return "", fmt.Errorf("艺人名转不出 X 账号名: %q", performerName)
	}
	misses := make([]string, 0, len(candidates))
	for _, handle := range candidates {
		src, err := p.lookup(ctx, handle, performerName)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			misses = append(misses, err.Error())
			continue
		}
		return src, nil
	}
	return "", fmt.Errorf("X 上没有匹配的账号：%s", strings.Join(misses, "；"))
}

// lookup 抓一个账号的个人页，校验它就是这位艺人，再取头像地址。
func (p *AvatarProvider) lookup(ctx context.Context, handle, want string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/"+url.PathEscape(handle), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("@%s 请求失败: %w", handle, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("X 上没有 @%s", handle)
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", fmt.Errorf("X 限流（429）")
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("X 返回 %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("解析 X 页面失败: %w", err)
	}
	title := metaContent(doc, `meta[property="og:title"]`)
	display, got, ok := splitTitle(title)
	if !ok {
		// 风控页 / 登录墙也会返回 200，但页面里没有账号信息。
		return "", fmt.Errorf("@%s 的页面没有账号信息（可能被风控）", handle)
	}
	if !strings.EqualFold(got, handle) {
		return "", fmt.Errorf("X 把 @%s 解析成了 @%s", handle, got)
	}
	if !nameMatches(display, want) {
		return "", fmt.Errorf("X @%s 的显示名 %q 和 %q 不是同一个人", handle, display, want)
	}
	return avatarFrom(metaContent(doc, `meta[property="og:image"]`))
}

func metaContent(doc *goquery.Document, selector string) string {
	v, ok := doc.Find(selector).First().Attr("content")
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// titleRe 从 og:title 里拆出显示名和账号名：`Milana (@Charming_Milana) on X`。
var titleRe = regexp.MustCompile(`^(.*?)\s*\(@([A-Za-z0-9_]+)\)`)

func splitTitle(title string) (display, handle string, ok bool) {
	m := titleRe.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return "", "", false
	}
	return strings.TrimSpace(m[1]), m[2], true
}

// avatarFrom 校验 og:image 是真人头像并统一到 400x400。
func avatarFrom(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("X 页面没有头像地址")
	}
	host := strings.ToLower(u.Hostname())
	if host != avatarHost && !strings.HasSuffix(host, "."+avatarHost) {
		// abs.twimg.com 上放的是默认头像和占位图，说明这账号没传过头像。
		return "", fmt.Errorf("X 账号没有头像（%s）", host)
	}
	if strings.Contains(u.Path, "default_profile_images") {
		return "", fmt.Errorf("X 账号没传过头像")
	}
	path := sizeSuffixRe.ReplaceAllString(u.Path, "_400x400$1")
	if path != u.Path {
		out := *u
		out.Path = path
		return out.String(), nil
	}
	return u.String(), nil
}

// sizeSuffixRe 匹配头像文件名里的尺寸后缀：…/abc_normal.jpg → …/abc_400x400.jpg。
var sizeSuffixRe = regexp.MustCompile(`_(?:normal|mini|bigger|reasonably_small|\d+x\d+)(\.\w+)$`)

// handleCandidates 按常见写法猜账号名：去掉分隔符拼一起，再试下划线连起来。
// X 账号名只能是字母数字下划线且不超过 15 个字符，不合规的候选直接丢掉。
// "Mini Diva" → ["minidiva" "mini_diva"]。
func handleCandidates(name string) []string {
	words := platform.Tokens(name)
	if len(words) == 0 {
		return nil
	}
	out := make([]string, 0, 2)
	add := func(s string) {
		if s == "" || len(s) > handleMaxLen || strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
			return
		}
		for _, v := range out {
			if v == s {
				return
			}
		}
		out = append(out, s)
	}
	add(strings.Join(words, ""))
	if len(words) > 1 {
		add(strings.Join(words, "_"))
	}
	return out
}

// nameMatches 判断 X 上的显示名是不是这位艺人。艺人常把显示名写成简称或加法：
// "Charming Milana" 的账号可能只叫 "Milana"，也可能叫 "Mini Diva | OF"，
// 所以按词比较，只要一方的每个词都在另一方出现过就算同一个人。
func nameMatches(display, want string) bool {
	a, b := platform.Tokens(display), platform.Tokens(want)
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	return tokensContain(a, b) || tokensContain(b, a)
}

func tokensContain(hay, needles []string) bool {
	set := make(map[string]struct{}, len(hay))
	for _, t := range hay {
		set[t] = struct{}{}
	}
	for _, n := range needles {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}

// 编译期检查：AvatarProvider 满足 platform.AvatarProvider。
var _ platform.AvatarProvider = (*AvatarProvider)(nil)
