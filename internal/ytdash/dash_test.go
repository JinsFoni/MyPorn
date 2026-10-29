package ytdash

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
)

// 解析器的测试：用手工拼出来的 mp4 头部，把 sidx 的两种版本、64 位 box、
// 层级引用（reference_type=1）都过一遍。
//
// 为什么值得单独测：sidx 的字段顺序（reference_ID / timescale / earliest_presentation_time /
// first_offset / reserved / reference_count / 每条 12 字节）很容易记错一位，错了不会报错，
// 只会表现为「播放器拿到一堆对不上的字节范围」——最后看到的是画面花屏或者干脆黑屏。

func boxOf(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// sidxPayload 拼一份 sidx 的负载（version 0）。
func sidxPayload(timescale uint32, durations []uint32, sizes []uint32, hierarchical bool) []byte {
	p := make([]byte, 0, 32+12*len(durations))
	p = append(p, 0)                                         // version
	p = append(p, 0, 0, 0)                                   // flags
	p = binary.BigEndian.AppendUint32(p, 1)                  // reference_ID
	p = binary.BigEndian.AppendUint32(p, timescale)          // timescale
	p = binary.BigEndian.AppendUint32(p, 0)                  // earliest_presentation_time
	p = binary.BigEndian.AppendUint32(p, 0)                  // first_offset
	p = binary.BigEndian.AppendUint16(p, 0)                  // reserved
	p = binary.BigEndian.AppendUint16(p, uint16(len(sizes))) // reference_count
	for i := range sizes {
		s := sizes[i]
		if hierarchical && i == 0 {
			s |= 0x80000000
		}
		p = binary.BigEndian.AppendUint32(p, s)
		p = binary.BigEndian.AppendUint32(p, durations[i])
		p = binary.BigEndian.AppendUint32(p, 0x90000000) // starts_with_SAP=1
	}
	return p
}

func TestParseSidx(t *testing.T) {
	var head []byte
	head = append(head, boxOf("ftyp", []byte("isom0000"))...)
	head = append(head, boxOf("moov", make([]byte, 40))...)
	initEnd := int64(len(head))
	head = append(head, boxOf("sidx", sidxPayload(90000,
		[]uint32{450000, 450000, 300000},
		[]uint32{1000, 2000, 500}, false))...)
	// 后面再跟一段分片，确保解析器不会把 moof 当成 sidx
	head = append(head, boxOf("moof", make([]byte, 20))...)
	head = append(head, boxOf("mdat", make([]byte, 1000))...)

	tr, err := Parse(head)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if tr.InitEnd != initEnd {
		t.Fatalf("init 段结尾 = %d，想要 %d", tr.InitEnd, initEnd)
	}
	if len(tr.Segments) != 3 {
		t.Fatalf("分段数 = %d，想要 3", len(tr.Segments))
	}
	// 第一段紧跟 sidx
	first := int64(len(head)) - (8 + 20) - (8 + 1000) // moof 的起点
	if tr.Segments[0].Offset != first || tr.Segments[0].Size != 1000 {
		t.Fatalf("第一段 = %+v，想要 offset=%d size=1000", tr.Segments[0], first)
	}
	if tr.Segments[1].Offset != first+1000 {
		t.Fatalf("第二段起点 = %d，想要 %d（偏移要按长度累加）", tr.Segments[1].Offset, first+1000)
	}
	if d := tr.Segments[0].Duration; d != 5 || tr.Segments[2].Duration != 300000.0/90000 {
		t.Fatalf("时长换算不对: %v / %v", d, tr.Segments[2].Duration)
	}
	// 总时长按毫秒取整（13.333），免得 321.19999999999993 这种值漏到前端
	if want := math.Round(1200000.0/90000*1000) / 1000; tr.Total != want {
		t.Fatalf("总时长 = %v，想要 %v", tr.Total, want)
	}
	if got := tr.TargetDuration(); got != 5 {
		t.Fatalf("TARGETDURATION = %d，想要 5", got)
	}
}

func TestParseRejectsHierarchicalSidx(t *testing.T) {
	var head []byte
	head = append(head, boxOf("ftyp", []byte("isom0000"))...)
	head = append(head, boxOf("moov", make([]byte, 16))...)
	head = append(head, boxOf("sidx", sidxPayload(1000, []uint32{1000}, []uint32{50}, true))...)
	if _, err := Parse(head); err == nil {
		t.Fatal("reference_type=1 应当报错（我们没实现层级索引）")
	}
}

func TestParseWithoutMoovOrSidx(t *testing.T) {
	if _, err := Parse(boxOf("ftyp", []byte("isom0000"))); err == nil {
		t.Fatal("没有 sidx 应当报错")
	}
	var head []byte
	head = append(head, boxOf("ftyp", []byte("isom0000"))...)
	head = append(head, boxOf("sidx", sidxPayload(1000, []uint32{1000}, []uint32{50}, false))...)
	if _, err := Parse(head); err == nil {
		t.Fatal("没有 moov 应当报错（不知道 init 段到哪结束）")
	}
}

// 采样上真实字节（2026-10-29 抓的 YFsMnJEbV1U 头部 8 KiB）：2160p AV1 的
// ftyp+moov+sidx 合计 1417 字节、57 段、总时长 321.2 秒；音轨 moov+sidx 到 1151 字节、33 段。
func TestPlaylistUsesByteRanges(t *testing.T) {
	tr := Track{
		InitEnd:   1417,
		Timescale: 90000,
		Segments: []Segment{
			{Offset: 1417, Size: 8835988, Duration: 5.272},
			{Offset: 8837405, Size: 9100000, Duration: 5.4},
		},
		Total: 10.672,
	}
	got := tr.Playlist("init.mp4", "seg.mp4")
	for _, want := range []string{
		"#EXT-X-VERSION:7",
		"#EXT-X-PLAYLIST-TYPE:VOD",
		"#EXT-X-TARGETDURATION:6",
		`#EXT-X-MAP:URI="init.mp4"`,
		"#EXT-X-BYTERANGE:8835988@1417\nseg.mp4?i=0",
		"#EXT-X-BYTERANGE:9100000@8837405\nseg.mp4?i=1",
		"#EXT-X-ENDLIST",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("播放列表里没有 %q:\n%s", want, got)
		}
	}
}

func TestMasterAdvertisesBothCodecs(t *testing.T) {
	got := Master(MasterInfo{
		VideoBandwidth: 13461975,
		AudioBandwidth: 129501,
		Codecs:         "av01.0.13M.08,mp4a.40.2",
		Height:         2160,
		Width:          3840,
		FrameRate:      60,
		AudioURI:       "a.m3u8",
		VideoURI:       "v.m3u8",
	})
	for _, want := range []string{
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud"`,
		`CODECS="av01.0.13M.08,mp4a.40.2"`,
		"RESOLUTION=3840x2160",
		"FRAME-RATE=60.000",
		"AUDIO=\"aud\"",
		"v.m3u8",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("master 里没有 %q:\n%s", want, got)
		}
	}
}

func TestSafeSidxReadSize(t *testing.T) {
	// 一段 540 MB 的文件，头部只读 256 KiB；这里只是把「读多少」的口径钉住，
	// 免得以后有人改成「先读 64 字节看看」——sidx 只有几百字节，但 moov 会有几 KB。
	if HeadProbeBytes < 4096 {
		t.Fatalf("探头的头 4 KiB 太小，moov 可能读不全: %d", HeadProbeBytes)
	}
	_ = fmt.Sprint()
}
