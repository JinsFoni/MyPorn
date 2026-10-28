package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mario/myporn/internal/config"
	"github.com/mario/myporn/internal/events"
	"github.com/mario/myporn/internal/imgcache"
	"github.com/mario/myporn/internal/model"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/store"
)

// 线上播放这条线的测试。之前只测了「本地文件能不能播」，于是 M6 那次改动把
// 没下过的线上影片全判成 unsupported_platform（xHamster 整站播不了），测试一点没反映。

// fakeAdapter 只做播放解析，其它接口按测试需要返回错误。
type fakeAdapter struct {
	variants []model.Variant
	err      error
}

func (f *fakeAdapter) Key() string     { return "xhamster" }
func (f *fakeAdapter) Name() string    { return "xHamster" }
func (f *fakeAdapter) BaseURL() string { return "https://xhamster.com" }

func (f *fakeAdapter) ParsePerformerURL(raw string) (string, error) { return raw, nil }
func (f *fakeAdapter) GetPerformer(context.Context, string) (*model.Performer, error) {
	return nil, errors.New("用不到")
}
func (f *fakeAdapter) ListPerformerVideos(context.Context, string, int) (*platform.Page, error) {
	return nil, errors.New("用不到")
}
func (f *fakeAdapter) GetVideo(context.Context, string) (*model.Video, error) {
	return nil, errors.New("用不到")
}
func (f *fakeAdapter) ResolveStreams(context.Context, string) ([]model.Variant, error) {
	return f.variants, f.err
}
func (f *fakeAdapter) SearchPerformers(context.Context, string, int) ([]*model.Performer, bool, error) {
	return nil, false, errors.New("用不到")
}

// ResolvePlayback 让播放走「快速解析 + 顺手带标签」那条线，跟真适配器一致。
func (f *fakeAdapter) ResolvePlayback(context.Context, string) (*model.Video, []model.Variant, error) {
	return nil, f.variants, f.err
}

type playbackResp struct {
	Variants []struct {
		ID        string `json:"id"`
		Height    int    `json:"height"`
		URL       string `json:"url"`
		HLS       bool   `json:"hls"`
		IsDefault bool   `json:"isDefault"`
		Local     bool   `json:"local"`
	} `json:"variants"`
	DefaultVariantID string `json:"defaultVariantId"`
	Error            *struct {
		Code string `json:"code"`
	} `json:"error"`
}

