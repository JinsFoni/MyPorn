package krx18

// Server 2 的第二套播放器：mov18plus.cloud（Abyss 播放器）。
//
// 为什么单独一个文件：它不是 HLS。抓到的链路是这样的（2026-11-05 实测，片子 3355
// 《Taste of a Former Classmate…》post 77806）：
//
//	GET  https://mov18plus.cloud/?v=<slug>            页面里有 const datas = "<base64>"
//	     base64 → JSON（media 是**原始二进制**，json 里既有裸字节也有 \u00XX 转义）
//	     media 用 AES-256-CTR 解密，key = md5hex("user_id:slug:md5_id") 的 32 个 ASCII 字节
//	     → {"mp4":{"sources":[…],"domains":[…],"fristDatas":[…]}}
//
// 播放器并不把 mp4 地址给浏览器，它给的是**假地址**
// https://storage.googleapis.com/mediastorage/{ts}/{rand}/{size}.mp4#mp4/chunk/1/{…}，
// 由 Service Worker（iamcdn.net/player-v2/sw.bundle.v3.js）拦下来，按 chunkSize=5 MB
// 把整片切成片，逐片换成：
//
//	GET https://{sub}.sssrr.org/sora/{size}/{token}
//	    token = b64(b64(AES-256-CTR(plain)))   （两层 base64 各自去掉 '='）
//	    plain = /mp4/{md5_id}/{res_id}/{size}/{chunkSize}/{分片序号}
//	    加密 key = md5(分片长的十进制**数字位**)，counter = key 前 16 字节，都是 ASCII
//
// 这里两个坑值得写下来（都是实测撞出来的）：
//   - key 不是 md5("1634796856")，而是 md5(数字 1634796856)。JS 那边 md5 收到 number
//     会走 bytesToWords(string) 分支，把字符 "1" 当成数值 1 而不是 0x31；
//     md5("1634796856")=ee5d… 全错，md5({1,6,3,4,7,9,6,8,5,6})=aaccae5f… 才对。
//   - plain 里必须带 chunkSize（5242880）。少了它 server 就回 404（Backblaze 的
//     "Object not found" 页，27511 字节）；而且请求不带 Referer: https://mov18plus.cloud/
//     一律 403（Cloudflare）。带了就 206，每片正好 5,242,880 字节。
//
// 拿到片之后是**明文** MP4（第 0 片开头就是 ftyp+moov，moov 4.5 MB、faststart），
// 所以站内只要按 Range 逐片转发，浏览器当普通文件播即可。

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
	"github.com/JinsFoni/MyPorn/internal/platform"
)

// 编译期断言：Server 的 /api/{platform}/file.mp4 路由靠这个接口认人。
var _ platform.FileStreamProvider = (*Adapter)(nil)

const (
	// abyssReferer 是 sssrr.org 那批存储域名唯一认的 Referer。
	// 实测：带它 206，不带 403（Cloudflare，4545 字节的拦截页），UA 随便写都一样。
	abyssReferer = "https://mov18plus.cloud/"

	// abyssHostSuffix 是 Abyss 播放页所在的域名（Server 2 的 embed）。
	abyssHostSuffix = "mov18plus.cloud"

	// abyssProbeTimeout 只包住「解析时顺手量一次分辨率」，别让它在起播路上拖太久。
	abyssProbeTimeout = 20 * time.Second

	// abyssChunkAttempts 是单片最多试几次：实测上游偶尔 RST 一条 HTTP/2 流，
	// 同一条地址紧接着再取就好，不需要退避太久。
	abyssChunkAttempts = 3

	// abyssChunkSize 是播放器自己用的分片粒度（sw.bundle.v3.js 里 maxChunkSize=0x500000）。
	// 它进 token 的明文，改不了；实测整条 1080p 片 = 312 片（311×5,242,880 + 4,261,176）。
	abyssChunkSize = 5242880
)

