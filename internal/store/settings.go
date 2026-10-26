package store

import (
	"database/sql"
	"errors"

	"github.com/mario/myporn/internal/secret"
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
	_, err = s.db.Exec(`INSERT INTO settings (key, value, encrypted, updated_at) VALUES (?,?, '1', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, encrypted = '1', updated_at = excluded.updated_at`,
		key, enc, now())
	return err
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
