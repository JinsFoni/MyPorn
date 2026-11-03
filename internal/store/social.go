package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JinsFoni/MyPorn/internal/model"
)

// 社交动态（M5）：博主账号、帖子、照片三层。
//
// 这里只做数据访问，抓取逻辑在 internal/social/*；解析规则怎么变都不影响这一层。
// 照片是「照片墙上的一格」，所以单独成表、并且把 posted_at 冗余进来——
// 墙的分页只查 social_photos 一列，不用 join 帖子。

// SocialAccount 是一个被订阅的社交账号。
type SocialAccount struct {
	ID           int64
	Platform     string // x / instagram
	Handle       string // 不带 @，小写
	DisplayName  string
	AvatarURL    string
	ProfileURL   string
	PerformerID  int64 // 0 = 未关联艺人
	Enabled      bool
	BackfillDone bool
	Cursor       string // 回填断点（上游分页游标）
	PostCount    int
	PhotoCount   int
	LastSyncedAt *time.Time
	LastError    string
	CreatedAt    time.Time
}

// Account.Ref 给前端用的一小块账号信息（照片墙每格都要带）。
type SocialAccountBrief struct {
	ID          int64
	Platform    string
	Handle      string
	DisplayName string
	AvatarURL   string
	ProfileURL  string
}

// SocialPostBrief 是照片所在帖子的最小信息（灯箱里显示正文与原帖链接）。
type SocialPostBrief struct {
	ID         int64
	URL        string
	Caption    string
	PhotoCount int
}

// SocialPhotoRow 是照片墙的一格：照片本身 + 所属账号 + 所属帖子。
type SocialPhotoRow struct {
	ID       int64
	Platform string
	URL      string
	Width    int
	Height   int
	Seq      int
	PostedAt *time.Time
	Saved    int // 已存进几个本地库
	Account  SocialAccountBrief
	Post     SocialPostBrief
}

// SocialPhotoInput 是抓取层交来的一张照片。
type SocialPhotoInput struct {
	URL    string
	Width  int
	Height int
	Seq    int
}

// SocialPostInput 是抓取层交来的一条帖子（含它自己的全部静态图）。
type SocialPostInput struct {
	PostID   string
	URL      string
	Caption  string
	PostedAt time.Time
	Photos   []SocialPhotoInput
}

const socialAccountCols = `id, platform, handle, display_name, avatar_url, profile_url,
	performer_id, enabled, backfill_done, sync_cursor, post_count, photo_count,
	last_synced_at, last_error, created_at`

func scanSocialAccount(sc interface{ Scan(...any) error }) (*SocialAccount, error) {
	a := &SocialAccount{}
	var performer sql.NullInt64
	var lastSync sql.NullString
	var created string
	if err := sc.Scan(&a.ID, &a.Platform, &a.Handle, &a.DisplayName, &a.AvatarURL, &a.ProfileURL,
		&performer, &a.Enabled, &a.BackfillDone, &a.Cursor, &a.PostCount, &a.PhotoCount,
		&lastSync, &a.LastError, &created); err != nil {
		return nil, err
	}
	if performer.Valid {
		a.PerformerID = performer.Int64
	}
	if t := scanTime(lastSync); t != nil {
		a.LastSyncedAt = t
	}
	a.CreatedAt = mustParse(created)
	return a, nil
}

// UpsertSocialAccount 按 (platform, handle) 插入或更新资料。
// 空字段不覆盖已有值——上游偶尔抽风返回空名，不该把库里的好数据洗掉。
func (s *Store) UpsertSocialAccount(a *SocialAccount) (*SocialAccount, error) {
	// handle 统一成「小写、不带 @」：上游的账号名大小写随机，链接里还可能带 @，
	// 不归一化就会出现 X/Mini 和 X/mini 两条记录。
	a.Handle = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(a.Handle), "@"))
	if a.Handle == "" {
		return nil, errors.New("账号名为空")
	}
	var performer any
	if a.PerformerID > 0 {
		performer = a.PerformerID
	}
	_, err := s.db.Exec(`INSERT INTO social_accounts
		(platform, handle, display_name, avatar_url, profile_url, performer_id, enabled)
		VALUES (?,?,?,?,?,?,1)
		ON CONFLICT(platform, handle) DO UPDATE SET
			display_name = CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE social_accounts.display_name END,
			avatar_url   = CASE WHEN excluded.avatar_url   <> '' THEN excluded.avatar_url   ELSE social_accounts.avatar_url END,
			profile_url  = CASE WHEN excluded.profile_url  <> '' THEN excluded.profile_url  ELSE social_accounts.profile_url END,
			performer_id = COALESCE(social_accounts.performer_id, excluded.performer_id)`,
		a.Platform, a.Handle, a.DisplayName, a.AvatarURL, a.ProfileURL, performer)
	if err != nil {
		return nil, err
	}
	return s.GetSocialAccountByHandle(a.Platform, a.Handle)
}

