package store

import (
	"database/sql"
	"time"
)

// B 站（M7）独立页面的数据访问。
//
// 为什么这里不再复制一套 bili_channels / bili_videos：B 站的数据模型和 Pornhub 完全一样
// ——UP 主就是 performers 的一行，投稿就是 videos 的一行（platform='bilibili'）。
// 变的是「界面语义」：它是「订阅一个内容源」，不是「追一个艺人」，所以要单独一页、
// 单独一个管理页签，而不是塞进「艺人」和「关注管理」里。
//
// 于是这里只做一件事：**按平台取数**。同一份数据两处存储（像 yt_* 那样）会立刻长出
// 两个真相，同步/下载/播放全都要写两遍，错一处就两边对不上。
//
// 一个必须记住的语义：is_new 就是「未看」。B 站投稿在同步时就进了 videos（不像 YouTube
// 是下载时才落库），所以墙上的未看角标 = 这些行还没被点开过。点开（/api/videos/{id}/seen）
// 或「全部标为已看」都只是把 is_new 清 0。

// BiliChannel 是 B 站 UP 主：艺人行 + 订阅行 + 本地计数合成的视图。
type BiliChannel struct {
	ID              int64 // performers.id
	MID             string
	Name            string
	AvatarURL       string
	URL             string
	SubscriberCount *int
	VideoCount      int // 本地已收录（video_performers 计数）
	UnseenCount     int // 其中 is_new = 1
	Downloaded      int // 已落盘到任一本地库的条数
	SubscriptionID  int64
	Enabled         bool
	BackfillDone    bool
	BackfillPage    int
	TotalPages      *int
	LastSyncAt      *time.Time
	NextSyncAt      *time.Time
	LastError       string
}

// BiliStats 是管理页顶部那几个读数。
type BiliStats struct {
	Channels   int `json:"channels"`
	Videos     int `json:"videos"`
	Unseen     int `json:"unseen"`
	Downloaded int `json:"downloaded"`
	Errors     int `json:"errors"`
}

// biliChannelQuery 只列「还在订阅里」的 UP 主：取消订阅是软删（deleted_at），
// 删掉之后不该继续出现在墙上和管理页里——这和 YouTube 那边「删除即消失」一致。
const biliChannelQuery = `
SELECT p.id, p.performer_id, p.name, p.avatar_url, p.url, p.subscriber_count,
       su.id, su.enabled, su.backfill_done, su.backfill_page, su.backfill_total_pages,
       su.last_sync_at, su.next_sync_at, su.last_error,
       (SELECT COUNT(*) FROM video_performers vp JOIN videos v ON v.id = vp.video_id
          WHERE vp.performer_id = p.id AND v.platform = 'bilibili'),
       (SELECT COUNT(*) FROM video_performers vp JOIN videos v ON v.id = vp.video_id
          WHERE vp.performer_id = p.id AND v.platform = 'bilibili' AND v.is_new = 1),
       (SELECT COUNT(DISTINCT vp.video_id) FROM video_performers vp
          JOIN library_files lf ON lf.video_id = vp.video_id
          WHERE vp.performer_id = p.id)
FROM performers p
JOIN subscriptions su ON su.performer_id = p.id AND su.deleted_at IS NULL
WHERE p.platform = 'bilibili'`

// biliChannelOrder 让列表稳定：最近同步过的排前面，没同步过的按加进来的时间。
const biliChannelOrder = ` ORDER BY COALESCE(su.last_sync_at, su.created_at) DESC`

