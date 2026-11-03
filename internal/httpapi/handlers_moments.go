package httpapi

// 社交动态（M5）的接口：博主管理 + 照片墙 + 一键存进本地库。
//
// 契约见 docs/M5-SOCIAL.md §11。设计取舍：
//   - 照片墙一次请求带回「照片 + 下一页游标」，博主列表单独一个接口（它可以缓存）；
//   - 添加博主分两步（preview → create），因为上游大概率要先验证账号存在/需不需要登录态；
//   - 抓取本身异步：POST 只回 jobId，进度走 SSE。

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/JinsFoni/MyPorn/internal/social"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// ---- DTO ----

type momentAccountDTO struct {
	ID            int64  `json:"id"`
	Platform      string `json:"platform"`
	Handle        string `json:"handle"`
	DisplayName   string `json:"displayName"`
	AvatarURL     string `json:"avatarUrl"`
	ProfileURL    string `json:"profileUrl"`
	PerformerID   int64  `json:"performerId"`
	PerformerName string `json:"performerName"`
	Enabled       bool   `json:"enabled"`
	BackfillDone  bool   `json:"backfillDone"`
	PostCount     int    `json:"postCount"`
	PhotoCount    int    `json:"photoCount"`
	LastSyncedAt  any    `json:"lastSyncedAt"`
	LastError     string `json:"lastError"`
}

type momentPhotoDTO struct {
	ID       int64  `json:"id"`
	URL      string `json:"url"`
	ThumbURL string `json:"thumbUrl"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	// Seq 是这条帖子里的第几张（从 1 开始），灯箱/角标用得上。
	Seq       int    `json:"seq"`
	PostedAt  any    `json:"postedAt"`
	Saved     bool   `json:"saved"`
	AccountID int64  `json:"accountId"`
	Handle    string `json:"handle"`
	// DisplayName 是博主自己写的名字，悬停/灯箱上显示它（2026-11-04 用户要求：
	// 「悬浮时左下角显示名字，不要显示 X 的 ID」）；空着才回退 @handle。
	DisplayName string `json:"displayName"`
	Platform    string `json:"platform"`
	PostURL     string `json:"postUrl"`
	Caption     string `json:"caption"`
}

type momentsPageDTO struct {
	Items      []momentPhotoDTO `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func (s *Server) accountDTO(a *store.SocialAccount) momentAccountDTO {
	dto := momentAccountDTO{
		ID: a.ID, Platform: a.Platform, Handle: a.Handle, DisplayName: a.DisplayName,
		AvatarURL: imgProxy("", a.AvatarURL), ProfileURL: a.ProfileURL,
		PerformerID: a.PerformerID, Enabled: a.Enabled, BackfillDone: a.BackfillDone,
		PostCount: a.PostCount, PhotoCount: a.PhotoCount, LastSyncedAt: rfc3339(a.LastSyncedAt),
		LastError: a.LastError,
	}
	if a.PerformerID > 0 {
		if p, err := s.st.GetPerformer(a.PerformerID); err == nil {
			dto.PerformerName = p.Name
		}
	}
	return dto
}

func (s *Server) photoDTO(r *store.SocialPhotoRow) momentPhotoDTO {
	return momentPhotoDTO{
		ID: r.ID, URL: imgProxy(r.Platform, r.URL), ThumbURL: imgProxy(r.Platform, r.URL),
		Width: r.Width, Height: r.Height, Seq: r.Seq, PostedAt: rfc3339(r.PostedAt), Saved: r.Saved > 0,
		AccountID: r.Account.ID, Handle: r.Account.Handle, DisplayName: r.Account.DisplayName,
		Platform: r.Platform, PostURL: r.Post.URL, Caption: r.Post.Caption,
	}
}

// ---- 照片墙 ----

func (s *Server) handleListMoments(w http.ResponseWriter, r *http.Request) {
	rows, next, err := s.st.ListMoments(store.MomentsQuery{
		AccountID:   int64(queryInt(r, "accountId", 0)),
		PerformerID: int64(queryInt(r, "performerId", 0)),
		Platform:    strings.TrimSpace(r.URL.Query().Get("platform")),
		Cursor:      r.URL.Query().Get("cursor"),
		Limit:       queryInt(r, "limit", 36),
	})
	if err != nil {
		s.log.Error("查询照片墙失败", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal", "读取照片墙失败")
		return
	}
	items := make([]momentPhotoDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.photoDTO(row))
	}
	writeJSON(w, http.StatusOK, momentsPageDTO{Items: items, NextCursor: next})
}

func (s *Server) handleListMomentAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.st.ListSocialAccounts(false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取博主列表失败")
		return
	}
	items := make([]momentAccountDTO, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, s.accountDTO(a))
	}
	unseen, _ := s.unseenPhotos()
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"unseen": unseen,
		"platforms": []map[string]any{
			{"key": "x", "name": "X", "needsCookie": false,
				"hasCookie": true, "hint": "免登录可用：新帖从主页发现，历史每次 20 帖（越同步越全）"},
			{"key": "instagram", "name": "Instagram", "needsCookie": true,
				"hasCookie": s.hasInstagramCookie(), "hint": "必须先在设置 → 动态里粘贴 Cookie"},
		},
	})
}

