package httpapi

import (
	"net/http"
	"time"

	"github.com/mario/myporn/internal/store"
)

// 管理页（/manage）：统计 + 关注管理。整页只打一个请求——上面那排读数、下面的分项
// 明细、右侧的订阅列表必须是同一时刻的库，分开取就会出现这块是新的、那块是旧的。

func (s *Server) handleManage(w http.ResponseWriter, _ *http.Request) {
	stats, err := s.st.Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	subs, err := s.st.ListSubscriptions()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	local, err := s.st.PerformerLocalCounts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	ids := make([]int64, len(subs))
	for i, p := range subs {
		ids[i] = p.ID
	}
	counts, _ := s.st.NewVideoCounts(ids)

	items := make([]map[string]any, 0, len(subs))
	for _, p := range subs {
		dto := s.performerDTO(p, counts[p.ID])
		own := local[p.ID]
		// 本地收录量：平台上总共多少（videoCount）不等于库里存了多少，管理页看的是后者。
		dto["localVideoCount"] = own.Videos
		dto["localFileCount"] = own.Files
		dto["localBytes"] = own.Bytes
		items = append(items, dto)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"generatedAt":   time.Now().UTC().Format(time.RFC3339),
		"stats":         statsDTO(stats),
		"subscriptions": items,
	})
}

// statsDTO 把 store 的统计快照翻成前端要的形状（字段名与 types.ts 的 ManageStats 对齐）。
func statsDTO(st *store.Stats) map[string]any {
	counts := func(rows []store.StatsCount) []map[string]any {
		out := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]any{"key": r.Key, "n": r.N, "bytes": r.Bytes})
		}
		return out
	}
	libs := make([]map[string]any, 0, len(st.ByLibrary))
	for _, l := range st.ByLibrary {
		libs = append(libs, map[string]any{
			"id": l.ID, "name": l.Name, "videos": l.Videos, "bytes": l.Bytes,
		})
	}
	recent := make([]map[string]any, 0, len(st.Recent))
	for _, r := range st.Recent {
		recent = append(recent, map[string]any{
			"id":          r.ID,
			"title":       r.Title,
			"platform":    r.Platform,
			"thumbUrl":    imgProxy(r.Platform, r.ThumbURL),
			"publishedAt": rfc3339(r.PublishedAt),
			"firstSeenAt": rfc3339(r.FirstSeenAt),
		})
	}
	return map[string]any{
		"performers":        st.Performers,
		"performersEnabled": st.PerformersEnabled,
		"performersFailed":  st.PerformersFailed,
		"videos":            st.Videos,
		"videosNew":         st.VideosNew,
		"videosWithFile":    st.VideosWithFile,
		"files":             st.Files,
		"bytes":             st.Bytes,
		"byPlatform":        counts(st.ByPlatform),
		"byLibrary":         libs,
		"byDuration":        counts(st.ByDuration),
		"byQuality":         counts(st.ByQuality),
		"byYear":            counts(st.ByYear),
		"ingest":            counts(st.Ingest),
		"tags":              counts(st.Tags),
		"downloads": map[string]any{
			"done":   st.Downloads.Done,
			"failed": st.Downloads.Failed,
			"active": st.Downloads.Active,
		},
		"recent": recent,
	}
}
