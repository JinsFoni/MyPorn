package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// 播放器的「上一个 / 下一个」走 /api/videos/{id}/adjacent：带哪套列表参数就按哪套找邻居。
func TestVideoAdjacentHandler(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(&config.Config{DataDir: tmp}, st, platform.NewRegistry(), nil, events.NewHub(),
		nil, nil, log, nil, imgcache.New(t.TempDir(), nil, "", log))

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	ids := map[string]int64{}
	for _, spec := range []struct {
		key      string
		platform string
		agoHour  int
	}{
		{"newest", "pornhub", 0},
		{"middle", "pornhub", 4},
		{"oldest", "pornhub", 8},
		{"bili", "bilibili", 2},
		{"yt", "youtube", 1},
	} {
		pub := base.Add(-time.Duration(spec.agoHour) * time.Hour)
		id, _, err := st.UpsertVideo(&model.Video{
			Platform: spec.platform, PlatformVideoID: spec.key, Title: spec.key,
			URL: "https://example.invalid/" + spec.key, PublishedAt: &pub,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[spec.key] = id
	}

	get := func(videoID int64, query string) (prev, next *int64) {
		t.Helper()
		path := "/api/videos/" + strconv.FormatInt(videoID, 10) + "/adjacent" + query
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.handleVideoAdjacent(rec, withRouteParam(req, "id", strconv.FormatInt(videoID, 10)))
		if rec.Code != http.StatusOK {
			t.Fatalf("应 200，得到 %d: %s", rec.Code, rec.Body.String())
		}
		var got struct {
			Prev *struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			} `json:"prev"`
			Next *struct {
				ID int64 `json:"id"`
			} `json:"next"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Prev != nil {
			if got.Prev.Title == "" {
				t.Errorf("prev 应当带上片名（切歌按钮要显示）: %+v", got.Prev)
			}
			v := got.Prev.ID
			prev = &v
		}
		if got.Next != nil {
			v := got.Next.ID
			next = &v
		}
		return prev, next
	}

	// 关注流口径（排除 B 站 + YouTube，和 /api/videos 一致）：middle 上面是 newest、下面是 oldest。
	prev, next := get(ids["middle"], "?excludePlatform=bilibili,youtube")
	if prev == nil || *prev != ids["newest"] {
		t.Errorf("上一条应为 newest(%d)，得到 %v", ids["newest"], prev)
	}
	if next == nil || *next != ids["oldest"] {
		t.Errorf("下一条应为 oldest(%d)，得到 %v", ids["oldest"], next)
	}

	// 只排 B 站：YouTube 那条就插进来了（证明逗号分隔是两项都在生效，不是只认第一项）。
	prev, next = get(ids["middle"], "?excludePlatform=bilibili")
	if prev == nil || *prev != ids["yt"] {
		t.Errorf("只排 B 站时上一条应为 yt(%d)，得到 %v", ids["yt"], prev)
	}

	// 不排除任何平台：bili 插在中间。
	prev, next = get(ids["middle"], "")
	if prev == nil || *prev != ids["bili"] {
		t.Errorf("不排除 B 站时上一条应为 bili(%d)，得到 %v", ids["bili"], prev)
	}

	// 当前这条自己不在那份列表里（YouTube 被关注流排掉）：上下家都是 null，不给编出来的邻居。
	prev, next = get(ids["yt"], "?excludePlatform=bilibili,youtube")
	if prev != nil || next != nil {
		t.Errorf("YouTube 片不在关注流里，两边都该是 null，得到 prev=%v next=%v", prev, next)
	}

	// B 站墙口径：只在自己的平台里找，bili 前后都空。
	prev, next = get(ids["bili"], "?platform=bilibili")
	if prev != nil || next != nil {
		t.Errorf("B 站墙里只有一条，两边都该是 null，得到 prev=%v next=%v", prev, next)
	}
}
