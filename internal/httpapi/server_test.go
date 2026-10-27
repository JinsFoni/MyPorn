package httpapi

import "testing"

// 长连接接口一旦被超时中间件包住，SSE 会在 60 秒后收到一次多余的头写入，
// 媒体流则会被当场掐断——这两个路径必须始终被排除在外。
func TestIsLongLived(t *testing.T) {
	// 传进来的是 req.URL.Path，本来就不含查询串（/api/stream?id=3 这里就是 /api/stream）
	yes := []string{"/api/stream", "/api/events"}
	no := []string{"/api/videos", "/api/events/foo", "/api/streams", "/api/settings"}
	for _, p := range yes {
		if !isLongLived(p) {
			t.Errorf("%s 应当被当作长连接", p)
		}
	}
	for _, p := range no {
		if isLongLived(p) {
			t.Errorf("%s 不该被当作长连接", p)
		}
	}
}
