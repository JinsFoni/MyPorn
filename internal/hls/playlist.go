// Package hls 解析 HLS 清单：master 的变体列表与 media 的分段列表。
// 只做解析与 URL 解析，不做下载（下载在 internal/download）。
package hls

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Segment 是 media playlist 里的一个分片。
type Segment struct {
	URI      string  // 绝对地址
	Duration float64 // 秒
	Seq      int     // 媒体序号
}

// MediaPlaylist 是解析后的 media playlist。
type MediaPlaylist struct {
	TargetDuration float64
	MediaSequence  int
	Segments       []Segment
	InitURI        string // EXT-X-MAP 的初始化分段（fMP4 必需）
	Encrypted      bool   // 出现 EXT-X-KEY 且 METHOD != NONE
	ByteRange      bool   // 出现 EXT-X-BYTERANGE，需按 Range 取，交给 ffmpeg 兜底
	EndList        bool
	TotalDuration  float64
}

// IsMaster 判断清单是 master 还是 media。
func IsMaster(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#EXT-X-STREAM-INF:") {
			return true
		}
	}
	return false
}

// ParseMediaPlaylist 解析 media playlist。baseURL 用于把相对地址补全为绝对地址。
func ParseMediaPlaylist(content, baseURL string) (*MediaPlaylist, error) {
	pl := &MediaPlaylist{MediaSequence: 0}
	lines := strings.Split(content, "\n")
	var pendingDur float64
	seq := 0

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			pl.TargetDuration, _ = strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64)
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			pl.MediaSequence, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			seq = pl.MediaSequence
		case strings.HasPrefix(line, "#EXT-X-ENDLIST"):
			pl.EndList = true
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			attrs := ParseAttrList(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			method := strings.ToUpper(attrs["METHOD"])
			if method != "" && method != "NONE" {
				pl.Encrypted = true
			}
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			pl.ByteRange = true
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs := ParseAttrList(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if u := attrs["URI"]; u != "" {
				pl.InitURI = ResolveURL(baseURL, u)
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			v := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(v, ','); i >= 0 {
				v = v[:i]
			}
			pendingDur, _ = strconv.ParseFloat(strings.TrimSpace(v), 64)
		case strings.HasPrefix(line, "#"):
			// 其它标签忽略
		default:
			abs := ResolveURL(baseURL, line)
			if abs == "" {
				continue
			}
			pl.Segments = append(pl.Segments, Segment{
				URI:      abs,
				Duration: pendingDur,
				Seq:      seq,
			})
			pl.TotalDuration += pendingDur
			pendingDur = 0
			seq++
		}
	}
	if len(pl.Segments) == 0 {
		return nil, fmt.Errorf("清单里没有可用分段")
	}
	return pl, nil
}

// ParseAttrList 解析 HLS 属性列表（KEY=VALUE,KEY="VALUE"）。
func ParseAttrList(s string) map[string]string {
	out := map[string]string{}
	var key, val strings.Builder
	inVal, inQuote := false, false
	flush := func() {
		k := strings.TrimSpace(key.String())
		if k != "" {
			out[strings.ToUpper(k)] = strings.TrimSpace(val.String())
		}
		key.Reset()
		val.Reset()
		inVal = false
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == '=' && !inVal && !inQuote:
			inVal = true
		case r == ',' && !inQuote:
			flush()
		default:
			if inVal {
				val.WriteRune(r)
			} else {
				key.WriteRune(r)
			}
		}
	}
	flush()
	return out
}

// ResolveURL 以 base 为基准解析 ref（遵循 RFC 3986），并补一条现实中的例外：
// 很多 CDN 把签名放在查询串里（?h=...&e=...），而子清单/分片是相对路径且自己不带签名。
// RFC 规定相对引用不继承查询串，但那些站点期望签名被带过去，否则直接 410。
// 因此：ref 没带查询串时，沿用 base 的查询串。
func ResolveURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	out := b.ResolveReference(r)
	if out.RawQuery == "" && r.RawQuery == "" && b.RawQuery != "" && !strings.Contains(ref, "?") {
		out.RawQuery = b.RawQuery
	}
	return out.String()
}

// BestChildURL 从 master 清单里挑出质量最高的子清单地址（先比高度，再比带宽）。
func BestChildURL(content, baseURL string) string {
	lines := strings.Split(content, "\n")
	type cand struct {
		height, bandwidth int
		url               string
	}
	var best cand
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			continue
		}
		attrs := ParseAttrList(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
		var c cand
		c.bandwidth, _ = strconv.Atoi(attrs["BANDWIDTH"])
		if res := attrs["RESOLUTION"]; res != "" {
			if _, h, ok := strings.Cut(res, "x"); ok {
				c.height, _ = strconv.Atoi(h)
			}
		}
		for j := i + 1; j < len(lines); j++ {
			uri := strings.TrimSpace(lines[j])
			if uri == "" || strings.HasPrefix(uri, "#") {
				continue
			}
			c.url = ResolveURL(baseURL, uri)
			i = j
			break
		}
		if c.url == "" {
			continue
		}
		if c.height > best.height || (c.height == best.height && c.bandwidth > best.bandwidth) {
			best = c
		}
	}
	return best.url
}
