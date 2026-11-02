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

// 会员抢先看（availability=subscriber_only）：观看页的徽章要认出来，
// 但时间 / 时长照样收——这条信息是用来决定「别入库」的，不是用来判「解析失败」的。
//
// 页面片段照 2026-11-02 抓到的真实页面剪的（2gtrgAKIH88，1.26MB）：
// 徽章里 iconType=SPONSORSHIP_STAR、style=BADGE_STYLE_TYPE_MEMBERS_ONLY、label=会员抢先观看；
// playabilityStatus.status=UNPLAYABLE 且 errorScreen 带 offerId=sponsors_only_video。
func TestParseWatchMetaMembersOnly(t *testing.T) {
	page := `<html><head><title>会员片 - YouTube</title></head><body>
		"publishDate":"2026-11-02T06:13:08-08:00","lengthSeconds":"205",
		"badges":[{"metadataBadgeRenderer":{"icon":{"iconType":"SPONSORSHIP_STAR"},
			"style":"BADGE_STYLE_TYPE_MEMBERS_ONLY","label":"会员抢先观看"}}]
	</body></html>`
	got, err := parseWatchMeta(page)
	if err != nil {
		t.Fatalf("会员抢先看的页面也该解析出元信息: %v", err)
	}
	if !got.MembersOnly {
		t.Fatal("徽章 BADGE_STYLE_TYPE_MEMBERS_ONLY 在，却没认出会员抢先看")
	}
	if got.DurationSec != 205 {
		t.Errorf("时长 = %d，期望 205", got.DurationSec)
	}
	if got.PublishedAt.IsZero() {
		t.Error("发布时间应当解析出来")
	}

	// 只有拒播理由、徽章被折叠掉时也要认得（offerId=sponsors_only_video）。
	onlyOffer := `<html><body>"publishDate":"2026-11-02T06:13:08-08:00","playabilityStatus":
		{"status":"UNPLAYABLE","reason":"成为此频道的会员即可观看会员专享的内容",
		 "errorScreen":{"playerLegacyDesktopYpcOfferRenderer":{"offerId":"sponsors_only_video"}}}</body></html>`
	got2, err := parseWatchMeta(onlyOffer)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !got2.MembersOnly {
		t.Error("offerId=sponsors_only_video 也应该认出会员抢先看")
	}

	// 普通公开视频不能误判：同一个页面里没有这两个胎记。
	plain := `<html><body>"publishDate":"2026-11-02T06:13:08-08:00","lengthSeconds":"120",
		"badges":[{"metadataBadgeRenderer":{"style":"BADGE_STYLE_TYPE_VERIFIED"}}]</body></html>`
	got3, err := parseWatchMeta(plain)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got3.MembersOnly {
		t.Error("普通视频被误判成会员抢先看")
	}
}
