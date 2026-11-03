package store

import (
	"testing"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// xHamster 会把同一部片发成「片花 + 完整片」两份，标题一字不差、id 不同
// （2026-11-04 实测 hot-pearl：372 / 492 / 582 / 612 秒这几档反复出现，正片 663–1849 秒）。
// HasLongerSameTitle 就是入库前的闸门：库里已有更长的那份就别再收片花。
func TestHasLongerSameTitle(t *testing.T) {
	st := newTestStore(t)
	full := &model.Video{
		Platform: "xhamster", PlatformVideoID: "xhFULL",
		Title: "Jadilica and Hot Pearl. The Most Romantic Lesbian Sex. 4K",
		URL:   "https://xhamster.com/videos/a-xhFULL", DurationSec: 918,
	}
	if _, _, err := st.UpsertVideo(full); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		platform string
		title    string
		dur      int
		want     bool
	}{
		{"同名片花（372s 那档）", "xhamster", "jadilica and hot pearl. the most romantic lesbian sex. 4k", 372, true},
		{"同名的完整片本身", "xhamster", full.Title, 918, false},
		{"更长的版本", "xhamster", full.Title, 1024, false},
		{"别的平台同名", "pornhub", full.Title, 372, false},
		{"空标题", "xhamster", "  ", 100, false},
		{"时长为 0", "xhamster", full.Title, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := st.HasLongerSameTitle(c.platform, c.title, c.dur)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("HasLongerSameTitle(%q, %q, %d) = %v，期望 %v", c.platform, c.title, c.dur, got, c.want)
			}
		})
	}
}
