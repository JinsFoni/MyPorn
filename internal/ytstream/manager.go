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
	// Tracks 是 DASH 那条线上的「只有画面」直链（含共用音轨），4K 走它原生播放。
	// 字段照 ytsub.DashTrack 抄一份：这个包只管地址簿，不去 import 解析器。
	Tracks []Track
}

// Track 是一条只有画面的 DASH 直链 + 共用音轨地址。
//
// 为什么 4K 要换这条路（2026-10-29 实测，视频 YFsMnJEbV1U / 321 秒）：
// HLS 的 2160p 只有 VP9 28.2 Mbps（1.06 GB），单连接实测 3.5~8.4 MB/s，缓冲攒不起来，
// 40 秒内卡 13 次；而 DASH 的 AV1 2160p 只有 13.5 Mbps（540 MB），且原生 <video> 播放
// 绕开了 MSE——这台机器的 VideoToolbox 在 MSE 上放 VP9 4K 会每 5~30 秒崩一次。
type Track struct {
	Height      int
	Codecs      string
	Bandwidth   int64
	VideoURL    string
	AudioURL    string
	AudioCodecs string
}

// ResolveFunc 解析一条视频的上游地址（真实现是 ytsub.YtDlp.ResolveStream）。
type ResolveFunc func(ctx context.Context, videoID string) (Upstream, error)

// resolveTTL 是解析结果的缓存上限：yt-dlp 解析一条要 4~5 秒，是站内播放里最大的一笔开销，
// 而签名寿命（expire=）通常有 6 小时。实际取「这个上限」和「expire − 1 分钟」里更早的那个。
//
// 原来是 30 分钟，结果「看几条、过半小时回头再看」又得重新等一遍 4 秒；
// 期间签名其实一直有效，缓存到过期为止没有任何额外风险。
const resolveTTL = 6 * time.Hour

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
	inflight map[string]*inflightCall
	sessions map[string]*Session
	warm     map[string]*Warm
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
		inflight: map[string]*inflightCall{},
		sessions: map[string]*Session{},
		warm:     map[string]*Warm{},
	}
}

// inflightCall 是一次正在进行的解析：并发的调用方等同一个结果。
type inflightCall struct {
	done chan struct{}
	up   Upstream
	err  error
}

// Upstream 返回一条视频的上游地址，命中缓存就不碰 yt-dlp。
//
// 同一条视频同时只跑一次解析。这不是锦上添花：悬停预热、弹窗里的信息请求、
// 播放器拉的清单会挤在同一两秒里发出，没有去重就会并发起三四个 yt-dlp 子进程，
// 每个 4 秒，互相抢 CPU 和 YouTube 的连接，最后谁都慢。
// Forget 丢掉一条视频的解析缓存，逼下一次 Upstream 重跑 yt-dlp。
//
// 用在**合流失败**之后：googlevideo 的地址是「签名 + 绑定出口 IP」的，签名本身没过期、
// 但某些 CDN 节点会直接回 403（实测 sn-qja5m5-5b 这种边，同一份地址重试几次都是 403），
// 换一次解析就换一批节点，通常就好了。不丢掉缓存的话，用户点几次「重试」都撞同一堵墙。
func (m *Manager) Forget(videoID string) {
	m.mu.Lock()
	delete(m.ups, videoID)
	m.mu.Unlock()
}

func (m *Manager) Upstream(ctx context.Context, videoID string) (Upstream, error) {
	m.mu.Lock()
	if c, ok := m.ups[videoID]; ok && m.now().Sub(c.at) < resolveTTL && m.now().Before(c.up.ExpiresAt.Add(-time.Minute)) {
		m.mu.Unlock()
		return c.up, nil
	}
	if c, ok := m.inflight[videoID]; ok {
		m.mu.Unlock()
		select {
		case <-c.done:
			return c.up, c.err
		case <-ctx.Done():
			return Upstream{}, ctx.Err()
		}
	}
	if m.resolve == nil {
		m.mu.Unlock()
		return Upstream{}, ErrNoResolver
	}
	call := &inflightCall{done: make(chan struct{})}
	m.inflight[videoID] = call
	m.mu.Unlock()

	up, err := m.resolve(ctx, videoID)

	m.mu.Lock()
	call.up, call.err = up, err
	if err == nil {
		m.ups[videoID] = cachedUp{up: up, at: m.now()}
	}
	delete(m.inflight, videoID)
	m.mu.Unlock()
	close(call.done)

	if err != nil {
		return Upstream{}, err
	}
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
	for k, w := range m.warm {
		if now.Sub(w.at) >= warmTTL {
			delete(m.warm, k)
		}
	}
}

// warmTTL 是预热的存活时间。预热只是「把先后顺序调一下」，
// 超过这个时间的字节很可能对应的是一次早就结束的播放，留着没意义。
const warmTTL = 15 * time.Minute

