package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/muxcache"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/store"
)

// 4K 那一档的回归测试：合流要在后台跑，`?status=1` 立刻回进度，
// 前端轮询到 ready 才把地址交给 <video>。
// 以前的写法是「一个一直不回来的请求」，用户等 1~3 分钟只看到转圈，最后当成「放不了」。

type fakeRemuxMuxer struct {
	block  chan struct{} // 非 nil 时先报 50% 再阻塞，直到关闭
	report []float64
	out    []byte
	calls  int
}

func (f *fakeRemuxMuxer) RemuxTo(_ context.Context, _, _, _, _, outPath string, totalSec int,
	onProgress func(percent float64)) error {
	f.calls++
	if onProgress != nil {
		if len(f.report) == 0 {
			f.report = []float64{50}
		}
		for _, p := range f.report {
			onProgress(p)
		}
	}
	if f.block != nil {
		<-f.block
	}
	return os.WriteFile(outPath, f.out, 0o644)
}

func newRemuxServer(t *testing.T, m *fakeRemuxMuxer) (*Server, int64) {
	t.Helper()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{DataDir: tmp}
	srv := New(cfg, st, platform.NewRegistry(), nil, events.NewHub(), nil, nil, log, nil,
		imgcache.New(t.TempDir(), nil, "", log))
	srv.mux = muxcache.New(filepath.Join(tmp, "mux"), m)

	vidID, _, err := st.UpsertVideo(&model.Video{
		Platform: "youtube", PlatformVideoID: "dash1", Title: "分轨测试", DurationSec: 1492,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 库里存一档「音画分轨」的地址：有 AudioURL 才算 DASH 档。
	if err := st.SaveVariants(vidID, []model.Variant{
		{Height: 1080, Width: 1920, Bandwidth: 2000000, Codec: "avc1.640032", URL: "https://example.invalid/v.m4s"},
		{Height: 2160, Width: 3840, Bandwidth: 9000000, Codec: "avc1.640034",
			URL: "https://example.invalid/4k.m4s", AudioURL: "https://example.invalid/a.m4s"},
	}); err != nil {
		t.Fatal(err)
	}
	return srv, vidID
}

func remuxStatusOf(t *testing.T, srv *Server, id int64, variant string) map[string]any {
	t.Helper()
	path := "/api/videos/" + strconv.FormatInt(id, 10) + "/remux?v=" + variant + "&status=1"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.handleVideoRemux(rec, withRouteParam(req, "id", strconv.FormatInt(id, 10)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status 应 200，得到 %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRemuxStatusReportsProgressThenServes(t *testing.T) {
	m := &fakeRemuxMuxer{block: make(chan struct{}), out: []byte("fake 4k mp4")}
	srv, id := newRemuxServer(t, m)

	got := remuxStatusOf(t, srv, id, "2160p")
	if got["state"] != string(muxcache.StateMerging) {
		t.Fatalf("state = %v，期望 merging", got["state"])
	}
	// 第一次快照可能还没拿到 ffmpeg 的第一段进度（百分比 0），等它涨上来。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got = remuxStatusOf(t, srv, id, "2160p")
		if pct, _ := got["percent"].(float64); pct == 50 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pct, _ := got["percent"].(float64); pct != 50 {
		t.Errorf("percent = %v，期望 50", got["percent"])
	}

	// 再问一次：不能因为「有人在等」就重开一趟 ffmpeg。
	if got := remuxStatusOf(t, srv, id, "2160p"); got["state"] != string(muxcache.StateMerging) {
		t.Fatalf("第二次 state = %v，期望仍在 merging", got["state"])
	}
	if m.calls != 1 {
		t.Errorf("合流跑了 %d 次，期望 1 次", m.calls)
	}

	close(m.block)
	readyBy := time.Now().Add(3 * time.Second)
	for time.Now().Before(readyBy) {
		if got := remuxStatusOf(t, srv, id, "2160p"); got["state"] == string(muxcache.StateReady) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := remuxStatusOf(t, srv, id, "2160p"); got["state"] != string(muxcache.StateReady) {
		t.Fatalf("合流完成后 state = %v，期望 ready", got["state"])
	}

	// ready 之后正常路径直接读盘，而且支持 Range（拖动进度条）。
	path := "/api/videos/" + strconv.FormatInt(id, 10) + "/remux?v=2160p"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	srv.handleVideoRemux(rec, withRouteParam(req, "id", strconv.FormatInt(id, 10)))
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("Range 请求应回 206，得到 %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "fake" {
		t.Errorf("发了 %q，期望 `fake`", body)
	}
	if m.calls != 1 {
		t.Errorf("ready 之后不该再合流，跑了 %d 次", m.calls)
	}
}

func TestRemuxStatusReportsFailure(t *testing.T) {
	m := &fakeRemuxMuxer{out: nil}
	srv, id := newRemuxServer(t, m)
	m.block = make(chan struct{})
	close(m.block) // 立刻放行

	// 先失败一次：把合流器换成会炸的（图片目录写不了这种事在这里用错误值代替）。
	srv.mux = muxcache.New(filepath.Join(t.TempDir(), "mux"), errMuxer{})
	got := remuxStatusOf(t, srv, id, "2160p")
	var state string
	for i := 0; i < 100; i++ {
		got = remuxStatusOf(t, srv, id, "2160p")
		state, _ = got["state"].(string)
		if state == string(muxcache.StateFailed) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state != string(muxcache.StateFailed) {
		t.Fatalf("state = %v，期望 failed", got["state"])
	}
	if _, ok := got["error"]; !ok {
		t.Errorf("失败时应当带上 error: %v", got)
	}
}

type errMuxer struct{}

func (errMuxer) RemuxTo(context.Context, string, string, string, string, string, int, func(float64)) error {
	return os.ErrPermission
}