func (s *Store) GetSocialAccount(id int64) (*SocialAccount, error) {
	a, err := scanSocialAccount(s.db.QueryRow(`SELECT `+socialAccountCols+` FROM social_accounts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) GetSocialAccountByHandle(platform, handle string) (*SocialAccount, error) {
	a, err := scanSocialAccount(s.db.QueryRow(`SELECT `+socialAccountCols+` FROM social_accounts
		WHERE platform = ? AND handle = ?`, platform, strings.ToLower(handle)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// ListSocialAccounts 按添加时间倒序列出博主；onlyEnabled 只取启用的。
func (s *Store) ListSocialAccounts(onlyEnabled bool) ([]*SocialAccount, error) {
	q := `SELECT ` + socialAccountCols + ` FROM social_accounts`
	if onlyEnabled {
		q += ` WHERE enabled = 1`
	}
	q += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SocialAccount{}
	for rows.Next() {
		a, err := scanSocialAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SetSocialAccountEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE social_accounts SET enabled = ? WHERE id = ?`, enabled, id)
	return err
}

// SetSocialAccountPerformer 关联/解除艺人（performerID=0 表示解除）。
func (s *Store) SetSocialAccountPerformer(id, performerID int64) error {
	var v any
	if performerID > 0 {
		v = performerID
	}
	_, err := s.db.Exec(`UPDATE social_accounts SET performer_id = ? WHERE id = ?`, v, id)
	return err
}

func (s *Store) DeleteSocialAccount(id int64) error {
	_, err := s.db.Exec(`DELETE FROM social_accounts WHERE id = ?`, id)
	return err
}

// SaveSocialSync 记录一次同步的结果（游标、是否回填完、错误），并刷新计数。
func (s *Store) SaveSocialSync(id int64, cursor string, backfillDone bool, syncErr string, syncedAt time.Time) error {
	_, err := s.db.Exec(`UPDATE social_accounts
		SET sync_cursor = ?, backfill_done = ?, last_error = ?, last_synced_at = ?
		WHERE id = ?`, cursor, backfillDone, syncErr, fmtTime(syncedAt), id)
	if err != nil {
		return err
	}
	return s.RefreshSocialCounts(id)
}

// SetSocialAccountError 记录最近一次同步失败原因（空串 = 恢复正常）。
// 独立于 SaveSocialSync：刚添加博主就试抓一次失败时，也要能立刻在界面上看到原因。
func (s *Store) SetSocialAccountError(id int64, msg string) error {
	_, err := s.db.Exec(`UPDATE social_accounts SET last_error = ?, last_synced_at = ? WHERE id = ?`,
		msg, fmtTime(time.Now()), id)
	return err
}

// RefreshSocialCounts 重算某个博主的帖子/照片计数（照片只算没被隐藏的）。
func (s *Store) RefreshSocialCounts(id int64) error {
	_, err := s.db.Exec(`UPDATE social_accounts SET
			post_count  = (SELECT COUNT(*) FROM social_posts p WHERE p.account_id = social_accounts.id),
			photo_count = (SELECT COUNT(*) FROM social_photos ph WHERE ph.account_id = social_accounts.id AND ph.hidden = 0)
		WHERE id = ?`, id)
	return err
}

// ResetSocialCursor 清掉回填断点，让下一次同步重新从头回填。
func (s *Store) ResetSocialCursor(id int64) error {
	_, err := s.db.Exec(`UPDATE social_accounts SET sync_cursor = '', backfill_done = 0 WHERE id = ?`, id)
	return err
}

// HasSocialPost 判断某条帖子是否已经在库里（增量同步遇到它就停）。
func (s *Store) HasSocialPost(platform, postID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT 1 FROM social_posts WHERE platform = ? AND post_id = ?`, platform, postID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// InsertSocialPosts 写入一页帖子与它们的照片，返回新插入的照片 id（用于 SSE 推送）。
// 帖子按 (platform, post_id) 去重、照片按 (platform, url) 去重，重复同步不会产生新行。
func (s *Store) InsertSocialPosts(accountID int64, platform string, posts []SocialPostInput) ([]int64, error) {
	newPhotos := []int64{}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for _, p := range posts {
		if p.PostID == "" {
			continue
		}
		postedAt := ""
		if !p.PostedAt.IsZero() {
			postedAt = fmtTime(p.PostedAt)
		}
		if _, err := tx.Exec(`INSERT INTO social_posts
			(account_id, platform, post_id, url, caption, posted_at, photo_count)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(platform, post_id) DO UPDATE SET
				caption = CASE WHEN excluded.caption <> '' THEN excluded.caption ELSE social_posts.caption END,
				photo_count = MAX(social_posts.photo_count, excluded.photo_count)`,
			accountID, platform, p.PostID, p.URL, p.Caption, postedAt, len(p.Photos)); err != nil {
			return nil, err
		}
		var postRowID int64
		if err := tx.QueryRow(`SELECT id FROM social_posts WHERE platform = ? AND post_id = ?`,
			platform, p.PostID).Scan(&postRowID); err != nil {
			return nil, err
		}
		for i, ph := range p.Photos {
			if ph.URL == "" {
				continue
			}
			seq := ph.Seq
			if seq == 0 {
				seq = i + 1
			}
			res, err := tx.Exec(`INSERT OR IGNORE INTO social_photos
				(post_id, account_id, platform, seq, url, width, height, posted_at)
				VALUES (?,?,?,?,?,?,?,?)`,
				postRowID, accountID, platform, seq, ph.URL, ph.Width, ph.Height, postedAt)
			if err != nil {
				return nil, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				if id, err := res.LastInsertId(); err == nil {
					newPhotos = append(newPhotos, id)
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return newPhotos, nil
}

// MomentsQuery 是照片墙的查询条件。
type MomentsQuery struct {
	AccountID int64
	// PerformerID 只看某个艺人名下的全部账号（艺人页的「社交动态」区块用）。
	PerformerID int64
	Platform    string
	Cursor      string
	Limit       int
}

// ListMoments 按「帖子新→旧、帖内 seq 正序」做 keyset 分页。
//
// 排序键特意用 (posted_at, post_id, seq) 而不是 (posted_at, id)：
// 同一条帖子里的几张图必须挨在一起、并且按帖内顺序排（灯箱里先走完同帖再进下一帖），
// 用自增 id 会因为插入方向而变成倒序。
func (s *Store) ListMoments(q MomentsQuery) ([]*SocialPhotoRow, string, error) {
	if q.Limit <= 0 || q.Limit > 60 {
		q.Limit = 30
	}
	where := []string{"ph.hidden = 0"}
	args := []any{}
	if q.AccountID > 0 {
		where = append(where, "ph.account_id = ?")
		args = append(args, q.AccountID)
	}
	if q.PerformerID > 0 {
		where = append(where, "a.performer_id = ?")
		args = append(args, q.PerformerID)
	}
	if q.Platform != "" {
		where = append(where, "ph.platform = ?")
		args = append(args, q.Platform)
	}
	if ts, postID, seq, ok := decodeMomentCursor(q.Cursor); ok {
		where = append(where, `(COALESCE(ph.posted_at,'') < ?
			OR (COALESCE(ph.posted_at,'') = ? AND po.post_id < ?)
			OR (COALESCE(ph.posted_at,'') = ? AND po.post_id = ? AND ph.seq > ?))`)
		args = append(args, ts, ts, postID, ts, postID, seq)
	}
	sqlQ := `SELECT ph.id, ph.platform, ph.url, ph.width, ph.height, ph.seq, ph.posted_at,
			a.id, a.platform, a.handle, a.display_name, a.avatar_url, a.profile_url,
			po.id, po.url, po.caption, po.photo_count,
			(SELECT COUNT(*) FROM social_photo_files f WHERE f.photo_id = ph.id)
		FROM social_photos ph
		JOIN social_accounts a ON a.id = ph.account_id
		JOIN social_posts po ON po.id = ph.post_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY COALESCE(ph.posted_at,'') DESC, po.post_id DESC, ph.seq ASC
		LIMIT ?`
	rows, err := s.db.Query(sqlQ, append(args, q.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := []*SocialPhotoRow{}
	for rows.Next() {
		r := &SocialPhotoRow{}
		var postedAt sql.NullString
		if err := rows.Scan(&r.ID, &r.Platform, &r.URL, &r.Width, &r.Height, &r.Seq, &postedAt,
			&r.Account.ID, &r.Account.Platform, &r.Account.Handle, &r.Account.DisplayName,
			&r.Account.AvatarURL, &r.Account.ProfileURL,
			&r.Post.ID, &r.Post.URL, &r.Post.Caption, &r.Post.PhotoCount, &r.Saved); err != nil {
			return nil, "", err
		}
		r.PostedAt = scanTime(postedAt)
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
		if last.PostedAt != nil {
			ts = fmtTime(*last.PostedAt)
		}
		// post_id 与 seq 直接从 DB 取，避免在扫描里多带一列。
		var postID string
		if err := s.db.QueryRow(`SELECT p.post_id FROM social_posts p WHERE p.id = ?`, last.Post.ID).Scan(&postID); err == nil {
			next = encodeMomentCursor(ts, postID, last.Seq)
		}
	}
	return out, next, nil
}

// CountUnseenPhotos 数「上次看动态之后新入库的照片」，侧边栏红点用。
func (s *Store) CountUnseenPhotos(since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM social_photos
		WHERE hidden = 0 AND first_seen_at > ?`, fmtTime(since)).Scan(&n)
	return n, err
}

