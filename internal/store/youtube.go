package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// YouTube 订阅（M6）：频道 + 视频两层。
//
// 和 M5 的 social_* 分家的理由见 docs/M6-YOUTUBE.md §3：那边的对象是「帖子 + 照片」，
// 这边是「频道 + 视频」，而且视频还要能下载进本地库（落到 videos / library_files）。
// 这里同样只做数据访问，RSS / yt-dlp 的解析都在 internal/ytsub。
//
// 两处需要留意的语义：
//   - published_at 允许为空：yt-dlp 的 flat 列频道不给时间戳，深挖回来的老条目就是这样，
//     墙排序时它们排在最后（date_source 记录是谁给的）。
//   - video_id 全局唯一，不按频道去重——同一支视频被搬运/转发很常见，
//     按视频 id 去重才不会在墙上出现两条一模一样的。

// YtChannel 是一个被订阅的 YouTube 频道。
type YtChannel struct {
	ID           int64
	ChannelID    string // UC + 22 位
	Handle       string // 不带 @，可能为空
	Title        string
	AvatarURL    string
	ProfileURL   string
	Enabled      bool
	PerformerID  int64 // 0 = 未关联（本期只占位）
	VideoCount   int
	UnseenCount  int
	BackfilledAt *time.Time
	LastSyncedAt *time.Time
	LastError    string
	CreatedAt    time.Time
}

// YtChannelBrief 是墙上每张卡片都要带的一小块频道信息。
type YtChannelBrief struct {
	ID        int64
	ChannelID string
	Handle    string
	Title     string
	AvatarURL string
}

// YtVideoInput 是抓取层交来的一条视频（RSS 与 yt-dlp 两条通道都交这个）。
type YtVideoInput struct {
	VideoID      string
	Title        string
	Description  string
	ThumbnailURL string
	PublishedAt  time.Time // 零值 = 不知道（flat 模式）
	DateSource   string    // rss / ytdlp / ""
	Views        int64
	DurationSec  int
	IsLive       bool
}

// YtVideoRow 是视频墙上的一格：视频本身 + 所属频道 + 是否已落进本地库。
type YtVideoRow struct {
	ID           int64
	ChannelRowID int64
	VideoID      string
	Title        string
	Description  string
	ThumbnailURL string
	PublishedAt  *time.Time
	DateSource   string
	Views        int64
	DurationSec  int
	IsLive       bool
	Seen         bool
	Saved        bool
	CreatedAt    time.Time
	// RotateDeg = 显示时顺时针转多少度（0/90/180/270）。给「躺着拍」的上传用：
	// 那些文件像素本身是横的、容器里也没有 rotation 元数据，播放器无从判断，
	// 只能让用户点一次「旋转」然后记住（见 migrations/0012）。
	RotateDeg  int
	Channel    YtChannelBrief
	Downloaded int // 已落进几个本地库
}

// YtVideosQuery 是视频墙的查询条件。
type YtVideosQuery struct {
	ChannelRowID int64
	UnseenOnly   bool
	SavedOnly    bool
	Cursor       string
	Limit        int
}

// YtStats 是管理页要的几个汇总数。
type YtStats struct {
	Channels int `json:"channels"`
	Videos   int `json:"videos"`
	Unseen   int `json:"unseen"`
	New24h   int `json:"new24h"`
	Errors   int `json:"errors"`
}

const ytChannelCols = `id, channel_id, handle, title, avatar_url, profile_url, enabled,
	performer_id, video_count, unseen_count, backfilled_at, last_error, last_synced_at, created_at`

func scanYtChannel(sc interface{ Scan(...any) error }) (*YtChannel, error) {
	c := &YtChannel{}
	var backfilled, synced, created sql.NullString
	// performer_id 可能为 NULL（不关联艺人），直接扫进 int64 会报错。
	var performerID sql.NullInt64
	if err := sc.Scan(&c.ID, &c.ChannelID, &c.Handle, &c.Title, &c.AvatarURL, &c.ProfileURL,
		&c.Enabled, &performerID, &c.VideoCount, &c.UnseenCount,
		&backfilled, &c.LastError, &synced, &created); err != nil {
		return nil, err
	}
	if performerID.Valid {
		c.PerformerID = performerID.Int64
	}
	c.BackfilledAt = scanTime(backfilled)
	c.LastSyncedAt = scanTime(synced)
	if t := scanTime(created); t != nil {
		c.CreatedAt = *t
	}
	return c, nil
}

