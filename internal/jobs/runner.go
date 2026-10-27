// Package jobs 实现任务队列的执行器：领取任务、执行、回报进度。
//
// 队列就是 SQLite 的 jobs 表：Claim 用事务原子领取，启动时把遗留的
// running 任务重置回 queued，保证进程崩溃后可恢复。
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mario/myporn/internal/config"
	"github.com/mario/myporn/internal/events"
	"github.com/mario/myporn/internal/imgcache"
	"github.com/mario/myporn/internal/library"
	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/proxying"
	"github.com/mario/myporn/internal/social"
	"github.com/mario/myporn/internal/store"
	"github.com/mario/myporn/internal/ytsub"
)

// proxyFor 返回某平台当前该用的代理（设置优先，其次环境变量）。
func (r *Runner) proxyFor(platformKey string) string {
	if r == nil || r.px == nil {
		return ""
	}
	return r.px.For(platformKey)
}

// Handler 是某类任务的执行函数。
type Handler func(ctx context.Context, j *model.Job, c *Ctl) error

type worker struct {
	typ    string
	limit  func() int
	run    Handler
	active int32
}

type Runner struct {
	cfg        *config.Config
	st         *store.Store
	reg        *platform.Registry
	social     *social.Registry // 社交动态（M5）的适配器表
	hub        *events.Hub
	enc        *library.Encoder
	img        *imgcache.Service
	log        *slog.Logger
	interval   time.Duration
	avatars    *AvatarFiller
	profiles   *ProfileFiller
	px         *proxying.Resolver // 代理解析器：每次发请求才问，改设置即时生效
	ytDlp      *ytsub.YtDlp       // YouTube 的 yt-dlp 调用器（M6），nil 表示不启用
	ytThrottle *social.Throttle   // YouTube 的本地限速/冷却器（按 IP 计限流）

	repairing atomic.Bool // 封面修复是否已有一轮在跑

	mu      sync.Mutex
	workers []*worker
	byType  map[string]*worker

	clientsMu sync.Mutex
	clients   map[string]*http.Client
}

// NewRunner 构造执行器。avatarSources 是站外头像来源（nil 等于不补头像），
// profileSources 是站外资料来源（nil 等于不借资料）。
func NewRunner(cfg *config.Config, st *store.Store, reg *platform.Registry, socialReg *social.Registry,
	avatarSources *platform.AvatarRegistry, profileSources *platform.ProfileRegistry,
	hub *events.Hub, log *slog.Logger, images *imgcache.Service) *Runner {
	return &Runner{
		cfg: cfg, st: st, reg: reg, social: socialReg, hub: hub, log: log, img: images,
		enc:      library.NewEncoder(cfg.FFmpegPath, cfg.FFprobePath),
		interval: time.Duration(cfg.SyncHours) * time.Hour,
		byType:   map[string]*worker{},
		clients:  map[string]*http.Client{},
		avatars:  NewAvatarFiller(st, avatarSources, log),
		profiles: NewProfileFiller(st, profileSources, log),
		px:       proxying.New(st, cfg),
	}
}

// SetYouTube 注入 YouTube（M6）的抓取/下载依赖。与平台的 adapter 一样在 main 里装配，
// 这样「代理 / yt-dlp 路径 / Cookie」都是运行时现取的，改设置不用重启。
func (r *Runner) SetYouTube(dlp *ytsub.YtDlp, th *social.Throttle) {
	r.ytDlp = dlp
	r.ytThrottle = th
}

// YtDlp 暴露给 HTTP 层做健康检查（版本、是否过期）。
func (r *Runner) YtDlp() *ytsub.YtDlp { return r.ytDlp }

// YtThrottle 暴露给 HTTP 层：「清除冷却」按钮要能碰到同一个限速器。
func (r *Runner) YtThrottle() *social.Throttle {
	if r == nil {
		return nil
	}
	return r.ytThrottle
}

// LookupAvatar 按艺人名到站外头像源查一张头像（不落库，订阅预览用）。
func (r *Runner) LookupAvatar(ctx context.Context, name string) string {
	if r == nil || r.avatars == nil {
		return ""
	}
	return r.avatars.Lookup(ctx, name)
}

// FillAvatar 给已入库的艺人补头像（命中则写回数据库）。
func (r *Runner) FillAvatar(ctx context.Context, p *model.Performer) string {
	if r == nil || r.avatars == nil || p == nil {
		return ""
	}
	return r.avatars.Fill(ctx, p)
}

