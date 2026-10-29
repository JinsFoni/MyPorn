package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mario/myporn/internal/config"
	"github.com/mario/myporn/internal/events"
	"github.com/mario/myporn/internal/hls"
	"github.com/mario/myporn/internal/imgcache"
	"github.com/mario/myporn/internal/platform"
	"github.com/mario/myporn/internal/store"
	"github.com/mario/myporn/internal/ytstream"
)

// YouTube「站内播放」这条线的测试：清单改写 → 会话地址 → 分片取回。
//
// 为什么要这么测：这条链路真正的风险不在解析，而在「改写完的清单还能不能放」——
// 音轨是不是跟着搬了、分片地址是不是指向本机、会话号是不是认得出来。
// 所以上游用 httptest 假装 googlevideo，一路把播放器会走的请求都走一遍。

const testYtMaster = `#EXTM3U
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MEDIA:URI="a234/index.m3u8",TYPE=AUDIO,GROUP-ID="234",NAME="Default",DEFAULT=YES,AUTOSELECT=YES
#EXT-X-STREAM-INF:BANDWIDTH=400000,CODECS="avc1.4D4015,mp4a.40.2",RESOLUTION=426x240,AUDIO="234"
v229/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=876853,CODECS="avc1.640028,mp4a.40.2",RESOLUTION=1920x1080,AUDIO="234"
v270/index.m3u8
`

const testYtChild = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-TARGETDURATION:7
#EXT-X-MAP:URI="init.mp4"
#EXTINF:7.0,
seg1.mp4
#EXTINF:7.0,
seg2.mp4
#EXT-X-ENDLIST
`

// newYtStreamServer 起一套「上游 googlevideo + 本应用」的测试环境。
func newYtStreamServer(t *testing.T) (*Server, int64) {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/master.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, testYtMaster)
		case strings.HasSuffix(r.URL.Path, "index.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, testYtChild)
		case strings.HasSuffix(r.URL.Path, ".mp4"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = io.WriteString(w, "SEG:"+filepath.Base(r.URL.Path))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	// 白名单本来是给平台 CDN 用的；测试里的上游是 127.0.0.1，临时放行。
	orig := streamHostSuffixes
	streamHostSuffixes = append(append([]string{}, orig...), "127.0.0.1")
	t.Cleanup(func() { streamHostSuffixes = orig })

	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	chID, _, err := st.UpsertYtChannel(&store.YtChannel{
		ChannelID: "UCq-Fj5jknLsUf-MWSy4_brA", Title: "T-Series", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := st.UpsertYtVideos(chID, []store.YtVideoInput{{
		VideoID: "s8OtOH_Vfa0", Title: "Ek Onkar", DurationSec: 184,
	}})
	if err != nil || len(ids) != 1 {
		t.Fatalf("建视频失败: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{DataDir: tmp, LibraryRoot: filepath.Join(tmp, "media")}
	srv := New(cfg, st, platform.NewRegistry(), nil, events.NewHub(), nil, nil, log, nil,
		imgcache.New(t.TempDir(), nil, "", log))
	// 换掉解析器：不去跑 yt-dlp，直接把上游地址交进来。
	srv.ytStream = ytstream.NewManager(func(context.Context, string) (ytstream.Upstream, error) {
		return ytstream.Upstream{
			VideoID: "s8OtOH_Vfa0", Title: "Ek Onkar", DurationSec: 184,
			MasterURL: upstream.URL + "/master.m3u8",
			ExpiresAt: time.Now().Add(6 * time.Hour),
		}, nil
	}, log)
	return srv, ids[0]
}

// getStream 发一次 GET，返回 recorder。
func getStream(t *testing.T, h http.HandlerFunc, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// 一次把参数都塞进同一个 chi 上下文：withRouteParam 每次都新建上下文，
	// 连着调两次会把前一个参数冲掉。
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestYtStreamMasterRewritesToSession 清单要被改写成会话地址，且只留一档画质。
func TestYtStreamMasterRewritesToSession(t *testing.T) {
	srv, vid := newYtStreamServer(t)
	rec := getStream(t, srv.handleYtStreamMaster, map[string]string{"id": itoa(vid)})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "RESOLUTION=1920x1080") {
		t.Fatalf("默认档不是最高画质:\n%s", body)
	}
	if !strings.Contains(body, `#EXT-X-MEDIA:TYPE=AUDIO`) || !strings.Contains(body, `AUDIO="aud"`) {
		t.Fatalf("音轨没跟着搬过来（放出来会没有声音）:\n%s", body)
	}
	if strings.Contains(body, "googlevideo") || strings.Contains(body, "127.0.0.1") {
		t.Fatalf("清单里不该出现上游地址:\n%s", body)
	}
	if !strings.Contains(body, "/api/yt/seg/") {
		t.Fatalf("分片地址没换成站内地址:\n%s", body)
	}
	// 只留一条变体：ABR 会把画质拉来拉去，这里要的是「就是最高那档」。
	if n := strings.Count(body, "#EXT-X-STREAM-INF:"); n != 1 {
		t.Fatalf("变体数 = %d，想要 1:\n%s", n, body)
	}
}

