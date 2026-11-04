package igram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/JinsFoni/MyPorn/internal/social"
)

// anonNodes 是爬虫页 / 网格查询共用的三条帖子（单图 / 轮播 / 视频）。
// 字段名逐个照抄线上响应，真实页面 1265407 字节，这里只留结构。
func anonNodes() []map[string]any {
	return []map[string]any{
		{"pk": "4020717614598471869", "media_type": 1, "is_timeline_pinned": false,
			"caption": map[string]any{"text": "11月もよろしくお願いします"},
			"image_versions2": map[string]any{"candidates": []map[string]any{
				{"width": 3072, "height": 4096, "url": "https://x.cdninstagram.com/a.jpg"},
				{"width": 1080, "height": 1350, "url": "https://x.cdninstagram.com/a-small.jpg"},
			}}},
		{"pk": "4019241275077392564", "media_type": 8, "is_timeline_pinned": false,
			"caption": map[string]any{"text": "昨日の撮影"},
			"image_versions2": map[string]any{"candidates": []map[string]any{
				{"width": 1080, "height": 1350, "url": "https://x.cdninstagram.com/b1.jpg"},
			}}},
		{"pk": "4015606537439411666", "media_type": 2,
			"image_versions2": map[string]any{"candidates": []map[string]any{
				{"width": 1080, "height": 1350, "url": "https://x.cdninstagram.com/v.jpg"},
			}}},
	}
}

// anonHTML 是一份「爬虫页」的最小样本：资料段 + 首屏网格 + LSD 令牌。
func anonHTML() string {
	edges := make([]map[string]any, 0, 3)
	for i, n := range anonNodes() {
		edges = append(edges, map[string]any{"node": n, "cursor": "c" + itoa2(i)})
	}
	payload, _ := json.Marshal(map[string]any{"require": []any{[]any{
		"RelayPrefetchedStreamCache", "next", []any{"adp_PolarisLoggedOutMobileProfileRootQuery",
			map[string]any{"__bbox": map[string]any{"result": map[string]any{"data": map[string]any{
				"xig_user_by_igid_v2": map[string]any{
					"full_name": "楓カレン", "username": "kaedekaren0825", "is_verified": true,
					"follower_count": 667700, "pk": "11219506159", "id": "17841411104792438",
					"profile_pic_url": "https://x.cdninstagram.com/avatar.jpg",
					"polaris_timeline_connection": map[string]any{
						"edges":     edges,
						"page_info": map[string]any{"end_cursor": "EC", "has_next_page": true},
					},
				},
			}}}},
		},
	}}})
	return "<!DOCTYPE html><html><head><title>Instagram</title></head><body>" +
		`<script>["LSD",[],{"token":"ANONLSD123"}]</script>` +
		`<script type="application/json">` + string(payload) + `</script></body></html>`
}

// anonPostDetail 是免登录单帖详情的响应样本（形状照线上：data.xig_polaris_media.if_not_gated_logged_out）。
func anonPostDetail(pk string, takenAt int64, children []string) string {
	kids := make([]map[string]any, 0, len(children))
	for i, code := range children {
		kids = append(kids, map[string]any{
			"pk":         code,
			"code":       "CHILD" + string(rune('A'+i)),
			"media_type": 1,
			"image_versions2": map[string]any{"candidates": []map[string]any{
				{"width": 1080, "height": 1350, "url": "https://x.cdninstagram.com/" + code + ".jpg"},
			}},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"xig_polaris_media": map[string]any{
				"if_not_gated_logged_out": map[string]any{
					"pk":              pk,
					"code":            "DfHNqUTk3i0",
					"taken_at":        takenAt,
					"media_type":      8,
					"carousel_media":  kids,
					"image_versions2": map[string]any{"candidates": []map[string]any{{"width": 1080, "height": 1350, "url": "https://x.cdninstagram.com/first.jpg"}}},
				},
			},
		},
	})
	return string(body)
}

