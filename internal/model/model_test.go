package model

import "testing"

// HLS 的 CODECS 属性形如 av01.0.08M.08 / avc1.64001f / hev1.2.4.L120，带不带参数都要能认出来。
func TestCodecFamily(t *testing.T) {
	cases := map[string]string{
		"av01.0.08M.08":    "av1",
		"AV01.0.05M.10":    "av1",
		"av1":              "av1",
		"hev1.2.4.L120.B0": "hevc",
		"hvc1.1.6.L93.B0":  "hevc",
		"vp09.00.10.08":    "vp9",
		"avc1.64001f":      "avc",
		"avc3.4d401f":      "avc",
		"avc1":             "avc",
		"":                 "",
		"mp4a.40.2":        "",
	}
	for in, want := range cases {
		if got := CodecFamily(in); got != want {
			t.Errorf("CodecFamily(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestCodecRankAndLabel(t *testing.T) {
	// AV1 < HEVC < VP9 < H.264/未知：数字越小越优先
	if CodecRank("av01.0.08M.08") >= CodecRank("hev1.1.6.L93.B0") {
		t.Error("AV1 应排在 HEVC 前")
	}
	if CodecRank("hev1.1.6.L93.B0") >= CodecRank("avc1.64001f") {
		t.Error("HEVC 应排在 H.264 前")
	}
	if CodecRank("") != CodecRank("avc1.64001f") {
		t.Error("编码未知时不该被排到 H.264 之后")
	}
	if got := CodecLabel("av01.0.08M.08"); got != "AV1" {
		t.Errorf("CodecLabel = %q", got)
	}
	if got := CodecLabel(""); got != "" {
		t.Errorf("未知编码的 label 应为空，得到 %q", got)
	}
}

// 「优先 AV1」只在同分辨率内生效：1080p 的 H.264 仍然排在 720p 的 AV1 前面。
func TestSortVariantsDesc(t *testing.T) {
	vs := []Variant{
		{Height: 720, Width: 1280, Bandwidth: 1500000, Codec: "av01.0.05M.08", URL: "a"},
		{Height: 1080, Width: 1920, Bandwidth: 4500000, Codec: "avc1.64001f", URL: "b"},
		{Height: 1080, Width: 1920, Bandwidth: 2200000, Codec: "av01.0.08M.08", URL: "c"},
		{Height: 1080, Width: 1920, Bandwidth: 3000000, Codec: "avc1.64001f", URL: "d"},
	}
	SortVariantsDesc(vs)
	// c(1080p AV1) 挤掉同分辨率的 H.264 排第一——这就是「优先 AV1」；
	// 剩下的 1080p H.264 按码率降序（b 4.5M > d 3M）；720p 的 AV1 最后（分辨率优先）。
	want := []string{"c", "b", "d", "a"}
	for i, w := range want {
		if vs[i].URL != w {
			t.Fatalf("第 %d 位应是 %s，实际顺序 %+v", i, w, urls(vs))
		}
	}
}

// TestDedupeVariants 验「ID 撞车的档位合成一条、留码率高的」：
// B 站会把同一分辨率的普通版和高码率版都返回，两条的 ID() 一模一样。
func TestDedupeVariants(t *testing.T) {
	vs := []Variant{
		{Height: 1080, Width: 1920, Bandwidth: 1198322, Codec: "av01.0.08M.08", URL: "av1-hi"},
		{Height: 1080, Width: 1920, Bandwidth: 702429, Codec: "av01.0.08M.08", URL: "av1-lo"},
		{Height: 1080, Width: 1920, Bandwidth: 2010247, Codec: "avc1.640033", URL: "avc"},
		{Height: 720, Width: 1280, Bandwidth: 675806, Codec: "avc", URL: "720"},
	}
	got := DedupeVariants(vs)
	want := []string{"av1-hi", "avc", "720"}
	if len(got) != len(want) {
		t.Fatalf("应合成 %d 条，得到 %d 条：%+v", len(want), len(got), urls(got))
	}
	for i, w := range want {
		if got[i].URL != w {
			t.Fatalf("第 %d 位应是 %s，实际 %+v", i, w, urls(got))
		}
	}
	// 顺序反过来也得留码率高的那条（调用方没排序时的兜底）
	rev := DedupeVariants([]Variant{vs[1], vs[0]})
	if len(rev) != 1 || rev[0].URL != "av1-hi" {
		t.Fatalf("乱序输入也应留下高码率那条，实际 %+v", urls(rev))
	}
}

func urls(vs []Variant) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.URL
	}
	return out
}

// 同分辨率、同编码时按码率降序；编码未知按 H.264 那一档处理。
func TestSortVariantsDescTieBreaks(t *testing.T) {
	vs := []Variant{
		{Height: 1080, Bandwidth: 2000000, Codec: "", URL: "unknown"},
		{Height: 1080, Bandwidth: 3800000, Codec: "avc1.64001f", URL: "higher"},
		{Height: 1080, Bandwidth: 3000000, Codec: "avc1.64001f", URL: "lower"},
	}
	SortVariantsDesc(vs)
	if vs[0].URL != "higher" || vs[1].URL != "lower" || vs[2].URL != "unknown" {
		t.Fatalf("同编码应按码率降序，未知编码按 H.264 档参与比较: %v", urls(vs))
	}
}

// id 要能区分同一分辨率的两种编码，否则前端下拉框两条会撞成同一个值。
func TestVariantIDIncludesCodec(t *testing.T) {
	h264 := Variant{Height: 1080, Codec: "avc1.64001f"}
	av1 := Variant{Height: 1080, Codec: "av01.0.08M.08"}
	hevc := Variant{Height: 1080, Codec: "hev1.1.6.L93.B0"}
	unknown := Variant{Height: 1080}

	if got := h264.ID(); got != "1080p" {
		t.Errorf("H.264 的 id 保持原样，得到 %q", got)
	}
	if got := unknown.ID(); got != "1080p" {
		t.Errorf("编码未知的 id 保持原样，得到 %q", got)
	}
	if av1.ID() != "1080p.av1" {
		t.Errorf("AV1 的 id 应为 1080p.av1，得到 %q", av1.ID())
	}
	if hevc.ID() != "1080p.hevc" {
		t.Errorf("HEVC 的 id 应为 1080p.hevc，得到 %q", hevc.ID())
	}
	if h264.ID() == av1.ID() || av1.ID() == hevc.ID() {
		t.Error("同分辨率不同编码的 id 不能相同")
	}
	// 4K 那档也跟着走
	if got := (Variant{Height: 2160, Codec: "av01.0.12M.10"}).ID(); got != "2160p.av1" {
		t.Errorf("4K AV1 的 id 应为 2160p.av1，得到 %q", got)
	}
	if got := (Variant{Height: 2160, Codec: "avc1.640033"}).Quality(); got != "2160p" {
		t.Errorf("Quality 只看分辨率，得到 %q", got)
	}
}