// newOnlinePlaybackServer 建一个注册了（假）xHamster 适配器的服务，
// 库里一条 xHamster 影片；withFile 为 true 时再给它挂一个本地文件。
func newOnlinePlaybackServer(t *testing.T, ad *fakeAdapter, withFile bool) (*Server, int64) {
	t.Helper()
	tmp := t.TempDir()
	libRoot := filepath.Join(tmp, "media")
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	reg := platform.NewRegistry()
	reg.Register(ad)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{DataDir: tmp, LibraryRoot: libRoot}
	srv := New(cfg, st, reg, nil, events.NewHub(), nil, nil, log, nil, imgcache.New(t.TempDir(), nil, "", log))

	vidID, _, err := st.UpsertVideo(&model.Video{
		Platform: "xhamster", PlatformVideoID: "xh-test", Title: "片子", DurationSec: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if withFile {
		libID, err := st.CreateLibrary(&model.Library{Name: "主库", RootPath: libRoot, Enabled: true, IsDefault: true})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(libRoot, "xHamster", "测试")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "片子.mp4")
		if err := os.WriteFile(p, make([]byte, 4096), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveLibraryFile(&model.LibraryFile{
			VideoID: vidID, LibraryID: libID, VideoPath: p, SizeBytes: 4096,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return srv, vidID
}

// callPlayback 直接打 handler，返回状态码与解析后的响应体。
func callPlayback(t *testing.T, srv *Server, vidID int64) (*httptest.ResponseRecorder, playbackResp) {
	t.Helper()
	id := strconv.FormatInt(vidID, 10)
	req := httptest.NewRequest(http.MethodGet, "/api/videos/"+id+"/playback", nil)
	rec := httptest.NewRecorder()
	srv.handlePlayback(rec, withRouteParam(req, "id", id))
	var got playbackResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是 JSON（%d）: %s", rec.Code, rec.Body.String())
	}
	return rec, got
}

// TestPlaybackResolvesOnlineVariants 没下过的影片也要能播：线上档位照样返回，最高档是默认档。
func TestPlaybackResolvesOnlineVariants(t *testing.T) {
	ad := &fakeAdapter{variants: []model.Variant{
		{Height: 720, Width: 1280, Bandwidth: 2000000, Codec: "avc1.4d4015", URL: "https://video7.xhcdn.com/720p.mp4.m3u8"},
		{Height: 1080, Width: 1920, Bandwidth: 5339557, Codec: "avc1.4d4015", URL: "https://video7.xhcdn.com/1080p.mp4.m3u8"},
	}}
	srv, vidID := newOnlinePlaybackServer(t, ad, false)

	rec, got := callPlayback(t, srv, vidID)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if len(got.Variants) != 2 {
		t.Fatalf("应给 2 路线上档位，得到 %d 路", len(got.Variants))
	}
	// 最高画质在前，而且就是默认档。
	if got.Variants[0].Height != 1080 || !got.Variants[0].IsDefault {
		t.Errorf("默认档应当是 1080p: %+v", got.Variants[0])
	}
	if got.DefaultVariantID != "1080p" {
		t.Errorf("defaultVariantId 应为 1080p，得到 %q", got.DefaultVariantID)
	}
	// 线上地址必须包成 /api/stream，浏览器直连平台 CDN 会因为 Referer 拿到 404。
	q := url.Values{}
	q.Set("u", "https://video7.xhcdn.com/1080p.mp4.m3u8")
	q.Set("p", "xhamster")
	if want := "/api/stream?" + q.Encode(); got.Variants[0].URL != want {
		t.Errorf("线上地址应走 /api/stream:\n得到 %q\n应为 %q", got.Variants[0].URL, want)
	}
	if !got.Variants[0].HLS {
		t.Error(".m3u8 地址应标成 hls")
	}
	if got.Variants[0].Local {
		t.Error("线上档位不该标成本地")
	}
}

// TestPlaybackOnlineResolveFailure 线上解析失败且本地也没有：明确报错，别假装「平台没通道」。
func TestPlaybackOnlineResolveFailure(t *testing.T) {
	ad := &fakeAdapter{err: errors.New("上游 500")}
	srv, vidID := newOnlinePlaybackServer(t, ad, false)

	rec, got := callPlayback(t, srv, vidID)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("应 502，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if got.Error == nil || got.Error.Code != "resolve_failed" {
		t.Errorf("错误码应为 resolve_failed: %s", rec.Body.String())
	}
}

// TestPlaybackLocalFirstWhenOnlineResolveFails 本地有文件时线上解析失败不该挡住播放：
// 本地那一路排第一并作为默认档。
func TestPlaybackLocalFirstWhenOnlineResolveFails(t *testing.T) {
	ad := &fakeAdapter{err: errors.New("上游 500")}
	srv, vidID := newOnlinePlaybackServer(t, ad, true)

	rec, got := callPlayback(t, srv, vidID)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if len(got.Variants) != 1 {
		t.Fatalf("应只剩本地一路，得到 %d 路: %s", len(got.Variants), rec.Body.String())
	}
	if !got.Variants[0].Local || !got.Variants[0].IsDefault || got.DefaultVariantID != "local" {
		t.Errorf("本地档应当是默认档: %+v default=%q", got.Variants[0], got.DefaultVariantID)
	}
}

// TestPlaybackLocalFirstWithOnlineVariants 本地有文件 + 线上也解析成功：
// 本地排第一，线上档位照样列出来（清晰度菜单里可以切回线上）。
func TestPlaybackLocalFirstWithOnlineVariants(t *testing.T) {
	ad := &fakeAdapter{variants: []model.Variant{
		{Height: 1080, Width: 1920, Bandwidth: 5339557, Codec: "avc1.4d4015", URL: "https://video7.xhcdn.com/1080p.mp4.m3u8"},
	}}
	srv, vidID := newOnlinePlaybackServer(t, ad, true)

	rec, got := callPlayback(t, srv, vidID)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if len(got.Variants) != 2 {
		t.Fatalf("应本地 1 路 + 线上 1 路，得到 %d 路", len(got.Variants))
	}
	if !got.Variants[0].Local || !got.Variants[0].IsDefault || got.DefaultVariantID != "local" {
		t.Errorf("本地档应当排第一且是默认档: %+v default=%q", got.Variants[0], got.DefaultVariantID)
	}
	if got.Variants[1].Local || got.Variants[1].IsDefault {
		t.Errorf("线上档位不该是默认档: %+v", got.Variants[1])
	}
}
