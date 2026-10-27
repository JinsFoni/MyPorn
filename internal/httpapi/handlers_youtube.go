package httpapi

// YouTube 订阅（M6）的接口：频道管理 + 视频墙 + 下载进本地库。
//
// 契约见 docs/M6-YOUTUBE.md §9。与照片墙（M5）的差别：
//   - 添加频道要联网解析（@handle → UC… 需要抓一次频道页），所以同样分两步（resolve → create）；
//   - 视频要能下载，所以多一个「下载」入口和 yt-dlp 健康度接口——
//     yt-dlp 过期是这个功能最常见的失败原因，界面必须能直接看到版本号（M6 §11.5）。

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/social"
	"github.com/mario/myporn/internal/store"
	"github.com/mario/myporn/internal/ytsub"
)

// ---- DTO ----

type ytChannelBriefDTO struct {
	ID        int64  `json:"id"`
	ChannelID string `json:"channelId"`
	Handle    string `json:"handle"`
	Title     string `json:"title"`
	AvatarURL string `json:"avatarUrl"`
}

type ytChannelDTO struct {
	ID           int64  `json:"id"`
	ChannelID    string `json:"channelId"`
	Handle       string `json:"handle"`
	Title        string `json:"title"`
	AvatarURL    string `json:"avatarUrl"`
	ProfileURL   string `json:"profileUrl"`
	Enabled      bool   `json:"enabled"`
	VideoCount   int    `json:"videoCount"`
	UnseenCount  int    `json:"unseenCount"`
	BackfilledAt any    `json:"backfilledAt"`
	LastSyncedAt any    `json:"lastSyncedAt"`
	LastError    string `json:"lastError"`
}

type ytVideoDTO struct {
	ID          int64             `json:"id"`
	VideoID     string            `json:"videoId"`
	Title       string            `json:"title"`
	URL         string            `json:"url"`
	ThumbURL    string            `json:"thumbUrl"`
	PosterURL   string            `json:"posterUrl"`
	EmbedURL    string            `json:"embedUrl"`
	DurationSec int               `json:"durationSec"`
	Views       int64             `json:"views"`
	PublishedAt any               `json:"publishedAt"`
	DateKnown   bool              `json:"dateKnown"`
	IsLive      bool              `json:"isLive"`
	Seen        bool              `json:"seen"`
	Saved       bool              `json:"saved"`
	Downloaded  bool              `json:"downloaded"`
	Description string            `json:"description"`
	Channel     ytChannelBriefDTO `json:"channel"`
}