// TestYtStreamMasterHonoursQuality 手动降档。
func TestYtStreamMasterHonoursQuality(t *testing.T) {
	srv, vid := newYtStreamServer(t)
	req := httptest.NewRequest(http.MethodGet, "/?q=240", nil)
	req = withRouteParam(req, "id", itoa(vid))
	rec := httptest.NewRecorder()
	srv.handleYtStreamMaster(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "RESOLUTION=426x240") {
		t.Fatalf("q=240 没生效:\n%s", rec.Body.String())
	}
}

// TestYtStreamPlaysThroughSession 走完整条链：清单 → 子清单 → 分片。
func TestYtStreamPlaysThroughSession(t *testing.T) {
	srv, vid := newYtStreamServer(t)
	master := getStream(t, srv.handleYtStreamMaster, map[string]string{"id": itoa(vid)}).Body.String()

	sid, n := parseSegRef(t, master)
	if sid == "" {
		t.Fatalf("没解析出会话地址:\n%s", master)
	}

	// 第一把地址是音轨子清单，第二把是视频子清单；两把都得能取回并继续改写。
	child := getStream(t, srv.handleYtStreamSeg, map[string]string{"sid": sid, "n": n})
	if child.Code != http.StatusOK {
		t.Fatalf("取子清单失败: %d %s", child.Code, child.Body.String())
	}
	childBody := child.Body.String()
	if !strings.Contains(childBody, "#EXT-X-MAP:") {
		t.Fatalf("子清单里的 init 段丢了:\n%s", childBody)
	}
	if strings.Contains(childBody, "seg1.mp4") {
		t.Fatalf("子清单里的分片还是相对路径:\n%s", childBody)
	}

	sid2, n2 := parseSegRef(t, childBody)
	if sid2 != sid {
		t.Fatalf("子清单的地址换了会话: %s vs %s", sid2, sid)
	}
	seg := getStream(t, srv.handleYtStreamSeg, map[string]string{"sid": sid2, "n": n2})
	if seg.Code != http.StatusOK {
		t.Fatalf("取分片失败: %d", seg.Code)
	}
	if !strings.HasPrefix(seg.Body.String(), "SEG:") {
		t.Fatalf("分片内容不对: %q", seg.Body.String())
	}
}

// TestYtStreamUnknownSession 会话没了要回报 410（前端据此重开一次），而不是 404 让人以为片子没了。
func TestYtStreamUnknownSession(t *testing.T) {
	srv, _ := newYtStreamServer(t)
	rec := getStream(t, srv.handleYtStreamSeg, map[string]string{"sid": "deadbeef", "n": "0"})
	if rec.Code != http.StatusGone {
		t.Fatalf("状态码 = %d，想要 410", rec.Code)
	}
}

// TestYtStreamInfoListsQualities 弹窗先要一份画质清单。
func TestYtStreamInfoListsQualities(t *testing.T) {
	srv, vid := newYtStreamServer(t)
	rec := getStream(t, srv.handleYtStreamInfo, map[string]string{"id": itoa(vid)})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"default":1080`, `"label":"1080p"`, `"label":"240p"`,
		`/api/yt/videos/` + itoa(vid) + `/stream.m3u8`} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应里缺 %s:\n%s", want, body)
		}
	}
}

// parseSegRef 从清单里取出第一个 /api/yt/seg/{sid}/{n}。
func parseSegRef(t *testing.T, body string) (sid, n string) {
	t.Helper()
	i := strings.Index(body, "/api/yt/seg/")
	if i < 0 {
		return "", ""
	}
	rest := body[i+len("/api/yt/seg/"):]
	end := strings.IndexAny(rest, "\"\n")
	if end >= 0 {
		rest = rest[:end]
	}
	sid, n, ok := strings.Cut(rest, "/")
	if !ok {
		return "", ""
	}
	return sid, n
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// 4K 那一档要报「实际要播的那条」（AV1 13.5 Mbps），不是 HLS 里的 VP9 28.2 Mbps；
// 其余档位照旧报 HLS。数字取自 2026-10-29 实测的 YFsMnJEbV1U。
func TestYtQualitiesPrefersAV1TrackFor2160p(t *testing.T) {
	variants := []hls.StreamVariant{
		{Height: 2160, Bandwidth: 28220650, Codecs: "vp09.00.51.08,mp4a.40.2"},
		{Height: 1440, Bandwidth: 14143470, Codecs: "vp09.00.50.08,mp4a.40.2"},
		{Height: 1080, Bandwidth: 6234340, Codecs: "avc1.64002A,mp4a.40.2"},
	}
	tracks := []ytstream.Track{{Height: 2160, Codecs: "av01.0.13M.08", Bandwidth: 13461975}}

	got := ytQualities(variants, tracks)
	if len(got) != 3 {
		t.Fatalf("画质档数 = %d，想要 3", len(got))
	}
	// Codecs 保留完整串（菜单里前端自己按 codecFamily 折成「AV1」）
	if !got[0].Dash || got[0].Bandwidth != 13461975 || got[0].Codecs != "av01.0.13M.08" {
		t.Fatalf("2160p 应当按 AV1 报并且走 dash: %+v", got[0])
	}
	for _, q := range got[1:] {
		if q.Dash {
			t.Fatalf("%s 不该走原生播放: %+v", q.Label, q)
		}
	}
	if got[1].Bandwidth != 14143470 {
		t.Fatalf("1440p 码率被改了: %+v", got[1])
	}
}
