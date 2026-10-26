package store

import (
	"time"
)

func (s *Store) CreateSession(token string, ttl time.Duration) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token, expires_at) VALUES (?, ?)`,
		token, fmtTime(time.Now().UTC().Add(ttl)))
	return err
}

func (s *Store) SessionValid(token string) (bool, error) {
	var exp string
	err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token = ?`, token).Scan(&exp)
	if err != nil {
		return false, nil
	}
	t, err := time.Parse(time.RFC3339, exp)
	if err != nil {
		return false, nil
	}
	return t.After(time.Now().UTC()), nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *Store) CleanupSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now())
	return err
}

// NewVideoCounts 批量统计各艺人的未看新片数。
func (s *Store) NewVideoCounts(performerIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(performerIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(performerIDs))
	for i, id := range performerIDs {
		args[i] = id
	}
	q := `SELECT vp.performer_id, COUNT(*) FROM videos v
		JOIN video_performers vp ON vp.video_id = v.id
		WHERE v.is_new = 1 AND vp.performer_id IN (` + placeholders(len(args)) + `)
		GROUP BY vp.performer_id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var n int
		if err := rows.Scan(&pid, &n); err != nil {
			return nil, err
		}
		out[pid] = n
	}
	return out, rows.Err()
}
