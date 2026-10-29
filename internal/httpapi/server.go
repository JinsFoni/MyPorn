// Package httpapi 提供 HTTP 接口与静态资源服务。
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mario/myporn/internal/config"
	"github.com/mario/myporn/internal/events"
	"github.com/mario/myporn/internal/imgcache"
	"github.com/mario/myporn/internal/jobs"
	"github.com/mario/myporn/internal/muxcache"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/proxying"
	"github.com/mario/myporn/internal/secret"
	"github.com/mario/myporn/internal/social"
	"github.com/mario/myporn/internal/store"
	"github.com/mario/myporn/internal/ytstream"
)

type Server struct {
	cfg    *config.Config
	st     *store.Store
	reg    *platform.Registry
	social *social.Registry   // 社交动态（M5）适配器
	px     *proxying.Resolver // 代理解析器：设置里的代理改完立刻生效
	hub    *events.Hub
	runner *jobs.Runner
	box    *secret.Box
	log    *slog.Logger
	static fs.FS
	images *imgcache.Service // 图片缓存（与同步任务共用同一份磁盘缓存）
	// mux 是「音画分轨」档位的合流缓存（B 站 DASH 用）：第一次点播放时用 ffmpeg
	// 把画面和声音合成一份 mp4 落盘，之后直接当文件发。runner 为 nil 时也是 nil。
	mux *muxcache.Cache
	// ytStream 是 YouTube「站内播放」的地址簿与解析缓存（M6 补的那条线）。
	ytStream *ytstream.Manager
	// ytMaster 缓存「解析过的 master 清单」。googlevideo 每拉一次 master 都会重新签名
	// 子清单地址，所以预热和播放器必须用同一份原文，否则两边拿到的 URL 根本不一样。
	ytMasterMu sync.Mutex
	ytMaster   map[string]cachedYtSource
	limiter    *loginLimiter
	started    time.Time
	clients    map[string]*http.Client // 平台出网客户端（图片代理 / 串流共用）
	mu         sync.Mutex
}

func New(cfg *config.Config, st *store.Store, reg *platform.Registry, socialReg *social.Registry,
	hub *events.Hub, runner *jobs.Runner, box *secret.Box, log *slog.Logger, static fs.FS,
	images *imgcache.Service) *Server {
	s := &Server{
		cfg: cfg, st: st, reg: reg, social: socialReg, hub: hub, runner: runner, box: box, log: log,
		px:     proxying.New(st, cfg),
		static: static, images: images, limiter: newLoginLimiter(), started: time.Now(),
		clients:  map[string]*http.Client{},
		ytMaster: map[string]cachedYtSource{},
	}
	if runner != nil {
		s.mux = muxcache.New(filepath.Join(cfg.DataDir, "mux"), runner.Encoder())
		// 合流的耗时只有 muxcache 自己知道（4K 一档 554 MB 起步，实测 49 秒~7 分钟，
		// 差别全在 CDN 那条线上），日志记在这里才不会漏。
		s.mux.OnMerged = func(key string, elapsed time.Duration, size int64, err error) {
			if err != nil {
				log.Warn("分轨合流失败", "key", key, "elapsed", elapsed.Round(100*time.Millisecond), "err", err)
				return
			}
			log.Info("分轨合流完成", "key", key, "size", size,
				"elapsed", elapsed.Round(100*time.Millisecond))
		}
	}
	// 解析器现取 yt-dlp：设置页换完路径，下一个请求就用新的。
	s.ytStream = ytstream.NewManager(func(ctx context.Context, videoID string) (ytstream.Upstream, error) {
		dlp := s.ytDlp()
		if dlp == nil || !dlp.Available() {
			return ytstream.Upstream{}, social.StaleParser("没有找到 yt-dlp：站内播放依赖它解析清单，去设置 → YouTube 里填路径")
		}
		info, err := dlp.ResolveStream(ctx, videoID)
		if err != nil {
			return ytstream.Upstream{}, err
		}
		tracks := make([]ytstream.Track, 0, len(info.Tracks))
		for _, t := range info.Tracks {
			tracks = append(tracks, ytstream.Track{
				Height: t.Height, Codecs: t.Codecs, Bandwidth: t.Bandwidth,
				VideoURL: t.VideoURL, AudioURL: t.AudioURL, AudioCodecs: t.AudioCodecs,
			})
		}
		return ytstream.Upstream{
			VideoID:     info.VideoID,
			Title:       info.Title,
			DurationSec: info.DurationSec,
			MasterURL:   info.MasterURL,
			ExpiresAt:   info.ExpiresAt,
			Tracks:      tracks,
		}, nil
	}, log)
	return s
}

// platformClient 取某个平台出网用的客户端。
func (s *Server) platformClient(key string) *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.platformClientLocked(key)
}

