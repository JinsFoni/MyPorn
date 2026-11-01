package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JinsFoni/MyPorn/internal/social"
	"github.com/JinsFoni/MyPorn/internal/ytdash"
	"github.com/JinsFoni/MyPorn/internal/ytstream"
)

// 4K 的「单文件 HLS」：把 YouTube 的 AV1 分片 mp4 当成一条 HLS 流放出去。
//
// 这是这一档的第三条方案，前两条都被实测否掉了：
//   - HLS 代理那条：YouTube 的 HLS 清单里 2160p 只有 VP9 28.2 Mbps（1.06 GB），
//     单连接实测 3.5~8.4 MB/s，缓冲攒不起来，40 秒卡 13 次；
//   - 服务端 ffmpeg 合流那条：AV1 13.46 Mbps + m4a，但要把 540 MB 整条下完才能起播
//     （实测 4 分 0.5 秒），「点开等四分钟」本身就是不能接受的体验。
//
// 现在这条：AV1 mp4 是分片 mp4（ftyp+moov+sidx+一串 moof+mdat），sidx 逐段写着偏移/长度/时长，
// 于是每段都能当 HLS 分片发：起播只要第一段（约 1.5 秒），拖动到哪取到哪，带宽只有 VP9 的一半。
//
// 取数的并发度是量出来的（见 ytDashWorkers 的注释）：googlevideo 走的是「按请求起步、按连接限速」，
// 一条连接、一次大请求只有 1 MB/s 上下，三条各 20 MB 并发能干到 9 MB/s，而请求拆得太碎（十次各 1 MB）
// 只剩 0.18 MB/s。所以这条链路上的策略是「少开、开大、开两三条」，并且把下一段提前拉进内存
// （见 prefetchYtSegments）——这正是 YouTube 原站点看起来「一直在微微领先」的原因。

const (
	// ytDashWorkers 是「一次取数」拆成几条并发连接。
	//
	// 数字是量的（2026-10-30，YFsMnJEbV1U 的 itag 401 地址，curl 逐项计时）：
	//
	//	1 条 × 20 MB  → 0.99 MB/s
	//	2 条 × 20 MB  → 3.26 MB/s
	//	3 条 × 20 MB  → 9.31 MB/s（60 MB / 6.4 秒，AV1 4K 只要 1.68 MB/s，余量 5 倍）
	//	4 条 × 20 MB  → 1.19 MB/s（第 4 条就被限流，反而全慢）
	//	10 次 × 1 MB  → 0.18 MB/s（每开一个新请求都要重新起步，前两三秒几乎不出数据）
	//
	// 所以 3 是这块 CDN 边的甜点：再多就翻车，再少就喂不满。曾试过 6 路，结果 googlevideo
	// 直接不回应（连接建得起来、一个字节都不发，播放器卡在 73.32 秒 40 秒不动，服务端连 403 都没有）。
	ytDashWorkers = 3
	// ytDashMaxConns 是全站同时在飞的上游连接数上限：本次请求 3 路 + 音轨 1 路 + 预取 2 路 = 6。
	//
	// 超过就本地排队——排队比让上游静默挂死好，也比 4 条连接把整条链路拖垮好（见上）。
	ytDashMaxConns = 7
	// ytDashBulkConns 是「预取」单独能用的连接数。
	//
	// 预取和播放抢连接时，先饿死的一定是音轨（一段才 161 KB，却要排在 11 MB 的画面块后面，
	// 实测排到 5–7 秒）——音轨一时出不来，MSE 照样 `waiting`。所以预取单独一个闸门。
	ytDashBulkConns = 3
	// ytDashPrefetch 是「往后预取几段」，一次取完。
	//
	// 4 段 ≈ 45 MB：这个数字不是为了「多存」，而是为了**把上游请求做大**。googlevideo 的吞吐
	// 强烈依赖单次请求的体积（实测：1 条 × 20 MB = 0.99 MB/s，3 条 × 20 MB = 9.31 MB/s，
	// 而 10 次 × 1 MB 只有 0.18 MB/s——每开一个新请求都要重新起步）。
	// 预取按 4 段合并成一次、切 3 条连接，每条流式拉 15 MB，才吃得满这条链路。
	ytDashPrefetch = 4
	// ytDashCacheMax 是全局预取占用的上限（多会话同时开着时不能无节制吃内存）。
	ytDashCacheMax = 192 << 20
	// ytDashAttemptTimeout 是「一块」的超时。
	//
	// googlevideo 的限流不是 403，而是**接了连接不发数据**；客户端没有超时的话，
	// 这一段就永远等下去（实测 60 秒 0 字节）。所以每块都必须带超时，超时就重试。
	ytDashAttemptTimeout = 25 * time.Second
	// ytDashFirstBlock 是**流式**取数时第一块的字节数（见 ytStairSpans）。
	//
	// 服务端是「第 N 块到齐才写第 N 块」，所以第一块多大 = 播放器等多久才见到第一个字节。
	// 2026-11-01 实测（5A3yPgCSVrg，4K60 AV1 itag 401，一段 16 925 922 B / 5.038 s，当天出境链路 ~2 MB/s）：
	// 3 等分时首块 5.64 MB，第一个字节要 2.8~4.3 秒才发出去，正好越过播放器看门狗的 3 秒阈值，
	// 于是它每 4 秒 `startLoad()` 一次、把在飞的分片掐掉；掐断时服务端连一个字节都还没发，
	// 于是永远重来 —— 实测同一个 v.mp4?i=0 被取消 9 次、每次正好 4.0 秒、0 字节，首帧 34.8/38.5/41.7 秒。
	// 首块压到 1 MiB 后，第一个字节 ≈ 上游 TTFB（实测 1.1~2.5 s）+ 0.5 s。
	// 代价：尾块从 5.64 MB 长到 7.95 MB（整段取完晚约 1 秒），换「先出画面」值。
	ytDashFirstBlock = 1 << 20
	// ytDashSessionTTL 是会话的兜底寿命（真寿命令看上游签名）。
	ytDashSessionTTL = 6 * time.Hour
	// ytDashMaxSessions 是同时在册的会话上限（地铁里刷列表会点开一堆 4K）。
	ytDashMaxSessions = 64
)