// ListBiliChannels 返回所有订阅中的 UP 主。
func (s *Store) ListBiliChannels() ([]*BiliChannel, error) {
	rows, err := s.db.Query(biliChannelQuery + biliChannelOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*BiliChannel{}
	for rows.Next() {
		c, err := scanBiliChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetBiliChannel 按 performers.id 取一位 UP 主（不要求已订阅）。
func (s *Store) GetBiliChannel(id int64) (*BiliChannel, error) {
	row := s.db.QueryRow(biliChannelQuery+` AND p.id = ?`+biliChannelOrder, id)
	c, err := scanBiliChannel(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func scanBiliChannel(sc interface{ Scan(...any) error }) (*BiliChannel, error) {
	c := &BiliChannel{}
	var subID sql.NullInt64
	var enabled, backfillDone sql.NullBool
	var page int
	var totalPages sql.NullInt64
	var lastSync, nextSync sql.NullString
	if err := sc.Scan(&c.ID, &c.MID, &c.Name, &c.AvatarURL, &c.URL, &c.SubscriberCount,
		&subID, &enabled, &backfillDone, &page, &totalPages,
		&lastSync, &nextSync, &c.LastError,
		&c.VideoCount, &c.UnseenCount, &c.Downloaded); err != nil {
		return nil, err
	}
	c.SubscriptionID = subID.Int64
	c.Enabled = enabled.Bool
	c.BackfillDone = backfillDone.Bool
	c.BackfillPage = page
	if totalPages.Valid {
		n := int(totalPages.Int64)
		c.TotalPages = &n
	}
	c.LastSyncAt = scanTime(lastSync)
	c.NextSyncAt = scanTime(nextSync)
	return c, nil
}

// BiliStats 汇总 UP 主数 / 收录数 / 未看数 / 已落盘数 / 出错数。
//
// 影片那三个数只算「还在订阅里」的 UP 主的投稿，和墙（/api/bilibili/videos）同一把尺子：
// 取消订阅后如果读数还挂着那批片，会出现「未看 253、点进去一条都没有」这种自相矛盾的界面。
//
// 每一列都要 COALESCE：没有 UP 主（或没有投稿）时 SUM(...) 返回 NULL，扫进 int 会直接报
// 「converting NULL to int is unsupported」——管理页就会显示「读取统计失败」（2026-10-29 实测，删空 UP 主后必现）。
func (s *Store) BiliStats() (BiliStats, error) {
	var st BiliStats
	err := s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN last_error <> '' THEN 1 ELSE 0 END), 0)
		  FROM subscriptions su JOIN performers p ON p.id = su.performer_id
		 WHERE p.platform = 'bilibili' AND su.deleted_at IS NULL`).Scan(&st.Channels, &st.Errors)
	if err != nil {
		return st, err
	}
	err = s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN v.is_new = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN lf.video_id IS NOT NULL THEN 1 ELSE 0 END), 0)
		  FROM videos v
		  LEFT JOIN (SELECT DISTINCT video_id FROM library_files) lf ON lf.video_id = v.id
		 WHERE v.platform = 'bilibili' AND `+liveSubFilter("v.id")).Scan(&st.Videos, &st.Unseen, &st.Downloaded)
	return st, err
}

// CountBiliUnseen 数「B 站还有多少条没点开过」——侧边栏角标用。
// 口径同 BiliStats：取消订阅的 UP 主不算（否则角标点进去是空的）。
func (s *Store) CountBiliUnseen() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM videos v
		WHERE v.platform = 'bilibili' AND v.is_new = 1 AND `+liveSubFilter("v.id")).Scan(&n)
	return n, err
}

// MarkBiliSeen 把 B 站投稿标成已看；performerID > 0 时只标这一位 UP 主的。
//
// 返回标完之后剩下的未看数，前端拿它直接更新角标（和 YouTube 的 /yt/videos/seen 同款）。
func (s *Store) MarkBiliSeen(performerID int64) (int, error) {
	if performerID > 0 {
		if _, err := s.db.Exec(`UPDATE videos SET is_new = 0
			WHERE platform = 'bilibili' AND id IN (
				SELECT video_id FROM video_performers WHERE performer_id = ?)`, performerID); err != nil {
			return 0, err
		}
	} else if _, err := s.db.Exec(`UPDATE videos SET is_new = 0
		WHERE platform = 'bilibili' AND `+liveSubFilter("videos.id")); err != nil {
		return 0, err
	}
	return s.CountBiliUnseen()
}

// ListUnseenVideoIDs 取某位 UP 主（0 = 全部 B 站）还没看的投稿 id，供「下载未看」批量排队。
func (s *Store) ListUnseenVideoIDs(platform string, performerID int64, limit int) ([]int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT v.id FROM videos v
		JOIN video_performers vp ON vp.video_id = v.id
		WHERE v.platform = ? AND v.is_new = 1`
	args := []any{platform}
	if platform == "bilibili" {
		// 「下载未看」不该把已经取消订阅的 UP 主的投稿拉下来。
		q += ` AND ` + liveSubFilter("v.id")
	}
	if performerID > 0 {
		q += ` AND vp.performer_id = ?`
		args = append(args, performerID)
	}
	q += ` ORDER BY COALESCE(v.published_at, '') DESC, v.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