// maxWarmVideos / maxWarmBytes 兜住内存：一条视频预热 ≤ 两份子清单 + 几个分片，
// 十几条在跑也就几十 MB，再多就按最旧淘汰。
//
// 32 MiB 这个额度是照「分片几 MB」估的，4K 一上来就不够：实测 2160p 每段 21~23 MB，
// 影像段 + 音轨段就要 24 MB，再有一条视频在预热（最多 8 条）全被挤掉。
// 抬到 128 MiB 够放两段 4K 影像 + 各自音轨，8 条视频都满载时上限仍是 1 GB 量级，
// 对一台自托管的播放服务可以接受。
const (
	maxWarmVideos = 8
	maxWarmBytes  = 128 << 20
)

// Warm 是一条视频的起播预热：上游地址 → 已经取回来的字节。
//
// 和 Session 分开是有原因的：会话是「给浏览器发号」的，每次拉 master 都是新会话；
// 而预热要在浏览器开口之前就开始（点开弹窗的那一刻），那时还没有会话。
// 所以这里按上游地址存原文，谁拿到号都能用。
type Warm struct {
	mu    sync.Mutex
	at    time.Time
	items map[string]Prefetched
	// pending 是「这条地址已经有人在取了」的登记：一个空 channel，取完就关。
	// 没有它的话，预热和浏览器自己来要会各发一次上游请求，同一条分片下两遍、抢同一根出口带宽。
	pending map[string]chan struct{}
	bytes   int
}

// Warm 取（没有就建）一条视频的预热缓存。
func (m *Manager) Warm(videoID string) *Warm {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if w, ok := m.warm[videoID]; ok && now.Sub(w.at) < warmTTL {
		return w
	}
	w := &Warm{at: now, items: map[string]Prefetched{}, pending: map[string]chan struct{}{}}
	m.warm[videoID] = w
	// 超上限时把最旧的几条扔掉（地图里顺序不定，按时间戳挑）。
	for len(m.warm) > maxWarmVideos {
		var oldestKey string
		var oldest time.Time
		for k, v := range m.warm {
			if k == videoID {
				continue
			}
			if oldestKey == "" || v.at.Before(oldest) {
				oldestKey, oldest = k, v.at
			}
		}
		if oldestKey == "" {
			break
		}
		delete(m.warm, oldestKey)
	}
	return w
}

// WarmFor 只取已有预热（不新建），给分片请求用。
func (m *Manager) WarmFor(videoID string) (*Warm, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.warm[videoID]
	if !ok || m.now().Sub(w.at) >= warmTTL {
		return nil, false
	}
	return w, true
}

// Put 存一份字节（同一地址只存一次，超额度就不存）。
func (w *Warm) Put(abs, contentType string, body []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.items[abs]; ok {
		return
	}
	if w.bytes+len(body) > maxWarmBytes {
		return
	}
	w.items[abs] = Prefetched{ContentType: contentType, Body: body}
	w.bytes += len(body)
}

// Get 取一份字节。
func (w *Warm) Get(abs string) (Prefetched, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.items[abs]
	return p, ok
}

// Claim 认领「这条地址由我去上游取」。已经取回来、或者已经有人在取，都返回 false。
//
// 认领成功的调用方取完（成功或失败）必须 Settle 一次，否则等在这条地址上的
// 请求会一直等到 context 过期。只给预热 goroutine 用：浏览器那条路只做 Wait，
// 不认领——它认领了却不往缓存里放字节，等于白占一个坑。
func (w *Warm) Claim(abs string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.items[abs]; ok {
		return false
	}
	if w.pending == nil {
		w.pending = map[string]chan struct{}{}
	}
	if _, ok := w.pending[abs]; ok {
		return false
	}
	w.pending[abs] = make(chan struct{})
	return true
}

// Wait 等这条地址的在途取数结束（没有在途的就直接看缓存），返回取回来的字节。
//
// 起播时最贵的一步是「第一段分片」。预热早就把它排在路上了，播放器一开口却
// 又自己发一次，两条请求平分同一根 20 Mbps 的出口——本机实测第一次打开要 4.4 秒
// 才出画面，其中大半就是这么重复掉的。等一次比自己再要一次快。
func (w *Warm) Wait(ctx context.Context, abs string) (Prefetched, bool) {
	w.mu.Lock()
	ch, ok := w.pending[abs]
	w.mu.Unlock()
	if ok {
		select {
		case <-ch:
		case <-ctx.Done():
			return Prefetched{}, false
		}
	}
	return w.Get(abs)
}

// Settle 收尾一次认领：body 非空就存进缓存，然后叫醒所有在等的请求。
// body 为 nil 表示这次取失败了——不存，但照样要放行（等待方会退回自己回源）。
func (w *Warm) Settle(abs, contentType string, body []byte) {
	if body != nil {
		w.Put(abs, contentType, body)
	}
	w.mu.Lock()
	ch, ok := w.pending[abs]
	delete(w.pending, abs)
	w.mu.Unlock()
	if ok {
		close(ch)
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

// Prefetched 是一份已经取回来的字节（子清单原文或分片），带着它的 Content-Type。
type Prefetched struct {
	ContentType string
	Body        []byte
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