// TestShortcodeOfPK 用线上实测的两对 (pk, code) 当基准。
func TestShortcodeOfPK(t *testing.T) {
	cases := []struct{ pk, want string }{
		{"4020717614598471869", "DfMdV4oTEC9"}, // 单图帖，页面上 code 就是这个
		{"4019241275077392564", "DfHNqUTk3i0"}, // 轮播帖，同上
		{"", ""},
		{"abc", ""},
	}
	for _, c := range cases {
		if got := shortcodeOfPK(c.pk); got != c.want {
			t.Fatalf("shortcodeOfPK(%q) = %q，期望 %q", c.pk, got, c.want)
		}
	}
}

// TestTakenAtOfPK 校验雪花 id 推时间的公式：以实测的 taken_at 为基准，误差 60 s 以内。
func TestTakenAtOfPK(t *testing.T) {
	cases := []struct {
		pk      string
		wantUTC string
		slack   time.Duration
	}{
		{"4020717614598471869", "2026-11-01T09:55:52Z", 60 * time.Second},
		{"4019241275077392564", "2026-10-30T09:01:58Z", 60 * time.Second},
	}
	for _, c := range cases {
		want, err := time.Parse(time.RFC3339, c.wantUTC)
		if err != nil {
			t.Fatal(err)
		}
		got := takenAtOfPK(c.pk)
		if got.IsZero() {
			t.Fatalf("takenAtOfPK(%q) 是零值", c.pk)
		}
		if d := got.Sub(want); d > c.slack || d < -c.slack {
			t.Fatalf("takenAtOfPK(%q) = %s，与实测 %s 差 %s", c.pk, got, want, d)
		}
	}
	if !takenAtOfPK("x").IsZero() {
		t.Fatal("非数字 pk 应该返回零值")
	}
}