type ytVideosPageDTO struct {
	Items      []ytVideoDTO `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

func (s *Server) ytChannelDTO(c *store.YtChannel) ytChannelDTO {
	return ytChannelDTO{
		ID: c.ID, ChannelID: c.ChannelID, Handle: c.Handle, Title: c.Title,
		AvatarURL: imgProxy("youtube", c.AvatarURL), ProfileURL: c.ProfileURL,
		Enabled: c.Enabled, VideoCount: c.VideoCount, UnseenCount: c.UnseenCount,
		BackfilledAt: rfc3339(c.BackfilledAt), LastSyncedAt: rfc3339(c.LastSyncedAt),
		LastError: c.LastError,
	}
}

func (s *Server) ytVideoDTO(v *store.YtVideoRow) ytVideoDTO {
	return ytVideoDTO{
		ID: v.ID, VideoID: v.VideoID, Title: v.Title,
		URL:         "https://www.youtube.com/watch?v=" + v.VideoID,
		ThumbURL:    imgProxy("youtube", firstNonEmptyStr(v.ThumbnailURL, ytsub.ThumbURL(v.VideoID, "mqdefault"))),
		PosterURL:   imgProxy("youtube", ytsub.ThumbURL(v.VideoID, "maxresdefault")),
		EmbedURL:    "https://www.youtube-nocookie.com/embed/" + v.VideoID,
		DurationSec: v.DurationSec, Views: v.Views, PublishedAt: rfc3339(v.PublishedAt),
		DateKnown: v.PublishedAt != nil, IsLive: v.IsLive, Seen: v.Seen, Saved: v.Saved,
		Downloaded: v.Downloaded > 0, Description: v.Description,
		Channel: ytChannelBriefDTO{
			ID: v.Channel.ID, ChannelID: v.Channel.ChannelID, Handle: v.Channel.Handle,
			Title: v.Channel.Title, AvatarURL: imgProxy("youtube", v.Channel.AvatarURL),
		},
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---- 频道 ----

func (s *Server) handleListYtChannels(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListYtChannels(false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取频道列表失败")
		return
	}
	items := make([]ytChannelDTO, 0, len(list))
	for _, c := range list {
		items = append(items, s.ytChannelDTO(c))
	}
	stats, _ := s.st.YtStats()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "stats": stats})
}

// handleResolveYtChannel 只解析不落库，给「添加前预览」用。
func (s *Server) handleResolveYtChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	ch, err := s.resolveYt(r.Context(), in.URL)
	if err != nil {
		writeSocialErr(w, err)
		return
	}
	// 顺手告诉前端「这个频道是不是已经订阅过」。
	existing, _ := s.st.GetYtChannelByPlatformID(ch.ChannelID)
	resp := map[string]any{
		"channelId":  ch.ChannelID,
		"handle":     ch.Handle,
		"title":      ch.Title,
		"avatarUrl":  imgProxy("youtube", ch.AvatarURL),
		"profileUrl": ch.ProfileURL,
		"subscribed": existing != nil,
		"ytdlpOk":    s.ytDlp() != nil && s.ytDlp().Available(),
	}
	if existing != nil {
		resp["id"] = existing.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCreateYtChannel 解析 + 落库 + 立刻排一次深度回填。
func (s *Server) handleCreateYtChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	ch, err := s.resolveYt(r.Context(), in.URL)
	if err != nil {
		writeSocialErr(w, err)
		return
	}
	id, created, err := s.st.UpsertYtChannel(&store.YtChannel{
		ChannelID: ch.ChannelID, Handle: ch.Handle, Title: ch.Title,
		AvatarURL: ch.AvatarURL, ProfileURL: ch.ProfileURL, Enabled: true,
	})
	if err != nil {
		s.log.Error("写入频道失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "保存频道失败")
		return
	}
	if created {
		// 首次添加：跑一次深度回填（yt-dlp 一次列 200 条，比 RSS 的 15 条像样得多）。
		if _, err := s.runner.EnqueueYtSync(id, "deep"); err != nil {
			s.log.Warn("排队深度回填失败", "channel", id, "err", err)
		}
	}
	if got, err := s.st.GetYtChannel(id); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"channel": s.ytChannelDTO(got), "created": created})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "created": created})
}

func (s *Server) handlePatchYtChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "频道 id 不对")
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	if in.Enabled != nil {
		if err := s.st.SetYtChannelEnabled(id, *in.Enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "更新频道失败")
			return
		}
	}
	if got, err := s.st.GetYtChannel(id); err == nil {
		writeJSON(w, http.StatusOK, s.ytChannelDTO(got))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteYtChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "频道 id 不对")
		return
	}
	if err := s.st.DeleteYtChannel(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "删除频道失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSyncYtChannel 手动同步：deep=true 走 yt-dlp 深度回填，否则走 RSS 增量。
func (s *Server) handleSyncYtChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "频道 id 不对")
		return
	}
	var in struct {
		Deep bool `json:"deep"`
	}
	_ = decodeJSON(r, &in)
	mode := "auto"
	if in.Deep {
		mode = "deep"
	}
	jobID, err := s.runner.EnqueueYtSync(id, mode)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "排队同步失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": jobID})
}

// ---- 视频墙 ----

func (s *Server) handleListYtVideos(w http.ResponseWriter, r *http.Request) {
	rows, next, err := s.st.ListYtVideos(store.YtVideosQuery{
		ChannelRowID: int64(queryInt(r, "channelId", 0)),
		UnseenOnly:   r.URL.Query().Get("unseen") == "1",
		SavedOnly:    r.URL.Query().Get("saved") == "1",
		Cursor:       r.URL.Query().Get("cursor"),
		Limit:        queryInt(r, "limit", 36),
	})
	if err != nil {
		s.log.Error("查询视频墙失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "读取视频墙失败")
		return
	}
	items := make([]ytVideoDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.ytVideoDTO(row))
	}
	writeJSON(w, http.StatusOK, ytVideosPageDTO{Items: items, NextCursor: next})
}

// handleYtSeen 标记已看：给 ids 就标这些，给 channelId 就整个频道。
func (s *Server) handleYtSeen(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs       []int64 `json:"ids"`
		ChannelID int64   `json:"channelId"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	if in.ChannelID > 0 {
		if err := s.st.MarkYtChannelSeen(in.ChannelID); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "标记已看失败")
			return
		}
	} else if len(in.IDs) > 0 {
		if err := s.st.MarkYtVideosSeen(in.IDs); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "标记已看失败")
			return
		}
	}
	n, _ := s.st.CountUnseenYtVideos()
	writeJSON(w, http.StatusOK, map[string]any{"unseen": n})
}

// handleYtVideoLookup 按 YouTube 视频 id（不是行 id）取一条。
//
// 用途只有一个：站内播放页的深链接 `/youtube?v=<videoId>`。刷新或从别处贴进来时，
// 墙上那一页（36 条、还可能带着频道筛选）未必包含这条，得能单取。
func (s *Server) handleYtVideoLookup(w http.ResponseWriter, r *http.Request) {
	videoID := strings.TrimSpace(r.URL.Query().Get("videoId"))
	if videoID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "缺 videoId")
		return
	}
	row, err := s.st.GetYtVideoByVideoID(videoID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "没有这条视频：可能没订阅它的频道")
			return
		}
		s.log.Error("查 YouTube 视频失败", "videoId", videoID, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "查询失败")
		return
	}
	writeJSON(w, http.StatusOK, s.ytVideoDTO(row))
}

