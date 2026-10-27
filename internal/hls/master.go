package hls

import (
	"strconv"
	"strings"

	"github.com/mario/myporn/internal/model"
)

// ParseMasterPlaylist 解析 master playlist 的变体列表（相对 URI 会按 base 补全）。
// 四个平台的清单长得一样，所以放在这里共用：谁也不用再抄一遍 BANDWIDTH/RESOLUTION 的规矩。
func ParseMasterPlaylist(content, baseURL string) []model.Variant {
	lines := strings.Split(content, "\n")
	var out []model.Variant
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			continue
		}
		attrs := ParseAttrList(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
		var v model.Variant
		if bw, ok := attrs["BANDWIDTH"]; ok {
			v.Bandwidth, _ = strconv.Atoi(bw)
		}
		if res, ok := attrs["RESOLUTION"]; ok {
			if w, h, ok2 := strings.Cut(res, "x"); ok2 {
				v.Width, _ = strconv.Atoi(w)
				v.Height, _ = strconv.Atoi(h)
			}
		}
		v.Codec = PickVideoCodec(attrs["CODECS"])
		// 下一非注释行是 URI
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
			out = append(out, v)
		}
	}
	model.SortVariantsDesc(out)
	return out
}

// PickVideoCodec 从 CODECS 属性里挑出视频编码。
// 各站写 CODECS 的顺序不一样（xHamster 是 "mp4a.40.2,avc1.4d4015"，音频在前），
// 直接整串喂给 model.CodecFamily 会认不出来、于是被当成「未知编码」排到最后，
// AV1 优先就成了摆设。所以这里按下标逐个认，返回第一个认得的视频编码。
func PickVideoCodec(codecs string) string {
	codecs = strings.TrimSpace(codecs)
	if codecs == "" {
		return ""
	}
	if !strings.Contains(codecs, ",") {
		return codecs
	}
	for _, part := range strings.Split(codecs, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !isAudioCodec(part) && model.CodecFamily(part) != "" {
			return part
		}
	}
	return codecs
}

// isAudioCodec 判断单个编码串是不是音频（mp4a / ac-3 / ec-3 / opus / flac）。
func isAudioCodec(codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	for _, p := range []string{"mp4a", "ac-3", "ac3", "ec-3", "ec3", "opus", "flac", "dts"} {
		if strings.HasPrefix(c, p) {
			return true
		}
	}
	return false
}