// isAbyssHost 判断一个 embed 地址是不是 Abyss 播放器。
func isAbyssHost(host string) bool {
	return host == abyssHostSuffix || strings.HasSuffix(host, "."+abyssHostSuffix)
}

// abyssMedia 是解析出来的一条 Abyss 播放参数。
type abyssMedia struct {
	MD5ID int64
	ResID int64
	Size  int64
	Host  string // 形如 ygfi0yfyc10.sssrr.org
	Label string // 1080p / 720p …
	Codec string // avc1 / av01
	Path  string // 站内整片地址（variant 里直接给前端）

	Width       int
	Height      int
	DurationSec int
	Bandwidth   int
}

func (m *abyssMedia) quality() string {
	if m.Height > 0 {
		return model.Variant{Height: m.Height}.Quality()
	}
	return m.Label
}

// chunkURL 是第 i 片的真实地址。
func (m *abyssMedia) chunkURL(i int) string {
	plain := fmt.Sprintf("/mp4/%d/%d/%d/%d/%d", m.MD5ID, m.ResID, m.Size, abyssChunkSize, i)
	return fmt.Sprintf("https://%s/sora/%d/%s", m.Host, m.Size, abyssToken(plain, m.Size))
}

// chunks 是这条片子一共几片（最后一片通常短一截）。
func (m *abyssMedia) chunks() int {
	return int((m.Size + abyssChunkSize - 1) / abyssChunkSize)
}

// ---------- token ----------

// abyssToken 复刻 core/sw bundle 里那条 _0x1f36e6：
// base64(base64(AES-256-CTR(plain)).replace(/=/g,”)).replace(/=/g,”)
func abyssToken(plain string, seed int64) string {
	key := abyssTokenKey(seed)
	block, err := aes.NewCipher(key)
	if err != nil {
		// key 长度固定 32 字节，这里不可能失败。
		return ""
	}
	ct := make([]byte, len(plain))
	cipher.NewCTR(block, key[:16]).XORKeyStream(ct, []byte(plain))
	inner := strings.ReplaceAll(base64.StdEncoding.EncodeToString(ct), "=", "")
	return strings.ReplaceAll(base64.StdEncoding.EncodeToString([]byte(inner)), "=", "")
}

// abyssTokenKey 是 token 的 AES key：md5(seed) 的十六进制 32 个 ASCII 字节。
//
// 坑在 seed 的类型上：JS 那边传的是 number，md5 模块对 number 会先 toString、再把
// **字符串按字符下标**喂给 bytesToWords —— 也就是字符 "1" 变成了数值 1（不是 0x31）。
// 所以这里要按十进制位展开成 0..9 的字节，对 "1634796856" 得 aaccae5faad13ef8eec53c4344a7c2c5，
// 而 md5("1634796856") 是 ee5d… —— 两者完全不同，用错就整条 404。
func abyssTokenKey(seed int64) []byte {
	s := strconv.FormatInt(seed, 10)
	digits := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		digits[i] = s[i] - '0'
	}
	sum := md5.Sum(digits)
	return []byte(hex.EncodeToString(sum[:]))
}

// ---------- 配置层 ----------

// abyssSource 是 media 里的一档（360p h264 / 1080p av1 …）。
type abyssSource struct {
	Label  string `json:"label"`
	ResID  int64  `json:"res_id"`
	Size   int64  `json:"size"`
	Codec  string `json:"codec"`
	Status bool   `json:"status"`
	Sub    string `json:"sub"`
}

// abyssManifest 是 media 解密后的明文（只取用得上的字段）。
type abyssManifest struct {
	MP4 struct {
		Sources []abyssSource `json:"sources"`
		Domains []string      `json:"domains"`
	} `json:"mp4"`
}

// abyssPageConfig 是 datas 里那层 JSON 的元信息（media 之外的部分）。
type abyssPageConfig struct {
	Slug   string      `json:"slug"`
	MD5ID  json.Number `json:"md5_id"`
	UserID json.Number `json:"user_id"`
}

