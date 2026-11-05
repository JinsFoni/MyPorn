// Package platform 定义站点适配器接口与注册表。新增站点 = 实现 Adapter + 注册。
package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// ErrNotFound 表示上游明确回了 404：页面不存在。
//
// 各家适配器本来各有一个私有哨兵（pornhub.ErrNotFound / xhamster.errNotFound /
// beeg.errNotFound），调用方没法跨站判断。这里给一个公共的，各家把私有哨兵包在它外面
// （errors.Is(err, platform.ErrNotFound) 就能统一认出「404」）。
//
// 用得上它的地方是分页：艺人影片列表翻到末页之后，xHamster 回的是 404 而不是空页，
// 判断不出 404 就分不清「翻完了」和「抓取出错」，回填会一直卡在「未完成」。
var ErrNotFound = errors.New("上游 404：页面不存在")

// ErrRateLimited 表示上游把我们拦住了（429 / B 站 -799、412 风控）：等一会儿再来就行。
//
// 和 ErrNotFound 一样是跨站哨兵，各家把私有错误包在它外面。用得上它的地方是同步任务：
// 限流不该当「抓取出错」处理——已经抓到的页照常入库、断点照常保留，
// 下一轮接着翻就行；否则一个被限流的号每天都会红一次，而它其实什么都没做错。
var ErrRateLimited = errors.New("上游限流")

