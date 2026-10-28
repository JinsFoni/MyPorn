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
	"github.com/mario/myporn/internal/social/igram"
)

// socialSyncHours 把设置里的字符串夹到合理区间（1–48 小时）。
func socialSyncHours(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 6
	}
	if n > 48 {
		return 48
	}
	return n
}

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
	SyncIntervalHours   int         `json:"syncIntervalHours"`
	GlobalProxy         string      `json:"globalProxy"`
	PlatformProxy       proxyDTO    `json:"platformProxy"`
	NamingTemplate      string      `json:"namingTemplate"`
	DownloadConcurrency int         `json:"downloadConcurrency"`
	DownloadRateKB      int         `json:"downloadRateKb"`
	AutoDownloadNew     bool        `json:"autoDownloadNew"`
	DefaultLibraryID    int64       `json:"defaultLibraryId"`
	LibraryRoot         string      `json:"libraryRoot"`
	FFmpegAvailable     bool        `json:"ffmpegAvailable"`
	OpenList            olDTO       `json:"openlist"`
	Social              socialDTO   `json:"social"`
	YouTube             youtubeDTO  `json:"youtube"`
	Bilibili            bilibiliDTO `json:"bilibili"`
}

// bilibiliDTO 是 M7 B 站的设置。只有一项：Cookie。
//
// 为什么要 Cookie：未登录时 B 站只肯给 720P（durl）和 480P（dash），
// 1080P 起必须登录。Cookie 与 YouTube 那套一样只写不读，只回答「配没配」。
type bilibiliDTO struct {
	HasCookie bool `json:"hasCookie"`
}

// bilibiliPatch 传 "" 表示清空 Cookie。
type bilibiliPatch struct {
	Cookie *string `json:"cookie"`
}

// hasBiliCookie 只回答「配没配」，不回传明文。
func (s *Server) hasBiliCookie() bool {
	v, err := s.st.GetSecret(s.box, "bili_cookie")
	return err == nil && strings.TrimSpace(v) != ""
}

// normalizeBiliCookie 把用户粘进来的各种形状整理成一条 Cookie 头。
//
// 认三种输入（2026-10 实测都被用户拿来用过）：
//
//  1. 请求头里的整段 Cookie：`SESSDATA=xxx; bili_jct=yyy; DedeUserID=1`
//  2. 只贴 SESSDATA 的值（一长串 %2C 分隔的 base64）
//  3. **浏览器/curl 导出的 cookies.txt（Netscape 格式）**——每行 7 列、制表符分隔：
//     域  子域 路径 安全  过期时间  名  值
//
// 第 3 种只收域里带 bilibili 的行（整份浏览器 Cookie 导出时常常混着别的站点，那些不发）。
//
// 第 3 种以前会被逐段当成「裸 SESSDATA 值」，粘一份 30 条 cookie 的文件就会拼出
// 两百多个 `SESSDATA=<莫名其妙>` 的假字段（实测：4,639 字节、213 段）。B 站当然不认，
// 于是「配了 Cookie 还是 720P、还是被限流」。所以这里按行分类处理，注释行（# 开头，
// 但 HttpOnly 的 `#HttpOnly_` 行是数据行）跳过，同名 cookie 只留最后一个。
func normalizeBiliCookie(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var parts []string
	idx := map[string]int{}
	add := func(k, v string) {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			return
		}
		if i, ok := idx[k]; ok {
			parts[i] = k + "=" + v
			return
		}
		idx[k] = len(parts)
		parts = append(parts, k+"="+v)
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#HttpOnly_") {
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if strings.HasPrefix(line, "#") {
			continue // 真注释：cookies.txt 的头几行就是这些
		}
		if domain, name, value, ok := parseNetscapeLine(line); ok {
			// cookies.txt 常常是「整份浏览器 Cookie」导出的，里面混着别的站点。
			// 那些别往 B 站发：既没用，又等于把无关站点的登录态递给第三方。
			if strings.Contains(strings.ToLower(domain), "bilibili") {
				add(name, value)
			}
			continue
		}
		// Cookie 头那种 k=v; k=v（也兼容换行/制表符分隔的键值对）。
		for _, seg := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ';' || r == '\t'
		}) {
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			k, v, ok := strings.Cut(seg, "=")
			if !ok {
				// 只贴了值：SESSDATA 是唯一能这么认的。
				if looksLikeBareSessdata(seg) {
					add("SESSDATA", seg)
				}
				continue
			}
			add(k, v)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("Cookie 里没认出任何字段")
	}
	has := false
	for k := range idx {
		if strings.EqualFold(k, "SESSDATA") {
			has = true
		}
	}
	if !has {
		return "", fmt.Errorf("Cookie 里没有 SESSDATA：B 站登录态靠它，请从已登录的浏览器里复制整串")
	}
	return strings.Join(parts, "; "), nil
}

