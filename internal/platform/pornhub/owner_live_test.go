package pornhub

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// 视频页「上传者」区块： <a href="/model/xxx" class="userAvatarLink">
var reUploaderTag = regexp.MustCompile(`(?s)<a[^>]*userAvatarLink[^>]*>`)
var reHrefModel = regexp.MustCompile(`/model/([^/"?]+)`)

func uploaderOf(body []byte) string {
	tag := reUploaderTag.Find(body)
	if tag == nil {
		return ""
	}
	m := reHrefModel.FindSubmatch(tag)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func TestLiveUploaders(t *testing.T) {
	if os.Getenv("LIVE") == "" {
		t.Skip("LIVE=1")
	}
	db, _ := sql.Open("sqlite", os.Getenv("DB"))
	rows, err := db.Query(`select v.platform_video_id, v.title from videos v
		join video_performers vp on vp.video_id = v.id where vp.performer_id = 1 order by v.id`)
	if err != nil {
		t.Fatal(err)
	}
	type item struct{ id, title string }
	var items []item
	for rows.Next() {
		var it item
		rows.Scan(&it.id, &it.title)
		items = append(items, it)
	}
	rows.Close()

	a, _ := New("https://cn.pornhub.com", "", "")
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad, unknown := 0, 0
	for _, it := range items {
		wg.Add(1)
		go func(it item) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			body, err := a.get(ctx, "/view_video.php", map[string][]string{"viewkey": {it.id}})
			if err != nil {
				mu.Lock()
				unknown++
				t.Logf("%s 抓取失败: %v", it.id, err)
				mu.Unlock()
				return
			}
			up := uploaderOf(body)
			if up == "" {
				mu.Lock()
				unknown++
				t.Logf("%s 未解析到上传者: %s", it.id, it.title)
				mu.Unlock()
				return
			}
			if up != "mini-diva" {
				mu.Lock()
				bad++
				t.Logf("上传者是 %s 而非 mini-diva: %s %s", up, it.id, it.title)
				mu.Unlock()
			}
		}(it)
	}
	wg.Wait()
	t.Logf("共 %d 条；不属于 mini-diva %d 条；无法确认 %d 条", len(items), bad, unknown)
}
