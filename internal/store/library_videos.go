package store

import (
	"database/sql"
	"strconv"
	"strings"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// 媒体库页面的排序。空串等同 recent。
const (
	LibrarySortRecent   = "recent"
	LibrarySortSize     = "size"
	LibrarySortDuration = "duration"
	LibrarySortTitle    = "title"
)

// LibraryVideoQuery 是「媒体库」页面的筛选条件。
//
// 和关注流的 FeedQuery 是两件事：那边按 videos 的发布时间翻墙，这边按**落盘记录**
// （library_files.created_at）翻，因为用户在这里看的是「我什么时候下回来的」。
type LibraryVideoQuery struct {
	LibraryID int64  // 0 = 全部库
	Platform  string // 空 = 不限平台
	Query     string // 标题 / 文件名模糊匹配
	Sort      string
	Cursor    string
	Limit     int
}

// LibraryVideo 是「本地库里的一行影片」：落盘记录 + 影片展示信息 + 库名。
type LibraryVideo struct {
	File        *model.LibraryFile
	Video       *model.Video
	LibraryName string
	// sortKey 是这一行在**当前排序**下的键，下一页游标就取最后一行的它。
	// created_at 原样保存（库里是 `YYYY-MM-DD HH:MM:SS`，定宽，字符串比较即时间比较）。
	sortKey string
}

// LibraryVideoCounter 是「某个库有多少部、占多大」。
type LibraryVideoCounter struct {
	LibraryID int64
	Name      string
	FileCount int64
	SizeBytes int64
}

// 一行 = library_files 的全部列（16）+ 库名（1）+ videos 的展示列（14）。
// 列名和 scanLibraryVideo 的扫描顺序必须一一对应，改一处就得改另一处。
const libraryVideoSelect = `lf.id, lf.video_id, lf.library_id, lf.rel_dir, lf.video_name, lf.video_path,
	lf.nfo_path, lf.poster_path, lf.thumb_path, lf.fanart_path, lf.strm_path,
	lf.size_bytes, lf.width, lf.height, lf.duration_sec, lf.created_at, l.name, ` + videoColumns

func scanLibraryVideo(row rowScanner) (*LibraryVideo, error) {
	var (
		f       model.LibraryFile
		created string
		name    string
		v       model.Video
		pub     sql.NullString
		vc      sql.NullInt64
		rating  sql.NullFloat64
		tags    string
	)
	if err := row.Scan(&f.ID, &f.VideoID, &f.LibraryID, &f.RelDir, &f.VideoName, &f.VideoPath,
		&f.NFOPath, &f.PosterPath, &f.ThumbPath, &f.FanartPath, &f.StrmPath,
		&f.SizeBytes, &f.Width, &f.Height, &f.DurationSec, &created, &name,
		&v.ID, &v.Platform, &v.PlatformVideoID, &v.Title, &v.URL, &v.ThumbURL, &v.PreviewURL,
		&v.DurationSec, &pub, &vc, &rating, &tags, &v.Description, &v.IsNew); err != nil {
		return nil, err
	}
	f.CreatedAt = parseSQLiteTime(created)
	applyVideoScalars(&v, pub, vc, rating, tags)
	return &LibraryVideo{File: &f, Video: &v, LibraryName: name, sortKey: created}, nil
}

// libraryVideoFilter 把筛选条件翻成 WHERE 片段（lf = library_files，v = videos）。
func libraryVideoFilter(q LibraryVideoQuery) ([]string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if q.LibraryID > 0 {
		where = append(where, "lf.library_id = ?")
		args = append(args, q.LibraryID)
	}
	if q.Platform != "" {
		where = append(where, "v.platform = ?")
		args = append(args, q.Platform)
	}
	if q.Query != "" {
		// 两边都匹配：影库里的标题和磁盘上的文件名不一定一样（下载时按模板改过名）。
		where = append(where, "(v.title LIKE ? OR lf.video_name LIKE ?)")
		pattern := "%" + q.Query + "%"
		args = append(args, pattern, pattern)
	}
	return where, args
}

// libraryVideoOrder 返回这一种排序的 ORDER BY、游标键列，以及是否降序。
func libraryVideoOrder(sort string) (orderBy, keyExpr string, desc bool) {
	switch sort {
	case LibrarySortSize:
		return "lf.size_bytes DESC, lf.id DESC", "lf.size_bytes", true
	case LibrarySortDuration:
		return "lf.duration_sec DESC, lf.id DESC", "lf.duration_sec", true
	case LibrarySortTitle:
		// 文件名而不是标题：媒体库是「磁盘上有什么」，排序也该按磁盘上的名字
		return "lf.video_name ASC, lf.id ASC", "lf.video_name", false
	default:
		return "lf.created_at DESC, lf.id DESC", "lf.created_at", true
	}
}

// ListLibraryVideos 按落盘时间（或体积 / 时长 / 文件名）做 keyset 分页。
func (s *Store) ListLibraryVideos(q LibraryVideoQuery) ([]*LibraryVideo, string, error) {
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 30
	}
	where, args := libraryVideoFilter(q)
	orderBy, keyExpr, desc := libraryVideoOrder(q.Sort)

	if key, id, ok := decodeCursor(q.Cursor); ok {
		cmp, idCmp := "<", "<"
		if !desc {
			cmp, idCmp = ">", ">"
		}
		// 数字列必须 CAST：游标里的键是字符串，直接比会变成字典序（"9" > "10"）。
		ph := "?"
		if q.Sort == LibrarySortSize || q.Sort == LibrarySortDuration {
			ph = "CAST(? AS INTEGER)"
		}
		where = append(where, "("+keyExpr+" "+cmp+" "+ph+
			" OR ("+keyExpr+" = "+ph+" AND lf.id "+idCmp+" ?))")
		args = append(args, key, key, id)
	}

	sqlQ := `SELECT ` + libraryVideoSelect + `
		FROM library_files lf
		JOIN videos v ON v.id = lf.video_id
		JOIN libraries l ON l.id = lf.library_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY ` + orderBy + ` LIMIT ?`
	rows, err := s.db.Query(sqlQ, append(args, q.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := []*LibraryVideo{}
	// 同一部影片可能同时躺在两个库里 → 同一个 video 指针共享，attachPerformers 才不会
	// 只给最后一行补艺人（它内部按 video_id 建索引）。
	seen := map[int64]*model.Video{}
	videos := []*model.Video{}
	for rows.Next() {
		lv, err := scanLibraryVideo(rows)
		if err != nil {
			return nil, "", err
		}
		if shared, ok := seen[lv.File.VideoID]; ok {
			lv.Video = shared
		} else {
			seen[lv.File.VideoID] = lv.Video
			videos = append(videos, lv.Video)
		}
		out = append(out, lv)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var next string
	if len(out) > q.Limit {
		out = out[:q.Limit]
		last := out[len(out)-1]
		keys := last.sortKey
		// 非时间排序的键要按各自口径重新取，别把 created_at 当体积用
		switch q.Sort {
		case LibrarySortSize:
			keys = strconv.FormatInt(last.File.SizeBytes, 10)
		case LibrarySortDuration:
			keys = strconv.FormatInt(int64(last.File.DurationSec), 10)
		case LibrarySortTitle:
			keys = last.File.VideoName
		}
		next = encodeCursor(keys, last.File.ID)
	}

	// 卡片上要显示艺人（平台影片才有），一次批量补，别在循环里逐个查。
	if err := s.attachPerformers(videos); err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// LibraryVideoSummary 返回当前筛选下的总条数与总占用，外加**按库分组**的计数。
//
// 分组计数只跟随 Query / Platform，不跟 LibraryID：它画的是筛选胶囊上的数字，
// 得先看得见「每个库各有多少」才点得下去。
func (s *Store) LibraryVideoSummary(q LibraryVideoQuery) (total, bytes int64, counters []LibraryVideoCounter, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(lf.size_bytes),0)
		FROM library_files lf JOIN videos v ON v.id = lf.video_id
		WHERE `+strings.Join(mustFilter(q), " AND "), mustArgs(q)...).Scan(&total, &bytes); err != nil {
		return 0, 0, nil, err
	}

	byLib := q
	byLib.LibraryID = 0
	rows, err := s.db.Query(`SELECT lf.library_id, l.name, COUNT(*), COALESCE(SUM(lf.size_bytes),0)
		FROM library_files lf
		JOIN videos v ON v.id = lf.video_id
		JOIN libraries l ON l.id = lf.library_id
		WHERE `+strings.Join(mustFilter(byLib), " AND ")+`
		GROUP BY lf.library_id, l.name ORDER BY l.is_default DESC, l.id`, mustArgs(byLib)...)
	if err != nil {
		return 0, 0, nil, err
	}
	defer rows.Close()
	counters = []LibraryVideoCounter{}
	for rows.Next() {
		var c LibraryVideoCounter
		if err := rows.Scan(&c.LibraryID, &c.Name, &c.FileCount, &c.SizeBytes); err != nil {
			return 0, 0, nil, err
		}
		counters = append(counters, c)
	}
	return total, bytes, counters, rows.Err()
}

func mustFilter(q LibraryVideoQuery) []string {
	w, _ := libraryVideoFilter(q)
	return w
}

func mustArgs(q LibraryVideoQuery) []any {
	_, a := libraryVideoFilter(q)
	return a
}