// Page 是艺人影片列表的一页。
type Page struct {
	Items      []model.Video
	Page       int
	TotalPages int // 0 表示未知
	HasMore    bool
	// Skipped 是本页被主动扔掉、没入库的条目数（目前只有 xHamster 用它挡片花）。
	// 单独记一份是为了让同步日志能说出「这页有 8 条被跳过」，而不是让人以为站上只有 38 条。
	Skipped int
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

// VideoURLResolver 是可选接口：从「影片页地址」直接认出是哪条片子。
//
// krx18 需要它：那个站的订阅源是 tag 归档页（/tag/<女优名>/），影片页（/movies/<片名>-<年份>）
// 不在任何归档的父子关系里，要单独吃一条地址进来（见 httpapi.handleImportVideo）。
// 实现了它的平台会出现在「解析链接」里。
type VideoURLResolver interface {
	// ParseVideoURL 从影片页地址取出平台侧 id；不是本平台的地址就返回错误。
	ParseVideoURL(raw string) (string, error)
}

// SitePlaylistProvider 是可选接口：播放清单不是一条能直接 GET 的上游地址，得现做一份。
//
// krx18 的 m3u8 是拿影片页里的 token 去 POST 换回来的**内容**（接口故意不回地址，
// 这样浏览器的网络面板里看不到真实存储地址），所以既不能把它包成 /api/stream?u=…，
// 也不能让浏览器自己去取。这里的约定是：适配器给出一个站内地址，Server 路由到
// SitePlaylist 现取清单、把分片改写成 /api/stream 再发出去。
type SitePlaylistProvider interface {
	// PlaylistPath 返回这条片子的站内清单地址（形如 /api/krx18/playlist.m3u8?v=slug）。
	// 变体 url 留空的档位靠它接上，见 httpapi.handlePlayback。
	PlaylistPath(videoID string) string
	// SitePlaylist 返回清单原文与基准地址（分片里的相对地址要拼在它后面）。
	SitePlaylist(ctx context.Context, videoID string) ([]byte, *url.URL, error)
}

// FileStreamProvider 是可选接口：整片是一条支持 Range 的 mp4，但取字节的地址要按 Range 现算。
//
// krx18 的 Server 2（mov18plus.cloud / Abyss 播放器）就是这样：播放器给浏览器的是
// 一条**假**地址（storage.googleapis.com/mediastorage/…#mp4/chunk/…），真正的字节在
// {sub}.sssrr.org/sora/{size}/{token} 上，token 由「(md5_id, res_id, size, chunkSize, 分片序号)」
// 这串明文 AES-256-CTR 加出来、再套两层 base64（实测与浏览器抓到的 token 逐字节一致，
// 见 docs/M8-KRX18.md §11.4）。分片序号来自 Range 头，所以这条既不能包成
// /api/stream?u=…（那是一条固定地址），也不能让浏览器自己去取（要 Referer，且 token 是算出来的）。
//
// 约定：适配器给出一个站内整片地址（variant.url 直接用它），Server 路由到 ServeFile。
type FileStreamProvider interface {
	// FilePath 返回这条片子的站内整片地址（形如 /api/krx18/file.mp4?v=<id>）。
	FilePath(videoID string) string
	// ServeFile 按 Range 把上游的分片边取边发；出错时若已经写了响应头就只能断流。
	ServeFile(ctx context.Context, videoID string, w http.ResponseWriter, r *http.Request) error
}

// FileSourceProvider 是可选接口：把整片字节**直接**交给下载任务（不走 HTTP 回路）。
//
// 和 FileStreamProvider 是同一份字节的两个出口：那边服务浏览器（按客户端要的 Range
// 边取边发），这边服务下载任务（从头拉到尾落盘）。为什么要多一个出口：这类平台的档位
// url 是**站内相对地址**（`/api/krx18/file.mp4?v=…`），交给 ffmpeg 会被当成文件名，
// 实测报 `Error opening input file /api/krx18/file.mp4?v=…`；而让下载器回头请求自己的
// HTTP 口，又得把会话 Cookie 和本机端口变成下载任务的隐式依赖。
//
// 约定：实现了它，Server 的 `/api/{platform}/file.mp4` 与非空的档位 url 才成立，
// 下载任务的「字节在服务端手里」那条路径也才走得通（见 jobs.saveFromSiteStream）。
type FileSourceProvider interface {
	// OpenFile 从第 0 字节开始顺序读出整片，total 是总字节数（0 = 未知，调用方按估算值报进度）。
	// 调用方负责 Close；带上 ctx 才能在中途取消时把上游也停掉。
	OpenFile(ctx context.Context, videoID string) (rc io.ReadCloser, total int64, err error)
}

// PageResolver 是可选接口：能直接按「影片页地址」解析播放地址，省掉一次 301。
//
// 为什么会有这个需求：xHamster 的影片 ID 是 URL 末尾那段（xhW3Ivw），
// 而 `/videos/xhW3Ivw` 是 301 到带 slug 的规范地址；库里的 videos.url 存的就是规范地址。
// 实测：301 响应本身 0.94/1.06/1.27 秒（冷连接），端到端解析 2.41s（带 slug）
// vs 2.59~2.77s（裸 ID 多吃一跳）——每播一次都白花，上游还多收一次请求。
// 没实现这个接口的平台照旧走 PlaybackResolver。
type PageResolver interface {
	ResolvePlaybackPage(ctx context.Context, pageURL string) (*model.Video, []model.Variant, error)
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
	// Instagram 的照片走 CDN（cdninstagram.com / *.fbcdn.net），代理配置按站点给。
	if host == "cdninstagram.com" || strings.HasSuffix(host, ".cdninstagram.com") ||
		host == "fbcdn.net" || strings.HasSuffix(host, ".fbcdn.net") {
		return "instagram"
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

// NewHTTPClient 用固定代理地址建客户端（启动时一次性决定）。
//
// 需要「改设置立刻生效」的场景用 NewDynamicHTTPClient。
func NewHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	tr := newTransport()
	if proxyURL != "" {
		pu, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("代理地址非法 %q: %w", proxyURL, err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	return newClientWith(tr, timeout)
}

// NewDynamicHTTPClient 的代理是每次发请求时现问的（参数返回空串 = 直连）。
//
// 这样「设置里换代理」就不用重启进程：Transport.Proxy 本来就是个函数，
// 我们只是把它接到设置表上。代理地址填错时把错误抛给 Transport，
// 请求会明确失败，而不是悄悄直连——用户配错了要能看见。
func NewDynamicHTTPClient(proxyFor func() string, timeout time.Duration) (*http.Client, error) {
	tr := newTransport()
	if proxyFor != nil {
		tr.Proxy = func(*http.Request) (*url.URL, error) {
			raw := strings.TrimSpace(proxyFor())
			if raw == "" {
				return nil, nil
			}
			u, err := url.Parse(raw)
			if err != nil {
				return nil, fmt.Errorf("代理地址非法 %q: %w", raw, err)
			}
			return u, nil
		}
	}
	return newClientWith(tr, timeout)
}

func newTransport() *http.Transport {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		// 手工构造的 Transport 默认**不开 HTTP/2**（只有 DefaultTransport 会自动开）。
		// 实测这是致命的：X 的 syndication 对 HTTP/1.1 直接回 429（"30 次/窗口已用尽"，
		// 带一个假的 reset），同一个地址用 HTTP/2 立刻 200。也就是说请求头写得再像浏览器，
		// 走 h1 也永远拿不到数据。开着它对上游更友好，也省一次 TLS 往返。
		ForceAttemptHTTP2: true,
	}
	return tr
}

// newClientWith 给 Transport 配好超时与 cookie jar。
func newClientWith(tr *http.Transport, timeout time.Duration) (*http.Client, error) {
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
