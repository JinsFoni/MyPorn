package ytsub

import (
	"strings"
	"testing"
	"time"
)

// 真实的 RSS 响应被裁成 2 条，保留命名空间与 community（播放量）结构。
const feedFixture = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns:media="http://search.yahoo.com/mrss/" xmlns="http://www.w3.org/2005/Atom">
 <link rel="self" href="http://www.youtube.com/feeds/videos.xml?channel_id=UCq-Fj5jknLsUf-MWSy4_brA"/>
 <id>yt:channel:q-Fj5jknLsUf-MWSy4_brA</id>
 <yt:channelId>q-Fj5jknLsUf-MWSy4_brA</yt:channelId>
 <title>T-Series</title>
 <author><name>T-Series</name><uri>https://www.youtube.com/channel/UCq-Fj5jknLsUf-MWSy4_brA</uri></author>
 <published>2006-03-13T14:27:05+00:00</published>
 <entry>
  <id>yt:video:s8OtOH_Vfa0</id>
  <yt:videoId>s8OtOH_Vfa0</yt:videoId>
  <title>Ek Onkar (Full Video)</title>
  <link rel="alternate" href="https://www.youtube.com/watch?v=s8OtOH_Vfa0"/>
  <published>2026-10-27T14:58:48+00:00</published>
  <media:group>
   <media:title>Ek Onkar (Full Video)</media:title>
   <media:thumbnail url="https://i4.ytimg.com/vi/s8OtOH_Vfa0/hqdefault.jpg" width="480" height="360"/>
   <media:description>Presenting the Full Video Song &amp; more</media:description>
   <media:community>
    <media:starRating count="225" average="5.00" min="1" max="5"/>
    <media:statistics views="42614"/>
   </media:community>
  </media:group>
 </entry>
 <entry>
  <id>yt:video:uJKyS1w0swo</id>
  <yt:videoId>uJKyS1w0swo</yt:videoId>
  <title>Second One</title>
  <published>2026-10-26T10:00:00+00:00</published>
  <media:group>
   <media:title>Second One</media:title>
   <media:description>desc</media:description>
   <media:community><media:statistics views="1000"/></media:community>
  </media:group>
 </entry>
