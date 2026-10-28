package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// 预热取物的「要么完整、要么报错」纪律。
//
// 这一条是 YouTube 4K 卡顿的根因回归：旧实现用 io.LimitReader(resp.Body, maxWarmObject)
// 直接读，超限就**悄悄截断**，然后这份残缺字节被当成整段分片发给浏览器。
// 实测 2160p 每段 21~23 MB、上限 16 MiB，于是每一集 4K 的第一段分片都是 16777216 字节的残段，
// hls.js 报 bufferAppendError 后无限重试（40 秒 28 次 waiting，currentTime 一直是 0）。
func TestReadCappedRefusesOversize(t *testing.T) {
	body := []byte("0123456789") // 10 字节

	got, err := readCapped(bytes.NewReader(body), len(body))
	if err != nil {
		t.Fatalf("正好等于上限不该报错: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("读回的字节不一致: %q", got)
	}

	if data, err := readCapped(strings.NewReader(string(body)), len(body)-1); err == nil {
		t.Fatalf("超限必须报错，却拿到 %d 字节（截断冒充完整正是这次要修的 bug）", len(data))
	} else if data != nil {
		t.Fatalf("超限时不该返回任何字节，实际 %d 字节", len(data))
	}
}

// 上游偶发 403 必须自己吃掉，不能甩给播放器。
//
// 实测依据：一条 4K 视频播 40 秒里 googlevideo 回了 3 次 403（同一地址隔一秒再要就过），
// hls.js 收到 502 后重排整个分片、实测代价 3~9 秒，正好把缓冲吃穿。
func TestOpenUpstreamRetriesTransientStatus(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "nope")
			return
		}
		_, _ = io.WriteString(w, "SEG")
	}))
	defer upstream.Close()

	srv, _ := newYtStreamServer(t) // 顺手把 127.0.0.1 加进代理白名单
	u, err := url.Parse(upstream.URL + "/seg1.mp4")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.openUpstream(t.Context(), u, "youtube", "")
	if err != nil {
		t.Fatalf("第一次 403 应该被重试掉，实际报错: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "SEG" {
		t.Fatalf("重试后应拿到 200/SEG，实际 %d/%q", resp.StatusCode, body)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("上游应被请求 2 次，实际 %d 次", got)
	}
}

// 非可重试状态（404/410）要立刻返回，别把一次明确的失败拖成三次。
func TestOpenUpstreamDoesNotRetryGone(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusGone)
	}))
	defer upstream.Close()

	srv, _ := newYtStreamServer(t)
	u, _ := url.Parse(upstream.URL + "/seg1.mp4")
	resp, err := srv.openUpstream(t.Context(), u, "youtube", "")
	if err != nil {
		t.Fatalf("410 不该当网络错误处理: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("状态码应原样返回 410，实际 %d", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("410 只该请求 1 次，实际 %d 次", got)
	}
}
