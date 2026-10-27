// Package platform 定义站点适配器接口与注册表。新增站点 = 实现 Adapter + 注册。
package platform

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/mario/myporn/internal/model"
)

// Page 是艺人影片列表的一页。
type Page struct {
	Items      []model.Video
	Page       int
	TotalPages int // 0 表示未知
	HasMore    bool
}

// Adapter 是各站点必须实现的最小接口集。
type Adapter interface {
	Key() string
	Name() string
	BaseURL() string
	// ParsePerformerURL 从艺人主页 URL 中提取平台侧标识（slug 或 id）。
	ParsePerformerURL(raw string) (string, error)
	GetPerformer(ctx context.Context, performerID string) (*model.Performer, error)
	ListPerformerVideos(ctx context.Context, performerID string, page int) (*Page, error)
	GetVideo(ctx context.Context, videoID string) (*model.Video, error)
	ResolveStreams(ctx context.Context, videoID string) ([]model.Variant, error)
	SearchPerformers(ctx context.Context, query string, page int) ([]*model.Performer, bool, error)
}

// PlaybackResolver 是可选接口：解析「能播的地址」时顺带把详情页的元数据带回来
// （标签、简介、时长等）。标签这类信息只在详情页里有，列表页抓不到，
// 与其为它单独再抓一次页面，不如挂在播放解析上顺手落库。
// 播放不需要真实码率，这条路径也不会为了码率去拉 HLS 清单。
type PlaybackResolver interface {
	ResolvePlayback(ctx context.Context, videoID string) (*model.Video, []model.Variant, error)
}

// AvatarProvider 是可选接口：站外的艺人头像来源。
// Pornhub 的头像只存在于艺人页面的 DOM 里（改版就失效，抓不到只能显示占位图），
// 而 beeg 有按名字查询的公开接口，能直接拿到站内自己用的那张头像。
// 所以头像允许跨站借：**名字对得上**才采用，对不上宁可留空。
type AvatarProvider interface {
	Key() string
	ResolveAvatar(ctx context.Context, performerName string) (string, error)
}

// ProfileProvider 是可选接口：站外的艺人**资料**来源。
// beeg / xHamster 的艺人页只给名字、头像和几个站内统计，
// 简介、资料标签、三围、社交链接、排名这些正经资料都缺；
// 同一个人在 Pornhub 上通常有完整档案，所以允许整份借过来——
// 但**名字对得上**才采用，对不上宁可留空，绝不给艺人安别人的简介。
type ProfileProvider interface {
	Key() string
	// ResolveProfile 按艺人名找到站外的同一个人，返回它的资料；名字对不上就报错。
	ResolveProfile(ctx context.Context, performerName string) (*model.Performer, error)
}

// ProfileRegistry 保存资料源，按注册顺序依次尝试。零值/nil 都可用（视为没有来源）。
type ProfileRegistry struct {
	list []ProfileProvider
}

// NewProfileRegistry 创建空的资料来源表。
func NewProfileRegistry() *ProfileRegistry { return &ProfileRegistry{} }

// Register 追加一个资料来源。
func (r *ProfileRegistry) Register(p ProfileProvider) {
	if r == nil || p == nil {
		return
	}
	r.list = append(r.list, p)
}

// All 返回按注册顺序排列的资料来源。
func (r *ProfileRegistry) All() []ProfileProvider {
	if r == nil {
		return nil
	}
	return r.list
}

// ImageHostPlatform 判断图片地址实际属于哪个站点：跨站头像要用图床所在平台的
// HTTP 客户端（也就是它自己的代理配置），不能跟着影片所属平台走。
// 目前跨站使用的图床有两个：beeg 的照片（externulls）和 X 的头像（twimg）。
func ImageHostPlatform(rawURL, fallback string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fallback
	}
	host := strings.ToLower(u.Hostname())
	if host == "externulls.com" || strings.HasSuffix(host, ".externulls.com") {
		return "beeg"
	}
	if host == "twimg.com" || strings.HasSuffix(host, ".twimg.com") {
		return "x"
	}
	return fallback
}

// AvatarRegistry 保存头像来源，按注册顺序依次尝试。零值/nil 都可用（视为没有来源）。
type AvatarRegistry struct {
	list []AvatarProvider
}

// NewAvatarRegistry 创建空的头像来源表。
func NewAvatarRegistry() *AvatarRegistry { return &AvatarRegistry{} }

// Register 追加一个头像来源。
func (r *AvatarRegistry) Register(p AvatarProvider) {
	if r == nil || p == nil {
		return
	}
	r.list = append(r.list, p)
}

// All 返回按注册顺序排列的头像来源。
func (r *AvatarRegistry) All() []AvatarProvider {
	if r == nil {
		return nil
	}
	return r.list
}

type Registry struct {
	m     map[string]Adapter
	order []string
}

func NewRegistry() *Registry { return &Registry{m: map[string]Adapter{}} }

func (r *Registry) Register(a Adapter) {
	if _, ok := r.m[a.Key()]; !ok {
		r.order = append(r.order, a.Key())
	}
	r.m[a.Key()] = a
}

func (r *Registry) Get(key string) (Adapter, bool) {
	a, ok := r.m[key]
	return a, ok
}

func (r *Registry) All() []Adapter {
	out := make([]Adapter, 0, len(r.order))
	for _, k := range r.order {
		out = append(out, r.m[k])
	}
	return out
}

// NewHTTPClient 按代理配置构造 HTTP 客户端；proxyURL 为空则直连。
func NewHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if proxyURL != "" {
		pu, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("代理地址非法 %q: %w", proxyURL, err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// 带 cookie jar：Pornhub 的 get_media 接口要求先落 sessid/platform 等 cookie，
	// 否则只会回一个空数组（拿不到 4K/1080p 直链）。
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tr, Timeout: timeout, Jar: jar}, nil
}
