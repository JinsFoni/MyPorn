// Package ytdash 把 YouTube 的 DASH 分片 mp4 直接当成「单文件 HLS」放，
// 不下载整条流、不 remux、不落盘。
//
// 为什么值得这么绕：YouTube 的 HLS 清单里 2160p 只有 VP9 28.2 Mbps（实测单连接 3.5~8.4 MB/s，
// 缓冲攒不起来，40 秒卡 13 次），而 DASH 里的 AV1 2160p 只要 13.46 Mbps——但 AV1 是「只有画面」
// 的一条独立 mp4 直链，还配一条 m4a 音轨，浏览器一个 <video> 塞不进两条流。
//
// 之前的两条路都不好：
//   - 交给 hls.js 的 HLS 代理：只有 VP9 那档，码率翻倍，4K 还是卡；
//   - 服务端 ffmpeg 把 540 MB 整条下完再 `-c copy`（实测 4 分 0.5 秒才起播）：能播，但
//     「点开一个 4K 要等四分钟」这种体验本身就说明方案错了。
//
// 这里走第三条：YouTube 的 AV1 mp4 是**分片 mp4**（ftyp + moov + sidx + 一串 moof+mdat），
// sidx 里逐段写着每段的偏移/长度/时长。于是可以把每一段当成一个 HLS 分片：
// `#EXT-X-MAP` 指 init 段，`#EXT-X-BYTERANGE` 指 moof+mdat，播放器按需取、拖到哪取到哪，
// 和 YouTube 自己那边的行为一致——起播只要「拿到第一段」，不是「拿到整条」。
package ytdash

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Segment 是 sidx 里的一条引用：一段自洽的 moof+mdat，可以直接喂给 MSE。
type Segment struct {
	Offset   int64   // 在整份文件里的起始偏移
	Size     int64   // 字节数
	Duration float64 // 秒
}

// Track 是一条流的索引信息。
type Track struct {
	// InitEnd 是 ftyp+moov 的结束偏移：init 段 = 文件头的 [0, InitEnd)。
	InitEnd int64
	// Timescale 是 sidx 的时间基（每段时长换算成秒要用它）。
	Timescale uint32
	Segments  []Segment
	InitSize  int64 // = InitEnd，留着读起来顺
	Total     float64
}

// ErrNoSidx 表示文件头里没有 sidx（不是分片 mp4，或者布局变了）。
var ErrNoSidx = errors.New("文件头里没有 sidx")

// box 是顶层 box 的位置。
type box struct {
	typ  string
	off  int64
	size int64
}