// parseNetscapeLine 认 Netscape cookies.txt 的一行：域 子域 路径 安全 过期 名 值。
// 制表符分隔是标准写法，某些导出会用空格对齐，所以空格分隔也认（过期列必须全是数字）。
func parseNetscapeLine(line string) (domain, name, value string, ok bool) {
	fields := strings.Split(line, "\t")
	if len(fields) < 7 {
		fields = strings.Fields(line)
		if len(fields) != 7 || !isAllDigits(fields[4]) {
			return "", "", "", false
		}
	}
	// 末尾可能还有空字段（值里带制表符的极端情况），只取第 1、6、7 列。
	domain, name, value = strings.TrimSpace(fields[0]), strings.TrimSpace(fields[5]), strings.TrimSpace(fields[6])
	if name == "" || value == "" {
		return "", "", "", false
	}
	return domain, name, value, true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// looksLikeBareSessdata 判断这一段是不是「只贴了 SESSDATA 的值」：
// 没有空格、没有 =（否则就是 k=v 的键值对）、长度像样。
func looksLikeBareSessdata(seg string) bool {
	if len(seg) < 16 || strings.ContainsAny(seg, " =") {
		return false
	}
	// SESSDATA 是 URL 编码过的 base64 加上两个 %2C 分隔的时间戳。
	return strings.Contains(seg, "%2C") || len(seg) >= 32
}

// youtubeDTO 是 M6 YouTube 订阅的设置。
//
// 这里不放 yt-dlp 的版本号：取版本要 fork 一个进程，而设置弹窗打开就会拉一次设置。
// 版本信息走 /api/yt/health，前端进到 YouTube 那一节时才问（它决定要不要显示「请升级」）。
// Cookie 与 Instagram 一样只写不读，只告诉前端「有没有配」。
type youtubeDTO struct {
	SyncHours     int    `json:"syncHours"`
	BackfillLimit int    `json:"backfillLimit"`
	Format        string `json:"format"`
	YtDlpPath     string `json:"ytdlpPath"`
	HasCookies    bool   `json:"hasCookies"`
}

// youtubePatch 是设置里可以改的 YouTube 项；Cookies 传 "" 表示清空。
type youtubePatch struct {
	SyncHours     *int    `json:"syncHours"`
	BackfillLimit *int    `json:"backfillLimit"`
	Format        *string `json:"format"`
	YtDlpPath     *string `json:"ytdlpPath"`
	Cookies       *string `json:"cookies"`
}

// socialDTO 是 M5 社交动态的设置。
//
// InstagramCookie 只写不读：Cookie 是登录凭证，前端只需要知道「有没有配」，
// 每次把明文回传给浏览器等于白白多一处泄漏面。
type socialDTO struct {
	SyncHours          int    `json:"syncHours"`
	HasInstagramCookie bool   `json:"hasInstagramCookie"`
	InstagramCookie    string `json:"instagramCookie,omitempty"`
}

// socialPatch 是设置里可以改的社交项；InstagramCookie 传 "" 表示清空。
type socialPatch struct {
	SyncHours       *int    `json:"syncHours"`
	InstagramCookie *string `json:"instagramCookie"`
}

// proxyDTO 是各平台的专用代理。留空表示跟随全局代理。
//
// Instagram / X 也在这里：抓社交动态时的出口 IP 直接决定会不会被风控
// （见 docs/M5-SOCIAL.md §16.4），所以必须能在界面上改、改完立刻生效。
type proxyDTO struct {
	Pornhub   string `json:"pornhub"`
	Xhamster  string `json:"xhamster"`
	Beeg      string `json:"beeg"`
	Instagram string `json:"instagram"`
	X         string `json:"x"`
	YouTube   string `json:"youtube"`
	Bilibili  string `json:"bilibili"`
}

// youtubeInt 读一个整数设置并夹到区间内（坏值一律回落默认）。
func youtubeInt(raw string, def, min, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// hasYtCookies 只回答「配没配」，不回传明文。
func (s *Server) hasYtCookies() bool {
	v, err := s.st.GetSecret(s.box, "yt_cookies")
	return err == nil && strings.TrimSpace(v) != ""
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
			Pornhub:   get("proxy_pornhub", s.cfg.PlatformProxyFor("pornhub")),
			Xhamster:  get("proxy_xhamster", s.cfg.PlatformProxyFor("xhamster")),
			Beeg:      get("proxy_beeg", s.cfg.PlatformProxyFor("beeg")),
			Instagram: get("proxy_instagram", s.cfg.PlatformProxyFor("instagram")),
			X:         get("proxy_x", s.cfg.PlatformProxyFor("x")),
			YouTube:   get("proxy_youtube", s.cfg.PlatformProxyFor("youtube")),
			Bilibili:  get("proxy_bilibili", s.cfg.PlatformProxyFor("bilibili")),
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
		Social: socialDTO{
			SyncHours:          socialSyncHours(get("social_sync_hours", "6")),
			HasInstagramCookie: s.hasInstagramCookie(),
		},
		YouTube: youtubeDTO{
			SyncHours:     youtubeInt(get("yt_sync_hours", "6"), 6, 1, 48),
			BackfillLimit: youtubeInt(get("yt_backfill_limit", "200"), 200, 10, 2000),
			Format:        get("yt_format", ""),
			YtDlpPath:     get("ytdlp_path", ""),
			HasCookies:    s.hasYtCookies(),
		},
		Bilibili: bilibiliDTO{HasCookie: s.hasBiliCookie()},
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.buildSettingsDTO())
}

type settingsPatch struct {
	SyncIntervalHours   *int           `json:"syncIntervalHours"`
	GlobalProxy         *string        `json:"globalProxy"`
	PlatformProxy       *proxyDTO      `json:"platformProxy"`
	NamingTemplate      *string        `json:"namingTemplate"`
	DownloadConcurrency *int           `json:"downloadConcurrency"`
	DownloadRateKB      *int           `json:"downloadRateKb"`
	AutoDownloadNew     *bool          `json:"autoDownloadNew"`
	LibraryRoot         *string        `json:"libraryRoot"`
	OpenList            *olDTO         `json:"openlist"`
	Social              *socialPatch   `json:"social"`
	YouTube             *youtubePatch  `json:"youtube"`
	Bilibili            *bilibiliPatch `json:"bilibili"`
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
		set("proxy_instagram", strings.TrimSpace(p.PlatformProxy.Instagram))
		set("proxy_x", strings.TrimSpace(p.PlatformProxy.X))
		set("proxy_youtube", strings.TrimSpace(p.PlatformProxy.YouTube))
		set("proxy_bilibili", strings.TrimSpace(p.PlatformProxy.Bilibili))
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
	if p.Social != nil {
		if p.Social.SyncHours != nil {
			v := *p.Social.SyncHours
			if v < 1 {
				v = 1
			}
			if v > 48 {
				v = 48
			}
			set("social_sync_hours", fmt.Sprint(v))
		}
		if p.Social.InstagramCookie != nil {
			raw := strings.TrimSpace(*p.Social.InstagramCookie)
			if raw == "" {
				// 传空串 = 清除 Cookie（换号、或不想再用 IG 时用）。
				if err := s.st.SetSecret(s.box, "social_cookie_instagram", ""); err != nil {
					writeErr(w, http.StatusInternalServerError, "internal", "清除 Cookie 失败")
					return
				}
			} else {
				normalized, err := igram.NormalizeCookie(raw)
				if err != nil {
					writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
					return
				}
				if err := s.st.SetSecret(s.box, "social_cookie_instagram", normalized); err != nil {
					writeErr(w, http.StatusInternalServerError, "internal", "保存 Cookie 失败")
					return
				}
			}
		}
	}
	if p.YouTube != nil {
		if p.YouTube.SyncHours != nil {
			v := *p.YouTube.SyncHours
			if v < 1 {
				v = 1
			}
			if v > 48 {
				v = 48
			}
			set("yt_sync_hours", fmt.Sprint(v))
		}
		if p.YouTube.BackfillLimit != nil {
			v := *p.YouTube.BackfillLimit
			if v < 10 {
				v = 10
			}
			if v > 2000 {
				v = 2000
			}
			set("yt_backfill_limit", fmt.Sprint(v))
		}
		if p.YouTube.Format != nil {
			// 空串 = 用默认串（AV1 最高画质 + 最高音质），不写库，省得以后改默认值改不动。
			set("yt_format", strings.TrimSpace(*p.YouTube.Format))
		}
		if p.YouTube.YtDlpPath != nil {
			set("ytdlp_path", strings.TrimSpace(*p.YouTube.YtDlpPath))
		}
		if p.YouTube.Cookies != nil {
			raw := strings.TrimSpace(*p.YouTube.Cookies)
			if err := s.st.SetSecret(s.box, "yt_cookies", raw); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal", "保存 Cookie 失败")
				return
			}
		}
	}
	if p.Bilibili != nil && p.Bilibili.Cookie != nil {
		raw, err := normalizeBiliCookie(*p.Bilibili.Cookie)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		// 空串 = 清除（回到未登录画质）。
		if err := s.st.SetSecret(s.box, "bili_cookie", raw); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "保存 Cookie 失败")
			return
		}
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
