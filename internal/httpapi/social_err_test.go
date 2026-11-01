package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// writeSocialErr 是抓取失败唯一对外表达的地方：映射错了前端就只会看到「服务器错误」，
// 用户也就不知道到底是该去配 Cookie、该等冷却，还是解析该更新了。
func TestWriteSocialErrMapping(t *testing.T) {
	reset := time.Date(2026, 10, 28, 2, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		err  error
		code int
		kind string
	}{
		{"needs_auth", social.NeedsAuth("要登录态"), http.StatusConflict, "needs_auth"},
		{"rate_limited", social.RateLimited("限流了", reset), http.StatusTooManyRequests, "rate_limited"},
		{"blocked", social.Blocked("被风控拦截页挡住了"), http.StatusTooManyRequests, "blocked"},
		{"not_found", social.NotFound("没有这个人"), http.StatusNotFound, "not_found"},
		{"stale_parser", social.StaleParser("页面结构变了"), http.StatusBadGateway, "stale_parser"},
		{"upstream", social.Upstream("上游炸了", nil), http.StatusBadGateway, "upstream"},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, "upstream"},
		{"other", errors.New("随便什么错"), http.StatusBadGateway, "upstream"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writeSocialErr(rec, c.err)
		if rec.Code != c.code {
			t.Errorf("%s: 状态码 %d，期望 %d", c.name, rec.Code, c.code)
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Details struct {
					ResetAt string `json:"resetAt"`
				} `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: 响应不是合法 JSON: %v", c.name, err)
		}
		if body.Error.Code != c.kind {
			t.Errorf("%s: 错误码 %q，期望 %q", c.name, body.Error.Code, c.kind)
		}
		if body.Error.Message == "" {
			t.Errorf("%s: 不该给空消息（前端要拿它直接显示）", c.name)
		}
		if c.name == "rate_limited" && body.Error.Details.ResetAt != reset.Format(time.RFC3339) {
			t.Errorf("rate_limited 应带 details.resetAt=%s，得到 %q", reset.Format(time.RFC3339), body.Error.Details.ResetAt)
		}
		// blocked 和 rate_limited 都是 429，但前端行为不同：blocked 不能带 resetAt，
		// 否则界面会显示「等到 HH:MM」，而实际上等到点也不会好。
		if c.name == "blocked" && body.Error.Details.ResetAt != "" {
			t.Errorf("blocked 不该带 details.resetAt，得到 %q", body.Error.Details.ResetAt)
		}
		if c.name == "deadline" && body.Error.Message == context.DeadlineExceeded.Error() {
			t.Error("超时不该把 Go 的原始错误抛给用户")
		}
	}
}
