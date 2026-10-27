package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/mario/myporn/internal/model"
)

const jobCols = `id, type, status, priority, payload, progress, message, error, attempts, target_id,
	cancel_requested, speed_bps, bytes_done, bytes_total, created_at, started_at, finished_at`

func scanJob(sc interface{ Scan(...any) error }) (*model.Job, error) {
	var j model.Job
	var created string
	var started, finished sql.NullString
	var cancelFlag any
	if err := sc.Scan(&j.ID, &j.Type, &j.Status, &j.Priority, &j.Payload, &j.Progress,
		&j.Message, &j.Error, &j.Attempts, &j.TargetID, &cancelFlag,
		&j.SpeedBPS, &j.BytesDone, &j.BytesTotal, &created, &started, &finished); err != nil {
		return nil, err
	}
	// 老库里可能存成 true/false 文本，也可能是 0/1/2，两种都兼容。
	switch v := cancelFlag.(type) {
	case int64:
		j.CancelRequested = int(v)
	case bool:
		if v {
			j.CancelRequested = 1
		}
	case []byte:
		j.CancelRequested = parseCancelFlag(string(v))
	case string:
		j.CancelRequested = parseCancelFlag(v)
	}
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		j.CreatedAt = t
	}
	j.StartedAt = scanTime(started)
	j.FinishedAt = scanTime(finished)
	return &j, nil
}

// Enqueue 入队一个任务；payload 会被序列化为 JSON。
func (s *Store) Enqueue(jobType string, payload any, priority int) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO jobs (type, status, priority, payload) VALUES (?, ?, ?, ?)`,
		jobType, model.JobQueued, priority, string(b))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EnqueueTarget 入队并记录目标实体 id（如 videoId），便于幂等判重。
func (s *Store) EnqueueTarget(jobType string, targetID int64, payload any, priority int) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO jobs (type, status, priority, payload, target_id) VALUES (?, ?, ?, ?, ?)`,
		jobType, model.JobQueued, priority, string(b), targetID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Claim 原子领取一个指定类型的排队任务；没有则返回 nil。
