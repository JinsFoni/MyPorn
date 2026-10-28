package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mario/myporn/internal/model"
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
}

// ListFeed 按发布时间倒序做 keyset 分页。
func (s *Store) ListFeed(q FeedQuery) ([]*model.Video, string, error) {
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 30
	}
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
	if ts, id, ok := decodeCursor(q.Cursor); ok {
		if ts == "" {
			where = append(where, "(v.published_at IS NULL AND v.id < ?)")
			args = append(args, id)
		} else {
			where = append(where, "(COALESCE(v.published_at,'') < ? OR (COALESCE(v.published_at,'') = ? AND v.id < ?))")
			args = append(args, ts, ts, id)
		}
	}
	sqlQ := `SELECT v.id, v.platform, v.platform_video_id, v.title, v.url, v.thumb_url, v.preview_url,
			v.duration_sec, v.published_at, v.view_count, v.rating, v.tags, v.description, v.is_new
		FROM videos v WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY COALESCE(v.published_at,'') DESC, v.id DESC LIMIT ?`
	rows, err := s.db.Query(sqlQ, append(args, q.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := []*model.Video{}
	for rows.Next() {
		v := &model.Video{}
		var pub sql.NullString
		var vc sql.NullInt64
		var rating sql.NullFloat64
		var tags string
		if err := rows.Scan(&v.ID, &v.Platform, &v.PlatformVideoID, &v.Title, &v.URL, &v.ThumbURL,
			&v.PreviewURL, &v.DurationSec, &pub, &vc, &rating, &tags, &v.Description, &v.IsNew); err != nil {
			return nil, "", err
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
	q := `SELECT vp.video_id, p.id, p.name, p.url FROM video_performers vp
		JOIN performers p ON p.id = vp.performer_id WHERE vp.video_id IN (` + placeholders(len(ids)) + `)`
	rows, err := s.db.Query(q, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var vid int64
		var ref model.PerformerRef
		if err := rows.Scan(&vid, &ref.ID, &ref.Name, &ref.URL); err != nil {
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
			duration_sec, published_at, view_count, rating, tags, description, is_new
		FROM videos WHERE id = ?`, id).Scan(&v.ID, &v.Platform, &v.PlatformVideoID, &v.Title, &v.URL,
		&v.ThumbURL, &v.PreviewURL, &v.DurationSec, &pub, &vc, &rating, &tags, &v.Description, &v.IsNew)
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

func (s *Store) CountNewVideos() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM videos WHERE is_new = 1`).Scan(&n)
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
