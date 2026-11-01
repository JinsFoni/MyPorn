package jobs

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// thumbAdapter 在 fakeAdapter 基础上让 GetVideo 返回指定封面地址，用来验「换签名」那条路。
type thumbAdapter struct {
	fakeAdapter
	thumbURL string
}

func (a *thumbAdapter) GetVideo(ctx context.Context, id string) (*model.Video, error) {
	return &model.Video{Platform: "pornhub", PlatformVideoID: id, ThumbURL: a.thumbURL}, nil
}

// thumbServer 造一个图片服务器；老的（过期的）地址返回 404，新的正常给图。
func thumbServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/fresh.png", func(w http.ResponseWriter, r *http.Request) {
		hits++
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		for y := 0; y < 180; y++ {
			for x := 0; x < 320; x++ {
				img.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
			}
		}
		w.Header().Set("Content-Type", "image/png")
		_ = png.Encode(w, img)
	})
	mux.HandleFunc("/expired.png", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "signature expired", http.StatusGone)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newCoverFixture(t *testing.T, ad platform.Adapter) (*Runner, *store.Store, *imgcache.Service) {
	t.Helper()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "covers.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	img := imgcache.New(tmp, nil, "", log)
	reg := platform.NewRegistry()
	reg.Register(ad)
	cfg := &config.Config{}
	return NewRunner(cfg, st, reg, nil, nil, nil, events.NewHub(), log, img), st, img
}

func addVideo(t *testing.T, st *store.Store, key, thumb string) int64 {
	t.Helper()
	id, _, err := st.UpsertVideo(&model.Video{
		Platform: "pornhub", PlatformVideoID: key, Title: "标题 " + key,
		URL: "https://example.invalid/view_video.php?viewkey=" + key, ThumbURL: thumb,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWarmCoverListCachesImages(t *testing.T) {
	srv, hits := thumbServer(t)
	ad := &fakeAdapter{}
	r, _, img := newCoverFixture(t, ad)
	url := srv.URL + "/fresh.png"

	n := r.warmCoverList(context.Background(), []store.VideoThumb{
		{ID: 1, Platform: "pornhub", ThumbURL: url},
		{ID: 2, Platform: "pornhub", ThumbURL: url}, // 同一张图不该抓两次
	})
	if n != 2 {
		t.Errorf("预热成功数 = %d，期望 2", n)
	}
	if *hits != 1 {
		t.Errorf("上游被请求 %d 次，期望 1 次（第二张应命中缓存）", *hits)
	}
	if !img.Cached("pornhub", url) {
		t.Error("预热后缓存里应该有的")
	}
}

// 核心用例：库里存的是过期签名地址 → 抓不到 → 换详情页的新地址 → 抓到。
func TestRepairCoversRefreshesExpiredThumb(t *testing.T) {
	srv, _ := thumbServer(t)
	oldURL := srv.URL + "/expired.png"
	newURL := srv.URL + "/fresh.png"
	ad := &thumbAdapter{thumbURL: newURL}
	r, st, img := newCoverFixture(t, ad)
	id := addVideo(t, st, "abc123", oldURL)

	if got := r.RepairCovers(context.Background()); got != 1 {
		t.Fatalf("修复数 = %d，期望 1", got)
	}
	if !img.Cached("pornhub", newURL) {
		t.Error("新地址的封面应该已经进缓存")
	}
	v, err := st.GetVideo(id)
	if err != nil {
		t.Fatal(err)
	}
	if v.ThumbURL != newURL {
		t.Errorf("库里的封面地址 = %q，期望换成 %q", v.ThumbURL, newURL)
	}
}

// 已经缓存过的不再抓；换地址也换不出新东西时不该反复打上游。
func TestRepairCoversSkipsCachedAndUnchanged(t *testing.T) {
	srv, hits := thumbServer(t)
	url := srv.URL + "/fresh.png"
	ad := &thumbAdapter{thumbURL: url} // 详情页给的还是同一个地址
	r, st, _ := newCoverFixture(t, ad)
	addVideo(t, st, "cached1", url)

	if got := r.RepairCovers(context.Background()); got != 1 {
		t.Fatalf("首次修复数 = %d，期望 1", got)
	}
	if got := r.RepairCovers(context.Background()); got != 0 {
		t.Errorf("第二轮修复数 = %d，期望 0（已经缓存了）", got)
	}
	if *hits != 1 {
		t.Errorf("上游被请求 %d 次，期望 1 次", *hits)
	}
}

// 修复不能把没有封面的影片也算进去。
func TestRepairCoversIgnoresVideosWithoutThumb(t *testing.T) {
	srv, hits := thumbServer(t)
	r, st, _ := newCoverFixture(t, &fakeAdapter{})
	addVideo(t, st, "nothumb", "")
	if got := r.RepairCovers(context.Background()); got != 0 {
		t.Errorf("修复数 = %d，期望 0", got)
	}
	if *hits != 0 {
		t.Errorf("不该请求上游，实际 %d 次", *hits)
	}
	_ = srv
}
