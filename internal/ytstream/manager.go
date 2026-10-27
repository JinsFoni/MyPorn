// Package ytstream 管「在线播放」的两个薄层：上游地址的缓存，和播放会话的地址簿。
//
// 为什么要在服务端存一份地址簿，而不是把 YouTube 的清单原样改写后发给浏览器：
//  1. 分片地址是签名 + 绑定出口 IP 的，一条一小时的片子有一千多条分片，
//     每条地址一千多字符。编码进清单里就是好几 MB，纯属浪费。
//  2. 那些地址不该出现在页面上（个人媒体库的隐私口径：浏览器只跟本机说话）。
//
// 于是：浏览器看到的是 /api/yt/seg/{sid}/{n}，真地址留在进程内存里，
// 会话跟着签名寿命（expire=）一起过期，到期连人带地址一起扔掉。
package ytstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// Upstream 是解析结果（由 ytsub 提供）：一把 HLS master 清单的地址与寿命。
type Upstream struct {
	VideoID     string
	Title       string
	DurationSec int
	MasterURL   string
	ExpiresAt   time.Time
}

// ResolveFunc 解析一条视频的上游地址（真实现是 ytsub.YtDlp.ResolveStream）。
type ResolveFunc func(ctx context.Context, videoID string) (Upstream, error)

// resolveTTL 是解析结果的缓存时长：yt-dlp 解析一条要 2~4 秒，
// 打开弹窗、拉清单、切画质都会用到同一个地址，不能每次都去问一遍 YouTube。
// 上限还要跟着 expire= 走——取两者中更早的那个。
const resolveTTL = 30 * time.Minute

// grace 是会话过期后的宽限期：播放器手里可能还攥着上一版清单，
// 太早把地址簿删掉，正在看的人会在下一片 404。
const grace = 10 * time.Minute

// maxSessions 兜住内存：一个会话约等于「一条片子的分片地址表」，几十条在跑也能接受。
const maxSessions = 64

type cachedUp struct {
	up  Upstream
	at  time.Time
	hit bool // 已经命中过缓存（用于日志里区分冷热）
}

// Manager 是进程内唯一的一份：缓存 + 会话。
type Manager struct {
	resolve ResolveFunc
	log     *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	ups      map[string]cachedUp
	sessions map[string]*Session
	order    []string // 会话创建顺序，超上限时从头淘汰
}

// NewManager 构造管理器。resolve 为空时只提供会话能力（测试用）。
func NewManager(resolve ResolveFunc, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		resolve:  resolve,
		log:      log,
		now:      time.Now,
		ups:      map[string]cachedUp{},
		sessions: map[string]*Session{},
	}
}

// Upstream 返回一条视频的上游地址，命中缓存就不碰 yt-dlp。
func (m *Manager) Upstream(ctx context.Context, videoID string) (Upstream, error) {
	m.mu.Lock()
	if c, ok := m.ups[videoID]; ok && m.now().Sub(c.at) < resolveTTL && m.now().Before(c.up.ExpiresAt.Add(-time.Minute)) {
		m.mu.Unlock()
		return c.up, nil
	}
	m.mu.Unlock()

	if m.resolve == nil {
		return Upstream{}, ErrNoResolver
	}
	up, err := m.resolve(ctx, videoID)
	if err != nil {
		return Upstream{}, err
	}
	m.mu.Lock()
	m.ups[videoID] = cachedUp{up: up, at: m.now()}
	m.mu.Unlock()
	return up, nil
}

// ErrNoResolver 表示没有配置解析器（没装 yt-dlp 的运行环境）。
var ErrNoResolver = errNoResolver{}

type errNoResolver struct{}

func (errNoResolver) Error() string { return "没有配置 YouTube 解析器" }

// NewSession 开一个播放会话，返回带 ID 的地址簿。
func (m *Manager) NewSession(videoID string, expiresAt time.Time) *Session {
	s := &Session{
		ID:        newID(),
		VideoID:   videoID,
		ExpiresAt: expiresAt,
		created:   m.now(),
		index:     map[string]int{},
	}
	m.mu.Lock()
	m.gcLocked()
	m.sessions[s.ID] = s
	m.order = append(m.order, s.ID)
	for len(m.order) > maxSessions {
		old := m.order[0]
		m.order = m.order[1:]
		delete(m.sessions, old)
	}
	m.mu.Unlock()
	return s
}

// Session 按 id 取会话。
func (m *Manager) Session(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	if m.now().After(s.ExpiresAt.Add(grace)) {
		delete(m.sessions, id)
		return nil, false
	}
	return s, true
}

// gcLocked 顺手清掉过期的会话与上游缓存（调用方持锁）。
func (m *Manager) gcLocked() {
	now := m.now()
	kept := m.order[:0]
	for _, id := range m.order {
		s, ok := m.sessions[id]
		if !ok {
			continue
		}
		if now.After(s.ExpiresAt.Add(grace)) {
			delete(m.sessions, id)
			continue
		}
		kept = append(kept, id)
	}
	m.order = kept
	for k, c := range m.ups {
		if now.After(c.up.ExpiresAt.Add(-time.Minute)) && now.Sub(c.at) >= resolveTTL {
			delete(m.ups, k)
		}
	}
}

// Session 是一次播放的地址簿：分片/子清单的真实地址在这里领号。
type Session struct {
	ID        string
	VideoID   string
	ExpiresAt time.Time
	created   time.Time

	mu    sync.Mutex
	urls  []string
	index map[string]int
}

// Ref 把上游地址换成站内地址（同一地址重复登记会拿到同一个号）。
func (s *Session) Ref(abs string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.index[abs]; ok {
		return s.path(n)
	}
	n := len(s.urls)
	s.urls = append(s.urls, abs)
	s.index[abs] = n
	return s.path(n)
}

// URL 按号取回上游地址。
func (s *Session) URL(n int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 0 || n >= len(s.urls) {
		return "", false
	}
	return s.urls[n], true
}

// Len 返回登记了多少条地址（测试与日志用）。
func (s *Session) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.urls)
}

func (s *Session) path(n int) string {
	return "/api/yt/seg/" + s.ID + "/" + strconv.Itoa(n)
}

// newID 生成会话 id（16 字节随机，够用且不猜得出来）。
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 极端情况下退回时间戳：id 不需要密码学强度，只要能区分会话。
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}
