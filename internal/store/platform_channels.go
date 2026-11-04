package store

import (
	"database/sql"
	"time"
)

// 平台的「订阅源」列表数据访问：B 站的 UP 主、krx18 的 tag。
//
// 为什么这里不再复制一套 bili_channels / krx_channels：两家的数据模型和 Pornhub 完全一样
// ——订阅源就是 performers 的一行，投稿就是 videos 的一行（platform='bilibili' / 'krx18'）。
// 变的是「界面语义」：它们是「订阅一个内容源」，不是「追一个艺人」，所以各自一页、
// 各自一个管理页签，而不是塞进「艺人」和「关注管理」里。
//
// 于是这里只做一件事：**按平台取数**。同一份数据两处存储（像 yt_* 那样）会立刻长出
// 两个真相，同步/下载/播放全都要写两遍，错一处就两边对不上。
//
// krx18 的「订阅源」是 tag 归档页（/tag/<女优名>/）：站上没有艺人主页，
// 但每部片子都打了 tag，所以一位女优 = 一个 tag = 一行 performers。
//
// 一个必须记住的语义：is_new 就是「未看」。B 站投稿和 krx18 影片都是同步时就进了
// videos（不像 YouTube 是下载时才落库），所以墙上的未看角标 = 这些行还没被点开过。
// 点开（/api/videos/{id}/seen）或「全部标为已看」都只是把 is_new 清 0。

