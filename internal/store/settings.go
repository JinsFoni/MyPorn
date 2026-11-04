package store

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/JinsFoni/MyPorn/internal/secret"
)

// GetSetting 读取普通配置项，缺失返回空串。
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value, encrypted, updated_at) VALUES (?,?,'0',?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, encrypted = '0', updated_at = excluded.updated_at`,
		key, value, now())
	return err
}

// GetSecret 读取并用 box 解密敏感配置项。
//
// 密钥不匹配时返回包裹了 secret.ErrWrongKey 的错误——**不要**把这种错误当成「没配置」：
// 界面上就会变成「请重新填一次」，而用户明明填过（2026-10-29 的真实投诉）。
func (s *Store) GetSecret(box *secret.Box, key string) (string, error) {
	var enc string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ? AND encrypted = '1'`, key).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return box.Decrypt(enc)
}

func (s *Store) SetSecret(box *secret.Box, key, plain string) error {
	enc, err := box.Encrypt(plain)
	if err != nil {
		return err
	}
	// 顺手把「写这条密文用的密钥指纹」也记下来：下次启动一比对就知道密钥换过没有。
	if err := s.SetSetting("secret_key_fp", box.Fingerprint()); err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings (key, value, encrypted, updated_at) VALUES (?,?, '1', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, encrypted = '1', updated_at = excluded.updated_at`,
		key, enc, now())
	return err
}

// DeleteSecret 删掉一条敏感项（连密文一起），返回删掉的行数。
//
// 用途：某个凭证槽位被产品决定弃用之后，把老库里那条密文清掉——留着只是多一处
// 泄漏面，而且下次「扫不出来的密文」体检会一直报它（2026-11-04 Instagram Cookie）。
func (s *Store) DeleteSecret(key string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM settings WHERE key = ? AND encrypted = '1'`, key)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SecretUpdatedAt 返回这条敏感项最近一次被写入的时间（RFC3339，空 = 没这条）。
//
// 用途：B 站把登录态作废时，界面要说的是「这条 Cookie 是 X 写进来的、现在 B 站不认了」，
// 而不是含糊的「Cookie 失效」。2026-10-30 就是这么一次：用户以为又踩了密钥坑，
// 实际是 B 站在 SESSDATA 到期前把那条登录态作废了（内嵌到期 2027-04-25，第 5 天 -101）。
func (s *Store) SecretUpdatedAt(key string) (string, error) {
	var at string
	err := s.db.QueryRow(`SELECT updated_at FROM settings WHERE key = ?`, key).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return at, err
}

// SecretState 是三态答案，比 bool 多出来的那一态就是用户投诉的那个 bug。
type SecretState int

const (
	// SecretAbsent：压根没存过（或存的是空串）。
	SecretAbsent SecretState = iota
	// SecretOK：存了，用当前密钥能解开。
	SecretOK
	// SecretWrongKey：存了，但解不开——密钥换过 / 库是别处拷来的。
	SecretWrongKey
)

// SecretState 只回答「这个槽现在什么状态」，不回传明文。
func (s *Store) SecretState(box *secret.Box, key string) (SecretState, error) {
	var enc string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ? AND encrypted = '1'`, key).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return SecretAbsent, nil
	}
	if err != nil {
		return SecretAbsent, err
	}
	if strings.TrimSpace(enc) == "" {
		return SecretAbsent, nil
	}
	plain, err := box.Decrypt(enc)
	if err != nil {
		if secret.IsWrongKey(err) {
			return SecretWrongKey, nil
		}
		return SecretAbsent, err
	}
	if strings.TrimSpace(plain) == "" {
		return SecretAbsent, nil
	}
	return SecretOK, nil
}

// UndecryptableSecrets 把库里所有加密字段用当前密钥试解一遍，返回解不开的键名。
//
// 启动时调一次：密钥配错的话，日志里当场就写清楚是哪些 Cookie 废了，
// 而不是等用户发现「Cookie 又要重配」。
func (s *Store) UndecryptableSecrets(box *secret.Box) ([]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE encrypted = '1'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var key, enc string
		if err := rows.Scan(&key, &enc); err != nil {
			return nil, err
		}
		if strings.TrimSpace(enc) == "" {
			continue // 清空时写进去的就是空串，解不开才怪
		}
		if _, err := box.Decrypt(enc); err != nil {
			bad = append(bad, key)
		}
	}
	return bad, rows.Err()
}

// SeedPlatform 预置平台信息（幂等，仅补默认值）。
func (s *Store) SeedPlatform(key, name, baseURL string) error {
	_, err := s.db.Exec(`INSERT INTO platforms (key, name, base_url) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET name = excluded.name,
			base_url = CASE WHEN platforms.base_url = '' THEN excluded.base_url ELSE platforms.base_url END`,
		key, name, baseURL)
	return err
}

func (s *Store) UpdatePlatformBaseURL(key, baseURL string) error {
	_, err := s.db.Exec(`UPDATE platforms SET base_url = ?, updated_at = ? WHERE key = ?`, baseURL, now(), key)
	return err
}