// ---- 添加博主 ----

type momentResolveReq struct {
	URL  string `json:"url"`
	Sync bool   `json:"sync"` // true = 添加后立刻回填；false 仅预览
}

type momentResolveResp struct {
	Platform     string `json:"platform"`
	PlatformName string `json:"platformName"`
	Handle       string `json:"handle"`
	DisplayName  string `json:"displayName"`
	AvatarURL    string `json:"avatarUrl"`
	ProfileURL   string `json:"profileUrl"`
	ExistingID   int64  `json:"existingId"`
	// PerformerID/PerformerName 是「链接已经躺在某个艺人的社交链接里」的提示：
	// 添加前就告诉用户会挂到谁名下，比事后去管理页发现关联错了要好。
	PerformerID   int64  `json:"performerId"`
	PerformerName string `json:"performerName"`
	NeedsCookie   bool   `json:"needsCookie"`
	PostCount     int    `json:"postCount"` // 预览时已知的帖子数（X 是 20 封顶）
	Warning       string `json:"warning,omitempty"`
}

// handleResolveMoment 只做「链接 → 账号资料」的解析与探测，不写库。
func (s *Server) handleResolveMoment(w http.ResponseWriter, r *http.Request) {
	var req momentResolveReq
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写博主主页链接")
		return
	}
	resp, err := s.probeMoment(req.URL)
	if err != nil {
		writeSocialErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// probeMoment 依次问每个适配器「这是你的链接吗」，命中就抓一次资料。
func (s *Server) probeMoment(rawURL string) (*momentResolveResp, error) {
	if s.social == nil {
		return nil, social.Upstream("社交适配器未初始化", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var lastErr error
	for _, ad := range s.social.All() {
		handle, err := ad.ParseProfileURL(rawURL)
		if err != nil {
			lastErr = err
			continue
		}
		acc, err := ad.GetProfile(ctx, handle)
		if err != nil {
			return nil, err
		}
		resp := &momentResolveResp{
			Platform: ad.Key(), PlatformName: ad.Name(), Handle: handle,
			DisplayName: acc.DisplayName, AvatarURL: imgProxy("", acc.AvatarURL),
			ProfileURL: acc.ProfileURL, NeedsCookie: ad.NeedsCookie(),
		}
		if resp.DisplayName == "" {
			resp.DisplayName = handle
		}
		if resp.ProfileURL == "" {
			resp.ProfileURL = rawURL
		}
		if existing, err := s.st.GetSocialAccountByHandle(ad.Key(), handle); err == nil {
			resp.ExistingID = existing.ID
			resp.PostCount = existing.PostCount
			resp.Warning = "这个博主已经在列表里了"
		}
		if ad.Key() == "x" {
			resp.Warning = "X 免登录只能取到最近 20 帖，更早的会在之后的同步里慢慢补"
		}
		if pid, name, ok := s.st.FindPerformerBySocial(ad.Key(), handle); ok {
			resp.PerformerID, resp.PerformerName = pid, name
			if resp.Warning == "" {
				resp.Warning = "会自动关联到艺人「" + name + "」"
			}
		}
		return resp, nil
	}
	if lastErr != nil {
		return nil, social.NotFound("暂不支持这个链接，请填 X 或 Instagram 的博主主页")
	}
	return nil, social.NotFound("暂不支持这个链接")
}

type momentCreateReq struct {
	URL string `json:"url"`
}

func (s *Server) handleCreateMomentAccount(w http.ResponseWriter, r *http.Request) {
	var req momentCreateReq
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写博主主页链接")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var lastErr error
	for _, ad := range s.social.All() {
		handle, err := ad.ParseProfileURL(req.URL)
		if err != nil {
			lastErr = err
			continue
		}
		profile, err := ad.GetProfile(ctx, handle)
		if err != nil {
			writeSocialErr(w, err)
			return
		}
		acc, err := s.st.UpsertSocialAccount(&store.SocialAccount{
			Platform: ad.Key(), Handle: handle,
			DisplayName: profile.DisplayName, AvatarURL: profile.AvatarURL, ProfileURL: profile.ProfileURL,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "保存博主失败")
			return
		}
		// 按 URL 精确关联已入库艺人（不猜名字）；只关联这一个账号，
		// 不必为了一次添加去扫全表。
		if pid, _, ok := s.st.FindPerformerBySocial(ad.Key(), handle); ok {
			_ = s.st.SetSocialAccountPerformer(acc.ID, pid)
		}
		jobID, err := s.runner.EnqueueSocialSync(acc.ID, "full")
		if err != nil {
			s.log.Warn("投递博主回填任务失败", "account", acc.ID, "err", err)
		}
		if fresh, err := s.st.GetSocialAccount(acc.ID); err == nil {
			acc = fresh
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"account": s.accountDTO(acc),
			"jobId":   jobID,
		})
		return
	}
	if lastErr != nil {
		writeErr(w, http.StatusBadRequest, "unsupported_platform", "暂不支持这个链接，请填 X 或 Instagram 的博主主页")
		return
	}
	writeErr(w, http.StatusBadRequest, "unsupported_platform", "暂不支持这个链接")
}

type momentPatchReq struct {
	Enabled     *bool  `json:"enabled"`
	PerformerID *int64 `json:"performerId"`
	Mode        string `json:"mode"` // 传 "full" 表示清断点重新回填
}

func (s *Server) handlePatchMomentAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "博主 id 非法")
		return
	}
	var req momentPatchReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	if _, err := s.st.GetSocialAccount(id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "博主不存在")
		return
	}
	if req.Enabled != nil {
		if err := s.st.SetSocialAccountEnabled(id, *req.Enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "更新博主失败")
			return
		}
	}
	if req.PerformerID != nil {
		if *req.PerformerID > 0 {
			if _, err := s.st.GetPerformer(*req.PerformerID); err != nil {
				writeErr(w, http.StatusBadRequest, "bad_request", "指定的艺人不存在")
				return
			}
		}
		if err := s.st.SetSocialAccountPerformer(id, *req.PerformerID); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "关联艺人失败")
			return
		}
	}
	if req.Mode == "full" {
		if err := s.st.ResetSocialCursor(id); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "重置回填断点失败")
			return
		}
	}
	acc, err := s.st.GetSocialAccount(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取博主失败")
		return
	}
	writeJSON(w, http.StatusOK, s.accountDTO(acc))
}

