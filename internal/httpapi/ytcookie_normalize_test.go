package httpapi

import (
	"strings"
	"testing"
)

func TestNormalizeYtCookie(t *testing.T) {
	// 整段 Cookie 头（F12 里复制的那种，带 cookie: 前缀）
	raw := "cookie: SID=g.a000abc; HSID=Afx; SSID=Ax; APISID=xyz; SAPISID=abc; LOGIN_INFO=AFmmF2:QUJD"
	got, err := normalizeYtCookie(raw)
	if err != nil {
		t.Fatalf("整段 Cookie 头应该认: %v", err)
	}
	if !strings.HasPrefix(got, "# Netscape HTTP Cookie File") {
		t.Fatalf("没补文件头:\n%s", got)
	}
	for _, want := range []string{"\tSID\tg.a000abc", "\tLOGIN_INFO\tAFmmF2:QUJD"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cookie: ") {
		t.Fatalf("`cookie:` 前缀没剥掉:\n%s", got)
	}

	// Netscape 原样（含 #HttpOnly_ 行 + 重复项要去重）
	netscape := "# Netscape HTTP Cookie File\n" +
		".youtube.com\tTRUE\t/\tTRUE\t1827155755\tSID\told\n" +
		"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1827155755\tSID\tnew\n" +
		".youtube.com\tTRUE\t/\tTRUE\t1827155755\tLOGIN_INFO\tAFmm\n"
	got2, err := normalizeYtCookie(netscape)
	if err != nil {
		t.Fatalf("Netscape 应该照收: %v", err)
	}
	if strings.Contains(got2, "\told") || !strings.Contains(got2, "\tnew") {
		t.Fatalf("同名 cookie 应留最后一个:\n%s", got2)
	}
	if n := strings.Count(got2, "\tSID\t"); n != 1 {
		t.Fatalf("SID 出现 %d 次", n)
	}

	// 没有登录态：要拦住（不然设置里显示「已配置」但解析一直被当游客）
	if _, err := normalizeYtCookie("VISITOR_INFO1_LIVE=abc; YSC=xyz"); err == nil {
		t.Fatal("没有 LOGIN_INFO 的串应该报错")
	}
	// 空串 = 清除
	if got, err := normalizeYtCookie("   "); err != nil || got != "" {
		t.Fatalf("空串应返回空，得到 %q / %v", got, err)
	}
}
