// Package social 定义社交平台（X / Instagram）的适配器接口与注册表。
//
// 和 internal/platform 的区别：那边是「影片/艺人」，这边是「博主/帖子/照片」，
// 生命周期、失败语义、限流规则都不一样，所以单开一层。上层（订阅、同步、照片墙）
// 只认这里的接口，不认具体站点。
//
// 各站的解析规则集中在 internal/social/<站>/，站点改版只改那个包。
package social

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---- 数据模型 ----

// Account 是一个博主账号的资料。
type Account struct {
	Platform    string // x / instagram
	Handle      string // 不带 @，小写
	DisplayName string
	AvatarURL   string
	ProfileURL  string
}

// Photo 是帖子里的单张静态图。
type Photo struct {
	URL    string
	Width  int
	Height int
	Seq    int // 帖内序号，从 1 开始
}

// Post 是一条帖子（只当照片的容器与正文来源）。
type Post struct {
	PostID   string // 平台侧 id
	URL      string
	Caption  string
	PostedAt time.Time
	Photos   []Photo
}

// Page 是一页帖子。
type Page struct {
	Posts      []Post
	NextCursor string // 空 = 没有下一页
}

// ---- 错误分类 ----

// 错误种类：上层靠它决定「等一等」「让用户贴 Cookie」还是「提示更新解析版本」，
// 三种在界面上不能长一个样。
const (
	KindNeedsAuth   = "needs_auth"   // 上游要求登录态
	KindRateLimited = "rate_limited" // 上游限流（正常的配额/频率限制，等一会儿能好）
	KindBlocked     = "blocked"      // 上游风控拦截（返回拦截页/校验页，等也没用，得换 IP 或换会话）
	KindStaleParser = "stale_parser" // 端点/参数变了（queryId、app-id 之类）
	KindNotFound    = "not_found"    // 账号不存在或没有内容
	KindUpstream    = "upstream"     // 其它上游/网络错误
)

// Error 是适配器统一返回的错误。
type Error struct {
	Kind    string
	Message string
	ResetAt time.Time // 仅 KindRateLimited：冷却到这个时间点
	Err     error     // 底层错误（可选）
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Message, e.Err, e.Kind)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// NeedsAuth 表示「这个站要登录态才能读」。
func NeedsAuth(msg string) *Error { return &Error{Kind: KindNeedsAuth, Message: msg} }

// Blocked 表示被风控拦截：上游不是按配额限流，而是直接把请求挡在门外
// （IG 的典型表现是 429 + HTML 拦截页）。等下去不会好，所以和 rate_limited 分开：
// 前者给冷却时间，后者要提示「换出口 IP / 换 Cookie」，并且**不设冷却**——
// 用户换完 Cookie 立刻重试是合理的。
func Blocked(msg string) *Error { return &Error{Kind: KindBlocked, Message: msg} }

// RateLimited 表示被限流，until 是建议的冷却截止时间。
func RateLimited(msg string, until time.Time) *Error {
	return &Error{Kind: KindRateLimited, Message: msg, ResetAt: until}
}

// StaleParser 表示站点改了协议（不是限流、也不是没登录）。
func StaleParser(msg string) *Error { return &Error{Kind: KindStaleParser, Message: msg} }

// NotFound 表示账号不存在 / 没有内容。
func NotFound(msg string) *Error { return &Error{Kind: KindNotFound, Message: msg} }

// Upstream 包装其它上游错误。
func Upstream(msg string, err error) *Error {
	return &Error{Kind: KindUpstream, Message: msg, Err: err}
}

// KindOf 取出错误种类；普通错误一律算 upstream。
func KindOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindUpstream
}

// ResetAtOf 取限流错误里的冷却截止时间。
func ResetAtOf(err error) (time.Time, bool) {
	var e *Error
	if errors.As(err, &e) && e.Kind == KindRateLimited && !e.ResetAt.IsZero() {
		return e.ResetAt, true
	}
	return time.Time{}, false
}

// ---- 适配器接口 ----

// Adapter 是各站必须实现的接口。
type Adapter interface {
	Key() string
	Name() string
	// ParseProfileURL 从主页链接里取出 handle（不带 @，小写）。
	ParseProfileURL(raw string) (string, error)
	// GetProfile 抓博主资料（显示名 / 头像 / 主页地址）。
	GetProfile(ctx context.Context, handle string) (*Account, error)
	// ListPosts 抓一页帖子；cursor 为空表示第一页。
	ListPosts(ctx context.Context, handle, cursor string) (*Page, error)
	// NeedsCookie 报告这个站是否必须配 Cookie 才能用（用于界面提示，不是硬判断）。
	NeedsCookie() bool
	// Throttle 返回这个站的本地限速/冷却器：界面上的「清除冷却」要能碰到它。
	Throttle() *Throttle
}

// SettingFunc 读取一个设置值（已解密）。适配器不自持配置，避免重启才生效。
type SettingFunc func(key string) string

// ---- 注册表 ----

type Registry struct {
	m     map[string]Adapter
	order []string
}

func NewRegistry() *Registry { return &Registry{m: map[string]Adapter{}} }

func (r *Registry) Register(a Adapter) {
	if a == nil {
		return
	}
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

// ---- 限速与冷却 ----

// Throttle 给单个站点做「串行 + 固定间隔 + 冷却」。
//
// 为什么要冷却而不是直接重试：X 的 429 是**按 IP** 计数的公开配额
// （实测 30 次 / 15 分钟，docs/M5-SOCIAL.md §15.3），越重试越晚恢复；
// Instagram 更狠，会升级成风控。所以撞到限流就停到 reset，不试探。
type Throttle struct {
	mu    sync.Mutex
	every time.Duration
	last  time.Time
	until time.Time // 冷却截止
}

func NewThrottle(every time.Duration) *Throttle {
	if every <= 0 {
		every = 1500 * time.Millisecond
	}
	return &Throttle{every: every}
}

// CoolUntil 把冷却截止时间推后（撞到限流时调用）。
func (t *Throttle) CoolUntil(until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if until.After(t.until) {
		t.until = until
	}
}

// Clear 立刻解除冷却。
//
// 用在「用户自己动手了」的场景：换了 Cookie、换了出口 IP 之后，没有理由还让他干等
// 上一轮算出来的 15 分钟——那个时间是按旧请求猜的，条件已经变了。
func (t *Throttle) Clear() {
	t.mu.Lock()
	t.until = time.Time{}
	t.mu.Unlock()
}

// CooldownRemaining 返回还在冷却中的剩余时间（0 = 可以请求）。
func (t *Throttle) CooldownRemaining() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d := time.Until(t.until); d > 0 {
		return d
	}
	return 0
}

// Wait 按间隔让路；还在冷却期就返回限流错误。
func (t *Throttle) Wait(ctx context.Context) error {
	t.mu.Lock()
	if d := time.Until(t.until); d > 0 {
		until := t.until
		t.mu.Unlock()
		return RateLimited(fmt.Sprintf("限流冷却中，还需 %s", d.Round(time.Second)), until)
	}
	gap := t.every - time.Since(t.last)
	if gap < 0 {
		gap = 0
	}
	t.last = time.Now().Add(gap)
	t.mu.Unlock()

	if gap <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(gap)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ---- 小工具 ----

// NormalizeHandle 统一 handle 写法：去掉 @、去空白、转小写。
func NormalizeHandle(raw string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
}

// PlatformNames 给界面用的显示名。
func PlatformNames() map[string]string {
	return map[string]string{"x": "X", "instagram": "Instagram"}
}
