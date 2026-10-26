// Package model 定义跨层共享的领域类型（adapter / store / httpapi 共用）。
package model

import (
	"sort"
	"strings"
	"time"
)

type Performer struct {
	ID              int64
	Platform        string
	PerformerID     string
	Name            string
	URL             string
	AvatarURL       string
	Bio             string
	VideoCount      *int
	SubscriberCount *int
	LastFetchedAt   *time.Time
	Subscription    *Subscription
}

type Subscription struct {
	ID                 int64
	PerformerID        int64
	Enabled            bool
	BackfillDone       bool
	BackfillLimit      int
	BackfillPage       int
	BackfillTotalPages *int
	LastSyncAt         *time.Time
	NextSyncAt         *time.Time
	LastError          string
	CreatedAt          time.Time
}

type Video struct {
	ID              int64
	Platform        string
	PlatformVideoID string
	Title           string
	URL             string
	ThumbURL        string
	PreviewURL      string
	DurationSec     int
	PublishedAt     *time.Time
	ViewCount       *int
	Rating          *float64
	Tags            []string
	Description     string
	IsNew           bool
	Performers      []PerformerRef
	// LocalLibraryIDs 记录该影片已落盘到哪些本地库（M2）。
	LocalLibraryIDs []int64
}

type PerformerRef struct {
	ID   int64
	Name string
	URL  string
}

// Variant 是 HLS 的一个清晰度变体。
type Variant struct {
	Height    int
	Width     int
	Bandwidth int
	Codec     string
	URL       string
	// ApproxBandwidth 为 true 表示码率是从文件名猜的（如 "1080P_4000K"），
	// 并非清单声明的真实值，体积预估要按经验系数折算。
	ApproxBandwidth bool `json:",omitempty"`
}

// EstimateBytes 用码率与时长估算成品体积。
// 猜出来的码率是那档的上限，实测平均只有它的 45% 左右，故折算一下。
func (v Variant) EstimateBytes(durationSec int) int64 {
	if v.Bandwidth <= 0 || durationSec <= 0 {
		return 0
	}
	bw := float64(v.Bandwidth)
	if v.ApproxBandwidth {
		bw *= 0.45
	}
	return int64(bw/8) * int64(durationSec)
}

// Quality 只按分辨率给一个代号（1080p / 4K …），不看编码。
func (v Variant) Quality() string {
	switch {
	case v.Height >= 2160:
		return "2160p"
	case v.Height >= 1440:
		return "1440p"
	case v.Height >= 1080:
		return "1080p"
	case v.Height >= 720:
		return "720p"
	case v.Height >= 480:
		return "480p"
	case v.Height >= 360:
		return "360p"
	case v.Height > 0:
		return "sd"
	default:
		return "auto"
	}
}

// ID 是前端选档用的稳定标识。
// 同一个分辨率可能同时有 H.264 和 AV1 两版（两家 CDN 各给一条），
// 所以非 H.264 的编码要在后面缀上编码族，否则两条会撞成同一个 id、前端下拉框就没法区分。
// H.264 保持原样（1080p），老会话里记住的档位不受影响。
func (v Variant) ID() string {
	q := v.Quality()
	if fam := CodecFamily(v.Codec); fam != "" && fam != "avc" {
		return q + "." + fam
	}
	return q
}

// CodecFamily 把 HLS 的 CODECS 字符串归一化成编码族：
// av01.0.08M.08 → av1，hev1.2.4.L120 / hvc1… → hevc，vp09… → vp9，avc1.64001f / avc3… → avc。
// 认不出来就返回空串（当成「未知」，按 H.264 那个档对待，不因缺信息把可用片源排到最后）。
func CodecFamily(codec string) string {
	c := strings.ToLower(strings.TrimSpace(codec))
	switch {
	case c == "":
		return ""
	case strings.HasPrefix(c, "av01"), strings.HasPrefix(c, "av1"):
		return "av1"
	case strings.HasPrefix(c, "hev1"), strings.HasPrefix(c, "hvc1"), strings.HasPrefix(c, "hevc"):
		return "hevc"
	case strings.HasPrefix(c, "vp09"), strings.HasPrefix(c, "vp9"):
		return "vp9"
	case strings.HasPrefix(c, "avc1"), strings.HasPrefix(c, "avc3"), strings.HasPrefix(c, "avc"):
		return "avc"
	default:
		return ""
	}
}

