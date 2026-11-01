package store

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/secret"
)

func newTestBox(t *testing.T) *secret.Box {
	t.Helper()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	box, err := secret.New(base64.StdEncoding.EncodeToString(buf))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// 三态是这次的核心：库里有密文、但服务端这把密钥解不开时，必须报 SecretWrongKey。
// 报成 SecretAbsent 的话，界面上就变成「未配置 Cookie，请填一个」——用户明明填过
// （2026-10-29 的真实投诉：库里 2336 字节的 bili_cookie 解不开，页面让他重配）。
func TestSecretState(t *testing.T) {
	st := newTestStore(t)
	box := newTestBox(t)
	other := newTestBox(t)

	if got, err := st.SecretState(box, "bili_cookie"); err != nil || got != SecretAbsent {
		t.Fatalf("没存过应为 Absent，得到 %v / %v", got, err)
	}

	if err := st.SetSecret(box, "bili_cookie", "SESSDATA=abc%2C1%2Cxyz"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.SecretState(box, "bili_cookie"); err != nil || got != SecretOK {
		t.Fatalf("同密钥应为 OK，得到 %v / %v", got, err)
	}

	// 换一把密钥（真人踩过：服务被另一套 APP_ENCRYPTION_KEY 起过一次）。
	if got, err := st.SecretState(other, "bili_cookie"); err != nil || got != SecretWrongKey {
		t.Fatalf("异密钥应为 WrongKey，得到 %v / %v", got, err)
	}

	// 存空串 = 清空，回到 Absent，不该算解不开。
	if err := st.SetSecret(box, "bili_cookie", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := st.SecretState(other, "bili_cookie"); err != nil || got != SecretAbsent {
		t.Fatalf("清空后应为 Absent，得到 %v / %v", got, err)
	}
}

// 启动自检要能一次列出所有解不开的字段：只记一条「有 Cookie 解不开」等于没说清是哪个。
func TestUndecryptableSecrets(t *testing.T) {
	st := newTestStore(t)
	box := newTestBox(t)
	other := newTestBox(t)

	if err := st.SetSecret(box, "bili_cookie", "SESSDATA=abc"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSecret(box, "yt_cookies", "# Netscape HTTP Cookie File"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSecret(box, "openlist_token", ""); err != nil {
		t.Fatal(err) // 空串不算，扫的时候要跳过
	}

	bad, err := st.UndecryptableSecrets(box)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("同密钥不该报，得到 %v", bad)
	}

	bad, err = st.UndecryptableSecrets(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 2 {
		t.Fatalf("应报 2 个字段，得到 %v", bad)
	}
}

// 写密文时顺手记下密钥指纹，下次启动才能比对出「密钥换了」。
func TestSetSecretRecordsKeyFingerprint(t *testing.T) {
	st := newTestStore(t)
	box := newTestBox(t)

	if fp, _ := st.GetSetting("secret_key_fp"); fp != "" {
		t.Fatalf("初始不该有指纹，得到 %q", fp)
	}
	if err := st.SetSecret(box, "bili_cookie", "SESSDATA=abc"); err != nil {
		t.Fatal(err)
	}
	fp, err := st.GetSetting("secret_key_fp")
	if err != nil {
		t.Fatal(err)
	}
	if fp != box.Fingerprint() {
		t.Fatalf("指纹应为 %q，得到 %q", box.Fingerprint(), fp)
	}
}