</feed>`

func TestParseFeed(t *testing.T) {
	ch, videos, err := ParseFeed([]byte(feedFixture))
	if err != nil {
		t.Fatalf("ParseFeed 失败: %v", err)
	}
	if ch.ChannelID != "UCq-Fj5jknLsUf-MWSy4_brA" {
		t.Fatalf("频道 id 解析错了: %q", ch.ChannelID)
	}
	if ch.Title != "T-Series" {
		t.Fatalf("频道名解析错了: %q", ch.Title)
	}
	if len(videos) != 2 {
		t.Fatalf("应该解析出 2 条视频，实际 %d", len(videos))
	}
	v := videos[0]
	if v.VideoID != "s8OtOH_Vfa0" || v.Views != 42614 {
		t.Fatalf("第一条解析错了: %+v", v)
	}
	if v.DateSource != "rss" {
		t.Fatalf("date_source 应该是 rss，实际 %q", v.DateSource)
	}
	want := time.Date(2026, 10, 27, 14, 58, 48, 0, time.UTC)
	if !v.PublishedAt.Equal(want) {
		t.Fatalf("发布时间错了: %v != %v", v.PublishedAt, want)
	}
	if v.Description != "Presenting the Full Video Song & more" {
		t.Fatalf("简介没反转义: %q", v.Description)
	}
	// 第二条没有 media:thumbnail，应该退回按 id 拼的地址。
	if videos[1].ThumbnailURL != "https://i.ytimg.com/vi/uJKyS1w0swo/hqdefault.jpg" {
		t.Fatalf("兜底封面地址错了: %q", videos[1].ThumbnailURL)
	}
	// RSS 不提供时长，保持 0（界面上不显示时长角标）。
	if videos[0].DurationSec != 0 {
		t.Fatalf("RSS 不该有时长，实际 %d", videos[0].DurationSec)
	}
}

func TestParseFeedGarbage(t *testing.T) {
	if _, _, err := ParseFeed([]byte("<html>not a feed</html>")); err == nil {
		t.Fatal("非 RSS 内容应该报 stale_parser")
	}
	if _, _, err := ParseFeed([]byte("")); err == nil {
		t.Fatal("空内容应该报错")
	}
}

func TestParseChannelRef(t *testing.T) {
	cases := []struct {
		in      string
		kind    RefKind
		channel string
		handle  string
		videoID string
		wantErr bool
	}{
		{in: "UCq-Fj5jknLsUf-MWSy4_brA", kind: RefChannelID, channel: "UCq-Fj5jknLsUf-MWSy4_brA"},
		{in: "https://www.youtube.com/channel/UCq-Fj5jknLsUf-MWSy4_brA", kind: RefChannelID, channel: "UCq-Fj5jknLsUf-MWSy4_brA"},
		{in: "https://www.youtube.com/@Tubasa___Mai", kind: RefHandle, handle: "tubasa___mai"},
		{in: "@Tubasa___Mai", kind: RefHandle, handle: "tubasa___mai"},
		{in: "tubasa___mai", kind: RefHandle, handle: "tubasa___mai"},
		{in: "https://www.youtube.com/@tseries/videos", kind: RefHandle, handle: "tseries"},
		{in: "https://www.youtube.com/c/SomeName", kind: RefLegacy, handle: "somename"},
		{in: "https://www.youtube.com/user/SomeName", kind: RefLegacy, handle: "somename"},
		{in: "https://www.youtube.com/watch?v=s8OtOH_Vfa0&t=30s", kind: RefVideo, videoID: "s8OtOH_Vfa0"},
		{in: "https://youtu.be/s8OtOH_Vfa0", kind: RefVideo, videoID: "s8OtOH_Vfa0"},
		{in: "https://example.com/@foo", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, c := range cases {
		ref, err := ParseChannelRef(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q 应该报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q 解析失败: %v", c.in, err)
			continue
		}
		if ref.Kind != c.kind {
			t.Errorf("%q 类型错了: %v != %v", c.in, ref.Kind, c.kind)
		}
		if c.channel != "" && ref.ChannelID != c.channel {
			t.Errorf("%q 频道 id 错了: %q", c.in, ref.ChannelID)
		}
		if c.handle != "" && ref.Handle != c.handle {
			t.Errorf("%q handle 错了: %q", c.in, ref.Handle)
		}
		if c.videoID != "" && ref.VideoID != c.videoID {
			t.Errorf("%q videoId 错了: %q", c.in, ref.VideoID)
		}
	}
}

func TestParseVersionDate(t *testing.T) {
	got, ok := parseVersionDate("2026.10.26")
	if !ok {
		t.Fatal("版本号应该能解析出日期")
	}
	if got.Format("2006-01-02") != "2026-10-26" {
		t.Fatalf("日期错了: %v", got)
	}
	if _, ok := parseVersionDate("dev"); ok {
		t.Fatal("非日期版本号应该解析失败")
	}
}

func TestScanProgress(t *testing.T) {
	in := strings.Join([]string{
		"[youtube] Extracting URL: ...",
		"YTDLP 1048576 4194304 NA 524288.0 6",
		"YTDLP 4194304 4194304 NA 600000.0 0",
		"garbage line",
	}, "\n")
	var got []Progress
	scanProgress(strings.NewReader(in), func(p Progress) { got = append(got, p) })
	if len(got) != 2 {
		t.Fatalf("应该解析出 2 行进度，实际 %d", len(got))
	}
	if got[0].Percent < 24 || got[0].Percent > 26 {
		t.Fatalf("第一行百分比错了: %v", got[0].Percent)
	}
	if got[0].Speed != 524288 {
		t.Fatalf("速度解析错了: %v", got[0].Speed)
	}
	if got[1].Percent != 100 {
		t.Fatalf("第二行应该是 100%%: %v", got[1].Percent)
	}
}

func TestThumbURL(t *testing.T) {
	if got := ThumbURL("abc", ""); got != "https://i.ytimg.com/vi/abc/mqdefault.jpg" {
		t.Fatalf("默认封面地址错了: %s", got)
	}
	if got := ThumbURL("abc", "maxresdefault"); !strings.HasSuffix(got, "/maxresdefault.jpg") {
		t.Fatalf("指定封面名错了: %s", got)
	}
}
