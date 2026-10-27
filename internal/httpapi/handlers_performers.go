package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
)

// avatarHealTimeout 是后台补头像的超时，够慢也就放弃了，别把 goroutine 挂住。
const avatarHealTimeout = 30 * time.Second

// selfHealAvatar 在后台给没有头像的艺人补一张：接口先正常返回，
// 下一次刷新就能看到头像。重复触发由 AvatarFiller 的冷却/并发控制挡掉。
func (s *Server) selfHealAvatar(p *model.Performer) {
	if p == nil || p.ID == 0 || strings.TrimSpace(p.AvatarURL) != "" || s.runner == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), avatarHealTimeout)
		defer cancel()
		s.runner.FillAvatar(ctx, p)
	}()
}

// proxyRequired 记录「从国内直连一般不通、建议配代理」的平台。
// xHamster 与 beeg 实测可直连，所以不在此列——否则界面上会一直挂着一条没用的警告。
var proxyRequired = map[string]bool{"pornhub": true}

func (s *Server) handlePlatforms(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, a := range s.reg.All() {
		proxy := s.cfg.PlatformProxyFor(a.Key())
		status := "ok"
		if proxy == "" && proxyRequired[a.Key()] {
			status = "no_proxy"
		}
		out = append(out, map[string]any{
			"key":       a.Key(),
			"name":      a.Name(),
			"enabled":   true,
			"baseUrl":   a.BaseURL(),
			"hasProxy":  proxy != "",
			"hasCookie": false,
			"status":    status,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListPerformers(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "page", 1)
	limit := queryInt(r, "limit", 24)
	subscribed := r.URL.Query().Get("subscribed") != "false"

	items, total, err := s.st.ListPerformers(subscribed, (page-1)*limit, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ids := make([]int64, len(items))
	for i, p := range items {
		ids[i] = p.ID
	}
	counts, _ := s.st.NewVideoCounts(ids)
	out := make([]map[string]any, 0, len(items))
	for _, p := range items {
		sub, _ := s.st.GetSubscriptionByPerformer(p.ID)
		p.Subscription = sub
		s.selfHealAvatar(p)
		out = append(out, s.performerDTO(p, counts[p.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out, "total": total, "page": page, "limit": limit,
	})
}

func (s *Server) handleGetPerformer(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	p, err := s.st.GetPerformer(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "艺人不存在")
		return
	}
	if sub, err := s.st.GetSubscriptionByPerformer(p.ID); err == nil {
		p.Subscription = sub
	}
	s.selfHealAvatar(p)
	n, _ := s.st.NewVideoCountByPerformer(p.ID)
	dto := s.performerDTO(p, n)
	// 本地收录数：平台资料页没有「影片总数」，艺人页显示的就是这个。
	if local, err := s.st.LocalVideoCount(p.ID); err == nil {
		dto["localVideoCount"] = local
	}
	writeJSON(w, http.StatusOK, dto)
}

type resolveReq struct {
	URL string `json:"url"`
}

// handleResolvePerformer 解析艺人主页 URL，远程抓取资料但**不入库**（预览用）。
func (s *Server) handleResolvePerformer(w http.ResponseWriter, r *http.Request) {
	var req resolveReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	ad, performerID, err := s.adapterForURL(req.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unsupported_platform", err.Error())
		return
	}
	p, err := ad.GetPerformer(r.Context(), performerID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "抓取艺人资料失败: "+err.Error())
		return
	}
	p.PerformerID = performerID
	if p.Platform == "" {
		p.Platform = ad.Key()
	}
	// 资料内容改从站外资料源（Pornhub）借一份：预览里看到的就是订阅后会存下来的那份。
	if src := s.runner.LookupProfile(r.Context(), p.Name, ad.Key()); src != nil {
		p.AdoptProfileFrom(src)
	}
	// 平台页面抠不到头像时，按名字到站外头像源借一张（预览里就能看到）。
	if strings.TrimSpace(p.AvatarURL) == "" {
		p.AvatarURL = s.runner.LookupAvatar(r.Context(), p.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"platform":        ad.Key(),
		"performerId":     performerID,
		"name":            p.Name,
		"avatarUrl":       imgProxy(ad.Key(), p.AvatarURL),
		"bio":             p.Bio,
		"url":             p.URL,
		"videoCount":      p.VideoCount,
		"subscriberCount": p.SubscriberCount,
		"alreadyExists":   false,
	})
}

// adapterForURL 根据 URL 归属选出平台适配器。
func (s *Server) adapterForURL(raw string) (platform.Adapter, string, error) {
	for _, a := range s.reg.All() {
		if id, err := a.ParsePerformerURL(raw); err == nil {
			return a, id, nil
		}
	}
	keys := make([]string, 0, 4)
	for _, a := range s.reg.All() {
		keys = append(keys, a.Name())
	}
	return nil, "", fmt.Errorf("无法识别该链接所属平台（支持：%s）", strings.Join(keys, " / "))
}

type createSubReq struct {
	URL           string `json:"url"`
	PerformerID   string `json:"performerId"`
	Platform      string `json:"platform"`
	BackfillLimit int    `json:"backfillLimit"`
}

func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req createSubReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}

	var chosen platform.Adapter
	var performerID string
	if strings.TrimSpace(req.URL) != "" {
		a, id, err := s.adapterForURL(req.URL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "unsupported_platform", err.Error())
			return
		}
		chosen, performerID = a, id
	} else if req.PerformerID != "" {
		a, ok := s.reg.Get(req.Platform)
		if !ok {
			writeErr(w, http.StatusBadRequest, "unsupported_platform", "平台未注册")
			return
		}
		chosen, performerID = a, req.PerformerID
	} else {
		writeErr(w, http.StatusBadRequest, "bad_request", "需要提供 url 或 performerId")
		return
	}

	p, err := chosen.GetPerformer(r.Context(), performerID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "抓取艺人资料失败: "+err.Error())
		return
	}
	p.Platform = chosen.Key()
	p.PerformerID = performerID

	perfID, err := s.st.UpsertPerformer(p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	subID, err := s.st.CreateSubscription(perfID, req.BackfillLimit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	jobID, err := s.runner.EnqueueSync(subID, "full")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// 资料内容：beeg / xHamster 的艺人页资料很薄，这里同步借一份 Pornhub 的
	// （名字对不上就保持原样），订阅后打开艺人页立刻是完整档案。
	saved, _ := s.st.GetPerformer(perfID)
	if saved != nil && s.runner.FillProfile(r.Context(), saved) {
		if again, err := s.st.GetPerformer(perfID); err == nil {
			saved = again
		}
	}

	// 头像：新订阅的卡片要立刻有图，这里同步补一次（一次 HTTP，失败就留空）。
	if saved != nil && strings.TrimSpace(saved.AvatarURL) == "" {
		s.runner.FillAvatar(r.Context(), saved)
		if again, err := s.st.GetPerformer(perfID); err == nil {
			saved = again
		}
	}
	sub, _ := s.st.GetSubscriptionByPerformer(perfID)
	if saved != nil {
		saved.Subscription = sub
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"subscriptionId": subID,
		"performer":      s.performerDTO(saved, 0),
		"jobId":          jobID,
	})
}

func (s *Server) handleListSubscriptions(w http.ResponseWriter, _ *http.Request) {
	items, err := s.st.ListSubscriptions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	ids := make([]int64, len(items))
	for i, p := range items {
		ids[i] = p.ID
	}
	counts, _ := s.st.NewVideoCounts(ids)
	out := make([]map[string]any, 0, len(items))
	for _, p := range items {
		out = append(out, s.performerDTO(p, counts[p.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type patchSubReq struct {
	Enabled *bool `json:"enabled"`
}

func (s *Server) handlePatchSubscription(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req patchSubReq
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求体非法")
		return
	}
	sub, err := s.st.GetSubscription(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "订阅不存在")
		return
	}
	if req.Enabled != nil {
		if err := s.st.SetSubscriptionEnabled(id, *req.Enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
	}
	p, err := s.st.GetPerformer(sub.PerformerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	updated, _ := s.st.GetSubscription(id)
	p.Subscription = updated
	writeJSON(w, http.StatusOK, s.performerDTO(p, 0))
}

func (s *Server) handleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if _, err := s.st.GetSubscription(id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "订阅不存在")
		return
	}
	if err := s.st.SoftDeleteSubscription(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type syncReq struct {
	Mode string `json:"mode"`
}

func (s *Server) handleSyncSubscription(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	var req syncReq
	_ = decodeJSON(r, &req)
	if req.Mode != "full" && req.Mode != "incremental" {
		req.Mode = "incremental"
	}
	if _, err := s.st.GetSubscription(id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "订阅不存在")
		return
	}
	jobID, err := s.runner.EnqueueSync(id, req.Mode)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"jobId": jobID})
}

func (s *Server) handleSearchPerformers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	platformKey := r.URL.Query().Get("platform")
	page := queryInt(r, "page", 1)
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "page": page, "hasMore": false})
		return
	}
	if platformKey == "" {
		platformKey = "pornhub"
	}
	ad, ok := s.reg.Get(platformKey)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unsupported_platform", "平台未注册")
		return
	}
	items, hasMore, err := ad.SearchPerformers(r.Context(), q, page)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve_failed", "搜索失败: "+err.Error())
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, p := range items {
		var subscribedID any
		if local, err := s.st.FindPerformer(p.Platform, p.PerformerID); err == nil {
			if sub, err := s.st.GetSubscriptionByPerformer(local.ID); err == nil {
				subscribedID = sub.ID
			}
		}
		out = append(out, map[string]any{
			"platform":        p.Platform,
			"performerId":     p.PerformerID,
			"name":            p.Name,
			"avatarUrl":       imgProxy(p.Platform, p.AvatarURL),
			"url":             p.URL,
			"videoCount":      p.VideoCount,
			"subscriberCount": p.SubscriberCount,
			"subscribedId":    subscribedID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "page": page, "hasMore": hasMore})
}