var abyssDatasRe = regexp.MustCompile(`const\s+datas\s*=\s*"([^"]+)"`)

// resolveAbyss 从 mov18plus 的播放页解析出可播参数。
//
// 播放页有两种形态：新版是 ?v=<code>，老片给的是 /v/<code>/（实测那个路径现在整条 404），
// 所以老形态先去新版地址试一次——同一个 code 在新版上通常还在。
func (a *Adapter) resolveAbyss(ctx context.Context, embedURL string) (*abyssMedia, error) {
	page, err := a.getText(ctx, embedURL, map[string]string{"Referer": a.base + "/"})
	if err == nil {
		if m, perr := parseAbyssPage(page); perr == nil {
			return m, nil
		}
		if strings.Contains(page, "Video not found") {
			return nil, errors.New("播放器说这个片子已经删了（Video not found）")
		}
	}
	if alt, ok := abyssAltEmbed(embedURL); ok {
		if altPage, altErr := a.getText(ctx, alt, map[string]string{"Referer": a.base + "/"}); altErr == nil {
			if altMedia, perr := parseAbyssPage(altPage); perr == nil {
				return altMedia, nil
			}
			if strings.Contains(altPage, "Video not found") {
				return nil, errors.New("播放器说这个片子已经删了（Video not found）")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("取播放器页面失败: %w", err)
	}
	return nil, errors.New("播放器页里没有 const datas")
}

// abyssAltEmbed 把老形态 /v/<code>/ 换成新形态 ?v=<code>。
func abyssAltEmbed(embedURL string) (string, bool) {
	u, err := url.Parse(embedURL)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Query().Get("v") != "" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "v" || parts[1] == "" {
		return "", false
	}
	q := url.Values{"v": {parts[1]}}
	return u.Scheme + "://" + u.Host + "/?" + q.Encode(), true
}

// parseAbyssPage 把播放页 HTML 变成 abyssMedia（不含探测出来的分辨率）。
func parseAbyssPage(page string) (*abyssMedia, error) {
	mm := abyssDatasRe.FindStringSubmatch(page)
	if len(mm) != 2 {
		return nil, errors.New("播放器页里没有 const datas")
	}
	raw, err := base64.StdEncoding.DecodeString(mm[1])
	if err != nil {
		return nil, fmt.Errorf("datas 不是合法 base64: %w", err)
	}
	var cfg abyssPageConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("datas 里那层 JSON 读不出来: %w", err)
	}
	md5ID, err := strconv.ParseInt(cfg.MD5ID.String(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("datas 里的 md5_id 看不懂: %w", err)
	}
	userID, err := strconv.ParseInt(cfg.UserID.String(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("datas 里的 user_id 看不懂: %w", err)
	}

	// media 是**原始二进制**：整块 json 里有裸的 0xF1 这类非 UTF-8 字节，
	// 所以不能整体 json.Unmarshal（会把这些字节换成 U+FFFD，密文就废了），
	// 只能按 json 的转义规则自己抠出这段字节。
	mediaRaw, err := abyssJSONRawString(raw, "media")
	if err != nil {
		return nil, err
	}
	key := abyssConfigKey(userID, cfg.Slug, md5ID)
	plain, err := abyssCTR(key, mediaRaw)
	if err != nil {
		return nil, fmt.Errorf("media 解不开: %w", err)
	}
	var mj abyssManifest
	if err := json.Unmarshal(plain, &mj); err != nil {
		return nil, fmt.Errorf("media 明文不是 JSON（key 不对？）: %s", truncate(plain, 120))
	}
	if len(mj.MP4.Sources) == 0 || len(mj.MP4.Domains) == 0 {
		return nil, errors.New("播放器配置里没有可播的档位")
	}

	src, ok := pickAbyssSource(mj.MP4.Sources)
	if !ok {
		return nil, errors.New("播放器配置里的档位都不可用")
	}
	host := abyssHost(mj.MP4.Domains, src.Sub, src.Size)
	if host == "" {
		return nil, errors.New("播放器配置里没有可用的存储域名")
	}
	return &abyssMedia{
		MD5ID: md5ID, ResID: src.ResID, Size: src.Size, Host: host,
		Label: src.Label, Codec: codecTag(src.Codec),
		Height: abyssLabelHeight(src.Label),
	}, nil
}

// abyssConfigKey 是 media 那层的 AES key：md5hex("user_id:slug:md5_id") 的 ASCII。
// 这一处 JS 传的是**字符串**，走的是标准 md5（不是 token 那条数字位分支）。
func abyssConfigKey(userID int64, slug string, md5ID int64) []byte {
	s := fmt.Sprintf("%d:%s:%d", userID, slug, md5ID)
	sum := md5.Sum([]byte(s))
	return []byte(hex.EncodeToString(sum[:]))
}

// abyssCTR 用 key 的前 16 字节当 counter 做 AES-256-CTR。
//
// 计数器是**整块 128 位大端自增**（WebCrypto 的 length:128 和 Go 的 cipher.NewCTR 一致），
// 所以只要知道分片自身的起点，就能从任意偏移开始解（这里的分片都是各自从 0 起加密的）。
func abyssCTR(key, ct []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("key 长度 %d，应为 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	cipher.NewCTR(block, key[:16]).XORKeyStream(out, ct)
	return out, nil
}

// abyssJSONRawString 按 JSON 的转义规则抠出一个字符串字段的原始字节。
//
// 为什么不用 encoding/json：这个 json 不是合法 UTF-8（media 里是裸密文），
// 标准库会把非法字节替换成 U+FFFD，密文就解不开了。
func abyssJSONRawString(raw []byte, name string) ([]byte, error) {
	needle := []byte(`"` + name + `":"`)
	i := bytes.Index(raw, needle)
	if i < 0 {
		return nil, fmt.Errorf("datas 里没有 %s 字段", name)
	}
	i += len(needle)
	out := make([]byte, 0, len(raw)/2)
	for i < len(raw) {
		switch c := raw[i]; c {
		case '"':
			return out, nil
		case '\\':
			if i+1 >= len(raw) {
				return nil, errors.New("datas 被截断")
			}
			switch e := raw[i+1]; e {
			case 'u':
				if i+6 > len(raw) {
					return nil, errors.New("datas 被截断")
				}
				n, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 32)
				if err != nil {
					return nil, fmt.Errorf("datas 里有看不懂的 \\u 转义: %w", err)
				}
				// 站点把每个字节写成 \u00XX（ISO-8859-1 口径），直接落成那个字节。
				out = append(out, byte(n))
				i += 6
				continue
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			default:
				// \" \\ \/ 都等于原字符本身
				out = append(out, e)
			}
			i += 2
		default:
			out = append(out, c)
			i++
		}
	}
	return nil, errors.New("datas 被截断")
}

// abyssLabelHeight 把档位标签换成高度（1080p → 1080）。
func abyssLabelHeight(label string) int {
	label = strings.TrimSpace(strings.ToLower(label))
	if label == "origin" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSuffix(label, "p"))
	if err != nil {
		return 0
	}
	return n
}

// abyssRank 是同档位里挑哪一个的优先级：h264 优先（浏览器都能解），再比体积。
func abyssRank(label, codec string) int {
	return abyssLabelHeight(label)*10 + boolInt(strings.EqualFold(codec, "h264"))*5 +
		boolInt(strings.EqualFold(codec, "avc1"))*5
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pickAbyssSource 挑最高画质那档。
//
// 站点给 7 档（360p/720p/1080p × h264/av1），按「分辨率优先、同档 h264 优先」挑：
// 1080p av1 和 1080p h264 是同一分辨率，但 av1 在部分浏览器上解不了，
// 而 h264 那档实测 1920x1080 / 2.87 Mbps / 4261 s，就是最高画质，没有降级。
func pickAbyssSource(sources []abyssSource) (abyssSource, bool) {
	var best abyssSource
	found := false
	bestRank := -1
	for _, s := range sources {
		if s.Size <= 0 || s.ResID <= 0 {
			continue
		}
		r := abyssRank(s.Label, s.Codec)
		if r > bestRank || (r == bestRank && s.Size > best.Size) {
			best, bestRank, found = s, r, true
		}
	}
	return best, found
}

// abyssHost 选出这条档位该用哪个存储域名：优先 sub 对得上的那个，否则按下标取模。
// 这是 sw.bundle.v3.js 里的同一套规则（实测 1080p h264 的 sub=ygfi0yfyc10 在 domains 里）。
func abyssHost(domains []string, sub string, size int64) string {
	sub = strings.TrimSpace(sub)
	if sub != "" {
		for _, d := range domains {
			if d == sub || strings.HasPrefix(d, sub+".") || strings.Contains(d, sub) {
				return d
			}
		}
	}
	if len(domains) == 0 || size <= 0 {
		return ""
	}
	return domains[size%int64(len(domains))]
}

// ---------- 探测 ----------

// probeAbyss 从第 0 片里量出真实分辨率、编码、时长与码率。
//
// 为什么值得多取这一片：moov 就在片头（实测 moov 4.5 MB、mdat 从 4,564,397 开始，
// 也就是 faststart），5 MB 刚好够 ffprobe 读出整条片子的流信息和时长。
// 量不出来不算失败——档位标签本身已经写明分辨率，用标签兜底。
func (a *Adapter) probeAbyss(ctx context.Context, m *abyssMedia) {
	if a.ffprobe == "" {
		a.ffprobe = "ffprobe"
	}
	data, err := a.fetchAbyssChunk(ctx, m.chunkURL(0), 0, abyssChunkSize-1)
	if err != nil || len(data) < 1024 {
		return
	}
	dir, err := os.MkdirTemp("", "krx18-abyss")
	if err != nil {
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "head.mp4")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return
	}
	out, err := exec.CommandContext(ctx, a.ffprobe,
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,codec_name,bit_rate:format=duration",
		"-of", "json", path).Output()
	if err != nil {
		return
	}
	var probed struct {
		Streams []struct {
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			CodecName string `json:"codec_name"`
			BitRate   string `json:"bit_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probed); err != nil || len(probed.Streams) == 0 {
		return
	}
	s := probed.Streams[0]
	if s.Width > 0 && s.Height > 0 {
		m.Width, m.Height = s.Width, s.Height
	}
	if s.CodecName != "" {
		m.Codec = codecTag(s.CodecName)
	}
	if br, perr := strconv.Atoi(s.BitRate); perr == nil && br > 0 {
		m.Bandwidth = br
	}
	if d, perr := strconv.ParseFloat(probed.Format.Duration, 64); perr == nil && d > 0 {
		m.DurationSec = int(d + 0.5)
	}
}

// ---------- 整片转发 ----------

// FilePath 是这条片子的站内整片地址（前端当普通 mp4 播）。
func (a *Adapter) FilePath(videoID string) string {
	return "/api/" + a.Key() + "/file.mp4?v=" + url.QueryEscape(videoID)
}

// ServeFile 按 Range 逐片取上游、边取边发。
//
// 为什么服务端转发而不是把 sora 地址给浏览器：那条地址要求
// Referer: https://mov18plus.cloud/，浏览器直接取是 403；
// 而且每片的 token 是算出来的，前端没必要知道算法。
func (a *Adapter) ServeFile(ctx context.Context, videoID string, w http.ResponseWriter, r *http.Request) error {
	e, err := a.resolve(ctx, videoID)
	if err != nil {
		return err
	}
	if e.abyss == nil {
		return errors.New("这条片子不是 Abyss 通道")
	}
	m := e.abyss
	total := m.Size

	start, end := int64(0), total-1
	partial := false
	if rng := strings.TrimSpace(r.Header.Get("Range")); rng != "" {
		s, e2, ok := parseByteRange(rng, total)
		if !ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", total))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return nil
		}
		start, end, partial = s, e2, true
	}

	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Type", "video/mp4")
	// 地址里是算出来的 token，缓存起来只会拿到过期的东西，干脆不缓存。
	h.Set("Cache-Control", "no-store")
	if partial {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		h.Set("Content-Length", strconv.FormatInt(total, 10))
		w.WriteHeader(http.StatusOK)
	}

	for off := start; off <= end; {
		idx := int(off / abyssChunkSize)
		chunkStart := int64(idx) * abyssChunkSize
		inStart := off - chunkStart
		inEnd := end - chunkStart
		if inEnd > abyssChunkSize-1 {
			inEnd = abyssChunkSize - 1
		}
		if chunkStart+inEnd >= total {
			inEnd = total - chunkStart - 1
		}
		data, err := a.fetchAbyssChunk(ctx, m.chunkURL(idx), inStart, inEnd)
		if err != nil {
			// 头已经发出去了，只能断流让播放器自己重试。
			return fmt.Errorf("取第 %d 片失败: %w", idx, err)
		}
		if _, err := w.Write(data); err != nil {
			return nil // 播放器主动断开（拖动进度条就是这样），不算错误
		}
		off = chunkStart + inEnd + 1
	}
	return nil
}

// fetchAbyssChunk 取一片里的 [from,to] 区间（最多 5 MB，直接放内存）。
//
// 为什么不直接 io.Copy 到 ResponseWriter：上游会半路抽风——实测同一条片子
// 连续取两片，第 1 片在 16.3 秒时回 `stream error: stream ID 3; INTERNAL_ERROR`
// （HTTP/2 被对端 RST）。边取边写的话这种错误只能断流、让播放器重连；
// 先取回内存就能在没写出任何字节前重试，用户那边一次都不会察觉。
// 代价是每片要等一下整片到齐（5 MB / 实测 15~27 MB/s ≈ 0.2~0.3 秒），
// 播放器本来就在往前缓存，这个延迟看不出来。
func (a *Adapter) fetchAbyssChunk(ctx context.Context, chunkURL string, from, to int64) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < abyssChunkAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		data, err := a.tryFetchAbyssChunk(ctx, chunkURL, from, to)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (a *Adapter) tryFetchAbyssChunk(ctx context.Context, chunkURL string, from, to int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chunkURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", abyssReferer)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("上游返回 %d: %s", resp.StatusCode, truncate(body, 120))
	}
	want := to - from + 1
	data, err := io.ReadAll(io.LimitReader(resp.Body, want))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != want {
		return nil, fmt.Errorf("上游只给了 %d 字节，要的是 %d", len(data), want)
	}
	return data, nil
}

// parseByteRange 只认单段 bytes=a-b / bytes=a- / bytes=-n，多段一律当整片。
func parseByteRange(raw string, total int64) (int64, int64, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "bytes=") {
		return 0, 0, false
	}
	spec := strings.TrimSpace(strings.TrimPrefix(raw, "bytes="))
	if strings.Contains(spec, ",") || total <= 0 {
		return 0, total - 1, false
	}
	dash := strings.Index(spec, "-")
	if dash < 0 {
		return 0, 0, false
	}
	fromRaw, toRaw := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	var from, to int64
	if fromRaw == "" {
		// bytes=-N：最后 N 字节
		n, err := strconv.ParseInt(toRaw, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		from = total - n
		if from < 0 {
			from = 0
		}
		to = total - 1
		return from, to, true
	}
	from, err := strconv.ParseInt(fromRaw, 10, 64)
	if err != nil || from < 0 || from >= total {
		return 0, 0, false
	}
	to = total - 1
	if toRaw != "" {
		t, err := strconv.ParseInt(toRaw, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		if t < from {
			return 0, 0, false
		}
		if t < to {
			to = t
		}
	}
	return from, to, true
}
