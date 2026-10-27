package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/library"
	"github.com/mario/myporn/internal/model"
)

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	items, err := s.st.ListJobs(r.URL.Query().Get("type"), r.URL.Query().Get("status"), queryInt(r, "limit", 50))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	status, err := s.st.RequestCancel(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "任务不存在")
		return
	}
	if status != model.JobRunning && status != model.JobCanceled {
		writeErr(w, http.StatusBadRequest, "bad_request", "任务已结束，无法取消")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status})
}

// ---------- 设置 ----------

type settingsDTO struct {
	SyncIntervalHours   int      `json:"syncIntervalHours"`
	GlobalProxy         string   `json:"globalProxy"`
	PlatformProxy       proxyDTO `json:"platformProxy"`
	NamingTemplate      string   `json:"namingTemplate"`
	DownloadConcurrency int      `json:"downloadConcurrency"`
	DownloadRateKB      int      `json:"downloadRateKb"`
	AutoDownloadNew     bool     `json:"autoDownloadNew"`
	DefaultLibraryID    int64    `json:"defaultLibraryId"`
	LibraryRoot         string   `json:"libraryRoot"`
	FFmpegAvailable     bool     `json:"ffmpegAvailable"`
	OpenList            olDTO    `json:"openlist"`
}

// proxyDTO 是各平台的专用代理。留空表示跟随全局代理。
type proxyDTO struct {
	Pornhub  string `json:"pornhub"`
	Xhamster string `json:"xhamster"`
	Beeg     string `json:"beeg"`
}

type olDTO struct {
	BaseURL  string `json:"baseUrl"`
	Root     string `json:"root"`
	HasToken bool   `json:"hasToken"`
	Token    string `json:"token,omitempty"`
}

func (s *Server) buildSettingsDTO() settingsDTO {
	get := func(k, def string) string {
		v, err := s.st.GetSetting(k)
		if err != nil || v == "" {
			return def
		}
		return v
	}
	token, _ := s.st.GetSecret(s.box, "openlist_token")
	if token == "" {
		token = s.cfg.OpenList.Token
	}
	syncHours, _ := strconv.Atoi(get("sync_interval_hours", strconv.Itoa(s.cfg.SyncHours)))
	conc, _ := strconv.Atoi(get("download_concurrency", strconv.Itoa(s.cfg.MaxDownload)))
	rateKB, _ := strconv.Atoi(get("download_rate_kb", strconv.Itoa(s.cfg.DownloadRateKB)))
	var defaultLibID int64
	if def, err := s.st.DefaultLibrary(); err == nil {
		defaultLibID = def.ID
	}
	ffmpegOK := s.runner != nil && s.runner.Encoder().Check(context.Background()) == nil
	return settingsDTO{
		SyncIntervalHours: syncHours,
		GlobalProxy:       get("global_proxy", s.cfg.GlobalProxy),
		PlatformProxy: proxyDTO{
			Pornhub:  get("proxy_pornhub", s.cfg.PlatformProxyFor("pornhub")),
			Xhamster: get("proxy_xhamster", s.cfg.PlatformProxyFor("xhamster")),
			Beeg:     get("proxy_beeg", s.cfg.PlatformProxyFor("beeg")),
		},
		NamingTemplate:      get("naming_template", library.DefaultTemplate),
		DownloadConcurrency: conc,
		DownloadRateKB:      rateKB,
		AutoDownloadNew:     get("auto_download_new", "0") == "1",
		DefaultLibraryID:    defaultLibID,
		LibraryRoot:         get("library_root", s.cfg.LibraryRoot),
		FFmpegAvailable:     ffmpegOK,
		OpenList: olDTO{
			BaseURL:  get("openlist_base_url", s.cfg.OpenList.BaseURL),
			Root:     get("openlist_root", s.cfg.OpenList.Root),
			HasToken: token != "",
		},
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.buildSettingsDTO())
}

type settingsPatch struct {
	SyncIntervalHours   *int      `json:"syncIntervalHours"`
	GlobalProxy         *string   `json:"globalProxy"`
	PlatformProxy       *proxyDTO `json:"platformProxy"`
	NamingTemplate      *string   `json:"namingTemplate"`
	DownloadConcurrency *int      `json:"downloadConcurrency"`
	DownloadRateKB      *int      `json:"downloadRateKb"`
	AutoDownloadNew     *bool     `json:"autoDownloadNew"`
	LibraryRoot         *string   `json:"libraryRoot"`
	OpenList            *olDTO    `json:"openlist"`
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var p settingsPatch
	body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := body.Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	set := func(k, v string) {
		if err := s.st.SetSetting(k, v); err != nil {
			s.log.Warn("保存设置失败", "key", k, "err", err)
		}
	}
	if p.SyncIntervalHours != nil {
		set("sync_interval_hours", fmt.Sprint(*p.SyncIntervalHours))
	}
	if p.GlobalProxy != nil {
		set("global_proxy", strings.TrimSpace(*p.GlobalProxy))
	}
	if p.PlatformProxy != nil {
		set("proxy_pornhub", strings.TrimSpace(p.PlatformProxy.Pornhub))
		set("proxy_xhamster", strings.TrimSpace(p.PlatformProxy.Xhamster))
		set("proxy_beeg", strings.TrimSpace(p.PlatformProxy.Beeg))
	}
	if p.NamingTemplate != nil {
		set("naming_template", strings.TrimSpace(*p.NamingTemplate))
	}
	if p.DownloadConcurrency != nil {
		set("download_concurrency", fmt.Sprint(*p.DownloadConcurrency))
	}
	if p.DownloadRateKB != nil {
		v := *p.DownloadRateKB
		if v < 0 {
			v = 0
		}
		set("download_rate_kb", fmt.Sprint(v))
	}
	if p.AutoDownloadNew != nil {
		v := "0"
		if *p.AutoDownloadNew {
			v = "1"
		}
		set("auto_download_new", v)
	}
	if p.LibraryRoot != nil {
		set("library_root", strings.TrimSpace(*p.LibraryRoot))
	}
	if p.OpenList != nil {
		set("openlist_base_url", strings.TrimSpace(p.OpenList.BaseURL))
		set("openlist_root", strings.TrimSpace(p.OpenList.Root))
		if p.OpenList.Token != "" {
			if err := s.st.SetSecret(s.box, "openlist_token", p.OpenList.Token); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal", "保存 token 失败")
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, s.buildSettingsDTO())
}

// ---------- SSE ----------

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "不支持流式响应")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	id, ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(id)

	fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
	flusher.Flush()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, "event: ping\ndata: {}\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, payload)
			flusher.Flush()
		}
	}
}
