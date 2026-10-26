package store

import (
	"database/sql"
	"encoding/json"
	"sort"
	"time"
)

// 管理页要的一份统计快照。整页只发一个请求、只读一次库：分开取的话，
// 上面那张读数卡和下面那块明细可能一个新一个旧。
//
// 计数一律是「此刻数据库里的样子」，不缓存、不落表——总量在几万条以内，
// 这些 GROUP BY 都是毫秒级。

// StatsCount 是一个「代号 + 计数」的桶：平台、分档、月份、标签都用它。
// Key 是机器可读的代号，中文名字交给前端（改文案不用动后端）。
type StatsCount struct {
	Key string
	N   int
	// Bytes 只有平台/库这类分项才带（该分项占的记录体积），其余为 0。
	Bytes int64
}

// LibraryStat 是一个本地库此刻的占用。
type LibraryStat struct {
	ID     int64
	Name   string
	Videos int
	Bytes  int64
}

// JobStat 是下载任务按状态归的三堆。
type JobStat struct {
	Done   int
	Failed int
	Active int
}

// RecentVideo 是「最近入库」的一行。
type RecentVideo struct {
	ID          int64
	Title       string
	Platform    string
	ThumbURL    string
	PublishedAt *time.Time
	FirstSeenAt *time.Time
}

// Stats 是管理页的全部读数。
type Stats struct {
	// 关注
	Performers        int
	PerformersEnabled int
	PerformersFailed  int
	// 影片
	Videos       int
	VideosNew    int
	VideosWithFile int
	// 落盘
	Files int
	Bytes int64
	// 分项
	ByPlatform []StatsCount
	ByLibrary  []LibraryStat
	ByDuration []StatsCount
	ByQuality  []StatsCount
	ByYear     []StatsCount
	Ingest     []StatsCount
	Tags       []StatsCount
	// 下载
	Downloads JobStat
	// 最近入库
	Recent []RecentVideo
}

// PerformerLocal 是某位艺人本地已收录多少。
type PerformerLocal struct {
	Videos int
	Files  int
	Bytes  int64
}

