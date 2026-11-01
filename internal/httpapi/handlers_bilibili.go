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

	"github.com/JinsFoni/MyPorn/internal/store"
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
//
// SubscribedOnly：取消订阅的 UP 主的投稿也要一起下墙。B 站投稿是同步时就落库的，
// 软删订阅不会带走 videos 那几行（重新订阅要能原样回来），所以这里必须显式过滤——
// 否则管理页删掉一位 UP 主之后，墙上还挂着他几百条片子（2026-10-29 实测 219 条）。
func (s *Server) handleListBiliVideos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelID, _ := strconv.ParseInt(q.Get("channelId"), 10, 64)
	items, next, err := s.st.ListFeed(store.FeedQuery{
		PerformerID:    channelID,
		Cursor:         q.Get("cursor"),
		Limit:          queryInt(r, "limit", 36),
		OnlyNew:        q.Get("unseen") == "1",
		Query:          q.Get("q"),
		Platform:       platformBilibili,
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
	// CookieBroken：库里存着 Cookie，但用当前 APP_ENCRYPTION_KEY 解不开。
	// 这种情况重填 Cookie 也没用——要先让服务端用回当初那把密钥（见 secret.ErrWrongKey）。
	CookieBroken bool `json:"cookieBroken"`
	// LoggedIn / Uname 是真去打一次 nav 问出来的：只回答「存着的那条 Cookie 能不能用」。
	// 2026-10-29 踩过：hasCookie=true 但存的是被拆坏的 cookies.txt 片段，界面装没事。
	LoggedIn bool   `json:"loggedIn"`
	Uname    string `json:"uname"`
	// LastLoginOK 是上一次 nav 明确回「已登录」的时间（RFC3339，空 = 从没在这台服务上确认过）。
	//
	// 2026-10-30 实测：一条 SESSDATA 内嵌的到期时间是 2027-04-25，B 站却对 nav /
	// x/space/myinfo / nav/stat 一律回 -101。也就是说「Cookie 里的到期时间」不等于
	// 「登录态还有效」——B 站那边可以在到期前任意作废一条登录态（别处退出登录、
	// 改密码、风控都算）。有 LastLoginOK 才能把这两种完全不同的失效分开：
	// 从没成功过（多半是粘错了）vs 曾经有效、后来被 B 站作废（只能重填）。
	LastLoginOK string `json:"lastLoginOk,omitempty"`
	// NavError 是 nav 请求本身失败（网络断了 / 被风控挡了）。这时 LoggedIn=false
	// 只说明「没问出来」，不代表 Cookie 失效——旧代码把两者混成一个布尔，用户
	// 看到的就是「又失效了」，然后去重填一条本来没问题的 Cookie。
	NavError string `json:"navError,omitempty"`
	// CookieSetAt 是库里这条 Cookie 的写入时间：配上「现在 B 站说未登录」，
	// 界面才能说清是哪条 Cookie 什么时候填的，而不是笼统一句「失效」。
	CookieSetAt string `json:"cookieSetAt,omitempty"`
	// CanRefresh 表示库里这条凭据带 refresh_token（只有扫码登录拿得到）。
	// 带它才能自动续期；手填 Cookie 通篇没有这个字段，所以 B 站一作废就只能重填。
	CanRefresh bool `json:"canRefresh"`
	FFmpegOK   bool `json:"ffmpegOk"`
	HasProxy   bool `json:"hasProxy"`
}

// 记「上次确认登录有效」的间隔下限：健康检查是页面打开就调一次，写库不必那么勤。
const biliLoginOKWriteEvery = 30 * time.Minute

func (s *Server) handleBiliHealth(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ffmpegOK := false
	if s.runner != nil {
		ffmpegOK = s.runner.Encoder().Check(ctx) == nil
	}
	hasCookie, cookieBroken := s.secretState("bili_cookie")
	dto := biliHealthDTO{
		HasCookie:    hasCookie,
		CookieBroken: cookieBroken,
		FFmpegOK:     ffmpegOK,
		HasProxy:     s.px.For(platformBilibili) != "",
	}
	if hasCookie && !cookieBroken {
		if cred, err := s.biliCredential(); err == nil {
			dto.CanRefresh = cred.RefreshToken != ""
		}
	}
	// 只有真的存了 Cookie 才去问 nav：没存就没得问，省一次请求。
	if dto.HasCookie {
		if ad, ok := s.reg.Get(platformBilibili); ok {
			if login, ok2 := ad.(interface {
				LoginName(context.Context) (string, error)
			}); ok2 {
				name, err := login.LoginName(ctx)
				switch {
				case err != nil:
					dto.NavError = err.Error()
					s.log.Warn("B 站登录状态查不出来（不等于是 Cookie 失效）", "err", err)
				case name != "":
					dto.Uname, dto.LoggedIn = name, true
					s.noteBiliLoginOK(name)
				default:
					// nav 明确回 -101：这条登录态在 B 站那边没了。
					last, _ := s.st.GetSetting("bili_login_ok_at")
					dto.LastLoginOK = last
					dto.CookieSetAt, _ = s.st.SecretUpdatedAt("bili_cookie")
					s.log.Warn("B 站登录态被作废：nav 回未登录（Cookie 能解开，是 B 站那边不认了）",
						"填于", dto.CookieSetAt, "上次确认有效", last)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, dto)
}

// noteBiliLoginOK 记下「nav 刚刚确认这条 Cookie 是登录态」的时间和昵称。
//
// 存在的理由：B 站会在 SESSDATA 的到期时间之前就作废登录态（2026-10-30 实测：内嵌到期
// 2027-04-25，实际 5 天就回 -101）。只留这一条时间戳，下次用户问「怎么又失效了」时，
// 界面和日志能直接给出「X 时候还有效」，而不是让人怀疑是不是又把密钥搞坏了。
func (s *Server) noteBiliLoginOK(name string) {
	now := time.Now()
	prev, _ := s.st.GetSetting("bili_login_ok_at")
	if t, err := time.Parse(time.RFC3339, prev); err == nil && now.Sub(t) < biliLoginOKWriteEvery {
		return
	}
	if err := s.st.SetSetting("bili_login_ok_at", now.UTC().Format(time.RFC3339)); err != nil {
		s.log.Warn("记录 B 站登录确认时间失败", "err", err)
		return
	}
	_ = s.st.SetSetting("bili_login_name", name)
	s.log.Info("B 站登录态确认有效", "uname", name, "之前一次", prev)
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
