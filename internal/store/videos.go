package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// UpsertVideo 幂等写入影片；inserted 为 true 表示是新片。
func (s *Store) UpsertVideo(v *model.Video) (id int64, inserted bool, err error) {
	tags, _ := json.Marshal(v.Tags)
	if v.Tags == nil {
		tags = []byte("[]")
	}
	var pub any
	if v.PublishedAt != nil {
		pub = fmtTime(*v.PublishedAt)
	}
	res, err := s.db.Exec(`
		INSERT INTO videos (platform, platform_video_id, title, url, thumb_url, preview_url,
			duration_sec, published_at, view_count, rating, tags, description)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(platform, platform_video_id) DO UPDATE SET
			title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE videos.title END,
			thumb_url = CASE WHEN excluded.thumb_url <> '' THEN excluded.thumb_url ELSE videos.thumb_url END,
			preview_url = CASE WHEN excluded.preview_url <> '' THEN excluded.preview_url ELSE videos.preview_url END,
			duration_sec = CASE WHEN excluded.duration_sec > 0 THEN excluded.duration_sec ELSE videos.duration_sec END,
			published_at = COALESCE(excluded.published_at, videos.published_at),
			view_count = COALESCE(excluded.view_count, videos.view_count),
			rating = COALESCE(excluded.rating, videos.rating),
			tags = CASE WHEN excluded.tags <> '[]' THEN excluded.tags ELSE videos.tags END,
			description = CASE WHEN excluded.description <> '' THEN excluded.description ELSE videos.description END`,
		v.Platform, v.PlatformVideoID, v.Title, v.URL, v.ThumbURL, v.PreviewURL,
		v.DurationSec, pub, nullInt(v.ViewCount), nullFloat(v.Rating), string(tags), v.Description)
	if err != nil {
		return 0, false, err
	}
	_ = res
	inserted = false
	if err := s.db.QueryRow(`SELECT id FROM videos WHERE platform = ? AND platform_video_id = ?`,
		v.Platform, v.PlatformVideoID).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, inserted, nil
}