// Stats 汇总管理页的所有读数。
func (s *Store) Stats() (*Stats, error) {
	out := &Stats{}

	// ---- 关注 ----
	if err := s.db.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(enabled),0),
			COALESCE(SUM(CASE WHEN last_error <> '' THEN 1 ELSE 0 END),0)
		FROM subscriptions WHERE deleted_at IS NULL`).
		Scan(&out.Performers, &out.PerformersEnabled, &out.PerformersFailed); err != nil {
		return nil, err
	}

	// ---- 影片 ----
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(is_new),0) FROM videos`).
		Scan(&out.Videos, &out.VideosNew); err != nil {
		return nil, err
	}

	// ---- 落盘 ----
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size_bytes),0), COUNT(DISTINCT video_id) FROM library_files`).
		Scan(&out.Files, &out.Bytes, &out.VideosWithFile); err != nil {
		return nil, err
	}

	var err error
	if out.ByPlatform, err = s.statsCounts(`SELECT v.platform, COUNT(*), COALESCE(SUM(f.bytes),0)
		FROM videos v
		LEFT JOIN (SELECT video_id, SUM(size_bytes) AS bytes FROM library_files GROUP BY video_id) f
			ON f.video_id = v.id
		GROUP BY v.platform ORDER BY 2 DESC, 1`); err != nil {
		return nil, err
	}

	if out.ByLibrary, err = s.statsLibraries(); err != nil {
		return nil, err
	}

	// 时长分档的分界和平台上的「速食 / 短 / 中 / 长」一致：300 / 900 / 2400 秒。
	if out.ByDuration, err = s.statsCounts(`SELECT CASE
			WHEN duration_sec <= 0 THEN 'unknown'
			WHEN duration_sec < 300 THEN 'lt5'
			WHEN duration_sec < 900 THEN '5to15'
			WHEN duration_sec < 2400 THEN '15to40'
			ELSE 'gt40' END AS k, COUNT(*)
		FROM videos GROUP BY k`); err != nil {
		return nil, err
	}

	// 清晰度：每部影片取解析到的最高一档（video_streams 里最高的 height）。
	if out.ByQuality, err = s.statsCounts(`WITH best AS (SELECT video_id, MAX(height) AS h FROM video_streams GROUP BY video_id)
		SELECT CASE
			WHEN h >= 2160 THEN '2160p'
			WHEN h >= 1440 THEN '1440p'
			WHEN h >= 1080 THEN '1080p'
			WHEN h >= 720 THEN '720p'
			WHEN h >= 480 THEN '480p'
			WHEN h > 0 THEN 'sd'
			ELSE 'unknown' END AS k, COUNT(*)
		FROM best GROUP BY k`); err != nil {
		return nil, err
	}
	// 还没解析过清晰度的影片（video_streams 里没有记录）单独算一档。
	var withStreams int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT video_id) FROM video_streams`).Scan(&withStreams); err != nil {
		return nil, err
	}
	if out.Videos > withStreams {
		out.ByQuality = append(out.ByQuality, StatsCount{Key: "none", N: out.Videos - withStreams})
	}

	// 发布年份：顺带看出这个库是「追新」还是「补老片」。只留最近 6 个有数的年份。
	if out.ByYear, err = s.statsCounts(`SELECT substr(published_at,1,4) AS k, COUNT(*)
		FROM videos WHERE published_at IS NOT NULL AND published_at <> ''
		GROUP BY k ORDER BY k DESC LIMIT 6`); err != nil {
		return nil, err
	}
	for i, j := 0, len(out.ByYear)-1; i < j; i, j = i+1, j-1 {
		out.ByYear[i], out.ByYear[j] = out.ByYear[j], out.ByYear[i]
	}
	// 没有发布时间的那些单独算一格，免得「年份分布」加起来对不上总数。
	if unknown := out.Videos; unknown > 0 {
		var dated int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM videos WHERE published_at IS NOT NULL AND published_at <> ''`).Scan(&dated); err != nil {
			return nil, err
		}
		if unknown > dated {
			out.ByYear = append(out.ByYear, StatsCount{Key: "unknown", N: unknown - dated})
		}
	}

	// 入库趋势：最近 12 个有数的月份，按时间正序给前端画柱子。
	if out.Ingest, err = s.statsCounts(`SELECT substr(first_seen_at,1,7) AS k, COUNT(*)
		FROM videos GROUP BY k ORDER BY k DESC LIMIT 12`); err != nil {
		return nil, err
	}
	for i, j := 0, len(out.Ingest)-1; i < j; i, j = i+1, j-1 {
		out.Ingest[i], out.Ingest[j] = out.Ingest[j], out.Ingest[i]
	}

	if out.Tags, err = s.statsTopTags(12); err != nil {
		return nil, err
	}

	// ---- 下载 ----
	var st string
	var n int
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs WHERE type = 'download' GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		switch st {
		case "done":
			out.Downloads.Done += n
		case "failed":
			out.Downloads.Failed += n
		case "queued", "running", "paused":
			out.Downloads.Active += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if out.Recent, err = s.statsRecent(8); err != nil {
		return nil, err
	}
	return out, nil
}

// statsCounts 跑一条「代号 + 计数（+ 体积）」的聚合：出两列就只读两列，
// 出三列（平台、库这类分项）连带把该分项的体积读进来。
func (s *Store) statsCounts(query string, args ...any) ([]StatsCount, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []StatsCount{}
	for rows.Next() {
		var c StatsCount
		if len(cols) >= 3 {
			if err := rows.Scan(&c.Key, &c.N, &c.Bytes); err != nil {
				return nil, err
			}
		} else if err := rows.Scan(&c.Key, &c.N); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanLooseTime 读两种时间文本：平台字段是 RFC3339，而 first_seen_at 这类列是
// SQLite 的 datetime('now')（"2006-01-02 15:04:05"，UTC，不带时区）。
func scanLooseTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, ns.String); err == nil {
		return &t
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", ns.String, time.UTC); err == nil {
		return &t
	}
	return nil
}

// sortStatsCounts 按计数倒序、同数按名字正序。
func sortStatsCounts(rows []StatsCount) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].N != rows[j].N {
			return rows[i].N > rows[j].N
		}
		return rows[i].Key < rows[j].Key
	})
}

// statsLibraries 返回每个本地库的影片数与体积（没有文件的库也出现，读数为 0）。
func (s *Store) statsLibraries() ([]LibraryStat, error) {
	rows, err := s.db.Query(`SELECT l.id, l.name,
			COUNT(DISTINCT f.video_id), COALESCE(SUM(f.size_bytes),0)
		FROM libraries l LEFT JOIN library_files f ON f.library_id = l.id
		GROUP BY l.id ORDER BY 3 DESC, l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LibraryStat{}
	for rows.Next() {
		var l LibraryStat
		if err := rows.Scan(&l.ID, &l.Name, &l.Videos, &l.Bytes); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// statsTopTags 统计出现最多的标签。tags 列是 JSON 数组文本，这里在 Go 里拆，
// 不用 SQLite 的 json_each——不依赖编译期的 JSON1 扩展。
func (s *Store) statsTopTags(limit int) ([]StatsCount, error) {
	rows, err := s.db.Query(`SELECT tags FROM videos WHERE tags <> '' AND tags <> '[]'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var tags []string
		if err := json.Unmarshal([]byte(raw), &tags); err != nil {
			continue // 坏数据跳过，不拖垮整页统计
		}
		for _, t := range tags {
			if t == "" {
				continue
			}
			counts[t]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]StatsCount, 0, len(counts))
	for k, n := range counts {
		out = append(out, StatsCount{Key: k, N: n})
	}
	// 次数相同的按名字排，保证同一份数据每次给出同一顺序。
	sortStatsCounts(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// statsRecent 返回最近入库的影片。
func (s *Store) statsRecent(limit int) ([]RecentVideo, error) {
	rows, err := s.db.Query(`SELECT id, title, platform, thumb_url, published_at, first_seen_at
		FROM videos ORDER BY first_seen_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecentVideo{}
	for rows.Next() {
		var r RecentVideo
		var pub, seen sql.NullString
		if err := rows.Scan(&r.ID, &r.Title, &r.Platform, &r.ThumbURL, &pub, &seen); err != nil {
			return nil, err
		}
		r.PublishedAt = scanLooseTime(pub)
		r.FirstSeenAt = scanLooseTime(seen)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LocalVideoCount 返回某位艺人**本地已收录**的影片数。
// 平台资料页并不给「影片总数」（Pornhub 只给播放量和订阅数），所以艺人页那个「N 部影片」
// 用的是这个本地数——它是我们在库里确实收着的片子，和下面的影片列表一一对应。
func (s *Store) LocalVideoCount(performerID int64) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM video_performers WHERE performer_id = ?`, performerID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// PerformerLocalCounts 返回每位艺人本地收录的影片数、已落盘数与体积，键是艺人 id。
func (s *Store) PerformerLocalCounts() (map[int64]PerformerLocal, error) {
	out := map[int64]PerformerLocal{}
	rows, err := s.db.Query(`SELECT performer_id, COUNT(*) FROM video_performers GROUP BY performer_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		v := out[id]
		v.Videos = n
		out[id] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.Query(`SELECT vp.performer_id, COUNT(DISTINCT lf.video_id), COALESCE(SUM(lf.size_bytes),0)
		FROM video_performers vp JOIN library_files lf ON lf.video_id = vp.video_id
		GROUP BY vp.performer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var files int
		var bytes int64
		if err := rows.Scan(&id, &files, &bytes); err != nil {
			return nil, err
		}
		v := out[id]
		v.Files = files
		v.Bytes = bytes
		out[id] = v
	}
	return out, rows.Err()
}