// SocialStats 是管理页要的几个汇总数。
type SocialStats struct {
	Accounts int
	Posts    int
	Photos   int
	Errors   int
}

func (s *Store) SocialStats() (SocialStats, error) {
	var st SocialStats
	err := s.db.QueryRow(`SELECT
			(SELECT COUNT(*) FROM social_accounts),
			(SELECT COUNT(*) FROM social_posts),
			(SELECT COUNT(*) FROM social_photos WHERE hidden = 0),
			(SELECT COUNT(*) FROM social_accounts WHERE last_error <> '')`).
		Scan(&st.Accounts, &st.Posts, &st.Photos, &st.Errors)
	return st, err
}

// LinkSocialAccountsToPerformers 按 URL 把博主挂到艺人上，只认精确匹配、不猜名字。
//
// 数据源是 performers.social_json（Pornhub 艺人页上抓到的 Twitter / Instagram 链接），
// 规范化成 (platform, handle) 后再和 social_accounts 比。返回新建立的关联数。
func (s *Store) LinkSocialAccountsToPerformers() (int, error) {
	rows, err := s.db.Query(`SELECT id, social_json FROM performers WHERE social_json NOT IN ('', '[]')`)
	if err != nil {
		return 0, err
	}
	type kv struct {
		performerID int64
		platform    string
		handle      string
	}
	pairs := []kv{}
	for rows.Next() {
		var pid int64
		var raw string
		if err := rows.Scan(&pid, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		for _, link := range parseBlock[model.SocialLink](raw) {
			platform, handle, ok := NormalizeSocialURL(link.URL)
			if !ok {
				continue
			}
			pairs = append(pairs, kv{pid, platform, handle})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	linked := 0
	for _, p := range pairs {
		res, err := s.db.Exec(`UPDATE social_accounts SET performer_id = ?
			WHERE platform = ? AND handle = ? AND performer_id IS NULL`, p.performerID, p.platform, p.handle)
		if err != nil {
			return linked, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			linked++
		}
	}
	return linked, nil
}

// NormalizeSocialURL 把社交链接拆成 (platform, handle)。
// 认 x.com / twitter.com / instagram.com 三种，其余返回 ok=false。
func NormalizeSocialURL(raw string) (platform, handle string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	switch host {
	case "x.com", "twitter.com", "mobile.twitter.com":
		platform = "x"
	case "instagram.com":
		platform = "instagram"
	default:
		return "", "", false
	}
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return "", "", false
	}
	handle = strings.ToLower(strings.Split(path, "/")[0])
	switch handle {
	case "", "i", "home", "explore", "search", "accounts", "p", "reel", "reels", "stories", "tv":
		return "", "", false
	}
	return platform, handle, true
}

// ---- 照片落库（M5-c）----

// HideSocialPhoto 把一张照片从照片墙上删掉（软删：行留着当墓碑）。
//
// 为什么不真删：`social_photos` 上有 `UNIQUE (platform, url)`，写入是 `INSERT OR IGNORE`——
// 行真没了，下一轮同步（默认 6 小时一轮，也能手动点「同步」）会把这同一张图原样塞回来，
// 用户看到的就是「删了又回来」。留一行 `hidden = 1` 撞唯一键，它就不会再回到墙上
//（`ListMoments` 和账号的 photo_count 都只算 `hidden = 0`）。
//
// 已经存进本地库的文件不在这里动：那是媒体库里的资产，照片墙删图不该带走它。
// 返回这张照片属于哪个账号，调用方好刷一下照片计数。
func (s *Store) HideSocialPhoto(id int64) (int64, error) {
	var accountID int64
	err := s.db.QueryRow(`SELECT account_id FROM social_photos WHERE id = ?`, id).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`UPDATE social_photos SET hidden = 1 WHERE id = ?`, id); err != nil {
		return 0, err
	}
	return accountID, nil
}

// SavePhotoFile 记一条「这张照片已经存进某个库」的记录（已存在则更新体积）。
func (s *Store) SavePhotoFile(photoID, libraryID int64, relPath string, size int64) error {
	_, err := s.db.Exec(`INSERT INTO social_photo_files (photo_id, library_id, rel_path, size_bytes)
		VALUES (?,?,?,?)
		ON CONFLICT(photo_id, library_id) DO UPDATE SET
			rel_path = excluded.rel_path, size_bytes = excluded.size_bytes`, photoID, libraryID, relPath, size)
	return err
}

// GetSocialPhotoByID 按 id 取一张照片及其帖子/账号信息（落库任务用）。
func (s *Store) GetSocialPhotoByID(id int64) (*SocialPhotoRow, error) {
	r := &SocialPhotoRow{}
	var postedAt sql.NullString
	err := s.db.QueryRow(`SELECT ph.id, ph.platform, ph.url, ph.width, ph.height, ph.seq, ph.posted_at,
			a.id, a.platform, a.handle, a.display_name, a.avatar_url, a.profile_url,
			po.id, po.url, po.caption, po.photo_count
		FROM social_photos ph
		JOIN social_accounts a ON a.id = ph.account_id
		JOIN social_posts po ON po.id = ph.post_id
		WHERE ph.id = ?`, id).
		Scan(&r.ID, &r.Platform, &r.URL, &r.Width, &r.Height, &r.Seq, &postedAt,
			&r.Account.ID, &r.Account.Platform, &r.Account.Handle, &r.Account.DisplayName,
			&r.Account.AvatarURL, &r.Account.ProfileURL,
			&r.Post.ID, &r.Post.URL, &r.Post.Caption, &r.Post.PhotoCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.PostedAt = scanTime(postedAt)
	return r, nil
}

// PhotoSavedLibraries 返回这张照片已存进哪些库。
func (s *Store) PhotoSavedLibraries(photoID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT library_id FROM social_photo_files WHERE photo_id = ?`, photoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- 游标 ----

func encodeMomentCursor(ts, postID string, seq int) string {
	raw := strings.Join([]string{ts, postID, strconv.Itoa(seq)}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeMomentCursor(c string) (ts, postID string, seq int, ok bool) {
	if c == "" {
		return "", "", 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", "", 0, false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", "", 0, false
	}
	return parts[0], parts[1], n, true
}

// ListSocialAccountsByPerformer 列出挂在某个艺人名下的社交账号（艺人页「社交动态」区块用）。
func (s *Store) ListSocialAccountsByPerformer(performerID int64) ([]*SocialAccount, error) {
	all, err := s.ListSocialAccounts(false)
	if err != nil {
		return nil, err
	}
	out := []*SocialAccount{}
	for _, a := range all {
		if a.PerformerID == performerID {
			out = append(out, a)
		}
	}
	return out, nil
}

// FindPerformerBySocial 按「平台 + handle」找艺人：只认 performers.social_json 里已经存在的链接，
// 绝不靠名字/头像相似度去猜——艺人页上挂错人的代价比「没关联上」大得多。
func (s *Store) FindPerformerBySocial(platform, handle string) (int64, string, bool) {
	rows, err := s.db.Query(`SELECT id, name, social_json FROM performers WHERE social_json NOT IN ('', '[]')`)
	if err != nil {
		return 0, "", false
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   int64
			name string
			raw  string
		)
		if err := rows.Scan(&id, &name, &raw); err != nil {
			return 0, "", false
		}
		for _, link := range parseBlock[model.SocialLink](raw) {
			p, h, ok := NormalizeSocialURL(link.URL)
			if ok && p == platform && h == handle {
				return id, name, true
			}
		}
	}
	return 0, "", false
}