// 只认这几个顶层 box：init 段要 ftyp+moov，分段要 sidx。
func walkBoxes(data []byte) ([]box, error) {
	var out []box
	for off := int64(0); off+8 <= int64(len(data)); {
		size := int64(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		header := int64(8)
		if size == 1 {
			if off+16 > int64(len(data)) {
				return out, ErrNoSidx // 头还没读全，交给调用方多读一点
			}
			size = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
			header = 16
		} else if size == 0 {
			size = int64(len(data)) - off // 一直延伸到文件尾
		}
		if size < header || off+size > int64(len(data)) {
			// 这个 box 超出我们手里的字节数：sidx 可能就卡在这，别当成解析失败
			return out, nil
		}
		out = append(out, box{typ: typ, off: off, size: size})
		off += size
	}
	return out, nil
}

// Parse 从文件头部的若干字节里解析 init 段与分段表。
//
// head 至少要覆盖到 sidx 结束（实测一条 2160p AV1 的 ftyp+moov+sidx 一共 1417 字节，
// 但不同码率/时长会变，调用方按 256 KiB 去要最省事，一次 Range 请求就够了）。
func Parse(head []byte) (Track, error) {
	var t Track
	boxes, err := walkBoxes(head)
	if err != nil {
		return t, err
	}
	var sidx *box
	hasMoov := false
	for i := range boxes {
		switch boxes[i].typ {
		case "ftyp", "moov":
			if boxes[i].typ == "moov" {
				hasMoov = true
			}
			if end := boxes[i].off + boxes[i].size; end > t.InitEnd {
				t.InitEnd = end
			}
		case "sidx":
			b := boxes[i]
			sidx = &b
		}
	}
	if sidx == nil {
		return t, ErrNoSidx
	}
	if !hasMoov || t.InitEnd == 0 {
		return t, fmt.Errorf("%w: 没有 moov，不知道 init 段到哪结束", ErrNoSidx)
	}
	body := head[sidx.off : sidx.off+sidx.size]
	if len(body) < 12 {
		return t, ErrNoSidx
	}
	version := body[8]
	pos := 12 // 8 字节头之后：version(1)+flags(3) 已看过 version，从 reference_ID 开始
	if len(body) < pos+8 {
		return t, ErrNoSidx
	}
	t.Timescale = binary.BigEndian.Uint32(body[pos+4 : pos+8]) // reference_ID 占前 4 字节
	pos += 8
	if version == 0 {
		pos += 8 // earliest_presentation_time + first_offset（各 4 字节）
	} else {
		pos += 16
	}
	if len(body) < pos+4 {
		return t, ErrNoSidx
	}
	pos += 2 // reserved
	count := int(binary.BigEndian.Uint16(body[pos : pos+2]))
	pos += 2
	if t.Timescale == 0 {
		return t, fmt.Errorf("sidx 的 timescale 是 0")
	}

	first := sidx.off + sidx.size
	cur := first
	for i := 0; i < count; i++ {
		if len(body) < pos+12 {
			return t, fmt.Errorf("sidx 只读到 %d/%d 条引用", i, count)
		}
		ref := binary.BigEndian.Uint32(body[pos : pos+4])
		dur := binary.BigEndian.Uint32(body[pos+4 : pos+8])
		pos += 12
		if ref&0x80000000 != 0 {
			// reference_type=1 表示「这条引用指向另一个 sidx 的子段」，YouTube 不用这种
			return t, fmt.Errorf("sidx 里有层级引用（reference_type=1），不支持")
		}
		size := int64(ref & 0x7FFFFFFF)
		if size <= 0 {
			return t, fmt.Errorf("sidx 第 %d 条引用的长度是 %d", i, size)
		}
		d := float64(dur) / float64(t.Timescale)
		t.Segments = append(t.Segments, Segment{Offset: cur, Size: size, Duration: d})
		t.Total += d
		cur += size
	}
	if len(t.Segments) == 0 {
		return t, ErrNoSidx
	}
	// 总时长按四舍五入到毫秒，避免 321.19999999999993 这种值漏到前端
	t.Total = math.Round(t.Total*1000) / 1000
	t.InitSize = t.InitEnd
	return t, nil
}

// TargetDuration 是 HLS 的 #EXT-X-TARGETDURATION（整秒、向上取整）。
func (t Track) TargetDuration() int {
	max := 1.0
	for _, s := range t.Segments {
		if s.Duration > max {
			max = s.Duration
		}
	}
	return int(math.Ceil(max))
}

// HeadProbeBytes 是「读文件头」时一次要的字节数。
//
// 实测（2026-10-29，YFsMnJEbV1U）：2160p AV1 的 ftyp+moov+sidx = 1417 字节，
// 音轨 m4a 的 = 1151 字节；不同码率/时长会让 moov 变大（每多一个 track 就多几百字节），
// 256 KiB 是一次 Range 请求就能拿到的量，大到足够容错，小到不浪费带宽。
const HeadProbeBytes = 256 << 10

// Playlist 生成单条流的 HLS 播放列表：init 走 #EXT-X-MAP，每个 moof+mdat 走 #EXT-X-BYTERANGE。
//
// segURI 上会拼 ?i=<序号>：hls.js 对「同一个 URI + 不同 BYTERANGE」也能处理，但每段一个
// 独立 URL 在日志和缓存里都好认（排障时一眼看出卡在第几段）。
func (t Track) Playlist(initURI, segURI string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", t.TargetDuration())
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", initURI)
	for i, s := range t.Segments {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", s.Duration)
		fmt.Fprintf(&b, "#EXT-X-BYTERANGE:%d@%d\n", s.Size, s.Offset)
		fmt.Fprintf(&b, "%s?i=%d\n", segURI, i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// MasterInfo 是一条 master 清单需要的全部信息。
type MasterInfo struct {
	VideoBandwidth int64
	AudioBandwidth int64
	Codecs         string
	Width          int
	Height         int
	FrameRate      float64
	AudioURI       string
	VideoURI       string
}

// Master 拼一份「一条变体 + 一条音轨」的 master。
//
// 只留一条变体：这里放的是用户选中的那一档，ABR 交给「换一条清单」来做（和站内其它
// 播放路径一致），免得 hls.js 在半路自己降到 720p——这条线上没有别的档可选。
func Master(i MasterInfo) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"Default\",DEFAULT=YES,AUTOSELECT=YES,URI=%q\n",
		i.AudioURI)
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q,RESOLUTION=%dx%d",
		i.VideoBandwidth+i.AudioBandwidth, i.Codecs, i.Width, i.Height)
	if i.FrameRate > 0 {
		fmt.Fprintf(&b, ",FRAME-RATE=%.3f", i.FrameRate)
	}
	b.WriteString(",AUDIO=\"aud\"\n")
	b.WriteString(i.VideoURI + "\n")
	return b.String()
}
