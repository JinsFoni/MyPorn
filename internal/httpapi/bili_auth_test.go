package httpapi

import "testing"

// 续期要用的是四段特定的 cookie，别的字段带了也没用（而且会让「哪一段换了」更难比对）。
func TestParseBiliCookieCredential(t *testing.T) {
	raw := "buvid3=BU-1; b_nut=1764172708; SESSDATA=abc%2C123%2Cdef*11; " +
		"bili_jct=deadbeef; DedeUserID=34899708; theme_style=dark"
	cred := parseBiliCookieCredential(raw)
	if cred.Sessdata != "abc%2C123%2Cdef*11" {
		t.Errorf("SESSDATA 取错: %q", cred.Sessdata)
	}
	if cred.BiliJct != "deadbeef" {
		t.Errorf("bili_jct 取错: %q", cred.BiliJct)
	}
	if cred.DedeUserID != "34899708" {
		t.Errorf("DedeUserID 取错: %q", cred.DedeUserID)
	}
	if cred.Buvid3 != "BU-1" {
		t.Errorf("buvid3 取错: %q", cred.Buvid3)
	}
	// refresh_token 不在 Cookie 头里（它单独存一个 secret），这里必须是空的。
	if cred.RefreshToken != "" {
		t.Errorf("refresh_token 不该从 Cookie 头里来: %q", cred.RefreshToken)
	}
}

// 空串、残缺串不能 panic，也不该编出一个「看起来能用」的凭据。
func TestParseBiliCookieCredentialEdge(t *testing.T) {
	for _, raw := range []string{"", "   ", "SESSDATA", "=;;;", "SESSDATA="} {
		cred := parseBiliCookieCredential(raw)
		if cred.Sessdata != "" {
			t.Errorf("%q 不该解析出 SESSDATA，得到 %q", raw, cred.Sessdata)
		}
		if cred.Complete() {
			t.Errorf("%q 不该被当成完整凭据", raw)
		}
	}
}