// block 是一段取回来的字节（或错误）。
type block struct {
	data []byte
	err  error
}

// ytDashSide 是「一条流」的上游地址 + 索引。
type ytDashSide struct {
	url   string
	index ytdash.Track
}

// ytDashSession 是一次 4K 播放的会话：两条流的索引 + 拼 master 要的信息。
type ytDashSession struct {
	id      string
	videoID string
	height  int

	video ytDashSide
	audio ytDashSide

	codecs     string
	width      int
	fps        float64
	vBandwidth int64
	aBandwidth int64

	createdAt time.Time
	expiresAt time.Time

	// prefetch 是「下一两段先取到手」的缓存（见 prefetchYtSegments）。
	//
	// 为什么要它：播放器是「播到没数据了才来要下一段」，而 googlevideo 单段要 3–5 秒
	// （8.8 MB / 2–3 MB/s）。两者一叠加，缓冲最多只剩几秒，链路一抖就 `waiting`。
	// 服务端在发完这一段之后主动把后面一两段拉进内存，播放器下次要就是零等待——
	// 这也是 YouTube 原站点「看起来一直在微微领先」的原因。
	cacheMu sync.Mutex
	// cache 的键带上是哪条流：画面和音轨都用「第几段」编号，不区分就会串台
	cache map[ytSegKey][]byte
	// cacheAt 记录插入顺序，用来淘汰（FIFO 就够，反正只留一两段）
	cacheAt []ytSegKey
	// fetching 是「正在预取」的标记，避免同一段被好几个 goroutine 重复拉
	fetching map[ytSegKey]struct{}
}

// ytSegKey 是缓存键：哪条流 + 第几段。
type ytSegKey struct {
	side string
	idx  int
}