func (s *Store) Claim(jobType string) (*model.Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	row := tx.QueryRow(`SELECT `+jobCols+` FROM jobs
		WHERE type = ? AND status = ? ORDER BY priority, id LIMIT 1`, jobType, model.JobQueued)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE jobs SET status = ?, started_at = ?, updated_at = ?, attempts = attempts + 1 WHERE id = ?`,
		model.JobRunning, now(), now(), j.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.Status = model.JobRunning
	j.Attempts++
	return j, nil
}

func (s *Store) SetJobProgress(id int64, progress float64, message string) error {
	_, err := s.db.Exec(`UPDATE jobs SET progress = ?, message = ?, updated_at = ? WHERE id = ?`,
		progress, message, now(), id)
	return err
}

func (s *Store) FinishJob(id int64, status, errMsg string) error {
	// 完成的任务直接把进度补到 100：最后一次 Progress 常被 800ms 节流吞掉，
	// 否则下载页会出现「完成 · 92.0%」这种自相矛盾的读数。
	_, err := s.db.Exec(`UPDATE jobs SET status = ?, error = ?, finished_at = ?, updated_at = ?,
		progress = CASE WHEN ? = 'done' THEN 100 ELSE progress END WHERE id = ?`,
		status, errMsg, now(), now(), status, id)
	return err
}

func (s *Store) GetJob(id int64) (*model.Job, error) {
	row := s.db.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return j, err
}

func (s *Store) ListJobs(jobType, status string, limit int) ([]*model.Job, error) {
	if jobType == "" {
		return s.ListJobsByTypes(nil, status, limit)
	}
	return s.ListJobsByTypes([]string{jobType}, status, limit)
}

// ListJobsByTypes 是 ListJobs 的多类型版本：下载队列要把 download 与 yt_download
// 混在一起按时间倒序显示，否则下载页会看不到 YouTube 那条。
func (s *Store) ListJobsByTypes(types []string, status string, limit int) ([]*model.Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT ` + jobCols + ` FROM jobs WHERE 1=1`
	args := []any{}
	if len(types) > 0 {
		q += ` AND type IN (` + placeholders(len(types)) + `)`
		for _, t := range types {
			args = append(args, t)
		}
	}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CancelJob 只允许取消 queued 的任务；running 任务由 runner 通过 context 处理。
func (s *Store) CancelJob(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, model.JobCanceled, now(), now(), id, model.JobQueued)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// HasActiveSyncJob 判断某订阅是否已有排队/执行中的同步任务，避免重复投递。
func (s *Store) HasActiveSyncJob(subscriptionID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs
		WHERE type = ? AND status IN (?, ?)
		  AND json_extract(payload, '$.subscriptionId') = ?`,
		model.JobTypeSync, model.JobQueued, model.JobRunning, subscriptionID).Scan(&n)
	return n > 0, err
}

// ResetRunningJobs 在进程启动时把遗留的 running 任务重置为 queued（可恢复语义）。
func (s *Store) ResetRunningJobs() (int64, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, updated_at = ? WHERE status = ?`,
		model.JobQueued, now(), model.JobRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetJobStats 一次性更新进度与传输统计（下载任务用）。
func (s *Store) SetJobStats(id int64, progress float64, speedBPS, done, total int64, message string) error {
	_, err := s.db.Exec(`UPDATE jobs SET progress = ?, speed_bps = ?, bytes_done = ?, bytes_total = ?,
		message = ?, updated_at = ? WHERE id = ?`, progress, speedBPS, done, total, message, now(), id)
	return err
}

// RequestCancel 请求取消任务：排队中的直接终止，执行中的打标记由 worker 收尾。
func (s *Store) RequestCancel(id int64) (string, error) {
	j, err := s.GetJob(id)
	if err != nil {
		return "", err
	}
	switch j.Status {
	case model.JobQueued:
		if _, err := s.db.Exec(`UPDATE jobs SET status = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
			model.JobCanceled, now(), now(), id); err != nil {
			return "", err
		}
		return model.JobCanceled, nil
	case model.JobRunning:
		if _, err := s.db.Exec(`UPDATE jobs SET cancel_requested = 1, updated_at = ? WHERE id = ?`,
			now(), id); err != nil {
			return "", err
		}
		return model.JobRunning, nil
	default:
		return j.Status, nil
	}
}

// RequestPause 与取消同理，只是 worker 收尾时落成 paused（保留断点）。
func (s *Store) RequestPause(id int64) (string, error) {
	j, err := s.GetJob(id)
	if err != nil {
		return "", err
	}
	switch j.Status {
	case model.JobQueued:
		if _, err := s.db.Exec(`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
			model.JobPaused, now(), id); err != nil {
			return "", err
		}
		return model.JobPaused, nil
	case model.JobRunning:
		if _, err := s.db.Exec(`UPDATE jobs SET cancel_requested = 2, updated_at = ? WHERE id = ?`,
			now(), id); err != nil {
			return "", err
		}
		return model.JobRunning, nil
	default:
		return j.Status, nil
	}
}

// parseCancelFlag 兼容 "1"/"true" 这类历史写法。
func parseCancelFlag(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "t":
		return 1
	case "2":
		return 2
	}
	return 0
}

// CancelFlag 返回 cancel_requested 的原始值：0 继续、1 取消、2 暂停。
func (s *Store) CancelFlag(id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT cancel_requested FROM jobs WHERE id = ?`, id).Scan(&n)
	return n, err
}

// RetryJob 把失败/取消/暂停的任务重新排队；保留 payload，断点文件继续用。
func (s *Store) RetryJob(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, error = '', progress = 0, speed_bps = 0,
		bytes_done = 0, cancel_requested = 0, started_at = NULL, finished_at = NULL, message = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?, ?)`,
		model.JobQueued, "重新排队", now(), id, model.JobFailed, model.JobCanceled, model.JobPaused)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// HasActiveJobForTarget 判断某目标是否已有排队/执行中的同类任务（幂等投递）。
func (s *Store) HasActiveJobForTarget(jobType string, targetID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE type = ? AND target_id = ?
		AND status IN (?, ?)`, jobType, targetID, model.JobQueued, model.JobRunning).Scan(&n)
	return n > 0, err
}

// ActiveJobCount 统计某类任务排队+执行中的数量（队列页显示用）。
func (s *Store) ActiveJobCount(jobType string) (int, error) {
	var n int
	q := `SELECT COUNT(*) FROM jobs WHERE status IN (?, ?, ?)`
	args := []any{model.JobQueued, model.JobRunning, model.JobPaused}
	if jobType != "" {
		q += ` AND type = ?`
		args = append(args, jobType)
	}
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// DeleteJob 从历史中移除一条已结束的任务记录。
func (s *Store) DeleteJob(id int64) error {
	_, err := s.db.Exec(`DELETE FROM jobs WHERE id = ? AND status NOT IN (?, ?)`,
		id, model.JobQueued, model.JobRunning)
	return err
}
