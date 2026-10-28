package httpapi

// B 站（M7）独立页面的接口：UP 主列表 + 投稿墙 + 未看 + 健康度。
//
// 和 YouTube（M6）那套的差别是「数据从哪来」：
//   - YouTube 的视频是自己一张表（yt_videos），因为 yt-dlp 抓来的东西不属于
//     videos 表的任何一条；
//   - B 站的投稿同步时就落进 videos（platform='bilibili'），播放/下载/合流复用的
//     全是「关注」流那套现成接口，所以这里只提供「按平台取数」的几个读接口
//     （见 store/bilibili.go 顶部为什么不再复制一套表）。
//
// 播放走的还是 /api/videos/{id}/playback 与 /api/videos/{id}/remux，下载走的还是
// /api/videos/{id}/download——这一页只是把它们重新组织成「B 站」的样子。

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/store"
)

// platformBilibili 出现得很频繁，给它一个名字，免得字符串到处漏。
const platformBilibili = "bilibili"

type biliChannelDTO struct {
	ID              int64  `json:"id"`
	MID             string `json:"mid"`
	Name            string `json:"name"`
	AvatarURL       string `json:"avatarUrl"`
	URL             string `json:"url"`
	SubscriberCount *int   `json:"subscriberCount"`
	VideoCount      int    `json:"videoCount"`
	UnseenCount     int    `json:"unseenCount"`
	Downloaded      int    `json:"downloaded"`
	SubscriptionID  int64  `json:"subscriptionId"`
	Enabled         bool   `json:"enabled"`
	BackfillDone    bool   `json:"backfillDone"`
	BackfillPage    int    `json:"backfillPage"`
	TotalPages      *int   `json:"totalPages"`
	LastSyncAt      any    `json:"lastSyncAt"`
	NextSyncAt      any    `json:"nextSyncAt"`
	LastError       string `json:"lastError"`
}

func (s *Server) biliChannelDTO(c *store.BiliChannel) biliChannelDTO {
	return biliChannelDTO{
		ID: c.ID, MID: c.MID, Name: c.Name, AvatarURL: imgProxy(platformBilibili, c.AvatarURL),
		URL: c.URL, SubscriberCount: c.SubscriberCount, VideoCount: c.VideoCount,
		UnseenCount: c.UnseenCount, Downloaded: c.Downloaded, SubscriptionID: c.SubscriptionID,
		Enabled: c.Enabled, BackfillDone: c.BackfillDone, BackfillPage: c.BackfillPage,
		TotalPages: c.TotalPages, LastSyncAt: rfc3339(c.LastSyncAt), NextSyncAt: rfc3339(c.NextSyncAt),
		LastError: c.LastError,
	}
}

// handleListBiliChannels 是 /bilibili 页顶部的 UP 主胶囊 + 未看角标的数据源。
func (s *Server) handleListBiliChannels(w http.ResponseWriter, _ *http.Request) {
	channels, err := s.st.ListBiliChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取 UP 主失败")
		return
	}
	items := make([]biliChannelDTO, 0, len(channels))
	for _, c := range channels {
		items = append(items, s.biliChannelDTO(c))
	}
	unseen, _ := s.st.CountBiliUnseen()
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"stats": map[string]any{"unseen": unseen},
	})
}

// handleListBiliVideos 是投稿墙：只出 platform='bilibili' 的影片，按发布时间倒序。
func (s *Server) handleListBiliVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelID, _ := strconv.ParseInt(q.Get("channelId"), 10, 64)
	items, next, err := s.st.ListFeed(store.FeedQuery{
		PerformerID: channelID,
		Cursor:      q.Get("cursor"),
		Limit:       queryInt(r, "limit", 36),
		OnlyNew:     q.Get("unseen") == "1",
		Query:       q.Get("q"),
		Platform:    platformBilibili,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, v := range items {
		out = append(out, videoDTO(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "nextCursor": nullableString(next)})
}

type biliSeenReq struct {
	ChannelID int64   `json:"channelId"`
	IDs       []int64 `json:"ids"`
}

// handleBiliSeen 把 B 站投稿标成已看：channelId > 0 只标一位 UP 主的，
// ids 非空只标这几条（点开某一条时用），都不给就是全站 B 站。
func (s *Server) handleBiliSeen(w http.ResponseWriter, r *http.Request) {
	var req biliSeenReq
	_ = decodeJSON(r, &req)
	for _, id := range req.IDs {
		if err := s.st.MarkVideoSeen(id); err != nil {
			s.log.Warn("标记已看失败", "video", id, "err", err)
		}
	}
	unseen, err := s.st.MarkBiliSeen(req.ChannelID)
	if err != nil && len(req.IDs) == 0 {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if unseen == 0 {
		unseen, _ = s.st.CountBiliUnseen()
	}
	writeJSON(w, http.StatusOK, map[string]any{"unseen": unseen})
}

// biliHealthDTO 回答「为什么画质只有 720P / 为什么某个档位放不了」。
//
// 两件实测事实（docs/M7-BILIBILI.md §4、§7）：
//   - 未登录时 B 站只给 durl 720P 和 DASH 480P，1080P 起必须带 SESSDATA；
//   - 480P 这类档位音画分轨，浏览器放不了，服务端要靠 ffmpeg 合成 mp4。
type biliHealthDTO struct {
	HasCookie bool `json:"hasCookie"`
	FFmpegOK  bool `json:"ffmpegOk"`
	HasProxy  bool `json:"hasProxy"`
}

func (s *Server) handleBiliHealth(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ffmpegOK := false
	if s.runner != nil {
		ffmpegOK = s.runner.Encoder().Check(ctx) == nil
	}
	writeJSON(w, http.StatusOK, biliHealthDTO{
		HasCookie: s.hasBiliCookie(),
		FFmpegOK:  ffmpegOK,
		HasProxy:  s.px.For(platformBilibili) != "",
	})
}

// handleManageBili 是 管理 → B 站 页签的整页数据（一次请求给全，和 /api/manage/youtube 同构）。
func (s *Server) handleManageBili(w http.ResponseWriter, _ *http.Request) {
	channels, err := s.st.ListBiliChannels()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取 UP 主失败")
		return
	}
	stats, err := s.st.BiliStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取统计失败")
		return
	}
	items := make([]biliChannelDTO, 0, len(channels))
	for _, c := range channels {
		items = append(items, s.biliChannelDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats, "channels": items})
}

type biliDownloadReq struct {
	Limit int `json:"limit"`
}

// handleBiliDownloadUnseen 把一位 UP 主（或全部 B 站）未看的投稿排进下载队列。
//
// 为什么要有「批量」：B 站一条一条点下载太慢——它的 480P 是分轨，服务端要先合流，
// 一部 25 分钟的片子 15 秒，用户不会愿意为此守着一部一部长按。
func (s *Server) handleBiliDownloadUnseen(w http.ResponseWriter, r *http.Request) {
	channelID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req biliDownloadReq
	_ = decodeJSON(r, &req)
	lib, err := s.st.DefaultLibrary()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no_library", "还没有可用的本地库，请先在设置里创建")
		return
	}
	ids, err := s.st.ListUnseenVideoIDs(platformBilibili, channelID, req.Limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	queued := 0
	for _, id := range ids {
		if _, err := s.runner.EnqueueDownload(id, lib.ID, false); err != nil {
			s.log.Warn("排队下载失败", "video", id, "err", err)
			continue
		}
		queued++
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued, "libraryId": lib.ID})
}