func (s *Server) handleDeleteMomentAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "博主 id 非法")
		return
	}
	// 只删记录；已经存进本地库的文件是用户的资产，不动。
	if err := s.st.DeleteSocialAccount(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "删除博主失败")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSyncMomentAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "博主 id 非法")
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	_ = decodeJSON(r, &req)
	if _, err := s.st.GetSocialAccount(id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "博主不存在")
		return
	}
	jobID, err := s.runner.EnqueueSocialSync(id, req.Mode)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "投递同步任务失败")
		return
	}
	if jobID == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"jobId": nil, "queued": false, "reason": "这个博主已经在同步队列里了"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobId": jobID, "queued": true})
}

// ---- 已看标记 ----

const seenKey = "social_seen_at"

// unseenPhotos 数「上次看过动态之后新进来的照片」。
func (s *Server) unseenPhotos() (int, error) {
	since := time.Time{}
	if v, err := s.st.GetSetting(seenKey); err == nil && strings.TrimSpace(v) != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}
	if since.IsZero() {
		// 从没点过「动态」：把当前所有照片都算作未看。
		since = time.Unix(0, 0)
	}
	return s.st.CountUnseenPhotos(since)
}

func (s *Server) handleMarkMomentsSeen(w http.ResponseWriter, _ *http.Request) {
	if err := s.st.SetSetting(seenKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "保存已看时间失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unseen": 0})
}

// ---- 从照片墙删（单张 / 多选批量）----

// momentDeleteReq 是照片墙「多选 → 删除」的请求体。
type momentDeleteReq struct {
	PhotoIDs []int64 `json:"photoIds"`
}