// LookupProfile 按艺人名到站外资料源取一份完整资料（不落库，订阅预览用）。
func (r *Runner) LookupProfile(ctx context.Context, name, platformKey string) *model.Performer {
	if r == nil || r.profiles == nil {
		return nil
	}
	return r.profiles.Resolve(ctx, name, platformKey)
}

// FillProfile 给已入库的艺人借一份站外资料（真的写入了才返回 true）。
func (r *Runner) FillProfile(ctx context.Context, p *model.Performer) bool {
	if r == nil || r.profiles == nil || p == nil {
		return false
	}
	return r.profiles.Fill(ctx, p)
}

// Register 注册一类任务；limit 决定并发上限（可动态读取设置）。
func (r *Runner) Register(typ string, limit func() int, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byType[typ]; ok {
		old.limit, old.run = limit, h
		return
	}
	w := &worker{typ: typ, limit: limit, run: h}
	r.byType[typ] = w
	r.workers = append(r.workers, w)
}

// Encoder 暴露 ffmpeg 封装器，供启动自检使用。
func (r *Runner) Encoder() *library.Encoder { return r.enc }

// Start 重置遗留任务并启动轮询循环；阻塞直到 ctx 取消。
func (r *Runner) Start(ctx context.Context) {
	if n, err := r.st.ResetRunningJobs(); err != nil {
		r.log.Error("重置 running 任务失败", "err", err)
	} else if n > 0 {
		r.log.Info("已把遗留任务重新入队", "count", n)
	}

	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		r.mu.Lock()
		ws := append([]*worker(nil), r.workers...)
		r.mu.Unlock()

		for _, w := range ws {
			for {
				limit := w.limit()
				if limit <= 0 {
					break
				}
				if int(atomic.LoadInt32(&w.active)) >= limit {
					break
				}
				job, err := r.st.Claim(w.typ)
				if err != nil {
					r.log.Error("领取任务失败", "type", w.typ, "err", err)
					break
				}
				if job == nil {
					break
				}
				atomic.AddInt32(&w.active, 1)
				wg.Add(1)
				go func(w *worker, j *model.Job) {
					defer wg.Done()
					defer atomic.AddInt32(&w.active, -1)
					r.execute(ctx, w, j)
				}(w, job)
			}
		}
	}
}

func (r *Runner) execute(parent context.Context, w *worker, j *model.Job) {
	jobCtx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	ctl := &Ctl{ctx: jobCtx, cancel: cancel, st: r.st, hub: r.hub, log: r.log, job: j}

	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("任务 panic", "job", j.ID, "type", j.Type, "panic", rec)
			_ = r.st.FinishJob(j.ID, model.JobFailed, "内部错误")
			r.publishJob(j.ID, model.JobFailed, 0, "内部错误")
		}
	}()

	r.publishJob(j.ID, model.JobRunning, 0, "开始执行")

	err := w.run(jobCtx, j, ctl)
	switch {
	case err == nil:
		_ = r.st.FinishJob(j.ID, model.JobDone, "")
		r.publishJob(j.ID, model.JobDone, 100, ctl.message())
		return
	}

	cause := context.Cause(jobCtx)
	if errors.Is(cause, errPaused) {
		_ = r.st.FinishJob(j.ID, model.JobPaused, "")
		r.publishJob(j.ID, model.JobPaused, ctl.percent(), "已暂停")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(cause, errCanceled) {
		_ = r.st.FinishJob(j.ID, model.JobCanceled, "")
		r.publishJob(j.ID, model.JobCanceled, ctl.percent(), "已取消")
		return
	}
	if parent.Err() != nil {
		// 进程退出：任务保持 running 无意义，落成 failed 等下次手动/自动重试。
		_ = r.st.FinishJob(j.ID, model.JobFailed, "服务停止")
		return
	}

	r.log.Warn("任务失败", "job", j.ID, "type", j.Type, "err", err)
	r.onFailure(w.typ, j, err)
	_ = r.st.FinishJob(j.ID, model.JobFailed, err.Error())
	r.publishJob(j.ID, model.JobFailed, ctl.percent(), err.Error())
}

