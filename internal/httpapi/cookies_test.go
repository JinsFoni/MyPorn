package httpapi

import (
	"strings"
	"testing"
)

// 用户实际粘过的三种形状都要认。第 3 种（cookies.txt）以前会被拆成两百多个假字段，
// 症状是「配了 Cookie 还是 720P、还是被限流」——见 normalizeBiliCookie 的注释。
func TestNormalizeBiliCookie(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "整段 Cookie 头",
			in:   "SESSDATA=abc%2C1700000000%2Cxyz; bili_jct=deadbeef; DedeUserID=34899708",
			want: "SESSDATA=abc%2C1700000000%2Cxyz; bili_jct=deadbeef; DedeUserID=34899708",
		},
		{
			name: "只贴 SESSDATA 的值",
			in:   "abc%2C1700000000%2C0123456789abcdef0123456789abcdef",
			want: "SESSDATA=abc%2C1700000000%2C0123456789abcdef0123456789abcdef",
		},
		{
			name: "换行分隔的键值对",
			in:   "SESSDATA=abc%2C1700000000%2Cxyz\nDedeUserID=34899708\n",
			want: "SESSDATA=abc%2C1700000000%2Cxyz; DedeUserID=34899708",
		},
		{
			name: "cookies.txt（Netscape，含注释与 HttpOnly 行）",
			in: "# Netscape HTTP Cookie File\n" +
				"# https://curl.haxx.se/rfc/cookie_spec.html\n" +
				"# This is a generated file! Do not edit.\n\n" +
				".bilibili.com\tTRUE\t/\tFALSE\t1795708705\tbuvid3\tF662270A-D1F0\t\n" +
				"#HttpOnly_.bilibili.com\tTRUE\t/\tFALSE\t1809347801\tSESSDATA\tabc%2C1700000000%2Cxyz\t\n" +
				".bilibili.com\tTRUE\t/\tFALSE\t1809347801\tDedeUserID\t34899708\t\n",
			want: "buvid3=F662270A-D1F0; SESSDATA=abc%2C1700000000%2Cxyz; DedeUserID=34899708",
		},
		{
			name: "空格对齐的 cookies.txt",
			in:   ".bilibili.com TRUE / FALSE 1809347801 SESSDATA abc%2C1700000000%2Cxyz",
			want: "SESSDATA=abc%2C1700000000%2Cxyz",
		},
		{
			name: "同名 cookie 以最后一个为准",
			in:   "SESSDATA=old%2C1%2Caa; SESSDATA=new%2C2%2Cbb",
			want: "SESSDATA=new%2C2%2Cbb",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeBiliCookie(c.in)
			if err != nil {
				t.Fatalf("normalizeBiliCookie: %v", err)
			}
			if got != c.want {
				t.Errorf("得到 %q，期望 %q", got, c.want)
			}
			// 无论哪种输入，结果里 SESSDATA 只能出现一次。
			if n := strings.Count(got, "SESSDATA="); n != 1 {
				t.Errorf("SESSDATA 出现 %d 次，应当只有 1 次: %q", n, got)
			}
		})
	}
}

func TestNormalizeBiliCookieRejects(t *testing.T) {
	cases := map[string]string{
		"没有 SESSDATA": "bili_jct=deadbeef; DedeUserID=34899708",
		"纯垃圾":         "hello world",
		"注释行不算字段":     "# Netscape HTTP Cookie File",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := normalizeBiliCookie(in); err == nil {
				t.Fatalf("应当报错，却得到 %q", got)
			}
		})
	}
}
