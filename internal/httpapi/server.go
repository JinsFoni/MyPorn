// Package httpapi 提供 HTTP 接口与静态资源服务。
package httpapi

import (
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mario/myporn/internal/config"
	"github.com/mario/myporn/internal/events"
	"github.com/mario/myporn/internal/jobs"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/secret"
	"github.com/mario/myporn/internal/store"
)

type Server struct {
	cfg     *config.Config
	st      *store.Store
	reg     *platform.Registry
	hub     *events.Hub
	runner  *jobs.Runner
	box     *secret.Box
	log     *slog.Logger
	static  fs.FS
	limiter *loginLimiter
	started time.Time
	clients map[string]*http.Client // 平台图片代理专用
	mu      sync.Mutex
}

func New(cfg *config.Config, st *store.Store, reg *platform.Registry, hub *events.Hub,
	runner *jobs.Runner, box *secret.Box, log *slog.Logger, static fs.FS) *Server {
	return &Server{
		cfg: cfg, st: st, reg: reg, hub: hub, runner: runner, box: box, log: log,
		static: static, limiter: newLoginLimiter(), started: time.Now(),
		clients: map[string]*http.Client{},
	}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	// 普通接口给 60s；/api/stream 是长时间在传的媒体流，交给请求上下文自己收尾，
	// 否则看长片时一到点就被掐断。
	timed := middleware.Timeout(60 * time.Second)
	r.Use(func(next http.Handler) http.Handler {
		h := timed(next)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasPrefix(req.URL.Path, "/api/stream") {
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

			pr.Get("/jobs", s.handleListJobs)
			pr.Post("/jobs/{id}/cancel", s.handleCancelJob)
			pr.Post("/jobs/{id}/pause", s.handlePauseJob)
			pr.Post("/jobs/{id}/resume", s.handleResumeJob)
			pr.Post("/jobs/{id}/retry", s.handleRetryJob)
			pr.Delete("/jobs/{id}", s.handleDeleteJob)

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