// UpsertYtChannel 写入/更新一个频道。返回行 id 与「是不是新建的」
// （新建的要排队跑一次深度回填）。
func (s *Store) UpsertYtChannel(c *YtChannel) (id int64, created bool, err error) {
	if strings.TrimSpace(c.ChannelID) == "" {
		return 0, false, errors.New("频道 id 为空")
	}
	switch scanErr := s.db.QueryRow(`SELECT id FROM yt_channels WHERE channel_id = ?`, c.ChannelID).Scan(&id); {
	case scanErr == nil:
	case errors.Is(scanErr, sql.ErrNoRows):
		created = true
	default:
		return 0, false, scanErr
	}
	// performer_id 是外键：0 要写成 NULL，否则会撞 FOREIGN KEY constraint（M6 实测踩到过）。
	var performerID any
	if c.PerformerID > 0 {
		performerID = c.PerformerID
	}
	if _, err := s.db.Exec(`INSERT INTO yt_channels (channel_id, handle, title, avatar_url, profile_url, enabled, performer_id)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(channel_id) DO UPDATE SET
			handle      = CASE WHEN excluded.handle      <> '' THEN excluded.handle      ELSE yt_channels.handle END,
			title       = CASE WHEN excluded.title       <> '' THEN excluded.title       ELSE yt_channels.title END,
			avatar_url  = CASE WHEN excluded.avatar_url  <> '' THEN excluded.avatar_url  ELSE yt_channels.avatar_url END,
			profile_url = CASE WHEN excluded.profile_url <> '' THEN excluded.profile_url ELSE yt_channels.profile_url END`,
		c.ChannelID, c.Handle, c.Title, c.AvatarURL, c.ProfileURL, c.Enabled, performerID); err != nil {
		return 0, false, err
	}
	if err := s.db.QueryRow(`SELECT id FROM yt_channels WHERE channel_id = ?`, c.ChannelID).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, created, nil
}

