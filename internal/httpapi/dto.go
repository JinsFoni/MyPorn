package httpapi

import (
	"net/url"
	"time"

	"github.com/mario/myporn/internal/model"
)

// imgProxy 把远端图片地址改写为本地代理地址，规避 Referer/防盗链。
func imgProxy(platform, raw string) string {
	if raw == "" {
		return ""
	}
	q := url.Values{}
	q.Set("u", raw)
	if platform != "" {
		q.Set("p", platform)
	}
	return "/api/img?" + q.Encode()
}

func rfc3339(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// profileOrEmpty / socialOrEmpty：nil 会序列化成 null，前端还得判空，索性统一成空数组。
func profileOrEmpty(v []model.ProfileField) []model.ProfileField {
	if v == nil {
		return []model.ProfileField{}
	}
	return v
}

func socialOrEmpty(v []model.SocialLink) []model.SocialLink {
	if v == nil {
		return []model.SocialLink{}
	}
	return v
}

func (s *Server) performerDTO(p *model.Performer, newCount int) map[string]any {
	dto := map[string]any{
		"id":               p.ID,
		"platform":         p.Platform,
		"performerId":      p.PerformerID,
		"name":             p.Name,
		"url":              p.URL,
		"avatarUrl":        imgProxy(p.Platform, p.AvatarURL),
		"bio":              p.Bio,
		"profile":          profileOrEmpty(p.Profile),
		"social":           socialOrEmpty(p.Social),
		"videoCount":       p.VideoCount,
		"subscriberCount":  p.SubscriberCount,
		"subscriptionId":   nil,
		"enabled":          false,
		"backfillDone":     false,
		"backfillProgress": nil,
		"lastSyncAt":       nil,
		"nextSyncAt":       nil,
		"newVideoCount":    newCount,
		"lastError":        "",
	}
	if sub := p.Subscription; sub != nil {
		dto["subscriptionId"] = sub.ID
		dto["enabled"] = sub.Enabled
		dto["backfillDone"] = sub.BackfillDone
		dto["lastSyncAt"] = rfc3339(sub.LastSyncAt)
		dto["nextSyncAt"] = rfc3339(sub.NextSyncAt)
		dto["lastError"] = sub.LastError
		// 只有「回填还没结束、并且确实有进度」时才下发，否则前端会把空对象
		// 当成「正在回填历史影片」一直显示着。
		if !sub.BackfillDone && (sub.BackfillPage > 0 || sub.BackfillTotalPages != nil) {
			total := 0
			if sub.BackfillTotalPages != nil {
				total = *sub.BackfillTotalPages
			}
			dto["backfillProgress"] = map[string]any{
				"donePages":  sub.BackfillPage,
				"totalPages": total,
			}
		}
	}
	return dto
}

func videoDTO(v *model.Video) map[string]any {
	perf := make([]map[string]any, 0, len(v.Performers))
	for _, p := range v.Performers {
		perf = append(perf, map[string]any{
			"id":   p.ID,
			"name": p.Name,
			"url":  p.URL,
		})
	}
	return map[string]any{
		"id":              v.ID,
		"platform":        v.Platform,
		"platformVideoId": v.PlatformVideoID,
		"title":           v.Title,
		"url":             v.URL,
		"thumbUrl":        imgProxy(v.Platform, v.ThumbURL),
		"previewUrl":      imgProxy(v.Platform, v.PreviewURL),
		"durationSec":     v.DurationSec,
		"publishedAt":     rfc3339(v.PublishedAt),
		"viewCount":       v.ViewCount,
		"rating":          v.Rating,
		"tags":            v.Tags,
		"performers":      perf,
		"isNew":           v.IsNew,
		"hasLocalFile":    len(v.LocalLibraryIDs) > 0,
		"localLibraryIds": v.LocalLibraryIDs,
	}
}