// handleYtRefresh 单条视频按需补全信息（发布时间 / 时长）。
//
// 深度回填来的条目没有发布时间（yt-dlp flat 不给），墙上只能显示「时间未知」。
// 用户点开时补一次就够了——单条 4 秒，为了一张卡片批量拉 200 条不值得。
func (s *Server) handleYtRefresh(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "视频 id 不对")
		return
	}
	dlp := s.ytDlp()
	if dlp == nil || !dlp.Available() {
		writeErr(w, http.StatusConflict, "no_ytdlp", "没有可用的 yt-dlp，补不了信息")
		return
	}
	row, err := s.st.GetYtVideo(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "视频不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	info, err := dlp.VideoInfo(ctx, row.VideoID)
	if err != nil {
		writeSocialErr(w, err)
		return
	}
	var published *time.Time
	if !info.PublishedAt.IsZero() {
		published = &info.PublishedAt
	}
	if err := s.st.FillYtVideoMeta(id, published, info.DateSource, info.DurationSec, info.Views); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "写回视频信息失败")
		return
	}
	if fresh, err := s.st.GetYtVideo(id); err == nil {
		writeJSON(w, http.StatusOK, s.ytVideoDTO(fresh))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleYtSave(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "视频 id 不对")
		return
	}
	var in struct {
		Saved bool `json:"saved"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式不对")
		return
	}
	if err := s.st.SetYtVideoSaved(id, in.Saved); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "更新收藏失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "saved": in.Saved})
}

// handleYtDownload 把一条视频排进下载队列（yt-dlp → 本地库）。
func (s *Server) handleYtDownload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "视频 id 不对")
		return
	}
	var in struct {
		LibraryID int64 `json:"libraryId"`
		Overwrite bool  `json:"overwrite"`
	}
	_ = decodeJSON(r, &in)
	libID, err := s.ytLibraryID(in.LibraryID)
	if err != nil {
		writeErr(w, http.StatusConflict, "no_library", "没有可用的本地库，先去设置里建一个")
		return
	}
	jobID, err := s.runner.EnqueueYtDownload(id, libID, in.Overwrite)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "排队下载失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": jobID, "libraryId": libID})
}

// handleYtDownloadUnseen 把一个频道里所有未看的视频一起排进下载队列。
func (s *Server) handleYtDownloadUnseen(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "频道 id 不对")
		return
	}
	var in struct {
		LibraryID int64 `json:"libraryId"`
		Limit     int   `json:"limit"`
	}
	_ = decodeJSON(r, &in)
	libID, err := s.ytLibraryID(in.LibraryID)
	if err != nil {
		writeErr(w, http.StatusConflict, "no_library", "没有可用的本地库，先去设置里建一个")
		return
	}
	rows, err := s.st.ListUnseenYtVideos(id, in.Limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取待下载视频失败")
		return
	}
	jobIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		jobID, err := s.runner.EnqueueYtDownload(row.ID, libID, false)
		if err != nil {
			s.log.Warn("排队下载失败", "yt", row.ID, "err", err)
			continue
		}
		if jobID > 0 {
			jobIDs = append(jobIDs, jobID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": len(jobIDs), "jobIds": jobIDs})
}

// ---- 管理页 / 健康度 ----

func (s *Server) handleManageYt(w http.ResponseWriter, r *http.Request) {
	channels, err := s.st.ListYtChannels(false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取频道失败")
		return
	}
	stats, err := s.st.YtStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取统计失败")
		return
	}
	items := make([]ytChannelDTO, 0, len(channels))
	for _, c := range channels {
		items = append(items, s.ytChannelDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats, "channels": items})
}

// handleYtHealth 报告 yt-dlp 能不能用、版本多新。
//
// 「版本过期」是这个功能最常见的失败原因（实测 2026.07.04 全线 403，2026.10.26 正常），
// 所以它必须是界面上能直接看到的信息，而不是藏在日志里。
func (s *Server) handleYtHealth(w http.ResponseWriter, r *http.Request) {
	dlp := s.ytDlp()
	if dlp == nil {
		writeJSON(w, http.StatusOK, ytsub.VersionInfo{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	info := dlp.Version(ctx)
	writeJSON(w, http.StatusOK, info)
}

// ---- 内部工具 ----

func (s *Server) ytDlp() *ytsub.YtDlp {
	if s.runner == nil {
		return nil
	}
	return s.runner.YtDlp()
}

func (s *Server) ytThrottle() *social.Throttle {
	if s.runner == nil {
		return nil
	}
	return s.runner.YtThrottle()
}

func (s *Server) resolveYt(ctx context.Context, raw string) (*ytsub.Channel, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return ytsub.ResolveChannel(ctx, s.platformClient("youtube"), s.ytThrottle(), raw)
}

// ytLibraryID 决定下载落到哪个库：没指定就用默认库。
func (s *Server) ytLibraryID(explicit int64) (int64, error) {
	if explicit > 0 {
		return explicit, nil
	}
	lib, err := s.st.DefaultLibrary()
	if err != nil {
		return 0, err
	}
	return lib.ID, nil
}
