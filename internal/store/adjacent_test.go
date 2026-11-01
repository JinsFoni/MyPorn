package store

import (
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// seedFeed 造几条影片：3 条 pornhub（时间递减）+ 1 条 bilibili，用来验证「上下家」跟列表同口径。
func seedFeed(t *testing.T, st *Store) map[string]*model.Video {
	t.Helper()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	out := map[string]*model.Video{}
	for i, spec := range []struct {
		key      string
		platform string
		agoHour  int
	}{
		{"newest", "pornhub", 0},
		{"middle", "pornhub", 5},
		{"oldest", "pornhub", 9},
		{"bili", "bilibili", 2},
	} {
		pub := base.Add(-time.Duration(spec.agoHour) * time.Hour)
		v := &model.Video{
			Platform:        spec.platform,
			PlatformVideoID: spec.key,
			Title:           spec.key,
			URL:             "https://example.invalid/" + spec.key,
			PublishedAt:     &pub,
		}
		id, _, err := st.UpsertVideo(v)
		if err != nil {
			t.Fatal(err)
		}
		v.ID = id
		out[spec.key] = v
		_ = i
	}
	return out
}

func mustGet(t *testing.T, st *Store, id int64) *model.Video {
	t.Helper()
	v, err := st.GetVideo(id)
	if err != nil {
		t.Fatalf("GetVideo(%d): %v", id, err)
	}
	return v
}

// 关注流的顺序（发布时间倒序）里：middle 的上一条是 bili（更晚 2 小时），下一条是 oldest。
// 但关注流**排除 B 站**，所以上一条应该直接是 newest 之外没有别的 pornhub——即 null。
func TestAdjacentVideosFollowsFeedOrder(t *testing.T) {
	st := newTestStore(t)
	v := seedFeed(t, st)

	prev, next, err := st.AdjacentVideos(mustGet(t, st, v["middle"].ID), FeedQuery{ExcludePlatform: "bilibili"})
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil || prev.PlatformVideoID != "newest" {
		t.Errorf("上一条应为 newest，得到 %+v", prev)
	}
	if next == nil || next.PlatformVideoID != "oldest" {
		t.Errorf("下一条应为 oldest，得到 %+v", next)
	}

	// 取消 B 站排除：bili 插在中间，middle 的上一条变成 bili。
	prev, _, err = st.AdjacentVideos(mustGet(t, st, v["middle"].ID), FeedQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil || prev.PlatformVideoID != "bili" {
		t.Errorf("不排除 B 站时上一条应为 bili，得到 %+v", prev)
	}
}

func TestAdjacentVideosEnds(t *testing.T) {
	st := newTestStore(t)
	v := seedFeed(t, st)

	// 列表头：没有上一条，但不是错误。
	prev, next, err := st.AdjacentVideos(mustGet(t, st, v["newest"].ID), FeedQuery{ExcludePlatform: "bilibili"})
	if err != nil {
		t.Fatal(err)
	}
	if prev != nil {
		t.Errorf("列表头不该有上一条: %+v", prev)
	}
	if next == nil || next.PlatformVideoID != "middle" {
		t.Errorf("列表头的下一条应为 middle，得到 %+v", next)
	}

	// 列表尾：只有上一条。
	prev, next, err = st.AdjacentVideos(mustGet(t, st, v["oldest"].ID), FeedQuery{ExcludePlatform: "bilibili"})
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil || prev.PlatformVideoID != "middle" {
		t.Errorf("列表尾的上一条应为 middle，得到 %+v", prev)
	}
	if next != nil {
		t.Errorf("列表尾不该有下一条: %+v", next)
	}

	// 只有一条片子的列表：两边都空。
	only := &model.Video{
		Platform: "beeg", PlatformVideoID: "solo", Title: "solo",
		URL: "https://example.invalid/solo",
	}
	id, _, err := st.UpsertVideo(only)
	if err != nil {
		t.Fatal(err)
	}
	prev, next, err = st.AdjacentVideos(mustGet(t, st, id), FeedQuery{Platform: "beeg"})
	if err != nil {
		t.Fatal(err)
	}
	if prev != nil || next != nil {
		t.Errorf("单条列表不该有邻居: prev=%+v next=%+v", prev, next)
	}
}

// published_at 为 NULL 的片子也要能参与排序（列表用 COALESCE 按空串排）：
// 它们全在列表末尾，互相之间按 id 倒序。
func TestAdjacentVideosNullPublishedAt(t *testing.T) {
	st := newTestStore(t)
	withTime := &model.Video{
		Platform: "beeg", PlatformVideoID: "dated", Title: "dated",
		URL: "https://example.invalid/dated",
	}
	pub := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	withTime.PublishedAt = &pub
	if _, _, err := st.UpsertVideo(withTime); err != nil {
		t.Fatal(err)
	}
	var undated []int64
	for _, key := range []string{"u1", "u2"} {
		id, _, err := st.UpsertVideo(&model.Video{
			Platform: "beeg", PlatformVideoID: key, Title: key,
			URL: "https://example.invalid/" + key,
		})
		if err != nil {
			t.Fatal(err)
		}
		undated = append(undated, id)
	}

	// 排序：dated(有时间) → u2(后插) → u1
	prev, next, err := st.AdjacentVideos(mustGet(t, st, undated[1]), FeedQuery{Platform: "beeg"})
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil || prev.PlatformVideoID != "dated" {
		t.Errorf("无时间的片子，上一条应是有时间的那条: %+v", prev)
	}
	if next == nil || next.ID != undated[0] {
		t.Errorf("无时间的片子，下一条应是更早插入的那条: %+v", next)
	}
}
