package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

const libraryCols = `id, name, root_path, enabled, is_default, upload_policy, openlist_path, naming_template, created_at`

func scanLibrary(sc interface{ Scan(...any) error }) (*model.Library, error) {
	var l model.Library
	var created string
	if err := sc.Scan(&l.ID, &l.Name, &l.RootPath, &l.Enabled, &l.IsDefault,
		&l.UploadPolicy, &l.OpenListPath, &l.NamingTemplate, &created); err != nil {
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		l.CreatedAt = t
	}
	return &l, nil
}

func (s *Store) CreateLibrary(l *model.Library) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// 第一个库自动成为默认库，省得用户建完发现下载没落点。
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM libraries`).Scan(&count); err != nil {
		return 0, err
	}
	if count == 0 {
		l.IsDefault = true
	}
	res, err := tx.Exec(`INSERT INTO libraries (name, root_path, enabled, is_default, upload_policy, openlist_path, naming_template)
		VALUES (?,?,?,?,?,?,?)`,
		l.Name, l.RootPath, boolInt(l.Enabled), boolInt(l.IsDefault), l.UploadPolicy, l.OpenListPath, l.NamingTemplate)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if l.IsDefault {
		if _, err := tx.Exec(`UPDATE libraries SET is_default = 0 WHERE id <> ?`, id); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (s *Store) UpdateLibrary(l *model.Library) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE libraries SET name = ?, root_path = ?, enabled = ?, is_default = ?,
		upload_policy = ?, openlist_path = ?, naming_template = ? WHERE id = ?`,
		l.Name, l.RootPath, boolInt(l.Enabled), boolInt(l.IsDefault), l.UploadPolicy, l.OpenListPath,
		l.NamingTemplate, l.ID); err != nil {
		return err
	}
	if l.IsDefault {
		if _, err := tx.Exec(`UPDATE libraries SET is_default = 0 WHERE id <> ?`, l.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteLibrary(id int64) error {
	_, err := s.db.Exec(`DELETE FROM libraries WHERE id = ?`, id)
	return err
}

func (s *Store) GetLibrary(id int64) (*model.Library, error) {
	row := s.db.QueryRow(`SELECT `+libraryCols+` FROM libraries WHERE id = ?`, id)
	l, err := scanLibrary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

func (s *Store) ListLibraries() ([]*model.Library, error) {
	rows, err := s.db.Query(`SELECT ` + libraryCols + ` FROM libraries ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Library{}
	for rows.Next() {
		l, err := scanLibrary(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DefaultLibrary 返回默认库；没有则返回 ErrNotFound。
func (s *Store) DefaultLibrary() (*model.Library, error) {
	row := s.db.QueryRow(`SELECT ` + libraryCols + ` FROM libraries ORDER BY is_default DESC, id LIMIT 1`)
	l, err := scanLibrary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

// ---------- library_files ----------

const libraryFileCols = `id, video_id, library_id, rel_dir, video_name, video_path, nfo_path,
	poster_path, thumb_path, fanart_path, strm_path, size_bytes, width, height, duration_sec, created_at`

func scanLibraryFile(sc interface{ Scan(...any) error }) (*model.LibraryFile, error) {
	var f model.LibraryFile
	var created string
	if err := sc.Scan(&f.ID, &f.VideoID, &f.LibraryID, &f.RelDir, &f.VideoName, &f.VideoPath,
		&f.NFOPath, &f.PosterPath, &f.ThumbPath, &f.FanartPath, &f.StrmPath,
		&f.SizeBytes, &f.Width, &f.Height, &f.DurationSec, &created); err != nil {
		return nil, err
	}
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		f.CreatedAt = t
	} else {
		f.CreatedAt = parseSQLiteTime(created)
	}
	return &f, nil
}

// SaveLibraryFile 幂等写入落盘记录（同一影片同一库覆盖）。
func (s *Store) SaveLibraryFile(f *model.LibraryFile) error {
	_, err := s.db.Exec(`INSERT INTO library_files
		(video_id, library_id, rel_dir, video_name, video_path, nfo_path, poster_path, thumb_path,
		 fanart_path, strm_path, size_bytes, width, height, duration_sec)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(video_id, library_id) DO UPDATE SET
			rel_dir = excluded.rel_dir, video_name = excluded.video_name, video_path = excluded.video_path,
			nfo_path = excluded.nfo_path, poster_path = excluded.poster_path, thumb_path = excluded.thumb_path,
			fanart_path = excluded.fanart_path, strm_path = excluded.strm_path, size_bytes = excluded.size_bytes,
			width = excluded.width, height = excluded.height, duration_sec = excluded.duration_sec`,
		f.VideoID, f.LibraryID, f.RelDir, f.VideoName, f.VideoPath, f.NFOPath, f.PosterPath, f.ThumbPath,
		f.FanartPath, f.StrmPath, f.SizeBytes, f.Width, f.Height, f.DurationSec)
	return err
}

func (s *Store) GetLibraryFile(videoID, libraryID int64) (*model.LibraryFile, error) {
	row := s.db.QueryRow(`SELECT `+libraryFileCols+` FROM library_files WHERE video_id = ? AND library_id = ?`,
		videoID, libraryID)
	f, err := scanLibraryFile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

func (s *Store) ListLibraryFilesByVideo(videoID int64) ([]*model.LibraryFile, error) {
	rows, err := s.db.Query(`SELECT `+libraryFileCols+` FROM library_files WHERE video_id = ? ORDER BY id`, videoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.LibraryFile{}
	for rows.Next() {
		f, err := scanLibraryFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) DeleteLibraryFile(videoID, libraryID int64) error {
	_, err := s.db.Exec(`DELETE FROM library_files WHERE video_id = ? AND library_id = ?`, videoID, libraryID)
	return err
}

// LibraryUsage 汇总某个库已落盘的文件数与字节数。
type LibraryUsage struct {
	LibraryID int64
	FileCount int64
	SizeBytes int64
}

func (s *Store) LibraryUsages() (map[int64]LibraryUsage, error) {
	rows, err := s.db.Query(`SELECT library_id, COUNT(*), COALESCE(SUM(size_bytes),0) FROM library_files GROUP BY library_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]LibraryUsage{}
	for rows.Next() {
		var u LibraryUsage
		if err := rows.Scan(&u.LibraryID, &u.FileCount, &u.SizeBytes); err != nil {
			return nil, err
		}
		out[u.LibraryID] = u
	}
	return out, rows.Err()
}

// LocalFileMap 返回一批影片在哪些库里有文件，用于列表页标记"已有本地副本"。
func (s *Store) LocalFileMap(videoIDs []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(videoIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(videoIDs))
	for _, id := range videoIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT video_id, library_id FROM library_files WHERE video_id IN (`+
		placeholders(len(args))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var vid, lid int64
		if err := rows.Scan(&vid, &lid); err != nil {
			return nil, err
		}
		out[vid] = append(out[vid], lid)
	}
	return out, rows.Err()
}

// HasLocalFile 判断影片是否已在任一库落盘。
func (s *Store) HasLocalFile(videoID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM library_files WHERE video_id = ?`, videoID).Scan(&n)
	return n > 0, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