// GetYtChannel 按行 id 取频道。
func (s *Store) GetYtChannel(id int64) (*YtChannel, error) {
	row := s.db.QueryRow(`SELECT `+ytChannelCols+` FROM yt_channels WHERE id = ?`, id)
	c, err := scanYtChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// GetYtChannelByPlatformID 按 UC… 取频道。
func (s *Store) GetYtChannelByPlatformID(channelID string) (*YtChannel, error) {
	row := s.db.QueryRow(`SELECT `+ytChannelCols+` FROM yt_channels WHERE channel_id = ?`, channelID)
	c, err := scanYtChannel(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ListYtChannels 列出频道；enabledOnly 为真时只返回启用的（同步用）。
// 排序：有未看的排前面，再按订阅时间倒序——和照片墙的筛选胶囊顺序一致。
func (s *Store) ListYtChannels(enabledOnly bool) ([]*YtChannel, error) {
	q := `SELECT ` + ytChannelCols + ` FROM yt_channels`
	if enabledOnly {
		q += ` WHERE enabled = 1`
	}
	q += ` ORDER BY (unseen_count > 0) DESC, created_at DESC, id DESC`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*YtChannel{}
	for rows.Next() {
		c, err := scanYtChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetYtChannelEnabled 启用/停用一个频道（停用后不再抓，已抓内容保留）。
func (s *Store) SetYtChannelEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE yt_channels SET enabled = ? WHERE id = ?`, enabled, id)
	return err
}

// SetYtChannelPerformer 关联艺人（本期不暴露接口，只在表上留位置）。
func (s *Store) SetYtChannelPerformer(id, performerID int64) error {
	// 0 = 解除关联，要写 NULL（外键列不接受 0）。
	var v any
	if performerID > 0 {
		v = performerID
	}
	_, err := s.db.Exec(`UPDATE yt_channels SET performer_id = ? WHERE id = ?`, v, id)
	return err
}

// DeleteYtChannel 删频道，视频随外键级联删除；已下载进本地库的影片不受影响。
func (s *Store) DeleteYtChannel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM yt_channels WHERE id = ?`, id)
	return err
}

// SetYtChannelError 写回失败原因；空串表示清空。
func (s *Store) SetYtChannelError(id int64, msg string) error {
	_, err := s.db.Exec(`UPDATE yt_channels SET last_error = ? WHERE id = ?`, msg, id)
	return err
}

// TouchYtChannelSynced 记一次增量同步完成的时间。
func (s *Store) TouchYtChannelSynced(id int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE yt_channels SET last_synced_at = ? WHERE id = ?`, fmtTime(at), id)
	return err
}

// TouchYtChannelBackfilled 记一次深度回填完成的时间。
func (s *Store) TouchYtChannelBackfilled(id int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE yt_channels SET backfilled_at = ? WHERE id = ?`, fmtTime(at), id)
	return err
}

// UpsertYtVideos 写入一批视频，返回「这次真正新插进来的行 id」（SSE 与未看角标用）。
//
// 两条通道会补同一行：RSS 给 published_at，yt-dlp flat 给 duration_sec，
// 所以每个字段都是「非空才覆盖」，谁也不知道对方补过什么。
func (s *Store) UpsertYtVideos(channelRowID int64, vids []YtVideoInput) ([]int64, error) {
	if len(vids) == 0 {
		return []int64{}, nil
	}
	// 插入前先记下哪些已经在库里：upsert 的 RowsAffected 分不出「插的」和「更新的」，
	// 而 SSE 推送与未看角标只认真正新来的。
	existing := map[string]bool{}
	ph := make([]string, 0, len(vids))
	args := make([]any, 0, len(vids))
	for _, v := range vids {
		if strings.TrimSpace(v.VideoID) == "" {
			continue
		}
		ph = append(ph, "?")
		args = append(args, v.VideoID)
	}
	if len(ph) > 0 {
		rows, err := s.db.Query(`SELECT video_id FROM yt_videos WHERE video_id IN (`+strings.Join(ph, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var vid string
			if err := rows.Scan(&vid); err != nil {
				rows.Close()
				return nil, err
			}
			existing[vid] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for _, v := range vids {
		if strings.TrimSpace(v.VideoID) == "" {
			continue
		}
		published, source := "", v.DateSource
		if !v.PublishedAt.IsZero() {
			published = fmtTime(v.PublishedAt)
		} else {
			source = ""
		}
		if _, err := tx.Exec(`INSERT INTO yt_videos
			(channel_id, video_id, title, description, thumbnail_url, published_at, date_source,
			 views, duration_sec, is_live)
			VALUES (?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(video_id) DO UPDATE SET
				title         = CASE WHEN excluded.title         <> '' THEN excluded.title         ELSE yt_videos.title END,
				description   = CASE WHEN excluded.description   <> '' THEN excluded.description   ELSE yt_videos.description END,
				thumbnail_url = CASE WHEN excluded.thumbnail_url <> '' THEN excluded.thumbnail_url ELSE yt_videos.thumbnail_url END,
				published_at  = CASE WHEN excluded.published_at  <> '' THEN excluded.published_at  ELSE yt_videos.published_at END,
				date_source   = CASE WHEN excluded.published_at  <> '' THEN excluded.date_source   ELSE yt_videos.date_source END,
				views         = MAX(yt_videos.views, excluded.views),
				duration_sec  = CASE WHEN excluded.duration_sec > 0 THEN excluded.duration_sec ELSE yt_videos.duration_sec END,
				is_live       = excluded.is_live`,
			channelRowID, v.VideoID, v.Title, v.Description, v.ThumbnailURL, published, source,
			v.Views, v.DurationSec, v.IsLive); err != nil {
			return nil, err
		}
	}
	if err := refreshYtChannelCounts(tx, channelRowID); err != nil {
		return nil, err
	}

	fresh := []int64{}
	for _, v := range vids {
		if strings.TrimSpace(v.VideoID) == "" || existing[v.VideoID] {
			continue
		}
		var id int64
		if err := tx.QueryRow(`SELECT id FROM yt_videos WHERE video_id = ?`, v.VideoID).Scan(&id); err == nil {
			fresh = append(fresh, id)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return fresh, nil
}

// DeleteYtVideosByVideoIDs 按 YouTube 的 11 位 id 删行，返回真正删掉的条数。
//
// 只给「这条不该在库里」用：目前唯一调用方是会员抢先看（`availability=subscriber_only`，
// 见 ytsub.Video.MembersOnly）。**删而不是藏**是有意的——频道会员视频「会员先看 30 天、
// 之后转公开」（频道 4kaamg 的原话：「会员优先观看的视频，计划在30天后转为公开」），
// 藏起来的话那条记录永远不会再被检查，30 天后也出不来；删掉的话下一轮同步
// （RSS 或深度回填）看见它就是公开的了，自然重新入库。
func (s *Store) DeleteYtVideosByVideoIDs(videoIDs []string) (int, error) {
	if len(videoIDs) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	channels := map[int64]struct{}{}
	deleted := 0
	for _, id := range videoIDs {
		var channelRowID int64
		err := tx.QueryRow(`SELECT channel_id FROM yt_videos WHERE video_id = ?`, id).Scan(&channelRowID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM yt_videos WHERE video_id = ?`, id); err != nil {
			return 0, err
		}
		channels[channelRowID] = struct{}{}
		deleted++
	}
	for channelRowID := range channels {
		if err := refreshYtChannelCounts(tx, channelRowID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

func refreshYtChannelCounts(tx *sql.Tx, channelRowID int64) error {
	_, err := tx.Exec(`UPDATE yt_channels SET
			video_count  = (SELECT COUNT(*) FROM yt_videos v WHERE v.channel_id = yt_channels.id AND v.hidden = 0),
			unseen_count = (SELECT COUNT(*) FROM yt_videos v WHERE v.channel_id = yt_channels.id AND v.hidden = 0 AND v.seen = 0)
		WHERE id = ?`, channelRowID)
	return err
}

// ListYtVideos 查视频墙。排序与照片墙同一套思路：时间倒序 + id 倒序兜同秒；
// 时间未知的老条目（深挖回来的）排在最后。
func (s *Store) ListYtVideos(q YtVideosQuery) ([]*YtVideoRow, string, error) {
	if q.Limit <= 0 {
		q.Limit = 36
	}
	if q.Limit > 100 {
		q.Limit = 100
	}
	where := []string{"v.hidden = 0"}
	args := []any{}
	if q.ChannelRowID > 0 {
		where = append(where, "v.channel_id = ?")
		args = append(args, q.ChannelRowID)
	}
	if q.UnseenOnly {
		where = append(where, "v.seen = 0")
	}
	if q.SavedOnly {
		where = append(where, "v.saved = 1")
	}
	if ts, id, ok := decodeYtCursor(q.Cursor); ok {
		where = append(where, `(COALESCE(v.published_at,'') < ?
			OR (COALESCE(v.published_at,'') = ? AND v.id < ?))`)
		args = append(args, ts, ts, id)
	}
	sqlQ := `SELECT v.id, v.channel_id, v.video_id, v.title, v.description, v.thumbnail_url,
			v.published_at, v.date_source, v.views, v.duration_sec, v.is_live,
			v.seen, v.saved, v.created_at, v.rotate_deg,
			c.id, c.channel_id, c.handle, c.title, c.avatar_url,
			(SELECT COUNT(*) FROM library_files lf
				JOIN videos vid ON vid.id = lf.video_id
				WHERE vid.platform = 'youtube' AND vid.platform_video_id = v.video_id)
		FROM yt_videos v
		JOIN yt_channels c ON c.id = v.channel_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY COALESCE(v.published_at,'') DESC, v.id DESC
		LIMIT ?`
	rows, err := s.db.Query(sqlQ, append(args, q.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := []*YtVideoRow{}
	for rows.Next() {
		r := &YtVideoRow{}
		var published, created sql.NullString
		if err := rows.Scan(&r.ID, &r.ChannelRowID, &r.VideoID, &r.Title, &r.Description, &r.ThumbnailURL,
			&published, &r.DateSource, &r.Views, &r.DurationSec, &r.IsLive,
			&r.Seen, &r.Saved, &created, &r.RotateDeg,
			&r.Channel.ID, &r.Channel.ChannelID, &r.Channel.Handle, &r.Channel.Title, &r.Channel.AvatarURL,
			&r.Downloaded); err != nil {
			return nil, "", err
		}
		r.PublishedAt = scanTime(published)
		if t := scanTime(created); t != nil {
			r.CreatedAt = *t
		}
		out = append(out, r)
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
		next = encodeYtCursor(ts, last.ID)
	}
	return out, next, nil
}

// ListUnseenYtVideos 列出某频道里还没看过的视频（「整频道下载」用）。
func (s *Store) ListUnseenYtVideos(channelRowID int64, limit int) ([]*YtVideoRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT v.id, v.channel_id, v.video_id, v.title, v.description, v.thumbnail_url,
			v.published_at, v.date_source, v.views, v.duration_sec, v.is_live,
			v.seen, v.saved, v.created_at, v.rotate_deg,
			c.id, c.channel_id, c.handle, c.title, c.avatar_url,
			(SELECT COUNT(*) FROM library_files lf
				JOIN videos vid ON vid.id = lf.video_id
				WHERE vid.platform = 'youtube' AND vid.platform_video_id = v.video_id)
		FROM yt_videos v
		JOIN yt_channels c ON c.id = v.channel_id
		WHERE v.channel_id = ? AND v.hidden = 0 AND v.seen = 0
		ORDER BY COALESCE(v.published_at,'') DESC, v.id DESC
		LIMIT ?`, channelRowID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*YtVideoRow{}
	for rows.Next() {
		r := &YtVideoRow{}
		var published, created sql.NullString
		if err := rows.Scan(&r.ID, &r.ChannelRowID, &r.VideoID, &r.Title, &r.Description, &r.ThumbnailURL,
			&published, &r.DateSource, &r.Views, &r.DurationSec, &r.IsLive,
			&r.Seen, &r.Saved, &created, &r.RotateDeg,
			&r.Channel.ID, &r.Channel.ChannelID, &r.Channel.Handle, &r.Channel.Title, &r.Channel.AvatarURL,
			&r.Downloaded); err != nil {
			return nil, err
		}
		r.PublishedAt = scanTime(published)
		if t := scanTime(created); t != nil {
			r.CreatedAt = *t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetYtVideo 按行 id 取一条视频。
func (s *Store) GetYtVideo(id int64) (*YtVideoRow, error) {
	row := s.db.QueryRow(`SELECT v.id, v.channel_id, v.video_id, v.title, v.description, v.thumbnail_url,
			v.published_at, v.date_source, v.views, v.duration_sec, v.is_live,
			v.seen, v.saved, v.created_at, v.rotate_deg,
			c.id, c.channel_id, c.handle, c.title, c.avatar_url,
			(SELECT COUNT(*) FROM library_files lf
				JOIN videos vid ON vid.id = lf.video_id
				WHERE vid.platform = 'youtube' AND vid.platform_video_id = v.video_id)
		FROM yt_videos v
		JOIN yt_channels c ON c.id = v.channel_id
		WHERE v.id = ?`, id)
	r := &YtVideoRow{}
	var published, created sql.NullString
	if err := row.Scan(&r.ID, &r.ChannelRowID, &r.VideoID, &r.Title, &r.Description, &r.ThumbnailURL,
		&published, &r.DateSource, &r.Views, &r.DurationSec, &r.IsLive,
		&r.Seen, &r.Saved, &created, &r.RotateDeg,
		&r.Channel.ID, &r.Channel.ChannelID, &r.Channel.Handle, &r.Channel.Title, &r.Channel.AvatarURL,
		&r.Downloaded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.PublishedAt = scanTime(published)
	if t := scanTime(created); t != nil {
		r.CreatedAt = *t
	}
	return r, nil
}

// GetYtVideoByVideoID 按 YouTube 视频 id（不是行 id）取一条。
//
// 详情页那边只有 videos.platform_video_id（字符串），要让"下载到本地库"按钮
// 落到 yt_download 上就必须从字符串反查回来。
func (s *Store) GetYtVideoByVideoID(videoID string) (*YtVideoRow, error) {
	var rowID int64
	err := s.db.QueryRow(`SELECT id FROM yt_videos WHERE video_id = ?`, videoID).Scan(&rowID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.GetYtVideo(rowID)
}

// SetYtVideoRotate 记住这条视频要顺时针转多少度显示，返回真正改动了几行。
//
// 只接受 0/90/180/270（调用方校验过）：这是「躺着拍的上传」唯一能被修正的地方——
// 文件本身没有 rotation 元数据，任何播放器都只能照横的放（见 migrations/0012）。
// 不改 `updated_at` 之类的字段：这条不是抓取侧的事实，只是显示偏好。
func (s *Store) SetYtVideoRotate(id int64, deg int) (int, error) {
	switch deg {
	case 0, 90, 180, 270:
	default:
		return 0, fmt.Errorf("旋转角度只能是 0/90/180/270，收到 %d", deg)
	}
	res, err := s.db.Exec(`UPDATE yt_videos SET rotate_deg = ? WHERE id = ?`, deg, id)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// MarkYtVideosSeen 把若干视频标为已看，并重算这些频道的未看数。
func (s *Store) MarkYtVideosSeen(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		ph = append(ph, "?")
		args = append(args, id)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 先记下受影响的频道，改动后统一重算计数。
	chanIDs := []int64{}
	rows, err := tx.Query(`SELECT DISTINCT channel_id FROM yt_videos WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int64
		if err := rows.Scan(&cid); err != nil {
			rows.Close()
			return err
		}
		chanIDs = append(chanIDs, cid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if _, err := tx.Exec(`UPDATE yt_videos SET seen = 1 WHERE id IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
		return err
	}
	for _, cid := range chanIDs {
		if err := refreshYtChannelCounts(tx, cid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkYtChannelSeen 把一个频道的视频全部标为已看。
func (s *Store) MarkYtChannelSeen(channelRowID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE yt_videos SET seen = 1 WHERE channel_id = ? AND seen = 0`, channelRowID); err != nil {
		return err
	}
	if err := refreshYtChannelCounts(tx, channelRowID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetYtVideoSaved 收藏/取消收藏。
func (s *Store) SetYtVideoSaved(id int64, saved bool) error {
	_, err := s.db.Exec(`UPDATE yt_videos SET saved = ? WHERE id = ?`, saved, id)
	return err
}

// CountUnseenYtVideos 侧边栏角标要的数（直接读冗余计数，不做全表扫描）。
func (s *Store) CountUnseenYtVideos() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COALESCE(SUM(unseen_count), 0) FROM yt_channels`).Scan(&n)
	return n, err
}

// YtStats 管理页总览。
func (s *Store) YtStats() (YtStats, error) {
	var st YtStats
	since := fmtTime(time.Now().Add(-24 * time.Hour))
	err := s.db.QueryRow(`SELECT
			(SELECT COUNT(*) FROM yt_channels),
			(SELECT COUNT(*) FROM yt_videos WHERE hidden = 0),
			(SELECT COUNT(*) FROM yt_videos WHERE hidden = 0 AND seen = 0),
			(SELECT COUNT(*) FROM yt_videos WHERE hidden = 0 AND created_at > ?),
			(SELECT COUNT(*) FROM yt_channels WHERE last_error <> '')`, since).
		Scan(&st.Channels, &st.Videos, &st.Unseen, &st.New24h, &st.Errors)
	return st, err
}

// EnsureVideoForYt 把一条 yt_videos 落成 videos 表里的普通影片（platform='youtube'），
// 返回 videos.id。下载任务从这里拿到「可以进库、可以进关注流」的影片行。
func (s *Store) EnsureVideoForYt(v *YtVideoRow) (int64, error) {
	mv := &model.Video{
		Platform:        "youtube",
		PlatformVideoID: v.VideoID,
		Title:           v.Title,
		URL:             "https://www.youtube.com/watch?v=" + v.VideoID,
		ThumbURL:        v.ThumbnailURL,
		DurationSec:     v.DurationSec,
		PublishedAt:     v.PublishedAt,
		Description:     v.Description,
	}
	if v.Views > 0 {
		n := int(v.Views)
		mv.ViewCount = &n
	}
	id, _, err := s.UpsertVideo(mv)
	return id, err
}

// FillYtVideoMeta 按需补齐一条视频的元信息（发布时间 / 时长 / 播放量）。
//
// 只补「原来没有」的字段：这些值来自 yt-dlp 的单条抓取，比 RSS/flat 更权威，
// 但下载页展示的是历史值，别因为一次补抓把已有数据改坏。
//
// 不管这条补到了什么，`meta_tries` 都归零：只要有一次拿到过东西，说明这条路当下是通的，
// 下次缺口再出现就该重新试，别被以前的失败次数挡住。
func (s *Store) FillYtVideoMeta(id int64, publishedAt *time.Time, dateSource string, durationSec int, views int64) error {
	sets := []string{"meta_tries = 0"}
	args := []any{}
	if publishedAt != nil {
		sets = append(sets, "published_at = ?", "date_source = ?")
		args = append(args, fmtTime(*publishedAt), dateSource)
	}
	if durationSec > 0 {
		sets = append(sets, "duration_sec = ?")
		args = append(args, durationSec)
	}
	if views > 0 {
		sets = append(sets, "views = MAX(views, ?)")
		args = append(args, views)
	}
	args = append(args, id)
	_, err := s.db.Exec(`UPDATE yt_videos SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

// YtMetaMaxTries 是「补了这么多次还补不到就别再算它缺」的次数。
//
// 3 次是留给偶发的：实测 2026-11-03 有一批行首次连接偶发失败、重跑一次就补上了。
// 到了 3 次还在缺的那些，基本都是**结构上补不到**的（短视频：flat 列表里没有、
// 观看页又没有长度；或者登录态失效时观看页只剩登录墙），再撞下去只是每轮白抓几个 1.3MB。
const YtMetaMaxTries = 3

// BumpYtMetaTries 给这些行的「补了几次」加一次，最多加到 YtMetaMaxTries（留着给界面认）。
func (s *Store) BumpYtMetaTries(ids []int64, by int) error {
	if len(ids) == 0 {
		return nil
	}
	if by < 1 {
		by = 1
	}
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+2)
	args = append(args, by, YtMetaMaxTries)
	for _, id := range ids {
		ph = append(ph, "?")
		args = append(args, id)
	}
	_, err := s.db.Exec(`UPDATE yt_videos SET meta_tries = MIN(meta_tries + ?, ?)
		WHERE id IN (`+strings.Join(ph, ",")+`)`, args...)
	return err
}

// ResetYtMetaTries 把所有「补过但没补到」的记账清零：用户点「补齐」（force）或换了一份
// Cookie（新登录态可能就补得上）时调，让那些行重新参与自动补。
func (s *Store) ResetYtMetaTries() error {
	_, err := s.db.Exec(`UPDATE yt_videos SET meta_tries = 0 WHERE meta_tries > 0`)
	return err
}

// YtVideoLite 是「只需要上游 id」的场景用的最小行（补发布时间、补时长）。
type YtVideoLite struct {
	ID        int64
	VideoID   string // YouTube 的 11 位 id
	ChannelID int64
	// PublishedAt 是这一行**此刻**有没有发布时间（空 = 缺）。
	//
	// 补元信息的第一遍（yt-dlp flat 列表）只能补时长，必须靠这个字段区分
	// 「补完时长就算补齐了」和「时长本来就有、日期还缺」——只看时长的话，
	// 深度回填进来的那批行会被当成本行已补齐、从此没人再去补日期（2026-11-06 实测：
	// 频道 horizon 的 15 条一直显示「时间未知」，每轮同步都说「没有缺元信息的视频」）。
	PublishedAt string
	DurationSec int
}

// ListYtVideosMissingMeta 列出缺元信息的视频：没有发布时间的，或者没有时长的（直播除外）。
//
// 为什么会有这种行：两条抓取通道各缺一半——RSS（`yt_sync` 的默认模式）有绝对时间但**不带时长**；
// yt-dlp 的 flat 列表（深度回填）有时长但没有时间戳。所以「订阅后只跑过 RSS」的那些行
// 卡片上就没有时长（2026-11-02 实测库里 184 条里 54 条这样，`date_source` 全是 rss），
// 而墙上原来那条提示条只数「缺发布时间」，这批一条都不会去补。
// 补齐的办法见 jobs 里的 yt_meta：先按频道拉一次 yt-dlp 的 flat 列表批量补时长，
// 剩下的（短视频 / 列表深度以外的老条目）再逐条走观看页。
//
// 直播不算缺时长（本来就没有固定时长），`hidden` 的行也不补——看不见，白花请求。
// force=false 时跳过 `meta_tries >= YtMetaMaxTries` 的行（已经证明补不到，见 BumpYtMetaTries）。
func (s *Store) ListYtVideosMissingMeta(limit int, force bool) ([]YtVideoLite, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT id, video_id, channel_id, COALESCE(published_at, ''), duration_sec FROM yt_videos
		WHERE hidden = 0
		  AND (COALESCE(published_at, '') = '' OR (duration_sec = 0 AND is_live = 0))`
	if !force {
		q += fmt.Sprintf(` AND meta_tries < %d`, YtMetaMaxTries)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	rows, err := s.db.Query(q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []YtVideoLite
	for rows.Next() {
		var v YtVideoLite
		if err := rows.Scan(&v.ID, &v.VideoID, &v.ChannelID, &v.PublishedAt, &v.DurationSec); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// YtMetaGaps 是「缺元信息」的四个数（墙上的提示条和补齐接口都用它）。
type YtMetaGaps struct {
	// Rows 是要补的行数：缺时间或缺时长，一行只算一次（两样都缺不会算两遍）
	Rows int
	// Dates 是缺发布时间的行数
	Dates int
	// Durations 是缺时长的行数（直播不算——直播本来就没有固定时长）
	Durations int
	// Stuck 是「补了 YtMetaMaxTries 次仍然缺」的行数：它们不计入上面三个数，
	// 界面也不该再拿它们催用户，但设置页要说清楚有这么多条是真的拿不到（见 M6 §14.24）。
	Stuck int
}

// CountYtVideosMissingMeta 数一下还有多少条缺元信息（给墙上的提示条用）。
//
// 分两个数：还值得试的（meta_tries < YtMetaMaxTries）进 Rows/Dates/Durations，
// 已经试够次数的进 Stuck——墙上的「有 N 条没有时长」只数前者，否则那 3 条永远补不到的短视频
// 会让提示条永远挂在页面顶上（2026-11-04 用户问的就是这个）。
func (s *Store) CountYtVideosMissingMeta() (YtMetaGaps, error) {
	var g YtMetaGaps
	err := s.db.QueryRow(`SELECT
		COALESCE(SUM(COALESCE(published_at, '') = '' OR (duration_sec = 0 AND is_live = 0)), 0),
		COALESCE(SUM(COALESCE(published_at, '') = ''), 0),
		COALESCE(SUM(duration_sec = 0 AND is_live = 0), 0)
		FROM yt_videos WHERE hidden = 0 AND meta_tries < ?`, YtMetaMaxTries).
		Scan(&g.Rows, &g.Dates, &g.Durations)
	if err != nil {
		return g, err
	}
	err = s.db.QueryRow(`SELECT
		COALESCE(SUM(COALESCE(published_at, '') = '' OR (duration_sec = 0 AND is_live = 0)), 0)
		FROM yt_videos WHERE hidden = 0 AND meta_tries >= ?`, YtMetaMaxTries).Scan(&g.Stuck)
	return g, err
}

// ---- 游标 ----

// YtCursor 是视频墙的 keyset 游标：(published_at, id)。
func encodeYtCursor(ts string, id int64) string {
	raw := ts + "|" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeYtCursor(c string) (ts string, id int64, ok bool) {
	if c == "" {
		return "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", 0, false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 2 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return parts[0], n, true
}