// platformClientLocked 供已持锁的调用方使用（streamClient 会复制它）。
func (s *Server) platformClientLocked(key string) *http.Client {
	if c, ok := s.clients[key]; ok {
		return c
	}
	// 动态问代理：用户在设置里换完，下一个请求立刻走新出口。
	c, err := platform.NewDynamicHTTPClient(func() string { return s.px.For(key) }, 20*time.Second)
	if err != nil {
		c, _ = platform.NewHTTPClient("", 20*time.Second)
	}
	s.clients[key] = c
	return c
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	// 普通接口给 60s；长连接自己收尾，不进这个盒子：
	//   - /api/stream 是边下边传的媒体流，一到点就被掐断等于看长片必断；
	//   - /api/events 是 SSE，超时写手一发就会在已经流出去的响应上再写一次头
	//     （日志里每分钟一条 "superfluous response.WriteHeader call"）。
	timed := middleware.Timeout(60 * time.Second)
	r.Use(func(next http.Handler) http.Handler {
		h := timed(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if isLongLived(req.URL.Path) {
				next.ServeHTTP(w, req)
				return
			}
			h.ServeHTTP(w, req)
		})
	})

	r.Get("/healthz", s.handleHealth)

	r.Route("/api", func(api chi.Router) {
		api.Post("/auth/login", s.handleLogin)
		api.Group(func(pr chi.Router) {
			pr.Use(s.requireSession)
			pr.Post("/auth/logout", s.handleLogout)
			pr.Get("/auth/me", s.handleMe)

			pr.Get("/platforms", s.handlePlatforms)

			pr.Get("/performers", s.handleListPerformers)
			pr.Get("/performers/{id}", s.handleGetPerformer)
			pr.Post("/performers/resolve", s.handleResolvePerformer)

			pr.Post("/subscriptions", s.handleCreateSubscription)
			pr.Get("/subscriptions", s.handleListSubscriptions)
			pr.Patch("/subscriptions/{id}", s.handlePatchSubscription)
			pr.Delete("/subscriptions/{id}", s.handleDeleteSubscription)
			pr.Post("/subscriptions/{id}/sync", s.handleSyncSubscription)

			pr.Get("/search/performers", s.handleSearchPerformers)

			pr.Get("/libraries", s.handleListLibraries)
			pr.Post("/libraries", s.handleCreateLibrary)
			pr.Post("/libraries/validate", s.handleValidateLibraryPath)
			pr.Patch("/libraries/{id}", s.handlePatchLibrary)
			pr.Delete("/libraries/{id}", s.handleDeleteLibrary)

			pr.Get("/downloads", s.handleListDownloads)
			pr.Get("/videos/{id}/download-options", s.handleDownloadOptions)
			pr.Post("/videos/{id}/download", s.handleCreateDownload)

			pr.Get("/videos", s.handleListVideos)
			pr.Get("/videos/new", s.handleListNewVideos)
			pr.Get("/videos/{id}", s.handleGetVideo)
			pr.Post("/videos/{id}/seen", s.handleVideoSeen)
			pr.Get("/videos/{id}/playback", s.handlePlayback)
			// 本地库文件的直接下发（Range 续传）：下载过的影片不再依赖平台解析。
			pr.Get("/videos/{id}/file", s.handleVideoFile)
			// 音画分轨的档位（B 站 1080P+）：服务端合流成一份 mp4 再下发。
			pr.Get("/videos/{id}/remux", s.handleVideoRemux)

			pr.Get("/jobs", s.handleListJobs)
			pr.Post("/jobs/{id}/cancel", s.handleCancelJob)
			pr.Post("/jobs/{id}/pause", s.handlePauseJob)
			pr.Post("/jobs/{id}/resume", s.handleResumeJob)
			pr.Post("/jobs/{id}/retry", s.handleRetryJob)
			pr.Delete("/jobs/{id}", s.handleDeleteJob)

			// 管理页：统计 + 关注管理，一次请求给全（见 handlers_manage.go）
			pr.Get("/manage", s.handleManage)

			// 社交动态（M5）：照片墙 + 博主管理 + 一键存进本地库
			pr.Get("/moments", s.handleListMoments)
			pr.Get("/moments/accounts", s.handleListMomentAccounts)
			pr.Post("/moments/accounts", s.handleCreateMomentAccount)
			pr.Post("/moments/resolve", s.handleResolveMoment)
			pr.Patch("/moments/accounts/{id}", s.handlePatchMomentAccount)
			pr.Delete("/moments/accounts/{id}", s.handleDeleteMomentAccount)
			pr.Post("/moments/accounts/{id}/sync", s.handleSyncMomentAccount)
			pr.Post("/moments/seen", s.handleMarkMomentsSeen)
			pr.Post("/moments/photos/save", s.handleSaveMomentPhotos)
			pr.Post("/moments/cooldown", s.handleClearMomentCooldown)
			pr.Get("/manage/moments", s.handleManageMoments)

			// YouTube 订阅（M6）：频道管理 + 视频墙 + 下载进本地库。
			pr.Get("/yt/channels", s.handleListYtChannels)
			pr.Post("/yt/channels", s.handleCreateYtChannel)
			pr.Post("/yt/resolve", s.handleResolveYtChannel)
			pr.Patch("/yt/channels/{id}", s.handlePatchYtChannel)
			pr.Delete("/yt/channels/{id}", s.handleDeleteYtChannel)
			pr.Post("/yt/channels/{id}/sync", s.handleSyncYtChannel)
			pr.Post("/yt/channels/{id}/download-unseen", s.handleYtDownloadUnseen)
			pr.Get("/yt/videos", s.handleListYtVideos)
			pr.Post("/yt/videos/seen", s.handleYtSeen)
			pr.Post("/yt/videos/{id}/save", s.handleYtSave)
			pr.Post("/yt/videos/{id}/refresh", s.handleYtRefresh)
			pr.Post("/yt/videos/{id}/download", s.handleYtDownload)
			// 站内在线播放：清单（改写成会话地址）+ 画质清单 + 会话取物口。
			pr.Get("/yt/videos/{id}/stream", s.handleYtStreamInfo)
			pr.Get("/yt/videos/{id}/stream.m3u8", s.handleYtStreamMaster)
			// 4K 的原生播放：服务端把 DASH 的 AV1 画面 + AAC 音轨合成一份 mp4（ffmpeg -c copy），
			// 落盘缓存后按普通文件发。?status=1 立刻回进度 JSON，其余情况直接发文件（Range 照常）。
			pr.Get("/yt/videos/{id}/file", s.handleYtFile)
			pr.Get("/yt/seg/{sid}/{n}", s.handleYtStreamSeg)
			pr.Get("/yt/health", s.handleYtHealth)
			// 补「时间未知」：现场统计 + 排一个 yt_meta 任务（走观看页，不用 yt-dlp）。
			pr.Post("/yt/fill-dates", s.handleYtFillDates)
			// 深链接 /youtube?v=<videoId> 用：墙上那一页未必包含这条
			pr.Get("/yt/videos/lookup", s.handleYtVideoLookup)
			pr.Get("/manage/youtube", s.handleManageYt)

			// B 站（M7）：UP 主订阅 + 投稿墙，独立一页。
			// 数据就在 videos / performers 里（platform='bilibili'），所以这里只有读接口：
			// 播放、下载、合流复用 /api/videos/{id}/*（见 handlers_bilibili.go 顶部）。
			pr.Get("/bilibili/channels", s.handleListBiliChannels)
			pr.Get("/bilibili/videos", s.handleListBiliVideos)
			pr.Post("/bilibili/seen", s.handleBiliSeen)
			pr.Post("/bilibili/channels/{id}/download-unseen", s.handleBiliDownloadUnseen)
			pr.Get("/bilibili/health", s.handleBiliHealth)
			pr.Get("/manage/bilibili", s.handleManageBili)

			pr.Get("/settings", s.handleGetSettings)
			pr.Put("/settings", s.handlePutSettings)

			pr.Get("/img", s.handleImage)
			// 播放地址同源代理：平台 CDN 校验 Referer，浏览器直连会被 404。
			pr.Get("/stream", s.handleStream)
			pr.Get("/events", s.handleEvents)
		})
		api.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeErr(w, http.StatusNotFound, "not_found", "接口不存在")
		})
	})

	r.NotFound(s.serveSPA)
	return r
}

// isLongLived 报告这个路径是不是「靠连接本身活着」的接口，不该套超时中间件。
func isLongLived(path string) bool {
	// 用全等而不是前缀：/api/streams 这种将来真出现了要照旧走超时。
	if path == "/api/stream" || path == "/api/events" {
		return true
	}
	// 本地文件下发是整片传输，一部 4K 片子看两小时也可能一直连着。
	if strings.HasSuffix(path, "/file") && strings.HasPrefix(path, "/api/videos/") {
		return true
	}
	// 合流接口的第一请求要等 ffmpeg 跑完（长片十几秒），成品下发同样是一整片传输。
	if strings.HasSuffix(path, "/remux") && strings.HasPrefix(path, "/api/videos/") {
		return true
	}
	// YouTube 站内播放的分片：单片几秒，但接着一个 CDN 慢慢吐的时候也可能超 60s。
	if strings.HasPrefix(path, "/api/yt/seg/") {
		return true
	}
	return false
}

// ---------- JSON 工具 ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// 响应已开始写入，只能记录
		return
	}
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg}})
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(dst)
}

func queryInt(r *http.Request, key string, def int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"version":   s.cfg.Version,
		"uptimeSec": int(time.Since(s.started).Seconds()),
	})
}
