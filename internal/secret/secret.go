// Package secret 提供 AES-256-GCM 对称加密，用于落库保存 Cookie / Token 等敏感字段。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// ErrWrongKey 表示「这条密文不是这把密钥写的」。
//
// 真实踩过（2026-10-29）：服务被用另一套 APP_ENCRYPTION_KEY 起过一次，库里那条
// bili_cookie 就再也解不开了——GCM 只能分辨「验签失败」，到底为什么得靠日志里的
// 密钥指纹去比（LoadDotEnv 不覆盖已存在的环境变量，shell 里留个旧值就能盖掉 .env.local）。
var ErrWrongKey = errors.New("加密密钥不匹配，密文解不开")

type Box struct {
	aead cipher.AEAD
	// fp 是密钥指纹：能写进日志和设置表用于比对，且不可据此还原密钥。
	fp string
}

// New 用 base64 编码的 32 字节密钥构造 Box。
func New(base64Key string) (*Box, error) {
	raw, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY 不是合法 base64: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY 需为 32 字节，当前 %d", len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("myporn-key-fp:"), raw...))
	return &Box{aead: aead, fp: base64.RawURLEncoding.EncodeToString(sum[:6])}, nil
}

// Fingerprint 返回密钥指纹（12 字符）。同一把密钥永远同一个值，不同密钥基本不会撞。
//
// 用途：启动时和设置表里记着的 secret_key_fp 比一次，就能在上线前发现「密钥换了」，
// 而不是等用户发现「Cookie 又要重配」。
func (b *Box) Fingerprint() string { return b.fp }

// Encrypt 返回 nonce||ciphertext 的 base64 串。
func (b *Box) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (b *Box) Decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		// 不是合法 base64 也归到「解不开」：对使用者来说都是「这条得重配」。
		return "", fmt.Errorf("%w: %v", ErrWrongKey, err)
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("%w: 密文长度不足", ErrWrongKey)
	}
	plain, err := b.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrWrongKey, err)
	}
	return string(plain), nil
}

// IsWrongKey 判断错误是不是「密文解不开」。
func IsWrongKey(err error) bool { return errors.Is(err, ErrWrongKey) }

// RandomToken 生成 32 字节随机串（base64url），用于会话 token。
func RandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