// PlatformChannel 是一个订阅源：艺人行 + 订阅行 + 本地计数合成的视图。
//
// 名字不叫 BiliChannel 是因为它已经两家共用了；字段 PlatformID 在 B 站是 UP 主的 mid，
// 在 krx18 是 tag 的 slug。
type PlatformChannel struct {
	ID              int64 // performers.id
	PlatformID      string
	Name            string
	AvatarURL       string
	URL             string
	SubscriberCount *int
	SiteCount       int // 站点自己给的条数（B 站 UP 主的投稿数 / krx18 标签的 count）
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

// BiliChannel 是 PlatformChannel 的老名字（B 站那边的 handler / 测试还在用）。
type BiliChannel = PlatformChannel

// PlatformStats 是管理页顶部那几个读数（按平台算）。
type PlatformStats struct {
	Channels   int `json:"channels"`
	Videos     int `json:"videos"`
	Unseen     int `json:"unseen"`
	Downloaded int `json:"downloaded"`
	Errors     int `json:"errors"`
}

// platformChannelQuery 只列「还在订阅里」的订阅源：取消订阅是软删（deleted_at），
// 删掉之后不该继续出现在墙上和管理页里——这和 YouTube 那边「删除即消失」一致。
//
// 三个 ? 都是平台键（两个计数子查询 + WHERE），参数要按顺序传三次。
const platformChannelQuery = `
SELECT p.id, p.performer_id, p.name, p.avatar_url, p.url, p.subscriber_count,
       COALESCE(p.video_count, 0),
       su.id, su.enabled, su.backfill_done, su.backfill_page, su.backfill_total_pages,
       su.last_sync_at, su.next_sync_at, su.last_error,
       (SELECT COUNT(*) FROM video_performers vp JOIN videos v ON v.id = vp.video_id
          WHERE vp.performer_id = p.id AND v.platform = ?),
       (SELECT COUNT(*) FROM video_performers vp JOIN videos v ON v.id = vp.video_id
          WHERE vp.performer_id = p.id AND v.platform = ? AND v.is_new = 1),
       (SELECT COUNT(DISTINCT vp.video_id) FROM video_performers vp
          JOIN library_files lf ON lf.video_id = vp.video_id
          WHERE vp.performer_id = p.id)
FROM performers p
JOIN subscriptions su ON su.performer_id = p.id AND su.deleted_at IS NULL
WHERE p.platform = ?`

// platformChannelOrder 让列表稳定：最近同步过的排前面，没同步过的按加进来的时间。
const platformChannelOrder = ` ORDER BY COALESCE(su.last_sync_at, su.created_at) DESC`

// ListPlatformChannels 返回某平台所有订阅中的订阅源。
func (s *Store) ListPlatformChannels(platform string) ([]*PlatformChannel, error) {
	rows, err := s.db.Query(platformChannelQuery+platformChannelOrder, platform, platform, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PlatformChannel{}
	for rows.Next() {
		c, err := scanPlatformChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetPlatformChannel 按 performers.id 取一个订阅源（不要求已订阅）。
func (s *Store) GetPlatformChannel(platform string, id int64) (*PlatformChannel, error) {
	row := s.db.QueryRow(platformChannelQuery+` AND p.id = ?`+platformChannelOrder, platform, platform, platform, id)
	c, err := scanPlatformChannel(row)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func scanPlatformChannel(sc interface{ Scan(...any) error }) (*PlatformChannel, error) {
	c := &PlatformChannel{}
	var subID sql.NullInt64
	var enabled, backfillDone sql.NullBool
	var page int
	var totalPages sql.NullInt64
	var lastSync, nextSync sql.NullString
	if err := sc.Scan(&c.ID, &c.PlatformID, &c.Name, &c.AvatarURL, &c.URL, &c.SubscriberCount,
		&c.SiteCount, &subID, &enabled, &backfillDone, &page, &totalPages,
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

// PlatformStats 汇总订阅源数 / 收录数 / 未看数 / 已落盘数 / 出错数。
//
// 影片那三个数只算「还在订阅里」的 UP 主的投稿，和墙（/api/bilibili/videos）同一把尺子：
// 取消订阅后如果读数还挂着那批片，会出现「未看 253、点进去一条都没有」这种自相矛盾的界面。
//
// 每一列都要 COALESCE：没有 UP 主（或没有投稿）时 SUM(...) 返回 NULL，扫进 int 会直接报
// 「converting NULL to int is unsupported」——管理页就会显示「读取统计失败」（2026-10-29 实测，删空 UP 主后必现）。
func (s *Store) PlatformStats(platform string) (PlatformStats, error) {
	var st PlatformStats
	err := s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN last_error <> '' THEN 1 ELSE 0 END), 0)
		  FROM subscriptions su JOIN performers p ON p.id = su.performer_id
		 WHERE p.platform = ? AND su.deleted_at IS NULL`, platform).Scan(&st.Channels, &st.Errors)
	if err != nil {
		return st, err
	}
	err = s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN v.is_new = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN lf.video_id IS NOT NULL THEN 1 ELSE 0 END), 0)
		  FROM videos v
		  LEFT JOIN (SELECT DISTINCT video_id FROM library_files) lf ON lf.video_id = v.id
		 WHERE v.platform = ? AND `+liveSubFilter("v.id"), platform).Scan(&st.Videos, &st.Unseen, &st.Downloaded)
	return st, err
}

// CountPlatformUnseen 数「这个平台还有多少条没点开过」——侧边栏角标用。
// 口径同 PlatformStats：取消订阅的订阅源不算（否则角标点进去是空的）。
func (s *Store) CountPlatformUnseen(platform string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM videos v
		WHERE v.platform = ? AND v.is_new = 1 AND `+liveSubFilter("v.id"), platform).Scan(&n)
	return n, err
}

// MarkPlatformSeen 把某个平台的投稿标成已看；performerID > 0 时只标这一个订阅源的。
//
// 返回标完之后剩下的未看数，前端拿它直接更新角标（和 YouTube 的 /yt/videos/seen 同款）。
func (s *Store) MarkPlatformSeen(platform string, performerID int64) (int, error) {
	if performerID > 0 {
		if _, err := s.db.Exec(`UPDATE videos SET is_new = 0
			WHERE platform = ? AND id IN (
				SELECT video_id FROM video_performers WHERE performer_id = ?)`, platform, performerID); err != nil {
			return 0, err
		}
	} else if _, err := s.db.Exec(`UPDATE videos SET is_new = 0
		WHERE platform = ? AND `+liveSubFilter("videos.id"), platform); err != nil {
		return 0, err
	}
	return s.CountPlatformUnseen(platform)
}

// ---------- B 站的薄封装（handler / 测试用的还是这些名字） ----------

const platformBili = "bilibili"

// BiliStats 是 B 站那几个读数（PlatformStats 的老名字）。
type BiliStats = PlatformStats

// ListBiliChannels 返回所有订阅中的 UP 主。
func (s *Store) ListBiliChannels() ([]*BiliChannel, error) {
	return s.ListPlatformChannels(platformBili)
}

// GetBiliChannel 按 performers.id 取一位 UP 主（不要求已订阅）。
func (s *Store) GetBiliChannel(id int64) (*BiliChannel, error) {
	return s.GetPlatformChannel(platformBili, id)
}

// BiliStats 取 B 站的汇总读数。
func (s *Store) BiliStats() (BiliStats, error) { return s.PlatformStats(platformBili) }

// CountBiliUnseen 数 B 站还有多少条没点开过。
func (s *Store) CountBiliUnseen() (int, error) { return s.CountPlatformUnseen(platformBili) }

// MarkBiliSeen 把 B 站投稿标成已看（performerID 为 0 时是整个 B 站）。
func (s *Store) MarkBiliSeen(performerID int64) (int, error) {
	return s.MarkPlatformSeen(platformBili, performerID)
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
	if platform == "bilibili" || platform == "krx18" {
		// 「下载未看」不该把已经取消订阅的订阅源的投稿拉下来。
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
