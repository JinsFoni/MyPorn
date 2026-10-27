package hls

import (
	"strconv"
	"strings"
)

// Rendition 是 master playlist 里的一条 `#EXT-X-MEDIA`（独立的音轨/字幕轨）。
//
// 为什么需要它：YouTube 的清单把音轨拆成单独的 rendition（GROUP-ID 233 = Opus、
// 234 = AAC），变体只写 AUDIO="234" 指过去。改写清单时如果只搬 #EXT-X-STREAM-INF、
// 把 #EXT-X-MEDIA 丢掉，播放器拿到的就是一条没有声音的视频——而且不会报错，
// 只是静音，最难查的那种。
type Rendition struct {
	Type    string // AUDIO / SUBTITLES / CLOSED-CAPTIONS
	GroupID string
	Name    string
	URI     string // 已按 base 补全成绝对地址；CLOSED-CAPTIONS 是内嵌的，没有 URI
	Default bool
}

// StreamVariant 是 `#EXT-X-STREAM-INF` 加紧随其后的那把 URI。
//
// 和 model.Variant 的差别：这里保留 CODECS 原文与 AUDIO 组名——重建清单时
// 两样都得原样抄回去，否则解码器分不清「视频 H.264 + 音频 AAC」这回事。
type StreamVariant struct {
	URL        string
	Bandwidth  int
	Width      int
	Height     int
	Codecs     string
	AudioGroup string
}

// ParseMaster 把 master playlist 拆成「变体 + rendition」两半（相对地址按 base 补全）。
//
// ParseMasterPlaylist 是它的一半：只看变体、不管音轨，够四个成人站用（那边的清单是
// 单文件带音轨的）。YouTube 走 HLS 时必须连音轨一起搬，所以另开这一个。
func ParseMaster(content, baseURL string) (variants []StreamVariant, renditions []Rendition) {
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			attrs := ParseAttrList(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			uri := ""
			if raw := attrs["URI"]; raw != "" {
				uri = ResolveURL(baseURL, raw)
			}
			renditions = append(renditions, Rendition{
				Type:    attrs["TYPE"],
				GroupID: attrs["GROUP-ID"],
				Name:    attrs["NAME"],
				URI:     uri,
				Default: attrs["DEFAULT"] == "YES",
			})
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			attrs := ParseAttrList(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			var v StreamVariant
			v.Bandwidth, _ = strconv.Atoi(attrs["BANDWIDTH"])
			if res, ok := attrs["RESOLUTION"]; ok {
				if w, h, ok2 := strings.Cut(res, "x"); ok2 {
					v.Width, _ = strconv.Atoi(w)
					v.Height, _ = strconv.Atoi(h)
				}
			}
			v.Codecs = attrs["CODECS"]
			v.AudioGroup = attrs["AUDIO"]
			// 紧随其后的第一个非注释行是这把变体的 URI
			for j := i + 1; j < len(lines); j++ {
				uri := strings.TrimSpace(lines[j])
				if uri == "" || strings.HasPrefix(uri, "#") {
					continue
				}
				v.URL = ResolveURL(baseURL, uri)
				i = j
				break
			}
			if v.URL != "" {
				variants = append(variants, v)
			}
		}
	}
	return variants, renditions
}

// PickVariant 在变体里挑一条：高度不超过 maxHeight（0 = 不限）的最高画质。
//
// 同高度取带宽高的——YouTube 同一分辨率会同时给 H.264 和 VP9 两把，
// 码率高的那把清晰度更好；真要优先某个编码，调用方自己再排。
func PickVariant(variants []StreamVariant, maxHeight int) (StreamVariant, bool) {
	best := StreamVariant{}
	found := false
	for _, v := range variants {
		if maxHeight > 0 && v.Height > maxHeight {
			continue
		}
		if !found || v.Height > best.Height ||
			(v.Height == best.Height && v.Bandwidth > best.Bandwidth) {
			best, found = v, true
		}
	}
	return best, found
}

// AudioFor 找出某把变体指向的音轨（GROUP-ID 相同、TYPE=AUDIO），优先 DEFAULT=YES。
func AudioFor(renditions []Rendition, group string) (Rendition, bool) {
	var first Rendition
	found := false
	for _, r := range renditions {
		if !strings.EqualFold(r.Type, "AUDIO") || r.GroupID != group || r.URI == "" {
			continue
		}
		if r.Default {
			return r, true
		}
		if !found {
			first, found = r, true
		}
	}
	return first, found
}
