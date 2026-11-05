package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

var ErrNotFound = errors.New("not found")

const performerCols = `id, platform, performer_id, name, url, avatar_url, bio,
	profile_json, social_json, body_json, model_rank, video_count, subscriber_count, last_fetched_at`

// jsonBlock 把数组序列化成待入库的 JSON 文本；空数组也写成 '[]'，
// 写入时据此判断「这次没抓到资料」，不覆盖库里已有的。
func jsonBlock[T any](v []T) string {
	if len(v) == 0 {
		return "[]"
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return "[]"
}

// profileBlock 序列化资料行，顺手过掉「任何平台都不入库」的那几行
// （职业状态 / 工作起始及结束时间等，见 model.DropProfileLabel）。
// 解析层已经丢过一遍，这里再兜一次底：跨站借资料、老代码路径都不会把它们写进来。
func profileBlock(rows []model.ProfileField) string {
	return jsonBlock(model.CleanProfile(rows))
}

// pruneProfileLabels 把库里还留着的「不该入库」资料行删掉（幂等）。
// 只处理真的含有这些行的记录，正常情况下启动时一行都不会写。
func (s *Store) pruneProfileLabels() error {
	rows, err := s.db.Query(`SELECT id, profile_json FROM performers
		WHERE profile_json IS NOT NULL AND profile_json NOT IN ('', '[]')`)
	if err != nil {
		return err
	}
	type pending struct {
		id  int64
		raw string
	}
	var todo []pending
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var items []model.ProfileField
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			continue // 一行坏数据不值得让服务起不来
		}
		if cleaned := model.CleanProfile(items); len(cleaned) != len(items) {
			todo = append(todo, pending{id: id, raw: jsonBlock(cleaned)})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, u := range todo {
		if _, err := s.db.Exec(`UPDATE performers SET profile_json = ? WHERE id = ?`, u.raw, u.id); err != nil {
			return err
		}
	}
	return nil
}

// parseBlock 解析库里的 JSON 数组，坏了就当空（不让一行脏数据拖垮整页）。
func parseBlock[T any](raw string) []T {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" || raw == "null" {
		return nil
	}
	var out []T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// bodyBlock 把身材序列化成待入库的 JSON；整项为空时写空串，写入时据此保留库里的旧值。
func bodyBlock(b model.Body) string {
	if b.Empty() {
		return ""
	}
	if raw, err := json.Marshal(b); err == nil {
		return string(raw)
	}
	return ""
}

// parseBodyBlock 解析库里的身材 JSON，坏了就当没抓到（不让一行脏数据拖垮整页）。
func parseBodyBlock(raw string) model.Body {
	var b model.Body
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return b
	}
	_ = json.Unmarshal([]byte(raw), &b)
	return b
}

func scanPerformer(sc interface{ Scan(...any) error }) (*model.Performer, error) {
	var p model.Performer
	var vc, scur sql.NullInt64
	var fetched sql.NullString
	var profile, social, body string
	var rank int64
	if err := sc.Scan(&p.ID, &p.Platform, &p.PerformerID, &p.Name, &p.URL, &p.AvatarURL, &p.Bio,
		&profile, &social, &body, &rank, &vc, &scur, &fetched); err != nil {
		return nil, err
	}
	p.Profile = parseBlock[model.ProfileField](profile)
	p.Social = parseBlock[model.SocialLink](social)
	p.Body = parseBodyBlock(body)
	p.Rank = int(rank)
	if vc.Valid {
		n := int(vc.Int64)
		p.VideoCount = &n
	}
	if scur.Valid {
		n := int(scur.Int64)
		p.SubscriberCount = &n
	}
	p.LastFetchedAt = scanTime(fetched)
	return &p, nil
}