func (r *Runner) publishJob(id int64, status string, progress float64, msg string) {
	j, err := r.st.GetJob(id)
	if err != nil {
		return
	}
	r.hub.Publish("job.updated", map[string]any{
		"id":         j.ID,
		"type":       j.Type,
		"status":     status,
		"progress":   progress,
		"message":    msg,
		"error":      j.Error,
		"targetId":   j.TargetID,
		"bytesDone":  j.BytesDone,
		"bytesTotal": j.BytesTotal,
		"speedBps":   j.SpeedBPS,
	})
}

// ---------- 任务控制句柄 ----------

var (
	errCanceled = errors.New("任务被取消")
	errPaused   = errors.New("任务被暂停")
)

// Ctl 是交给任务实现的控制器：报告进度、检查取消/暂停、发事件。
type Ctl struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	st     *store.Store
	hub    *events.Hub
	log    *slog.Logger
	job    *model.Job

	mu          sync.Mutex
	progress    float64
	lastMsg     string
	lastPub     time.Time
	lastBytes   int64
	lastBytesAt time.Time
	speed       float64
}

// Check 轮询任务的取消/暂停标记，必要时中断 ctx。
func (c *Ctl) Check() error {
	if c.ctx.Err() != nil {
		return context.Cause(c.ctx)
	}
	flag, err := c.st.CancelFlag(c.job.ID)
	if err != nil {
		return nil
	}
	switch flag {
	case 1:
		c.cancel(errCanceled)
		return errCanceled
	case 2:
		c.cancel(errPaused)
		return errPaused
	}
	return nil
}

// Progress 落库并广播进度；内部按 800ms 节流，避免刷爆 SQLite 与 SSE。
func (c *Ctl) Progress(percent float64, msg string, speedBPS, bytesDone, bytesTotal int64) {
	c.mu.Lock()
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	c.progress = percent
	if msg != "" {
		c.lastMsg = msg
	}
	// ffmpeg 直连封装那条路不报速度，这里用体积差自己算（指数平滑，避免抖动）。
	if speedBPS <= 0 && bytesDone > 0 {
		now := time.Now()
		if c.lastBytes > 0 && bytesDone > c.lastBytes {
			if dt := now.Sub(c.lastBytesAt).Seconds(); dt > 0.2 {
				inst := float64(bytesDone-c.lastBytes) / dt
				if c.speed > 0 {
					c.speed = c.speed*0.6 + inst*0.4
				} else {
					c.speed = inst
				}
				speedBPS = int64(c.speed)
			}
		}
		if bytesDone > c.lastBytes {
			c.lastBytes = bytesDone
			c.lastBytesAt = now
		} else if speedBPS <= 0 && c.speed > 0 {
			speedBPS = int64(c.speed)
		}
	}
	throttled := time.Since(c.lastPub) < 800*time.Millisecond
	if !throttled {
		c.lastPub = time.Now()
	}
	c.mu.Unlock()
	if throttled {
		return
	}
	if err := c.st.SetJobStats(c.job.ID, percent, speedBPS, bytesDone, bytesTotal, msg); err != nil {
		c.log.Warn("更新任务进度失败", "job", c.job.ID, "err", err)
	}
	c.hub.Publish("job.updated", map[string]any{
		"id":         c.job.ID,
		"type":       c.job.Type,
		"status":     model.JobRunning,
		"progress":   percent,
		"message":    msg,
		"error":      "",
		"targetId":   c.job.TargetID,
		"bytesDone":  bytesDone,
		"bytesTotal": bytesTotal,
		"speedBps":   speedBPS,
	})
}

// Event 向所有前端广播一个自定义事件。
func (c *Ctl) Event(name string, data any) { c.hub.Publish(name, data) }

func (c *Ctl) percent() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.progress
}

func (c *Ctl) message() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastMsg
}

// onFailure 是任务失败时的补偿钩子。
func (r *Runner) onFailure(jobType string, j *model.Job, err error) {
	if jobType != model.JobTypeSync {
		return
	}
	var p syncPayload
	if json.Unmarshal([]byte(j.Payload), &p) != nil || p.SubscriptionID <= 0 {
		return
	}
	// 失败退避 30 分钟，避免调度器每分钟重试造成风控。
	if e := r.st.PostponeSync(p.SubscriptionID, 30*time.Minute, err.Error()); e != nil {
		r.log.Warn("记录同步失败状态失败", "subscription", p.SubscriptionID, "err", e)
	}
}