// ytDashSessionOf 取（或建）一条视频某一档的会话。
//
// 缓存的理由和清单那条一样：解析一次 yt-dlp 要 2~4 秒，而播放器会连着来好几次请求
// （master、两条子清单、init、一串分片）。会话里存的是「索引」，几十 KB，比缓存视频小得多。
func (s *Server) ytDashSessionOf(ctx context.Context, videoID string, height int) (*ytDashSession, error) {
	if s.ytStream == nil {
		return nil, social.Upstream("没有配置 yt-dlp，站内播放用不了", nil)
	}
	s.ytDashMu.Lock()
	for _, sess := range s.ytDash {
		if sess.videoID == videoID && sess.height == height && time.Now().Before(sess.expiresAt) {
			s.ytDashMu.Unlock()
			return sess, nil
		}
	}
	s.ytDashMu.Unlock()

	up, err := s.ytStream.Upstream(ctx, videoID)
	if err != nil {
		return nil, err
	}
	var track *ytstream.Track
	for i := range up.Tracks {
		if up.Tracks[i].Height == height {
			track = &up.Tracks[i]
			break
		}
	}
	if track == nil || track.VideoURL == "" || track.AudioURL == "" {
		return nil, social.Upstream(fmt.Sprintf("%dp 这一档没有 AV1 直链，只能走 HLS 播放", height), nil)
	}

	client := s.streamClient("youtube")
	// 两条流的文件头并发取：各是一次 Range（实测 ftyp+moov+sidx 一共 1417 / 1151 字节），
	// 串行要多等一个来回，而这一步在「点了播放」到「出画面」的那条路径上
	var (
		vIdx, aIdx ytdash.Track
		vErr, aErr error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); vIdx, vErr = s.fetchYtDashIndex(ctx, client, track.VideoURL) }()
	go func() { defer wg.Done(); aIdx, aErr = s.fetchYtDashIndex(ctx, client, track.AudioURL) }()
	wg.Wait()
	if vErr != nil {
		return nil, vErr
	}
	if aErr != nil {
		return nil, aErr
	}

	expires := time.Now().Add(ytDashSessionTTL)
	if !up.ExpiresAt.IsZero() && up.ExpiresAt.Before(expires) {
		// 签名一到点，所有分片请求都会被上游 403，会话提前作废
		expires = up.ExpiresAt.Add(-time.Minute)
	}
	codecs := track.Codecs + ",mp4a.40.2"
	sess := &ytDashSession{
		id:         newYtDashID(),
		videoID:    videoID,
		height:     height,
		video:      ytDashSide{url: track.VideoURL, index: vIdx},
		audio:      ytDashSide{url: track.AudioURL, index: aIdx},
		codecs:     codecs,
		width:      ytDashWidth(height),
		fps:        60,
		vBandwidth: track.Bandwidth,
		// itag 140 实测 129.5 kbps；这个值只影响 master 里的 BANDWIDTH，不影响播放
		aBandwidth: 129501,
		createdAt:  time.Now(),
		expiresAt:  expires,
	}

	s.ytDashMu.Lock()
	if s.ytDash == nil {
		s.ytDash = map[string]*ytDashSession{}
	}
	now := time.Now()
	for id, old := range s.ytDash {
		if now.After(old.expiresAt) {
			delete(s.ytDash, id)
		}
	}
	if len(s.ytDash) >= ytDashMaxSessions {
		// 满了按创建时间淘汰最老的
		type kv struct {
			id string
			at time.Time
		}
		all := make([]kv, 0, len(s.ytDash))
		for id, v := range s.ytDash {
			all = append(all, kv{id: id, at: v.createdAt})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
		for i := 0; i < len(all)-ytDashMaxSessions+1 && i < len(all); i++ {
			delete(s.ytDash, all[i].id)
		}
	}
	s.ytDash[sess.id] = sess
	s.ytDashMu.Unlock()

	// 这里不预取：播放器几乎立刻就会来要第一段，服务端再提前开三路去抢，
	// 等于同一时刻 6 条连接怼同一个 CDN 边（实测多于 3–4 条就开始互相拖）。
	// 预取留给「第一段已经在手上」之后（见 handleYtDashMedia）。
	return sess, nil
}

// ytDashWidth 由高度推宽度（这一档只会是 16:9 的 2160）。
func ytDashWidth(height int) int {
	switch height {
	case 2160:
		return 3840
	case 1440:
		return 2560
	case 1080:
		return 1920
	default:
		return height * 16 / 9
	}
}

func newYtDashID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// fetchYtDashIndex 读文件头并解析出 init 段与分段表。
//
// 一次 256 KiB 的 Range 请求就够了（实测 2160p AV1 的 ftyp+moov+sidx 只有 1417 字节）：
// 不下载、不解析整条流，只拿索引。
func (s *Server) fetchYtDashIndex(ctx context.Context, client *http.Client, abs string) (ytdash.Track, error) {
	var out ytdash.Track
	// 试两次：googlevideo 的边对同一份 Range 请求会偶发 403（限流/换边），
	// 重试一次通常就过——不能因为它闪一下就把 4K 整个降级回 VP9
	var head []byte
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		head, err = s.fetchYtBytes(ctx, client, abs, 0, ytdash.HeadProbeBytes-1)
		if err == nil && len(head) > 0 {
			break
		}
	}
	idx, err := ytdash.Parse(head)
	if err != nil {
		if errors.Is(err, ytdash.ErrNoSidx) {
			return out, social.Upstream("这条流不是分片 mp4（没有 sidx），4K 用不了单文件 HLS", err)
		}
		return out, social.Upstream("解析 DASH 索引失败: "+err.Error(), err)
	}
	return idx, nil
}

