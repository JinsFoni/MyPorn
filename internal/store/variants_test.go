package store

import (
	"testing"
	"time"

	"github.com/mario/myporn/internal/model"
)

// 同一分辨率、不同编码（H.264 与 AV1 各一条）必须能同时存下来：
// 表上的唯一键是 (video_id, url)，不是 (video_id, height)，将来平台给 AV1 时不用改表。
func TestVariantsSameHeightDifferentCodec(t *testing.T) {
	st := newTestStore(t)
	v, _, err := st.UpsertVideo(&model.Video{
		Platform:        "pornhub",
		PlatformVideoID: "codec1",
		Title:           "双编码",
		URL:             "https://example.invalid/view_video.php?viewkey=codec1",
	})
	if err != nil {
		t.Fatal(err)
	}
	vs := []model.Variant{
		{Height: 1080, Width: 1920, Bandwidth: 4500000, Codec: "avc1.64001f", URL: "https://cdn.invalid/h264-1080.mp4"},
		{Height: 1080, Width: 1920, Bandwidth: 2200000, Codec: "av01.0.08M.08", URL: "https://cdn.invalid/av1-1080.mp4"},
	}
	if err := st.SaveVariants(v, vs); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.GetVariants(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("同分辨率两条编码都应该保留，实际 %d 条", len(got))
	}
	// 库里的顺序按 (height, bandwidth)，编码偏好由调用方用 model.SortVariantsDesc 修正
	model.SortVariantsDesc(got)
	if got[0].Codec != "av01.0.08M.08" {
		t.Fatalf("排序后 AV1 应排第一，实际第一是 %q", got[0].Codec)
	}
	if got[0].ID() == got[1].ID() {
		t.Fatalf("两条变体的 id 撞了：%q", got[0].ID())
	}
}

// 重新解析后旧行要被整批替换掉，不能留上次解析的残留（换编码时尤其容易漏）。
func TestSaveVariantsReplacesOldRows(t *testing.T) {
	st := newTestStore(t)
	v, _, err := st.UpsertVideo(&model.Video{
		Platform:        "pornhub",
		PlatformVideoID: "codec2",
		Title:           "换编码",
		URL:             "https://example.invalid/view_video.php?viewkey=codec2",
	})
	if err != nil {
		t.Fatal(err)
	}
	old := []model.Variant{
		{Height: 1080, Bandwidth: 4500000, Codec: "avc1.64001f", URL: "https://cdn.invalid/old-h264.mp4"},
	}
	if err := st.SaveVariants(v, old); err != nil {
		t.Fatal(err)
	}
	fresh := []model.Variant{
		{Height: 1080, Bandwidth: 2200000, Codec: "av01.0.08M.08", URL: "https://cdn.invalid/new-av1.mp4"},
	}
	if err := st.SaveVariants(v, fresh); err != nil {
		t.Fatal(err)
	}
	got, fetchedAt, err := st.GetVariants(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Codec != "av01.0.08M.08" {
		t.Fatalf("旧解析结果应被替换，实际 %+v", got)
	}
	if time.Since(fetchedAt) > time.Minute {
		t.Fatalf("fetched_at 没有跟着更新: %v", fetchedAt)
	}
}
