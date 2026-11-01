package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JinsFoni/MyPorn/internal/config"
	"github.com/JinsFoni/MyPorn/internal/events"
	"github.com/JinsFoni/MyPorn/internal/imgcache"
	"github.com/JinsFoni/MyPorn/internal/platform"
	"github.com/JinsFoni/MyPorn/internal/secret"
	"github.com/JinsFoni/MyPorn/internal/store"
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
			name: "cookies.txt 里别的站点要丢掉",
			in: ".google.com\tTRUE\t/\tFALSE\t1809347801\tSID\tgoogle-session\t\n" +
				"space.bilibili.com\tFALSE\t/\tFALSE\t1809347801\tSESSDATA\tabc%2C1700000000%2Cxyz\t\n" +
				"www.youtube.com\tFALSE\t/\tTRUE\t1809347801\tSAPISID\tyt-session\t\n",
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

// 库里有密文、但服务端这把密钥解不开时，接口必须给出 cookieBroken，
// 而不是像「没配」那样 hasCookie=false 了事。
//
// 真人踩过（2026-10-29）：服务被另一套 APP_ENCRYPTION_KEY 起过一次，库里 2336 字节的
// bili_cookie 就解不开了，页面显示「没填 B 站 Cookie」，用户以为要重配一遍。
func TestBrokenSecretIsNotReportedAsMissing(t *testing.T) {
	box := newTestSecretBox(t)    // 服务端现在这把
	writer := newTestSecretBox(t) // 当初写库那把（另一把密钥 = 换过密钥的现场）

	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetSecret(writer, "bili_cookie", "SESSDATA=abc%2C1%2Cxyz; DedeUserID=1"); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{DataDir: tmp, LibraryRoot: filepath.Join(tmp, "media")}
	srv := New(cfg, st, platform.NewRegistry(), nil, events.NewHub(), nil, box, log, nil,
		imgcache.New(t.TempDir(), nil, "", log))

	// 设置接口：未配置 + 解不开，两个标志位都要对。
	rec := httptest.NewRecorder()
	srv.handleGetSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	var settings struct {
		Bilibili struct {
			HasCookie    bool `json:"hasCookie"`
			CookieBroken bool `json:"cookieBroken"`
		} `json:"bilibili"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
		t.Fatalf("解析设置: %v (%s)", err, rec.Body.String())
	}
	if settings.Bilibili.HasCookie || !settings.Bilibili.CookieBroken {
		t.Fatalf("应 hasCookie=false / cookieBroken=true，得到 %+v", settings.Bilibili)
	}

	// 健康检查也要带出来：/bilibili 页的横幅就是按它分支的。
	rec = httptest.NewRecorder()
	srv.handleBiliHealth(rec, httptest.NewRequest(http.MethodGet, "/api/bilibili/health", nil))
	var health struct {
		HasCookie    bool `json:"hasCookie"`
		CookieBroken bool `json:"cookieBroken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("解析健康检查: %v (%s)", err, rec.Body.String())
	}
	if health.HasCookie || !health.CookieBroken {
		t.Fatalf("健康检查应 hasCookie=false / cookieBroken=true，得到 %+v", health)
	}

	// 换成当初那把密钥：同一个库立刻恢复正常（说明问题在密钥，不在数据）。
	srv.box = writer
	rec = httptest.NewRecorder()
	srv.handleGetSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Bilibili.HasCookie || settings.Bilibili.CookieBroken {
		t.Fatalf("换回原作者密钥后应 hasCookie=true / cookieBroken=false，得到 %+v", settings.Bilibili)
	}
}

func newTestSecretBox(t *testing.T) *secret.Box {
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
