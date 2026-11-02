package ytsub

import "testing"

// 真实 watch 页里那几段被裁下来（键名、引号、转义都照原样），
// 顺便混进日志噪音：`"viewCount":"0"` 出现在没开放播放量的视频上，不该被当成有效值。
const watchFixture = `<!DOCTYPE html><html><head><title>10/13 쏘블리 라이브 - YouTube</title>
<meta itemprop="datePublished" content="2026-10-13T19:16:07-07:00">
</head><body><script>var ytInitialPlayerResponse = {"microformat":{"playerMicroformatRenderer":{
"lengthSeconds":"605","viewCount":"0","publishDate":"2026-10-13T19:16:07-07:00","uploadDate":"2026-10-13T19:16:07-07:00"}},
"videoDetails":{"lengthSeconds":"605","viewCount":"202282","isLiveContent":false}};</script></body></html>`

func TestParseWatchMeta(t *testing.T) {
	got, err := parseWatchMeta(watchFixture)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := "2026-10-14T02:16:07Z" // -07:00 转 UTC
	if got.PublishedAt.UTC().Format("2006-01-02T15:04:05Z") != want {
		t.Fatalf("发布时间 = %s，期望 %s", got.PublishedAt.UTC().Format("2006-01-02T15:04:05Z"), want)
	}
	if got.DateSource != "watch" {
		t.Fatalf("date_source = %q，期望 watch", got.DateSource)
	}
	if got.DurationSec != 605 {
		t.Fatalf("时长 = %d，期望 605", got.DurationSec)
	}
	// 第一个 viewCount 是 0（microformat 里没开放），要跳过取到 202282。
	if got.Views != 202282 {
		t.Fatalf("播放量 = %d，期望 202282", got.Views)
	}
	if got.Title != "10/13 쏘블리 라이브" {
		t.Fatalf("标题 = %q", got.Title)
	}
}

func TestParseWatchMetaFallsBackToUploadDate(t *testing.T) {
	page := `<script>var x = {"uploadDate":"2021-03-04T05:06:07+09:00"};</script>`
	got, err := parseWatchMeta(page)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if want := "2021-03-03T20:06:07Z"; got.PublishedAt.UTC().Format("2006-01-02T15:04:05Z") != want {
		t.Fatalf("发布时间 = %s，期望 %s", got.PublishedAt.UTC().Format("2006-01-02T15:04:05Z"), want)
	}
}

func TestParseWatchMetaNoDate(t *testing.T) {
	if _, err := parseWatchMeta(`<html><body>This video isn't available anymore</body></html>`); err == nil {
		t.Fatal("没有发布时间时应当报错，不能瞎猜一个值")
	}
}

// 时间被隐藏（或页面只放了 videoDetails）但时长在的页面：时长要照样收下来。
// 卡片上的时长是独立补的一格（RSS 通道不给时长），别因为时间缺就整条丢掉。
func TestParseWatchMetaDurationOnly(t *testing.T) {
	page := `<html><head><title>一条视频 - YouTube</title></head><body>
		"videoDetails":{"lengthSeconds":"247","viewCount":"1234"}
	</body></html>`
	got, err := parseWatchMeta(page)
	if err != nil {
		t.Fatalf("只有时长时应该收下时长，却报错: %v", err)
	}
	if got.DurationSec != 247 {
		t.Fatalf("时长 = %d，期望 247", got.DurationSec)
	}
	if !got.PublishedAt.IsZero() {
		t.Fatalf("没有时间就该是零值，实际 = %v", got.PublishedAt)
	}
}