// TestAnonSlicePage 校验本地切片：100 条按 gqlPageSize 切页、断点是 anon:{end}，
// 而且轮播帖的单帖详情**只打一次**（第二次切同一页走 6 小时缓存）。
func TestAnonSlicePage(t *testing.T) {
	a, _, detailCalls := anonServer(t, "")
	nodes := make([]anonMedia, 0, anonMaxFirst)
	for i := 0; i < anonMaxFirst; i++ {
		nodes = append(nodes, anonMedia{
			PK:        fmt.Sprintf("4020717614598471%03d", i),
			MediaType: 8, // 轮播：每条都要打一次单帖详情
			ImageVersions: struct {
				Candidates []candidate `json:"candidates"`
			}{Candidates: []candidate{{URL: "https://x.cdninstagram.com/n.jpg", Width: 1080, Height: 1350}}},
		})
	}
	c := &anonCache{nodes: nodes, lsd: "L"}
	p1 := a.anonSlicePage(context.Background(), "kaedekaren0825", c, 0)
	if len(p1.Posts) != gqlPageSize {
		t.Fatalf("第一页 %d 条，期望 %d", len(p1.Posts), gqlPageSize)
	}
	if p1.NextCursor != "anon:"+itoa(gqlPageSize) {
		t.Fatalf("第一页断点 %q", p1.NextCursor)
	}
	if *detailCalls != gqlPageSize {
		t.Fatalf("第一页的 %d 条轮播应各打 1 次详情，实际 %d", gqlPageSize, *detailCalls)
	}
	offset, ok := anonOffset(p1.NextCursor)
	if !ok || offset != gqlPageSize {
		t.Fatalf("断点解析 %d %v", offset, ok)
	}
	// 同一页再切一次：全走缓存，不再打详情
	a.anonSlicePage(context.Background(), "kaedekaren0825", c, 0)
	if *detailCalls != gqlPageSize {
		t.Fatalf("重复切页不该再打详情，实际 %d", *detailCalls)
	}
	// 100 条 = 33 + 33 + 34
	p2 := a.anonSlicePage(context.Background(), "kaedekaren0825", c, offset)
	if len(p2.Posts) != gqlPageSize || p2.NextCursor != "anon:"+itoa(2*gqlPageSize) {
		t.Fatalf("第二页 %d 条，断点 %q", len(p2.Posts), p2.NextCursor)
	}
	p3 := a.anonSlicePage(context.Background(), "kaedekaren0825", c, 2*gqlPageSize)
	if len(p3.Posts) != gqlPageSize || p3.NextCursor != "anon:"+itoa(3*gqlPageSize) {
		t.Fatalf("第三页 %d 条，断点 %q", len(p3.Posts), p3.NextCursor)
	}
	p4 := a.anonSlicePage(context.Background(), "kaedekaren0825", c, 3*gqlPageSize)
	if want := anonMaxFirst - 3*gqlPageSize; len(p4.Posts) != want {
		t.Fatalf("最后一页 %d 条，期望 %d", len(p4.Posts), want)
	}
	if p4.NextCursor != "" {
		t.Fatalf("最后一页不该有断点，得到 %q", p4.NextCursor)
	}
	if n := len(a.anonPosts); n != anonMaxFirst {
		t.Fatalf("100 条轮播应全部进缓存，实际 %d", n)
	}
	if len(a.anonSlicePage(context.Background(), "kaedekaren0825", c, 200).Posts) != 0 {
		t.Fatal("越界应该回空页")
	}
	if _, ok := anonOffset("anon:x"); ok {
		t.Fatal("非法断点不该解析成功")
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// anonServer 起一个「会话已死、爬虫页还活着」的假 IG。
//
//   - 普通 UA 的 GET /{handle}/ → 302 到 /accounts/login/（模拟会话被拦）
//   - 爬虫 UA 的 GET /{handle}/ → 爬虫页（200，带资料 + 网格 + LSD）
//   - POST /api/graphql：网格查询回 first 条；单帖查询回详情
func anonServer(t *testing.T, gridErr string) (*Adapter, *int, *int) {
	t.Helper()
	var gridCalls, detailCalls int
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/") {
			http.Error(w, `{"message":"down"}`, http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			if !strings.Contains(r.Header.Get("User-Agent"), "Googlebot") {
				// 会话路径：跟 graphql.go 的 loginRedirectError 对齐（302 到登录页）
				http.Redirect(w, r, "/accounts/login/?next=%2Fkaedekaren0825%2F", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(anonHTML()))
			return
		}
		// POST /api/graphql
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		switch r.PostForm.Get("doc_id") {
		case anonGridDocID:
			gridCalls++
			if gridErr != "" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(gridErr))
				return
			}
			edges := make([]map[string]any, 0, 3)
			for i, n := range anonNodes() {
				edges = append(edges, map[string]any{"node": n, "cursor": "c" + itoa2(i)})
			}
			body, _ := json.Marshal(map[string]any{"data": map[string]any{"xig_user_by_igid_v2": map[string]any{
				"full_name": "楓カレン", "username": "kaedekaren0825", "pk": "11219506159",
				"polaris_timeline_connection": map[string]any{"edges": edges,
					"page_info": map[string]any{"has_next_page": false, "end_cursor": ""}},
			}}})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case anonPostDocID:
			detailCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(anonPostDetail("4019241275077392564", 1793350918,
				[]string{"c1", "c2"})))
		default:
			http.Error(w, "unknown doc", http.StatusBadRequest)
		}
	}, "")
	return a, &gridCalls, &detailCalls
}