// fetchYtBytes 取上游的一段字节（带 UA/Referer，走同一个出网客户端）。
func (s *Server) fetchYtBytes(ctx context.Context, client *http.Client, abs string, start, end int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, abs, nil)
	if err != nil {
		return nil, social.Upstream("分片地址不合法", err)
	}
	req.Header.Set("User-Agent", mediaUserAgent)
	req.Header.Set("Referer", "https://www.youtube.com/")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := client.Do(req)
	if err != nil {
		return nil, social.Upstream("取分片失败: "+err.Error(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone {
		return nil, social.Upstream("上游分片返回 403（签名过期或这条 CDN 边不认）", nil)
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, social.Upstream("上游分片返回 "+strconv.Itoa(resp.StatusCode), nil)
	}
	want := end - start + 1
	if want > 0 && resp.ContentLength > 0 && resp.ContentLength != want {
		want = resp.ContentLength // 上游最多给到这里，别按自己的想法读
	}
	return io.ReadAll(io.LimitReader(resp.Body, want))
}

// fetchYtBlock 取一块，带超时、重试、并且受全局连接闸门约束。
//
// 三种失败都要区分开：403（签名/换边，重试常能过）、半路掐断（拿到的字节比要的少）、
// 以及最恶心的「接了连接不发数据」（不设超时就会永久挂住，见 ytDashAttemptTimeout）。
func (s *Server) fetchYtBlock(ctx context.Context, client *http.Client, abs string, start, end int64) block {
	return s.fetchYtBlockWith(ctx, client, abs, start, end, s.ytDashConn)
}

// fetchYtBulkBlock 是预取专用的取数：只走 ytDashBulkConns 那 2 条，抢不到也不影响播放。
func (s *Server) fetchYtBulkBlock(ctx context.Context, client *http.Client, abs string, start, end int64) block {
	return s.fetchYtBlockWith(ctx, client, abs, start, end, s.ytDashBulk)
}

func (s *Server) fetchYtBlockWith(ctx context.Context, client *http.Client, abs string, start, end int64, sem chan struct{}) block {
	want := end - start + 1
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := acquireSem(ctx, sem); err != nil {
			return block{err: err}
		}
		actx, cancel := context.WithTimeout(ctx, ytDashAttemptTimeout)
		data, err := s.fetchYtBytes(actx, client, abs, start, end)
		cancel()
		releaseSem(sem)
		if err == nil && int64(len(data)) == want {
			return block{data: data}
		}
		if err == nil {
			lastErr = fmt.Errorf("分片短了：要 %d 字节，只拿到 %d", want, len(data))
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			return block{err: ctx.Err()}
		}
	}
	return block{err: lastErr}
}

// acquireSem / releaseSem 是上游连接的闸门（容量见 ytDashMaxConns / ytDashBulkConns）。
func acquireSem(ctx context.Context, sem chan struct{}) error {
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSem(sem chan struct{}) {
	select {
	case <-sem:
	default:
	}
}

// fetchYtRangeAll 并发把 [start, end] 整段取进内存（预取用）。
//
// 和 streamYtRange 的区别只是「不边取边发」：预取的字节要留着给下一次请求，所以得先拼齐。
func (s *Server) fetchYtRangeAll(ctx context.Context, client *http.Client, abs string, start, end int64, workers int, bulk bool) ([]byte, error) {
	if workers < 1 {
		workers = 1
	}
	spans := ytBlockSpans(start, end, workers)
	parts := make([][]byte, len(spans))
	errs := make([]error, len(spans))
	var wg sync.WaitGroup
	for i, sp := range spans {
		wg.Add(1)
		go func(i int, s0, s1 int64) {
			defer wg.Done()
			var b block
			if bulk {
				b = s.fetchYtBulkBlock(ctx, client, abs, s0, s1)
			} else {
				b = s.fetchYtBlock(ctx, client, abs, s0, s1)
			}
			if b.err != nil {
				errs[i] = b.err
				return
			}
			parts[i] = b.data
		}(i, sp[0], sp[1])
	}
	wg.Wait()
	out := make([]byte, 0, end-start+1)
	for i := range parts {
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = append(out, parts[i]...)
	}
	if int64(len(out)) != end-start+1 {
		return nil, fmt.Errorf("取回的字节数不对：要 %d，拿到 %d", end-start+1, len(out))
	}
	return out, nil
}

// ytBlockSpans 把 [start, end] 均分成 count 段（并发取数用）。
func ytBlockSpans(start, end int64, count int) [][2]int64 {
	n := end - start + 1
	if n <= 0 {
		return nil
	}
	if int64(count) > n {
		count = int(n)
	}
	chunk := (n + int64(count) - 1) / int64(count)
	spans := make([][2]int64, 0, count)
	for s0 := start; s0 <= end; s0 += chunk {
		s1 := s0 + chunk - 1
		if s1 > end {
			s1 = end
		}
		spans = append(spans, [2]int64{s0, s1})
	}
	return spans
}

// ytStairSpans 把 [start, end] 切成「先小后大」的楼梯块，专门给**流式**取数用（streamYtRange）。
//
// 为什么不均分：流式取数是「第 N 块到齐才写第 N 块」，第一块的大小直接等于播放器等多久
// 才见到第一个字节。2026-11-01 实测（5A3yPgCSVrg，一段 16 925 922 B / 5.038 s，出境链路 ~2 MB/s）：
// 3 等分 ⇒ 首块 5.64 MB ⇒ 首字节 2.8~4.3 秒才出去，正好卡在播放器「3 秒算卡住、每 4 秒推一把」
// 的阈值上，于是它每 4 秒 `startLoad()` 一次、把这条分片掐掉重来 —— 首帧 34.8/38.5/41.7 秒，
// 甚至 45 秒都起不来（详见 web/src/components/VideoPlayer.svelte 的 fragInFlight）。
//
// 首块取 first 字节、其余按 workers-1 均分：首块 1 MiB ⇒ 首字节 ≈ TTFB（1.1~2.5 s）+ 0.5 s，
// 而尾块从 5.64 MB 变 7.95 MB，整段取完只晚约 1 秒（三条连接是并发的，尾块本来就最慢）。
// 段比 2*first 还小时直接均分：那种段本来就快，别为了几毫秒多开一次请求
// （实测每次新请求都要重新起步，1 MB×10 次只有 0.18 MB/s，见 ytDashWorkers）。
func ytStairSpans(start, end int64, workers int, first int64) [][2]int64 {
	n := end - start + 1
	if workers < 2 || first <= 0 || n <= first*2 {
		return ytBlockSpans(start, end, workers)
	}
	spans := make([][2]int64, 0, workers)
	spans = append(spans, [2]int64{start, start + first - 1})
	return append(spans, ytBlockSpans(start+first, end, workers-1)...)
}

// streamYtRange 把 [start, end] 这段字节按块并发取、**按顺序边到边发**。
//
// 两个要点：
//
//   - 并发：googlevideo 对单条连接限速，切成两路能把吞吐补到 2–4 MB/s
//     （AV1 4K 要 1.68 MB/s）。路数再往上加反而会被静默挂死，见 ytDashWorkers 的注释。
//   - 边到边发：过去是「全部拼完再一次性写回」，起播要等整段下完（8.8 MB / 2 MB/s = 4.4 秒）。
//     现在第 0 块一到就往下写，播放器几百毫秒就能开始喂 MSE。
//
// 块不是均分的：首块压到 ytDashFirstBlock，先把第一个字节尽快送出去（见 ytStairSpans）。
// 代价是响应头（含 Content-Length）必须先发：某一块彻底失败时客户端只会看到一个短了的响应，
// hls.js 会整段重来——比「什么都不发」好，因为重来一次通常就过了。
// 返回拼好的整段字节（取满才返回），供调用方塞进预取缓存——同一份字节只从上游拿一次。
func (s *Server) streamYtRange(ctx context.Context, w http.ResponseWriter, client *http.Client, abs string, start, end int64, total int64) []byte {
	n := end - start + 1
	if n <= 0 {
		return nil
	}
	// 楼梯块：首块 1 MiB 先出去，剩下两路均分（见 ytStairSpans 的量测）
	spans := ytStairSpans(start, end, ytDashWorkers, ytDashFirstBlock)
	results := make([]chan block, len(spans))
	for i := range results {
		results[i] = make(chan block, 1)
	}
	for i, sp := range spans {
		go func(i int, s0, s1 int64) { results[i] <- s.fetchYtBlock(ctx, client, abs, s0, s1) }(i, sp[0], sp[1])
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusPartialContent)
	flusher, canFlush := w.(http.Flusher)
	// 边发边攒一份（上限 32 MB，超了就不留了——缓存里放一大坨反而把别的段挤掉）
	var keep []byte
	if n <= 32<<20 {
		keep = make([]byte, 0, n)
	}
	for i := range results {
		b := <-results[i]
		if b.err != nil {
			s.log.Warn("分片块取失败", "offset", start, "err", b.err)
			return nil
		}
		if _, err := w.Write(b.data); err != nil {
			return nil
		}
		if canFlush {
			flusher.Flush()
		}
		if keep != nil {
			keep = append(keep, b.data...)
		}
	}
	return keep
}

// cacheGet 取一段已预取的字节。
func (sess *ytDashSession) cacheGet(side string, i int) ([]byte, bool) {
	sess.cacheMu.Lock()
	defer sess.cacheMu.Unlock()
	b, ok := sess.cache[ytSegKey{side: side, idx: i}]
	return b, ok
}

// claimFetch 占住「这一段我来取」；已经有人在取就返回 false。
func (sess *ytDashSession) claimFetch(side string, i int) bool {
	sess.cacheMu.Lock()
	defer sess.cacheMu.Unlock()
	if sess.fetching == nil {
		sess.fetching = map[ytSegKey]struct{}{}
	}
	k := ytSegKey{side: side, idx: i}
	if _, ok := sess.fetching[k]; ok {
		return false
	}
	sess.fetching[k] = struct{}{}
	return true
}

func (sess *ytDashSession) releaseFetch(side string, i int) {
	sess.cacheMu.Lock()
	defer sess.cacheMu.Unlock()
	delete(sess.fetching, ytSegKey{side: side, idx: i})
}

// cachePut 存一段；超过 ytDashPrefetch 的旧段丢掉，返回被丢掉的字节数。
func (sess *ytDashSession) cachePut(side string, i int, b []byte) int {
	sess.cacheMu.Lock()
	defer sess.cacheMu.Unlock()
	if sess.cache == nil {
		sess.cache = map[ytSegKey][]byte{}
	}
	k := ytSegKey{side: side, idx: i}
	if _, ok := sess.cache[k]; ok {
		return 0
	}
	sess.cache[k] = b
	sess.cacheAt = append(sess.cacheAt, k)
	dropped := 0
	for len(sess.cacheAt) > ytDashPrefetch+2 {
		old := sess.cacheAt[0]
		sess.cacheAt = sess.cacheAt[1:]
		dropped += len(sess.cache[old])
		delete(sess.cache, old)
	}
	return dropped
}

// prefetchYtSegments 把当前段之后的几段先取进内存（播放器下次要就是零等待）。
//
// 失败就忘掉：预取只是提前量，不该让任何用户可见的错误冒出来——
// 真正要这段的时候会照常走一次正常取数。
func (s *Server) prefetchYtSegments(sess *ytDashSession, side string, from int) {
	track := sess.video
	if side == "a" {
		track = sess.audio
	}
	segs := track.index.Segments
	first := from + 1
	if first < 0 || first >= len(segs) {
		return
	}
	last := first + ytDashPrefetch - 1
	if last >= len(segs) {
		last = len(segs) - 1
	}
	// 已经都在缓存里就别再费劲了
	missing := 0
	for i := first; i <= last; i++ {
		if _, ok := sess.cacheGet(side, i); !ok {
			missing++
		}
	}
	if missing == 0 {
		return
	}
	if s.ytCacheBytes.Load() > ytDashCacheMax {
		return
	}
	if !sess.claimFetch(side, first) {
		return // 已经有人在取这一段
	}
	defer sess.releaseFetch(side, first)

	spanStart := segs[first].Offset
	spanEnd := segs[last].Offset + segs[last].Size - 1
	data, err := s.fetchYtRangeAll(context.Background(), s.streamClient("youtube"), track.url,
		spanStart, spanEnd, ytDashBulkConns, true)
	if err != nil {
		return // 预取失败就算了，真要用的时候会正常再取一次
	}
	for i := first; i <= last; i++ {
		off := segs[i].Offset - spanStart
		if off < 0 || off+segs[i].Size > int64(len(data)) {
			continue
		}
		if _, ok := sess.cacheGet(side, i); ok {
			continue
		}
		chunk := data[off : off+segs[i].Size]
		s.ytCacheBytes.Add(int64(len(chunk)) - int64(sess.cachePut(side, i, chunk)))
	}
}

// handleYtDashMaster 出「4K 的单文件 HLS」master；失败就 302 回 VP9 的 HLS 那条路。
//
// 回退而不是报错，是因为这条路上有一串「可能不成立」的前提：视频得有 AV1 直链、
// 得是分片 mp4、sidx 得是扁平索引。任何一条不成立时，VP9 那条虽然慢但能播——
// 让用户在「卡」和「什么都没有」之间选，显然选前者。
func (s *Server) handleYtDashMaster(w http.ResponseWriter, r *http.Request) {
	row, err := s.ytVideoByParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "视频 id 不对")
		return
	}
	height := 2160
	if q := r.URL.Query().Get("q"); q != "" {
		if n, cerr := strconv.Atoi(q); cerr == nil && n > 0 {
			height = n
		}
	}
	sess, err := s.ytDashSessionOf(r.Context(), row.VideoID, height)
	if err != nil {
		s.log.Warn("4K 单文件 HLS 建不起来，回退到 HLS 清单", "video", row.VideoID, "height", height, "err", err)
		target := fmt.Sprintf("/api/yt/videos/%d/stream.m3u8", row.ID)
		if height != 2160 {
			target += "?q=" + strconv.Itoa(height)
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}

	base := "/api/yt/dash/" + sess.id
	body := ytdash.Master(ytdash.MasterInfo{
		VideoBandwidth: sess.vBandwidth,
		AudioBandwidth: sess.aBandwidth,
		Codecs:         sess.codecs,
		Width:          sess.width,
		Height:         sess.height,
		FrameRate:      sess.fps,
		AudioURI:       base + "/a.m3u8",
		VideoURI:       base + "/v.m3u8",
	})
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, max-age=10")
	_, _ = io.WriteString(w, body)
}

// handleYtDashAny 按 `/yt/dash/{sid}/{rest}` 里的 rest 分派：
//
//	v.m3u8 / a.m3u8            单条流的播放列表
//	v-init.mp4 / a-init.mp4    init 段（ftyp+moov）
//	v.mp4 / a.mp4              分片本体（按 Range 取）
func (s *Server) handleYtDashAny(w http.ResponseWriter, r *http.Request) {
	rest := chi.URLParam(r, "rest")
	switch {
	case strings.HasSuffix(rest, ".m3u8"):
		s.handleYtDashPlaylist(w, r)
	case strings.Contains(rest, "-init."):
		s.handleYtDashInit(w, r)
	default:
		s.handleYtDashMedia(w, r)
	}
}

// handleYtDashPlaylist 出单条流的播放列表（v.m3u8 / a.m3u8）。
func (s *Server) handleYtDashPlaylist(w http.ResponseWriter, r *http.Request) {
	sess, side, ok := s.ytDashSide(r)
	if !ok {
		writeErr(w, http.StatusGone, "session_expired", "会话没了，重新解析一次即可")
		return
	}
	base := "/api/yt/dash/" + sess.id + "/" + side
	body := sess.video.index.Playlist(base+"-init.mp4", base+".mp4")
	if side == "a" {
		body = sess.audio.index.Playlist(base+"-init.mp4", base+".mp4")
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, max-age=10")
	_, _ = io.WriteString(w, body)
}

// handleYtDashInit 出 init 段（ftyp+moov）。
func (s *Server) handleYtDashInit(w http.ResponseWriter, r *http.Request) {
	sess, side, ok := s.ytDashSide(r)
	if !ok {
		writeErr(w, http.StatusGone, "session_expired", "会话没了，重新解析一次即可")
		return
	}
	track := sess.video
	if side == "a" {
		track = sess.audio
	}
	client := s.streamClient("youtube")
	var body []byte
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		body, err = s.fetchYtBytes(r.Context(), client, track.url, 0, track.index.InitEnd-1)
		if err == nil && int64(len(body)) == track.index.InitEnd {
			break
		}
	}
	if err != nil {
		s.writeYtDashUpstreamErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "private, max-age=600")
	_, _ = w.Write(body)
}

// handleYtDashMedia 是分片本体：按播放器给的 Range 取字节，Range 里是**整份文件的偏移**
// （HLS 的 EXT-X-BYTERANGE 就是这个语义），我们只把它翻译成上游的 Range。
func (s *Server) handleYtDashMedia(w http.ResponseWriter, r *http.Request) {
	sess, side, ok := s.ytDashSide(r)
	if !ok {
		writeErr(w, http.StatusGone, "session_expired", "会话没了，重新解析一次即可")
		return
	}
	track := sess.video
	if side == "a" {
		track = sess.audio
	}
	start, end, ok := parseByteRange(r.Header.Get("Range"))
	if !ok {
		// 没有 Range（或者不是分片）：退回 init，hls.js 的 EXT-X-MAP 就吃这个
		start, end = 0, track.index.InitEnd-1
	}
	segs := track.index.Segments
	last := segs[len(segs)-1]
	total := last.Offset + last.Size

	// 命中预取：直接发内存里的字节（播放器看到的是「这一段秒到」）
	if idx, ok := segmentAt(segs, start, end); ok {
		if b, hit := sess.cacheGet(side, idx); hit && int64(len(b)) == end-start+1 {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Cache-Control", "private, max-age=3600")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(b)
			s.log.Debug("4K 分片命中预取", "seg", idx, "bytes", len(b))
			from := idx
			go s.prefetchYtSegments(sess, side, from)
			return
		}
	}

	begin := time.Now()
	data := s.streamYtRange(r.Context(), w, s.streamClient("youtube"), track.url, start, end, total)
	idx, exact := segmentAt(segs, start, end)
	if r.Context().Err() != nil {
		return
	}
	if exact {
		s.log.Debug("4K 分片取回", "seg", idx, "bytes", end-start+1,
			"elapsed", time.Since(begin).Round(time.Millisecond).String())
		// 刚取的这一份顺手放进缓存：同一次播放里往回拖、或者播放器重试，就不用再跑一趟上游
		if data != nil {
			if _, hit := sess.cacheGet(side, idx); !hit {
				s.ytCacheBytes.Add(int64(len(data)) - int64(sess.cachePut(side, idx, data)))
			}
		}
		go s.prefetchYtSegments(sess, side, idx)
	}
}

// ytDashSide 从 URL 里取出会话与哪条流（v/a）。
func (s *Server) ytDashSide(r *http.Request) (*ytDashSession, string, bool) {
	id := chi.URLParam(r, "sid")
	rest := chi.URLParam(r, "rest") // "v.m3u8" / "v-init.mp4" / "v.mp4"
	side := strings.SplitN(rest, ".", 2)[0]
	side = strings.TrimSuffix(side, "-init")
	if side != "v" && side != "a" {
		return nil, "", false
	}
	s.ytDashMu.Lock()
	sess := s.ytDash[id]
	s.ytDashMu.Unlock()
	if sess == nil || time.Now().After(sess.expiresAt) {
		return nil, "", false
	}
	return sess, side, true
}

// writeYtDashUpstreamErr 统一成 502：分片失败对播放器来说就是「这一段没拿到」，
// hls.js 会自己按协议重试；把 4xx/5xx 混着回反而会让它判定成「清单坏了」。
func (s *Server) writeYtDashUpstreamErr(w http.ResponseWriter, err error) {
	writeErr(w, http.StatusBadGateway, "upstream", err.Error())
}

// segmentAt 找出这个 Range 对应第几段（对不上就 ok=false：那就没有预取可用）。
func segmentAt(segs []ytdash.Segment, start, end int64) (int, bool) {
	for i := range segs {
		if segs[i].Offset == start && segs[i].Offset+segs[i].Size-1 == end {
			return i, true
		}
	}
	return 0, false
}

// parseByteRange 解析 `Range: bytes=a-b`。
//
// HLS 的 BYTERANGE 一定是全区间（hls.js 会原样把分片里的偏移发过来），所以这里不做
// 「开区间猜一个结束位置」这种好事——猜错了就是往 MSE 里塞一段偏移对不上的字节，
// 表现是花屏而不是报错。解析不出来就当成「没有 Range」处理（回 init 段）。
func parseByteRange(raw string) (int64, int64, bool) {
	if raw == "" {
		return 0, 0, false
	}
	spec := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "bytes="))
	if i := strings.IndexByte(spec, ','); i >= 0 {
		spec = spec[:i] // 多段 Range 不支持，取第一段就够播放器用了
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false
	}
	startStr, endStr := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < start {
		return 0, 0, false
	}
	return start, end, true
}