// CodecRank 是同分辨率内的编码偏好序，数字越小越优先：
// AV1（同等画质省 30–50% 码率）> HEVC > VP9 > H.264 / 未知。
// 分辨率永远排在它前面——不会为了 AV1 去降一档清晰度，见 SortVariantsDesc。
func CodecRank(codec string) int {
	switch CodecFamily(codec) {
	case "av1":
		return 0
	case "hevc":
		return 1
	case "vp9":
		return 2
	default:
		return 3
	}
}

// CodecLabel 是给人看的编码名，日志与前端角标用；未知返回空串。
func CodecLabel(codec string) string {
	switch CodecFamily(codec) {
	case "av1":
		return "AV1"
	case "hevc":
		return "HEVC"
	case "vp9":
		return "VP9"
	case "avc":
		return "H.264"
	default:
		return ""
	}
}

// SortVariantsDesc 按「分辨率降序 → 编码偏好（AV1 优先）→ 码率降序」排序。
// 三个 adapter 与下载、播放两条读路径都走这一个比较函数，
// 免得「播放用 AV1、下载却拿 H.264」这种前后不一致。
func SortVariantsDesc(vs []Variant) {
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].Height != vs[j].Height {
			return vs[i].Height > vs[j].Height
		}
		if ri, rj := CodecRank(vs[i].Codec), CodecRank(vs[j].Codec); ri != rj {
			return ri < rj
		}
		return vs[i].Bandwidth > vs[j].Bandwidth
	})
}

type Job struct {
	ID       int64   `json:"id"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
	Priority int     `json:"-"`
	Payload  string  `json:"payload"`
	Progress float64 `json:"progress"`
	Message  string  `json:"message"`
	Error    string  `json:"error"`
	Attempts int     `json:"attempts"`
	TargetID int64   `json:"targetId"`
	// CancelRequested 是控制位：0 继续、1 取消、2 暂停（见 store.CancelFlag）。
	CancelRequested int        `json:"-"`
	SpeedBPS        int64      `json:"speedBps"`
	BytesDone       int64      `json:"bytesDone"`
	BytesTotal      int64      `json:"bytesTotal"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
}

const (
	JobQueued   = "queued"
	JobRunning  = "running"
	JobDone     = "done"
	JobFailed   = "failed"
	JobCanceled = "canceled"
)

const (
	JobTypeSync = "sync"
)

// Library 是一个本地媒体库（一个根目录）。
type Library struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	RootPath       string    `json:"rootPath"`
	Enabled        bool      `json:"enabled"`
	IsDefault      bool      `json:"isDefault"`
	UploadPolicy   string    `json:"uploadPolicy"`
	OpenListPath   string    `json:"openlistPath"`
	NamingTemplate string    `json:"namingTemplate"`
	CreatedAt      time.Time `json:"createdAt"`
}

// 上传策略：M2 只用得到 Local（仅本地），其余在 M3 生效。
const (
	PolicyLocal       = "local"
	PolicyLocalRemote = "local_remote"
	PolicyStrm        = "strm"
)

// LibraryFile 是一部影片落在某个库里的落盘记录。
type LibraryFile struct {
	ID          int64
	VideoID     int64
	LibraryID   int64
	RelDir      string
	VideoName   string
	VideoPath   string
	NFOPath     string
	PosterPath  string
	ThumbPath   string
	FanartPath  string
	StrmPath    string
	SizeBytes   int64
	Width       int
	Height      int
	DurationSec int
	CreatedAt   time.Time
}

const (
	JobTypeDownload = "download"
	JobTypeUpload   = "upload"
	JobTypeStrm     = "strm"
)

// JobPaused 是 M2 新增状态：暂停的任务保留已下载分段，恢复时断点续传。
const JobPaused = "paused"
