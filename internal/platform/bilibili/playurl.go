package bilibili

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/mario/myporn/internal/model"
)

// playurl 是 /x/player/playurl 的返回。同一个接口按 fnval 给两种形态：
//
//	fnval=0    → durl：**单文件、音画一体**（最多 720P，登录后可能到 1080P）
//	fnval=16+  → dash：视频轨与音频轨分开给（1080P+ / 4K / 8K 只有这一条路）
//
// 这里两份都要：durl 起播最快又不用转封装，dash 是唯一能上高画质的路。
type playurlResp struct {
	Quality           int      `json:"quality"`
	Format            string   `json:"format"`
	Timelength        int      `json:"timelength"` // 毫秒
	AcceptQuality     []int    `json:"accept_quality"`
	AcceptDescription []string `json:"accept_description"`
	Durl              []struct {
		Order     int      `json:"order"`
		Length    int      `json:"length"` // 毫秒
		Size      int64    `json:"size"`   // 字节
		URL       string   `json:"url"`
		BackupURL []string `json:"backup_url"`
	} `json:"durl"`
	Dash *struct {
		Duration int          `json:"duration"`
		Video    []dashStream `json:"video"`
		Audio    []dashStream `json:"audio"`
	} `json:"dash"`
}

// dashStream 是一条分轨。大小写两套字段 B 站都给，这里都接住。
type dashStream struct {
	ID        int      `json:"id"`
	BaseURL   string   `json:"baseUrl"`
	BaseURL2  string   `json:"base_url"`
	Backup    []string `json:"backupUrl"`
	Backup2   []string `json:"backup_url"`
	Bandwidth int      `json:"bandwidth"`
	Codecs    string   `json:"codecs"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	FrameRate string   `json:"frameRate"`
}

func (s dashStream) primary() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return s.BaseURL2
}

func (s dashStream) backups() []string {
	if len(s.Backup) > 0 {
		return s.Backup
	}
	return s.Backup2
}

// mediaHostSuffixes 是「快的那条 CDN」。实测同一条片子：
//
//	PCDN（*.edge.mountaintoys.cn / *.mcdn.bilivideo.cn）: 0.9 MB/s
//	官方 upos-*（*.bilivideo.com）                      : 6.6 MB/s
//
// 所以只要列表里有一条官方 CDN 就用它——每条 durl/dash 的 backup_url 里基本都有。
var mediaHostSuffixes = []string{"bilivideo.com", "bilivideo.cn", "akamaized.net"}

// pickMediaURL 在「主地址 + 备用地址」里挑一条官方 CDN 的；没有就退回主地址
// （PCDN 也能放，只是慢；不能为了快就什么都不给）。
func pickMediaURL(primary string, backups []string) string {
	cands := make([]string, 0, len(backups)+1)
	if primary != "" {
		cands = append(cands, primary)
	}
	cands = append(cands, backups...)
	for _, c := range cands {
		if c == "" {
			continue
		}
		if u, err := url.Parse(c); err == nil && hostAllowed(u.Hostname()) {
			return c
		}
	}
	for _, c := range cands {
		if c != "" {
			return c
		}
	}
	return ""
}

func hostAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, s := range mediaHostSuffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

// playURL 取一份播放信息。fnval="0" 要 durl，fnval="4048" 要 dash
// （4048 = 16 dash | 64 HDR | 128 4K | 256 杜比 | 512 8K | 1024 AV1 | 2048 杜比音频）。
func (a *Adapter) playURL(ctx context.Context, bvid string, cid int64, fnval string) (*playurlResp, error) {
	q := url.Values{
		"bvid":  {bvid},
		"cid":   {strconv.FormatInt(cid, 10)},
		"qn":    {"127"}, // 要最高，实际给到哪一档由账号权限决定
		"fnval": {fnval},
		"fourk": {"1"},
	}
	var d playurlResp
	if err := a.call(ctx, "/x/player/playurl", q, a.lim, a.base+"/video/"+bvid, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ResolvePlayback 一次把元数据和所有可用画质都拿回来（播放页最常用）。
func (a *Adapter) ResolvePlayback(ctx context.Context, videoID string) (*model.Video, []model.Variant, error) {
	d, err := a.view(ctx, videoID)
	if err != nil {
		return nil, nil, err
	}
	variants, err := a.variants(ctx, d)
	if err != nil {
		return a.video(d), nil, err
	}
	return a.video(d), variants, nil
}

// ResolveStreams 只要画质列表。
func (a *Adapter) ResolveStreams(ctx context.Context, videoID string) ([]model.Variant, error) {
	d, err := a.view(ctx, videoID)
	if err != nil {
		return nil, err
	}
	return a.variants(ctx, d)
}

// variants 按「先看 durl，再补 dash 里比 durl 更高的档」的顺序拼出画质列表。
//
// 为什么不是简单地全都要：
//   - 同一分辨率下 durl 和 dash 会撞成同一个 id（都是 720p），前端下拉框里会出现两条一样的；
//   - durl 是单文件、开箱即播；dash 要服务端把两轨合成一份才能播（见 internal/muxcache）。
//     同样清晰度，让用户等一次转封装没有任何收益。
//
// 所以规则是：durl 那一档留下，dash 里**同分辨率**的丢掉，只保留比它更高的。
// 未登录时典型结果就是一条 720p（durl）；登录后是 4K/1080P（dash）+ 1080P（durl）。
func (a *Adapter) variants(ctx context.Context, d *viewData) ([]model.Variant, error) {
	cid := d.firstCID()
	if cid == 0 {
		return nil, errors.New("详情里没有 cid，取不到播放地址")
	}

	dash, dashErr := a.playURL(ctx, d.BVID, cid, "4048")
	// durl 拿不到不算失败（有的投稿只给 dash），但两份都拿不到就是真失败。
	var durl *playurlResp
	if r, err := a.playURL(ctx, d.BVID, cid, "0"); err == nil {
		durl = r
	}

	bestAudio := pickAudio(dash)
	out := make([]model.Variant, 0, 8)
	durlHeight := 0

	// 1) 单文件那一档
	if durl != nil && len(durl.Durl) > 0 && durl.Durl[0].URL != "" {
		item := durl.Durl[0]
		w, h := qualitySize(durl.Quality, dash)
		bw := 0
		if item.Size > 0 && item.Length > 0 {
			// size 是字节、length 是毫秒：换成 bps。
			bw = int(float64(item.Size) * 8 * 1000 / float64(item.Length))
		}
		out = append(out, model.Variant{
			Height:    h,
			Width:     w,
			Bandwidth: bw,
			Codec:     "avc", // durl 全是 H.264 mp4
			URL:       pickMediaURL(item.URL, item.BackupURL),
		})
		durlHeight = h
	}

	// 2) DASH 的档位（去掉与 durl 同分辨率的那一条）
	if dash != nil && dash.Dash != nil && bestAudio != nil {
		audioURL := pickMediaURL(bestAudio.primary(), bestAudio.backups())
		audioBW := bestAudio.Bandwidth
		for _, s := range dash.Dash.Video {
			src := pickMediaURL(s.primary(), s.backups())
			// 同分辨率的那一档直接跳过：留着就是下拉框里两条一模一样的 720p。
			// 比它低的档照留——慢网下用户确实会主动选 480P。
			if src == "" || (durlHeight > 0 && s.Height == durlHeight) {
				continue
			}
			out = append(out, model.Variant{
				Height:    s.Height,
				Width:     s.Width,
				Bandwidth: s.Bandwidth + audioBW, // 体积预估要算上音频轨
				Codec:     s.Codecs,
				URL:       src,
				AudioURL:  audioURL,
			})
		}
	}

	if len(out) == 0 {
		if dashErr != nil {
			return nil, fmt.Errorf("解析播放地址失败: %w", dashErr)
		}
		return nil, errors.New("这条投稿没有任何可取用的播放地址（可能是充电专属或已下架）")
	}
	model.SortVariantsDesc(out)
	return out, nil
}

// pickAudio 选码率最高的那条音轨（实测 30280 = 192kbps，30232 = 132k，30216 = 64k）。
// 杜比/无损音轨（dash.dolby / dash.flac）要大会员，这里不掺和：
// 一旦拿到 flac 就得多操心 mp4 容器兼容性，收益却是听不出来的。
func pickAudio(p *playurlResp) *dashStream {
	if p == nil || p.Dash == nil || len(p.Dash.Audio) == 0 {
		return nil
	}
	list := p.Dash.Audio
	sort.SliceStable(list, func(i, j int) bool { return list[i].Bandwidth > list[j].Bandwidth })
	for i := range list {
		if pickMediaURL(list[i].primary(), list[i].backups()) != "" {
			return &list[i]
		}
	}
	return nil
}

// qnSize 是「登录前就能确定的分辨率」表：durl 的 quality 只给档位号，不给宽高。
// 能从 DASH 里查到同档就用 DASH 的（竖版/方片才准），查不到才退回这张表。
var qnSize = map[int][2]int{
	6:   {426, 240},
	16:  {640, 360},
	32:  {854, 480},
	64:  {1280, 720},
	74:  {1280, 720},
	80:  {1920, 1080},
	112: {1920, 1080},
	116: {1920, 1080},
	120: {3840, 2160},
	125: {3840, 2160},
	126: {3840, 2160},
	127: {7680, 4320},
}

func qualitySize(qn int, dash *playurlResp) (w, h int) {
	if dash != nil && dash.Dash != nil {
		for _, s := range dash.Dash.Video {
			if s.ID == qn && s.Width > 0 && s.Height > 0 {
				return s.Width, s.Height
			}
		}
	}
	if sz, ok := qnSize[qn]; ok {
		return sz[0], sz[1]
	}
	return 0, 0
}
