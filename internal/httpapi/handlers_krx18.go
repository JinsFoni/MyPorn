package httpapi

// krx18 独立页面的接口：订阅源（tag 归档页）+ 影片墙 + 未看。
//
// 和 B 站（M7）那套同构：订阅源是 performers 的一行（这里一行 = 一个 tag），
// 影片是 videos 的一行（platform='krx18'），所以播放/详情/上一部下一部复用的
// 全是「关注」流那套现成接口（见 store/platform_channels.go 顶部为什么不再复制一套表）。
//
// 和 B 站不一样的地方只有一处：没有 UP 主主页可查，订阅源是 tag 归档页
// （/tag/<女优名>/），用户点开一部片子、点下女优名那个 tag、把地址贴进来就能订阅。

import (
	"net/http"
	"strconv"

	"github.com/JinsFoni/MyPorn/internal/store"
)

// platformKrx18 是这条平台键的唯一定义（适配器 Key()、videos.platform、订阅源都用它）。
const platformKrx18 = "krx18"

type krxChannelDTO struct {
	ID              int64  `json:"id"`
	PlatformID      string `json:"platformId"` // tag 的 slug
	Name            string `json:"name"`
	AvatarURL       string `json:"avatarUrl"`
	URL             string `json:"url"`
	SubscriberCount *int   `json:"subscriberCount"`
	SiteCount       int    `json:"siteCount"` // 站点自己说有这么多片（tag 的 count）
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

// handleListKrxChannels 是 /krx18 页顶部的订阅源胶囊 + 未看角标的数据源。
func (s *Server) handleListKrxChannels(w http.ResponseWriter, _ *http.Request) {
	channels, err := s.st.ListPlatformChannels(platformKrx18)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取订阅失败")
		return
	}
	items := make([]krxChannelDTO, 0, len(channels))
	for _, c := range channels {
		items = append(items, krxChannelDTO{
			ID: c.ID, PlatformID: c.PlatformID, Name: c.Name,
			AvatarURL: imgProxy(platformKrx18, c.AvatarURL), URL: c.URL,
			SubscriberCount: c.SubscriberCount, SiteCount: c.SiteCount,
			VideoCount: c.VideoCount, UnseenCount: c.UnseenCount, Downloaded: c.Downloaded,
			SubscriptionID: c.SubscriptionID, Enabled: c.Enabled,
			BackfillDone: c.BackfillDone, BackfillPage: c.BackfillPage, TotalPages: c.TotalPages,
			LastSyncAt: rfc3339(c.LastSyncAt), NextSyncAt: rfc3339(c.NextSyncAt), LastError: c.LastError,
		})
	}
	stats, _ := s.st.PlatformStats(platformKrx18)
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"stats": map[string]any{
			"channels": stats.Channels, "videos": stats.Videos,
			"unseen": stats.Unseen, "downloaded": stats.Downloaded, "errors": stats.Errors,
		},
	})
}

// handleListKrxVideos 是影片墙：只出 platform='krx18' 的影片，按发布时间倒序。
func (s *Server) handleListKrxVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelID, _ := strconv.ParseInt(q.Get("channelId"), 10, 64)
	items, next, err := s.st.ListFeed(store.FeedQuery{
		PerformerID:    channelID,
		Cursor:         q.Get("cursor"),
		Limit:          queryInt(r, "limit", 36),
		OnlyNew:        q.Get("unseen") == "1",
		Query:          q.Get("q"),
		Platform:       platformKrx18,
		SubscribedOnly: true,
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

type krxSeenReq struct {
	ChannelID int64   `json:"channelId"`
	IDs       []int64 `json:"ids"`
}

// handleKrxSeen 把 krx18 影片标成已看：channelId > 0 只标这个 tag 的，
// ids 非空只标这几条（点开某一条时用），都不给就是整个 krx18。
func (s *Server) handleKrxSeen(w http.ResponseWriter, r *http.Request) {
	var req krxSeenReq
	_ = decodeJSON(r, &req)
	for _, id := range req.IDs {
		if err := s.st.MarkVideoSeen(id); err != nil {
			s.log.Warn("标记已看失败", "video", id, "err", err)
		}
	}
	unseen, err := s.st.MarkPlatformSeen(platformKrx18, req.ChannelID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "标记已看失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unseen": unseen})
}
