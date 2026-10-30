package bilibili

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// 扫码成功的响应长什么样：三件套在 Set-Cookie 里（SESSDATA 是 HttpOnly，只有这里能拿到），
// refresh_token 在 JSON 里。2026-10-30 参照 B 站 passport 的实际响应结构写的。
func TestCredentialFromResponse(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Add("Set-Cookie", "SESSDATA=abc%2C123%2Cdef*11; Path=/; Domain=bilibili.com; HttpOnly")
	resp.Header.Add("Set-Cookie", "bili_jct=deadbeef; Path=/; Domain=bilibili.com")
	resp.Header.Add("Set-Cookie", "DedeUserID=34899708; Path=/; Domain=bilibili.com")
	resp.Header.Add("Set-Cookie", "buvid3=F662-BUVID; Path=/; Domain=bilibili.com")

	cred, err := credentialFromResponse(resp, "refresh-token-xyz")
	if err != nil {
		t.Fatalf("应当解析成功，却报错: %v", err)
	}
	if cred.Sessdata != "abc%2C123%2Cdef*11" {
		t.Errorf("SESSDATA 取错: %q", cred.Sessdata)
	}
	if cred.BiliJct != "deadbeef" || cred.DedeUserID != "34899708" {
		t.Errorf("bili_jct/DedeUserID 取错: %q %q", cred.BiliJct, cred.DedeUserID)
	}
	if cred.Buvid3 != "F662-BUVID" {
		t.Errorf("buvid3 取错: %q", cred.Buvid3)
	}
	if cred.RefreshToken != "refresh-token-xyz" {
		t.Errorf("refresh_token 取错: %q", cred.RefreshToken)
	}
	if !cred.Complete() {
		t.Error("五个字段齐了，Complete 应当为真")
	}
}

func TestCredentialFromResponseErrors(t *testing.T) {
	build := func(cookies ...string) *http.Response {
		resp := &http.Response{Header: http.Header{}}
		for _, c := range cookies {
			resp.Header.Add("Set-Cookie", c)
		}
		return resp
	}
	cases := map[string]struct {
		resp  *http.Response
		token string
	}{
		// 少了 SESSDATA 就是没登录，没必要往下走。
		"没有 SESSDATA": {build("bili_jct=x; Path=/", "DedeUserID=1; Path=/"), "t"},
		// 有 SESSDATA 但没 refresh_token：能看 1080P，却永远无法续期——按失败处理，
		// 让用户重扫，而不是收下一条「用几天又失效」的凭据。
		"没有 refresh_token": {build("SESSDATA=s; Path=/", "bili_jct=x; Path=/", "DedeUserID=1; Path=/"), ""},
		"响应为空":             {nil, "t"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if cred, err := credentialFromResponse(c.resp, c.token); err == nil {
				t.Fatalf("应当报错，却得到 %+v", cred)
			}
		})
	}
}

// Cookie 头只发四个字段，顺序固定——日志里要能一眼看出哪一段换了。
func TestCredentialCookieHeader(t *testing.T) {
	cred := Credential{Sessdata: "s", BiliJct: "j", Buvid3: "b", DedeUserID: "1", RefreshToken: "t"}
	want := "buvid3=b; SESSDATA=s; bili_jct=j; DedeUserID=1"
	if got := cred.CookieHeader(); got != want {
		t.Errorf("得到 %q，期望 %q", got, want)
	}
	// 手填的 Cookie 里没有 buvid3 时不能拼出空的 "buvid3="。
	if got := (Credential{Sessdata: "s", BiliJct: "j", DedeUserID: "1"}).CookieHeader(); strings.Contains(got, "buvid3") {
		t.Errorf("没有 buvid3 时不该出现这个字段: %q", got)
	}
	// refresh_token 不进 Cookie 头（它是续期用的，不是 cookie）。
	if strings.Contains(want, "t") && strings.Contains(cred.CookieHeader(), "refresh") {
		t.Error("refresh_token 不应出现在 Cookie 头")
	}
}

// 第 2 步要的不是明文时间戳，而是 B 站公钥加密后的 128 字节密文（2048 位 RSA）。
func TestEncryptCorrespondTime(t *testing.T) {
	now := time.UnixMilli(1793302171000)
	got, err := encryptCorrespondTime(now)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if len(got) != 256 {
		t.Errorf("2048 位 RSA 的密文应是 128 字节 = 256 个 hex 字符，实际 %d", len(got))
	}
	if strings.Contains(got, "refresh_") {
		t.Error("密文里不该出现明文")
	}
	// 时间戳每次都不同 → 密文每次都不同（OAEP 每次随机填充，也保证不会相同）。
	again, err := encryptCorrespondTime(now)
	if err != nil {
		t.Fatalf("第二次加密失败: %v", err)
	}
	if got == again {
		t.Error("两次加密结果相同，OAEP 填充可能没生效")
	}
}

// correspond 页面里抠 refresh_csrf：真实页面结构（2026-10 版）是 <div id="1-name">…</div>。
func TestRefreshCSRFPattern(t *testing.T) {
	page := []byte(`<html><body><div id="1-name">b0cc8411ded2f9db2cff2edb3123acac</div></body></html>`)
	m := refreshCSRFPattern.FindSubmatch(page)
	if len(m) < 2 {
		t.Fatal("没匹配到 refresh_csrf")
	}
	if string(m[1]) != "b0cc8411ded2f9db2cff2edb3123acac" {
		t.Errorf("抠出的值不对: %q", m[1])
	}
	// 被风控换成验证页时应当抠不到（此时要报错，而不是拿空串去刷新）。
	if refreshCSRFPattern.MatchString(`<html><body>请完成验证</body></html>`) {
		t.Error("验证页不该匹配成功")
	}
}