// handleDeleteMomentPhoto 把一张照片从墙上删掉（软删，见 store.HideSocialPhoto）。
func (s *Server) handleDeleteMomentPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "照片 id 非法")
		return
	}
	accountID, err := s.st.HideSocialPhoto(id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "这张照片已经不在了")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "删除照片失败")
		return
	}
	// 头像条那边的「N 张」来自账号计数，删完顺手对齐（失败也不影响这次删除）。
	if err := s.st.RefreshSocialCounts(accountID); err != nil {
		s.log.Warn("刷新博主照片计数失败", "account", accountID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteMomentPhotos 批量软删（照片墙多选删除）。一次最多 500 张：
// 墙上一页 36 张，多选到这个量级已经不是「点两下」了，再多就该在上游分页里做。
func (s *Server) handleDeleteMomentPhotos(w http.ResponseWriter, r *http.Request) {
	var req momentDeleteReq
	if err := decodeJSON(r, &req); err != nil || len(req.PhotoIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "请先选中照片")
		return
	}
	if len(req.PhotoIDs) > 500 {
		writeErr(w, http.StatusBadRequest, "bad_request", "一次最多删 500 张")
		return
	}
	deleted, accounts, err := s.st.HideSocialPhotos(req.PhotoIDs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "删除照片失败")
		return
	}
	// 照片数挂在账号上，删完顺手对齐；对不齐不影响这次删除。
	for _, id := range accounts {
		if err := s.st.RefreshSocialCounts(id); err != nil {
			s.log.Warn("刷新博主照片计数失败", "account", id, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// ---- 管理页 ----

func (s *Server) handleManageMoments(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.st.ListSocialAccounts(false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取博主列表失败")
		return
	}
	stats, err := s.st.SocialStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "读取统计失败")
		return
	}
	items := make([]momentAccountDTO, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, s.accountDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"stats": map[string]any{
			"accounts": stats.Accounts,
			"posts":    stats.Posts,
			"photos":   stats.Photos,
			"errors":   stats.Errors,
		},
		"hasInstagramCookie": s.hasInstagramCookie(),
	})
}

// hasInstagramCookie 报告 Instagram 是否配了登录态（界面据此提示先填 Cookie）。
func (s *Server) hasInstagramCookie() bool {
	if v, err := s.st.GetSecret(s.box, "social_cookie_instagram"); err == nil && strings.TrimSpace(v) != "" {
		return true
	}
	return false
}

// writeSocialErr 把适配器的错误分类翻成 HTTP 状态与错误码。
//
// 这三类在前端要长得不一样（docs/M5-SOCIAL.md §5.3）：
// needs_auth → 让用户去贴 Cookie；rate_limited → 告诉他等多久；stale_parser → 解析规则过期了。
// handleClearMomentCooldown 手动解除某平台（或全部）的本地冷却。
//
// 冷却时间是「上一次撞墙时猜的」：用户换了 Cookie、换了出口 IP 之后，那个时间已经没有意义，
// 不该让他干等到点。只清本地计时器，不碰任何上游状态。
func (s *Server) handleClearMomentCooldown(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Platform string `json:"platform"`
	}
	_ = decodeJSON(r, &in) // 空 body 视为「全部平台」
	cleared := []string{}
	for _, a := range s.social.All() {
		if in.Platform != "" && a.Key() != in.Platform {
			continue
		}
		a.Throttle().Clear()
		cleared = append(cleared, a.Key())
	}
	// YouTube 的限速器不在 social.Registry 里（它不是 social.Adapter），单独清一次。
	if in.Platform == "" || in.Platform == "youtube" {
		if th := s.ytThrottle(); th != nil {
			th.Clear()
			cleared = append(cleared, "youtube")
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": cleared})
}

func writeSocialErr(w http.ResponseWriter, err error) {
	kind := social.KindOf(err)
	switch kind {
	case social.KindNeedsAuth:
		writeErr(w, http.StatusConflict, "needs_auth", err.Error())
	case social.KindRateLimited:
		// 冷却截止时间塞进 details：前端据此显示「冷却到 HH:MM」而不是干等或反复重试。
		body := map[string]apiError{"error": {Code: "rate_limited", Message: err.Error()}}
		if reset, ok := social.ResetAtOf(err); ok {
			body["error"] = apiError{
				Code: "rate_limited", Message: err.Error(),
				Details: map[string]any{"resetAt": reset.UTC().Format(time.RFC3339)},
			}
		}
		writeJSON(w, http.StatusTooManyRequests, body)
	case social.KindBlocked:
		// 429 但不带 resetAt：前端据此显示「换 Cookie / 换 IP」，而不是「等到 HH:MM」。
		writeErr(w, http.StatusTooManyRequests, "blocked", err.Error())
	case social.KindNotFound:
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case social.KindStaleParser:
		writeErr(w, http.StatusBadGateway, "stale_parser", err.Error())
	default:
		if errors.Is(err, context.DeadlineExceeded) {
			writeErr(w, http.StatusGatewayTimeout, "upstream", "抓取超时，稍后再试")
			return
		}
		writeErr(w, http.StatusBadGateway, "upstream", err.Error())
	}
}