// UpsertPerformer 按 (platform, performer_id) 幂等写入，返回本地 id。
func (s *Store) UpsertPerformer(p *model.Performer) (int64, error) {
	_, err := s.db.Exec(`
		INSERT INTO performers (platform, performer_id, name, url, avatar_url, bio,
			profile_json, social_json, body_json, model_rank, video_count, subscriber_count, last_fetched_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(platform, performer_id) DO UPDATE SET
			name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE performers.name END,
			url = CASE WHEN excluded.url <> '' THEN excluded.url ELSE performers.url END,
			avatar_url = CASE WHEN excluded.avatar_url <> '' THEN excluded.avatar_url ELSE performers.avatar_url END,
			bio = CASE WHEN excluded.bio <> '' THEN excluded.bio ELSE performers.bio END,
			profile_json = CASE WHEN excluded.profile_json NOT IN ('', '[]') THEN excluded.profile_json ELSE performers.profile_json END,
			social_json = CASE WHEN excluded.social_json NOT IN ('', '[]') THEN excluded.social_json ELSE performers.social_json END,
			body_json = CASE WHEN excluded.body_json <> '' THEN excluded.body_json ELSE performers.body_json END,
			model_rank = CASE WHEN excluded.model_rank > 0 THEN excluded.model_rank ELSE performers.model_rank END,
			video_count = COALESCE(excluded.video_count, performers.video_count),
			subscriber_count = COALESCE(excluded.subscriber_count, performers.subscriber_count),
			last_fetched_at = excluded.last_fetched_at`,
		p.Platform, p.PerformerID, p.Name, p.URL, p.AvatarURL, p.Bio,
		profileBlock(p.Profile), jsonBlock(p.Social), bodyBlock(p.Body), p.Rank,
		nullInt(p.VideoCount), nullInt(p.SubscriberCount), now())
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM performers WHERE platform = ? AND performer_id = ?`,
		p.Platform, p.PerformerID).Scan(&id)
	return id, err
}

func (s *Store) GetPerformer(id int64) (*model.Performer, error) {
	row := s.db.QueryRow(`SELECT `+performerCols+` FROM performers WHERE id = ?`, id)
	p, err := scanPerformer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Store) FindPerformer(platform, performerID string) (*model.Performer, error) {
	row := s.db.QueryRow(`SELECT `+performerCols+` FROM performers WHERE platform = ? AND performer_id = ?`,
		platform, performerID)
	p, err := scanPerformer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// ListPerformers 返回本地艺人，默认只返回已订阅（soft delete 的不含）。
//
// B 站不在这个列表里：UP 主是「订阅一个内容源」，不是「追一个艺人」，
// 它的入口是独立的 /bilibili 页（见 store/bilibili.go 顶部）。这里排掉，
// 「艺人」页和影片卡的头像表才不会被 B 站占掉位置。
func (s *Store) ListPerformers(subscribedOnly bool, offset, limit int) ([]*model.Performer, int, error) {
	where := "p.platform <> 'bilibili'"
	args := []any{}
	if subscribedOnly {
		where += ` AND EXISTS (SELECT 1 FROM subscriptions su WHERE su.performer_id = p.id AND su.deleted_at IS NULL)`
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM performers p WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + perfixedCols("p") + ` FROM performers p WHERE ` + where + ` ORDER BY p.name COLLATE NOCASE LIMIT ? OFFSET ?`
	rows, err := s.db.Query(q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*model.Performer{}
	for rows.Next() {
		p, err := scanPerformer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// SetPerformerAvatar 只更新头像地址（站外头像源命中后写回）。
func (s *Store) SetPerformerAvatar(id int64, avatarURL string) error {
	_, err := s.db.Exec(`UPDATE performers SET avatar_url = ? WHERE id = ?`, avatarURL, id)
	return err
}

// ListPerformersWithoutAvatar 返回头像为空的艺人，供一次性补齐头像用。
func (s *Store) ListPerformersWithoutAvatar() ([]*model.Performer, error) {
	rows, err := s.db.Query(`SELECT ` + performerCols + ` FROM performers
		WHERE TRIM(avatar_url) = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Performer{}
	for rows.Next() {
		p, err := scanPerformer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPerformersByPlatform 返回某个平台的艺人（含已取消订阅的），供一次性返工用。
func (s *Store) ListPerformersByPlatform(platformKey string) ([]*model.Performer, error) {
	rows, err := s.db.Query(`SELECT `+performerCols+` FROM performers WHERE platform = ? ORDER BY id`,
		platformKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Performer{}
	for rows.Next() {
		p, err := scanPerformer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePerformerProfile 只写资料区块（简介 / 资料标签 / 身材 / 社交链接 / 排名）与头像，
// 不动名字、主页链接和平台统计——跨站借资料时只该换这些。传入的是合并后的整份资料。
func (s *Store) UpdatePerformerProfile(p *model.Performer) error {
	_, err := s.db.Exec(`UPDATE performers SET bio = ?, profile_json = ?, social_json = ?,
		body_json = ?, model_rank = ?, avatar_url = ? WHERE id = ?`,
		p.Bio, profileBlock(p.Profile), jsonBlock(p.Social), bodyBlock(p.Body), p.Rank, p.AvatarURL, p.ID)
	return err
}

func perfixedCols(alias string) string {
	return alias + ".id, " + alias + ".platform, " + alias + ".performer_id, " + alias + ".name, " +
		alias + ".url, " + alias + ".avatar_url, " + alias + ".bio, " + alias + ".profile_json, " +
		alias + ".social_json, " + alias + ".body_json, " + alias + ".model_rank, " + alias + ".video_count, " +
		alias + ".subscriber_count, " + alias + ".last_fetched_at"
}

const subCols = `su.id, su.performer_id, su.enabled, su.backfill_done, su.backfill_limit,
	su.backfill_page, su.backfill_total_pages, su.last_sync_at, su.next_sync_at,
	su.last_error, su.created_at`

func scanSubscription(sc interface{ Scan(...any) error }) (*model.Subscription, error) {
	var sub model.Subscription
	var totalPages sql.NullInt64
	var lastSync, nextSync sql.NullString
	var created string
	if err := sc.Scan(&sub.ID, &sub.PerformerID, &sub.Enabled, &sub.BackfillDone, &sub.BackfillLimit,
		&sub.BackfillPage, &totalPages, &lastSync, &nextSync, &sub.LastError, &created); err != nil {
		return nil, err
	}
	if totalPages.Valid {
		n := int(totalPages.Int64)
		sub.BackfillTotalPages = &n
	}
	sub.LastSyncAt = scanTime(lastSync)
	sub.NextSyncAt = scanTime(nextSync)
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		sub.CreatedAt = t
	}
	return &sub, nil
}

// CreateSubscription 幂等创建订阅（若被软删则恢复）。
func (s *Store) CreateSubscription(performerID int64, backfillLimit int) (int64, error) {
	next := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO subscriptions (performer_id, enabled, backfill_limit, next_sync_at)
		VALUES (?, 1, ?, ?)
		ON CONFLICT(performer_id) DO UPDATE SET
			deleted_at = NULL, enabled = 1,
			backfill_limit = CASE WHEN subscriptions.backfill_done = 1 THEN subscriptions.backfill_limit ELSE excluded.backfill_limit END,
			backfill_page = CASE WHEN subscriptions.backfill_done = 1 THEN subscriptions.backfill_page ELSE 0 END,
			backfill_done = CASE WHEN excluded.backfill_limit > 0 THEN subscriptions.backfill_done ELSE subscriptions.backfill_done END,
			next_sync_at = excluded.next_sync_at`,
		performerID, backfillLimit, next)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM subscriptions WHERE performer_id = ?`, performerID).Scan(&id)
	return id, err
}

func (s *Store) GetSubscription(id int64) (*model.Subscription, error) {
	row := s.db.QueryRow(`SELECT `+subCols+` FROM subscriptions su WHERE su.id = ? AND su.deleted_at IS NULL`, id)
	sub, err := scanSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sub, err
}

func (s *Store) GetSubscriptionByPerformer(performerID int64) (*model.Subscription, error) {
	row := s.db.QueryRow(`SELECT `+subCols+` FROM subscriptions su WHERE su.performer_id = ? AND su.deleted_at IS NULL`, performerID)
	sub, err := scanSubscription(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sub, err
}

// ListSubscriptions 返回订阅及其艺人信息（按最近更新倒序）。
//
// B 站同样不在里面：「关注」流只管追艺人的片，「关注管理」也不该替 B 站列 UP 主
// （那是 管理 → B 站 那一页签的事）。
//
// excludePlatforms 是**调用方额外要排掉**的平台：关注页顶部那条艺人头像条是「筛选关注流」
// 用的，而关注流本身不收 krx18 的片子（store.FeedExcludedPlatforms），点那几个 tag
// 只会筛出一个空列表——所以那边传 "krx18" 进来。艺人页（/performers）不传：tag 背后
// 就是真人女优，在那一页它们本来就是正经的「艺人」。
func (s *Store) ListSubscriptions(excludePlatforms ...string) ([]*model.Performer, error) {
	where := `su.deleted_at IS NULL AND p.platform <> 'bilibili'`
	args := make([]any, 0, len(excludePlatforms))
	if len(excludePlatforms) > 0 {
		where += ` AND p.platform NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(excludePlatforms)), ",") + `)`
		for _, p := range excludePlatforms {
			args = append(args, p)
		}
	}
	q := `SELECT ` + perfixedCols("p") + `, ` + subCols + `
		FROM subscriptions su JOIN performers p ON p.id = su.performer_id
		WHERE ` + where + `
		ORDER BY COALESCE(su.last_sync_at, su.created_at) DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Performer{}
	for rows.Next() {
		var p model.Performer
		var vc, scur sql.NullInt64
		var fetched sql.NullString
		var profile, social, body string
		var rank int64
		var sub model.Subscription
		var totalPages sql.NullInt64
		var lastSync, nextSync sql.NullString
		var created string
		if err := rows.Scan(&p.ID, &p.Platform, &p.PerformerID, &p.Name, &p.URL, &p.AvatarURL, &p.Bio,
			&profile, &social, &body, &rank, &vc, &scur, &fetched,
			&sub.ID, &sub.PerformerID, &sub.Enabled, &sub.BackfillDone, &sub.BackfillLimit,
			&sub.BackfillPage, &totalPages, &lastSync, &nextSync, &sub.LastError, &created); err != nil {
			return nil, err
		}
		p.Profile = parseBlock[model.ProfileField](profile)
		p.Social = parseBlock[model.SocialLink](social)
		p.Body = parseBodyBlock(body)
		p.Rank = int(rank)
		if vc.Valid {
			n := int(vc.Int64)
			p.VideoCount = &n
		}
		if scur.Valid {
			n := int(scur.Int64)
			p.SubscriberCount = &n
		}
		p.LastFetchedAt = scanTime(fetched)
		sub.LastSyncAt = scanTime(lastSync)
		sub.NextSyncAt = scanTime(nextSync)
		if t, err := time.Parse(time.RFC3339, created); err == nil {
			sub.CreatedAt = t
		}
		p.Subscription = &sub
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (s *Store) SetSubscriptionEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET enabled = ? WHERE id = ? AND deleted_at IS NULL`, enabled, id)
	return err
}

func (s *Store) SoftDeleteSubscription(id int64) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET deleted_at = ?, enabled = 0 WHERE id = ?`, now(), id)
	return err
}

// MarkBackfillProgress 记录回填进度；done=true 表示全量回填结束。
func (s *Store) MarkBackfillProgress(subID int64, page int, totalPages *int, done bool) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET backfill_page = ?, backfill_total_pages = ?, backfill_done = ? WHERE id = ?`,
		page, nullInt(totalPages), done, subID)
	return err
}

// MarkSynced 更新同步时间并计算下次同步。
func (s *Store) MarkSynced(subID int64, interval time.Duration, lastErr string) error {
	t := time.Now().UTC()
	_, err := s.db.Exec(`UPDATE subscriptions SET last_sync_at = ?, next_sync_at = ?, last_error = ? WHERE id = ?`,
		fmtTime(t), fmtTime(t.Add(interval)), lastErr, subID)
	return err
}

// PostponeSync 在同步失败时推迟下次尝试，避免每分钟重试打爆上游。
func (s *Store) PostponeSync(subID int64, delay time.Duration, errMsg string) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET next_sync_at = ?, last_error = ? WHERE id = ?`,
		fmtTime(time.Now().UTC().Add(delay)), errMsg, subID)
	return err
}

// DueSubscriptions 返回到期需要增量同步的订阅 id。
func (s *Store) DueSubscriptions(limit int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT id FROM subscriptions
		WHERE deleted_at IS NULL AND enabled = 1 AND (next_sync_at IS NULL OR next_sync_at <= ?)
		ORDER BY next_sync_at LIMIT ?`, now(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// NewVideoCountByPerformer 统计某艺人未看的新片数。
func (s *Store) NewVideoCountByPerformer(performerID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM videos v
		JOIN video_performers vp ON vp.video_id = v.id
		WHERE vp.performer_id = ? AND v.is_new = 1`, performerID).Scan(&n)
	return n, err
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