// ListVideosMissingMeta 列出还没有标签的影片（用于一次性补齐元数据）。
func (s *Store) ListVideosMissingMeta(limit int) ([]*model.Video, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, platform, platform_video_id, title
		FROM videos WHERE tags = '[]' ORDER BY COALESCE(published_at, '') DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Video
	for rows.Next() {
		var v model.Video
		if err := rows.Scan(&v.ID, &v.Platform, &v.PlatformVideoID, &v.Title); err != nil {
			return nil, err
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

// VideoThumb 是封面预热/修复用的最小字段集。
type VideoThumb struct {
	ID              int64
	Platform        string
	PlatformVideoID string
	ThumbURL        string
}

// ListVideoThumbs 按 id 倒序列出带封面的影片（封面预热/修复用）。
// beforeID 是游标，传 0 表示从最新的开始。
func (s *Store) ListVideoThumbs(beforeID int64, limit int) ([]VideoThumb, error) {
	if limit <= 0 {
		limit = 50
	}
	if beforeID <= 0 {
		beforeID = 1 << 62
	}
	rows, err := s.db.Query(`SELECT id, platform, platform_video_id, thumb_url
		FROM videos WHERE thumb_url <> '' AND id < ? ORDER BY id DESC LIMIT ?`, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VideoThumb
	for rows.Next() {
		var v VideoThumb
		if err := rows.Scan(&v.ID, &v.Platform, &v.PlatformVideoID, &v.ThumbURL); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UpdateVideoThumb 覆盖封面地址。旧地址可能带签名且已过期，只能用新解析到的换掉。
func (s *Store) UpdateVideoThumb(id int64, thumbURL string) error {
	_, err := s.db.Exec(`UPDATE videos SET thumb_url = ? WHERE id = ?`, thumbURL, id)
	return err
}

// FillVideoMeta 用详情页解析到的元数据补齐影片记录：只填空缺字段，
// 不覆盖已有内容（列表页给的信息粗，详情页才准，但已有数据没必要反复写）。
func (s *Store) FillVideoMeta(id int64, meta *model.Video) error {
	if meta == nil {
		return nil
	}
	tags, _ := json.Marshal(meta.Tags)
	if meta.Tags == nil {
		tags = []byte("[]")
	}
	var pub any
	if meta.PublishedAt != nil {
		pub = fmtTime(*meta.PublishedAt)
	}
	_, err := s.db.Exec(`
		UPDATE videos SET
			tags = CASE WHEN tags = '[]' AND ? <> '[]' THEN ? ELSE tags END,
			description = CASE WHEN description = '' AND ? <> '' THEN ? ELSE description END,
			duration_sec = CASE WHEN duration_sec = 0 AND ? > 0 THEN ? ELSE duration_sec END,
			thumb_url = CASE WHEN thumb_url = '' AND ? <> '' THEN ? ELSE thumb_url END,
			view_count = COALESCE(view_count, ?),
			rating = COALESCE(rating, ?),
			published_at = COALESCE(published_at, ?)
		WHERE id = ?`,
		string(tags), string(tags),
		meta.Description, meta.Description,
		meta.DurationSec, meta.DurationSec,
		meta.ThumbURL, meta.ThumbURL,
		nullInt(meta.ViewCount), nullFloat(meta.Rating), pub,
		id)
	return err
}

// mustParse 解析时间列。历史上 fetched_at 用的是 SQLite 的 datetime('now') 格式
// （"2006-01-02 15:04:05"，UTC 但不带时区），只认 RFC3339 会让缓存永远"过期"。
func mustParse(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// VideoExists 判断影片是否已入库（用于增量同步提前停止翻页）。
func (s *Store) VideoExists(platform, platformVideoID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM videos WHERE platform = ? AND platform_video_id = ?`,
		platform, platformVideoID).Scan(&n)
	return n > 0, err
}

// LinkVideoPerformer 建立影片与艺人关联。
func (s *Store) LinkVideoPerformer(videoID, performerID int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO video_performers (video_id, performer_id) VALUES (?,?)`,
		videoID, performerID)
	return err
}

type FeedQuery struct {
	PerformerID int64
	Cursor      string
	Limit       int
	OnlyNew     bool
	Query       string
	// Platform 只要这一个平台的影片；ExcludePlatform 反过来，把某个平台整体摘掉。
	// B 站用两者：自己的页面只取 platform='bilibili'，「关注」流则把它排除掉——
	// 它有自己的页面（/bilibili），不该在追艺人的流里混着。
	Platform        string
	ExcludePlatform string
	// SubscribedOnly 只出「所属艺人还在订阅里」的片，B 站墙用（见 liveSubFilter）。
	SubscribedOnly bool
	// HideDownloaded 把已经落到本地库里的片整个摘掉，「关注」流用（2026-11-03 用户要求：
	// 下回来的片子在这一流里再出现一次没有意义——它们已经在媒体库那一页了）。
	// 判据是 library_files 表里有没有这一行，跟 videoDTO.localLibraryIds 同一个事实来源。
	HideDownloaded bool
}

// liveSubFilter 生成「这条片所属的艺人里，至少还有一位在订阅里」这个条件；idExpr 是影片 id 的列（如 "v.id"）。
//
// 为什么 B 站墙需要它：那边的投稿是同步时就落进 videos 的，取消订阅只软删 subscriptions 那一行，
// 影片行还在。YouTube 不用这个条件——它在 store 里就是 yt_videos JOIN yt_channels，删频道自然消失。
// 2026-10-29 实测没过滤的后果：管理页删掉「影视飓风」之后，墙上仍有它 219 条投稿、侧边栏未看角标照样数 209 条。
func liveSubFilter(idExpr string) string {
	return `EXISTS (SELECT 1 FROM video_performers vp
		JOIN subscriptions su ON su.performer_id = vp.performer_id AND su.deleted_at IS NULL
		WHERE vp.video_id = ` + idExpr + `)`
}

// feedFilter 把 FeedQuery 里「筛哪些片」的部分翻译成 SQL 条件（游标与分页不在这里）。
//
// 单独抽出来的理由：AdjacentVideos（播放器里的「上一个 / 下一个」）必须用和列表
// **一模一样**的过滤条件去找上下家。两份条件各写一遍迟早写岔，那时候播放器的
// 「下一个」会跳到列表里根本没出现过的片子上——用户看到的就是「顺序乱了」。
func feedFilter(q FeedQuery) ([]string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if q.PerformerID > 0 {
		where = append(where, "EXISTS (SELECT 1 FROM video_performers vp WHERE vp.video_id = v.id AND vp.performer_id = ?)")
		args = append(args, q.PerformerID)
	}
	if q.OnlyNew {
		where = append(where, "v.is_new = 1")
	}
	if q.Query != "" {
		where = append(where, "v.title LIKE ?")
		args = append(args, "%"+q.Query+"%")
	}
	if q.Platform != "" {
		where = append(where, "v.platform = ?")
		args = append(args, q.Platform)
	}
	if q.ExcludePlatform != "" {
		where = append(where, "v.platform <> ?")
		args = append(args, q.ExcludePlatform)
	}
	if q.SubscribedOnly {
		where = append(where, liveSubFilter("v.id"))
	}
	if q.HideDownloaded {
		// 走 library_files 的存在性判断（不是 videos 上的某个标记位）：媒体库里删掉文件之后，
		// 那一行跟着消失，这条片就该重新回到关注流里。
		where = append(where, "NOT EXISTS (SELECT 1 FROM library_files lf WHERE lf.video_id = v.id)")
	}
	return where, args
}

// videoColumns 是 videos 行的读取顺序；ListFeed 与 AdjacentVideos 共用，保证 scanVideo 对得上。
const videoColumns = `v.id, v.platform, v.platform_video_id, v.title, v.url, v.thumb_url, v.preview_url,
			v.duration_sec, v.published_at, v.view_count, v.rating, v.tags, v.description, v.is_new, v.rotate_deg`

// scanVideo 按 videoColumns 的顺序扫一行出来（sql.Row 与 sql.Rows 都满足 rowScanner）。
func scanVideo(row rowScanner) (*model.Video, error) {
	v := &model.Video{}
	var pub sql.NullString
	var vc sql.NullInt64
	var rating sql.NullFloat64
	var tags string
	if err := row.Scan(&v.ID, &v.Platform, &v.PlatformVideoID, &v.Title, &v.URL, &v.ThumbURL,
		&v.PreviewURL, &v.DurationSec, &pub, &vc, &rating, &tags, &v.Description, &v.IsNew,
		&v.RotateDeg); err != nil {
		return nil, err
	}
	applyVideoScalars(v, pub, vc, rating, tags)
	return v, nil
}

// applyVideoScalars 把 videos 行里那几个「可空 / 编码过的」列装回 model.Video。
// scanVideo 和媒体库那条联表查询（library_videos.go 的 scanLibraryVideo）共用，
// 免得两处各写一份、迟早有一处漏掉某个字段。
func applyVideoScalars(v *model.Video, pub sql.NullString, vc sql.NullInt64, rating sql.NullFloat64, tags string) {
	v.PublishedAt = scanTime(pub)
	if vc.Valid {
		n := int(vc.Int64)
		v.ViewCount = &n
	}
	if rating.Valid {
		r := rating.Float64
		v.Rating = &r
	}
	v.Tags = decodeTags(tags)
}

type rowScanner interface {
	Scan(dest ...any) error
}

// ListFeed 按发布时间倒序做 keyset 分页。
func (s *Store) ListFeed(q FeedQuery) ([]*model.Video, string, error) {
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 30
	}
	where, args := feedFilter(q)
	if ts, id, ok := decodeCursor(q.Cursor); ok {
		if ts == "" {
			where = append(where, "(v.published_at IS NULL AND v.id < ?)")
			args = append(args, id)
		} else {
			where = append(where, "(COALESCE(v.published_at,'') < ? OR (COALESCE(v.published_at,'') = ? AND v.id < ?))")
			args = append(args, ts, ts, id)
		}
	}
	sqlQ := `SELECT ` + videoColumns + ` FROM videos v WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY COALESCE(v.published_at,'') DESC, v.id DESC LIMIT ?`
	rows, err := s.db.Query(sqlQ, append(args, q.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := []*model.Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var next string
	if len(out) > q.Limit {
		out = out[:q.Limit]
		last := out[len(out)-1]
		ts := ""
		if last.PublishedAt != nil {
			ts = fmtTime(*last.PublishedAt)
		}
		next = encodeCursor(ts, last.ID)
	}
	if err := s.attachPerformers(out); err != nil {
		return nil, "", err
	}
	if err := s.attachLocal(out); err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// AdjacentVideos 找出 anchor 在「同一份过滤条件、同一排序」里的上一条与下一条。
//
// 方向按列表的**视觉顺序**：列表是发布时间倒序，所以 prev = 排在它上面那条（更新），
// next = 排在下面那条（更旧）——播放器上的「下一个」就是把列表继续往下翻一张。
// 到列表头/尾就返回 nil（不是错误：意味着那个方向没有了）。
//
// anchor 一般就是刚 GetVideo 出来的那一行；只要它的 ID 与 PublishedAt 是对的就够。
func (s *Store) AdjacentVideos(anchor *model.Video, q FeedQuery) (prev, next *model.Video, err error) {
	if anchor == nil {
		return nil, nil, nil
	}
	ts := ""
	if anchor.PublishedAt != nil {
		ts = fmtTime(*anchor.PublishedAt)
	}
	// q 里的 Cursor / Limit 对「找上下家」没有意义，别让调用方误传后改变结果：这里压根不看它们。
	if prev, err = s.neighborOf(anchor, ts, q, true); err != nil {
		return nil, nil, err
	}
	if next, err = s.neighborOf(anchor, ts, q, false); err != nil {
		return nil, nil, err
	}
	found := []*model.Video{}
	if prev != nil {
		found = append(found, prev)
	}
	if next != nil {
		found = append(found, next)
	}
	// 卡片列表要的是完整一行（艺人、本地库角标）：切歌按钮上会显示片名，相关页也要能直接跳。
	if err := s.attachPerformers(found); err != nil {
		return nil, nil, err
	}
	if err := s.attachLocal(found); err != nil {
		return nil, nil, err
	}
	return prev, next, nil
}

// neighborOf 取某个方向上紧挨着的那一行。newer=true 取更新的（列表上方），false 取更旧的。
func (s *Store) neighborOf(anchor *model.Video, ts string, q FeedQuery, newer bool) (*model.Video, error) {
	where, args := feedFilter(q)
	cmp, order := "<", "DESC"
	if newer {
		cmp, order = ">", "ASC"
	}
	// published_at 为 NULL 的行按空串参与比较（与 ORDER BY COALESCE(...) 同口径），
	// 否则「时间未知」的片子永远比不出上下家。
	where = append(where, "(COALESCE(v.published_at,'') "+cmp+
		" ? OR (COALESCE(v.published_at,'') = ? AND v.id "+cmp+" ?))")
	args = append(args, ts, ts, anchor.ID)
	sqlQ := `SELECT ` + videoColumns + ` FROM videos v WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY COALESCE(v.published_at,'') ` + order + `, v.id ` + order + ` LIMIT 1`
	v, err := scanVideo(s.db.QueryRow(sqlQ, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// attachLocal 批量填充"已落到哪些本地库"，供列表角标与已下载筛选使用。
func (s *Store) attachLocal(videos []*model.Video) error {
	if len(videos) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(videos))
	for _, v := range videos {
		ids = append(ids, v.ID)
	}
	m, err := s.LocalFileMap(ids)
	if err != nil {
		return err
	}
	for _, v := range videos {
		if libs, ok := m[v.ID]; ok {
			v.LocalLibraryIDs = libs
		}
	}
	return nil
}

func (s *Store) attachPerformers(videos []*model.Video) error {
	if len(videos) == 0 {
		return nil
	}
	ids := make([]any, 0, len(videos))
	idx := map[int64]*model.Video{}
	for _, v := range videos {
		ids = append(ids, v.ID)
		idx[v.ID] = v
	}
	q := `SELECT vp.video_id, p.id, p.name, p.url, p.avatar_url, p.platform FROM video_performers vp
		JOIN performers p ON p.id = vp.performer_id WHERE vp.video_id IN (` + placeholders(len(ids)) + `)`
	rows, err := s.db.Query(q, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var vid int64
		var ref model.PerformerRef
		if err := rows.Scan(&vid, &ref.ID, &ref.Name, &ref.URL, &ref.AvatarURL, &ref.Platform); err != nil {
			return err
		}
		if v, ok := idx[vid]; ok {
			v.Performers = append(v.Performers, ref)
		}
	}
	return rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Store) GetVideo(id int64) (*model.Video, error) {
	var v model.Video
	var pub sql.NullString
	var vc sql.NullInt64
	var rating sql.NullFloat64
	var tags string
	err := s.db.QueryRow(`SELECT id, platform, platform_video_id, title, url, thumb_url, preview_url,
			duration_sec, published_at, view_count, rating, tags, description, is_new, rotate_deg
		FROM videos WHERE id = ?`, id).Scan(&v.ID, &v.Platform, &v.PlatformVideoID, &v.Title, &v.URL,
		&v.ThumbURL, &v.PreviewURL, &v.DurationSec, &pub, &vc, &rating, &tags, &v.Description, &v.IsNew,
		&v.RotateDeg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.PublishedAt = scanTime(pub)
	if vc.Valid {
		n := int(vc.Int64)
		v.ViewCount = &n
	}
	if rating.Valid {
		r := rating.Float64
		v.Rating = &r
	}
	v.Tags = decodeTags(tags)
	if err := s.attachPerformers([]*model.Video{&v}); err != nil {
		return nil, err
	}
	if err := s.attachLocal([]*model.Video{&v}); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Store) MarkVideoSeen(id int64) error {
	_, err := s.db.Exec(`UPDATE videos SET is_new = 0 WHERE id = ?`, id)
	return err
}

// CountNewVideos 数「关注」流里的新片；B 站不算在内（它有自己那条未看计数）。
func (s *Store) CountNewVideos() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM videos WHERE is_new = 1 AND platform <> 'bilibili'`).Scan(&n)
	return n, err
}

// SaveVariants 用最新解析结果替换某影片的清晰度列表。
func (s *Store) SaveVariants(videoID int64, vs []model.Variant) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM video_streams WHERE video_id = ?`, videoID); err != nil {
		return err
	}
	for _, v := range vs {
		approx := 0
		if v.ApproxBandwidth {
			approx = 1
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO video_streams
			(video_id, height, width, bandwidth, codec, url, audio_url, approx_bw, fetched_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, videoID, v.Height, v.Width, v.Bandwidth, v.Codec, v.URL,
			v.AudioURL, approx, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetVariants 返回按质量降序的清晰度列表。
func (s *Store) GetVariants(videoID int64) ([]model.Variant, time.Time, error) {
	rows, err := s.db.Query(`SELECT height, width, bandwidth, codec, url, audio_url, approx_bw, fetched_at
		FROM video_streams WHERE video_id = ? ORDER BY height DESC, bandwidth DESC`, videoID)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	var out []model.Variant
	var fetched time.Time
	for rows.Next() {
		var v model.Variant
		var approx int
		var f string
		if err := rows.Scan(&v.Height, &v.Width, &v.Bandwidth, &v.Codec, &v.URL, &v.AudioURL, &approx, &f); err != nil {
			return nil, time.Time{}, err
		}
		v.ApproxBandwidth = approx != 0
		fetched = mustParse(f)
		out = append(out, v)
	}
	return out, fetched, rows.Err()
}

func encodeCursor(ts string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ts + "|" + strconv.FormatInt(id, 10)))
}

func decodeCursor(c string) (string, int64, bool) {
	if c == "" {
		return "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", 0, false
	}
	ts, ids, ok := strings.Cut(string(raw), "|")
	if !ok {
		return "", 0, false
	}
	id, err := strconv.ParseInt(ids, 10, 64)
	if err != nil {
		return "", 0, false
	}
	return ts, id, true
}

func decodeTags(s string) []string {
	if s == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []string{}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// SetVideoRotateDeg 记住这部影片要顺时针转多少度显示（0/90/180/270）。
// 只接受这四个值：别的角度（比如 45°）在 CSS 里会把画面转出一个黑三角，播放器也没法算缩放。
func (s *Store) SetVideoRotateDeg(id int64, deg int) error {
	deg = ((deg % 360) + 360) % 360
	switch deg {
	case 0, 90, 180, 270:
	default:
		return fmt.Errorf("旋转角度只能是 0/90/180/270，收到 %d", deg)
	}
	res, err := s.db.Exec(`UPDATE videos SET rotate_deg = ? WHERE id = ?`, deg, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