func itoa2(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestAnonProfileWhenSessionDead：会话被 302 到登录页时，资料改由爬虫页给出。
func TestAnonProfileWhenSessionDead(t *testing.T) {
	a, _, _ := anonServer(t, "")
	acct, err := a.GetProfile(context.Background(), "kaedekaren0825")
	if err != nil {
		t.Fatalf("GetProfile 失败：%v", err)
	}
	if acct.DisplayName != "楓カレン" || acct.Handle != "kaedekaren0825" {
		t.Fatalf("资料不对：%+v", acct)
	}
	if !strings.HasSuffix(acct.AvatarURL, "avatar.jpg") {
		t.Fatalf("头像不对：%q", acct.AvatarURL)
	}
}

// TestAnonListPostsGridAndCarousel：会话断了也能出帖子；轮播帖的子图靠单帖详情补齐。
func TestAnonListPostsGridAndCarousel(t *testing.T) {
	a, gridCalls, detailCalls := anonServer(t, "")
	page, err := a.ListPosts(context.Background(), "kaedekaren0825", "")
	if err != nil {
		t.Fatalf("ListPosts 失败：%v", err)
	}
	if *gridCalls != 1 {
		t.Fatalf("网格查询应该只打 1 次，实际 %d", *gridCalls)
	}
	if len(page.Posts) != 3 {
		t.Fatalf("应该 3 条帖子，实际 %d", len(page.Posts))
	}
	// 第一条：单图，时间由 pk 推出，图片取最大档
	first := page.Posts[0]
	if first.PostID != "DfMdV4oTEC9" {
		t.Fatalf("第一条 id %q（短码应由 pk 推出）", first.PostID)
	}
	if first.PostedAt.IsZero() {
		t.Fatal("第一条没有时间（应该由 pk 推出）")
	}
	if len(first.Photos) != 1 || first.Photos[0].Width != 3072 {
		t.Fatalf("第一条图片不对：%+v", first.Photos)
	}
	if first.Caption != "11月もよろしくお願いします" {
		t.Fatalf("第一条正文不对：%q", first.Caption)
	}
	// 第二条：轮播 → 打单帖详情，拿到 2 张子图 + 精确时间
	second := page.Posts[1]
	if *detailCalls != 1 {
		t.Fatalf("轮播帖应该打 1 次单帖详情，实际 %d", *detailCalls)
	}
	if len(second.Photos) != 2 {
		t.Fatalf("轮播帖应有 2 张子图，实际 %d：%+v", len(second.Photos), second.Photos)
	}
	if got := second.PostedAt.UTC().Format(time.RFC3339); got != "2026-10-30T09:01:58Z" {
		t.Fatalf("轮播帖时间应为详情里的精确值，实际 %s", got)
	}
	// 第三条：视频 → 入库但不取图（和登录态的 postOfGQL 一致）
	third := page.Posts[2]
	if len(third.Photos) != 0 {
		t.Fatalf("视频帖不该取图，实际 %+v", third.Photos)
	}
	// 3 条 < 一页 33 条，所以没有下一页
	if page.NextCursor != "" {
		t.Fatalf("不该有下一页，得到 %q", page.NextCursor)
	}
	// 第二轮直接走缓存，不再打网格
	if _, err := a.ListPosts(context.Background(), "kaedekaren0825", ""); err != nil {
		t.Fatal(err)
	}
	if *gridCalls != 1 {
		t.Fatalf("10 分钟内的第二轮应该走缓存，网格查询累计 %d 次", *gridCalls)
	}
}

// TestAnonGridFallsBackToPageEdges：网格查询被限流（1675004）时退回页面自带的 3 条。
func TestAnonGridFallsBackToPageEdges(t *testing.T) {
	a, _, _ := anonServer(t, `{"errors":[{"message":"Rate limit exceeded","code":1675004}]}`)
	page, err := a.ListPosts(context.Background(), "kaedekaren0825", "")
	if err != nil {
		t.Fatalf("网格限流时应退回页面首屏，实际报错：%v", err)
	}
	if len(page.Posts) != 3 {
		t.Fatalf("应退回页面自带的 3 条，实际 %d", len(page.Posts))
	}
}

// TestAnonBothPathsFailKeepsNeedsAuth：两条路都不通时，报的仍然是会话那条的错，
// 并且把免登录的结论也带上（用户能动手修的是会话）。
func TestAnonBothPathsFailKeepsNeedsAuth(t *testing.T) {
	a := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/") {
			http.Error(w, `{"message":"down"}`, http.StatusNotFound)
			return
		}
		if strings.Contains(r.Header.Get("User-Agent"), "Googlebot") {
			// 爬虫页也不给数据（模拟 IG 又改了爬虫页）
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>nothing</body></html>`))
			return
		}
		http.Redirect(w, r, "/accounts/login/", http.StatusFound)
	}, "")
	_, err := a.GetProfile(context.Background(), "kaedekaren0825")
	if err == nil {
		t.Fatal("两条路都不通时应该报错")
	}
	if social.KindOf(err) != social.KindNeedsAuth {
		t.Fatalf("错误种类应为 needs_auth（会话才是用户能修的那条），实际 %s：%v", social.KindOf(err), err)
	}
	if !strings.Contains(err.Error(), "验证") && !strings.Contains(err.Error(), "登录") {
		t.Fatalf("文案应说清会话怎么了：%v", err)
	}
}
