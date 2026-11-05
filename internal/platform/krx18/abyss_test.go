package krx18

// Abyss（Server 2 / mov18plus.cloud）这一层的单元测试。
//
// 期望值全部来自实测：token 与浏览器里 Service Worker 抓到的那条逐字节比对过，
// 分片 key 也与三档源各自抓到的 key 对上（见 docs/M8-KRX18.md §11.4）。

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// TestAbyssTokenKey 盯的是最容易写错的那一步：seed 是 JS number，
// md5 拿到 number 会把字符串的字符按**数值**取（"1"=1 而不是 0x31）。
func TestAbyssTokenKey(t *testing.T) {
	cases := []struct {
		seed int64
		want string
	}{
		// 1080p h264（片子 3355）
		{1634796856, "aaccae5faad13ef8eec53c4344a7c2c5"},
		// 360p h264：这一档的 key 是从浏览器里抓到的原始值
		{582520983, "81cbae139c2599840d44ffb45101c7db"},
		// 1080p av1
		{1495725746, "5b36020f43043115c29df784ee9e157a"},
	}
	for _, c := range cases {
		if got := string(abyssTokenKey(c.seed)); got != c.want {
			t.Errorf("abyssTokenKey(%d) = %s, want %s", c.seed, got, c.want)
		}
	}
	if plain := plainMD5("1634796856"); plain == string(abyssTokenKey(1634796856)) {
		t.Fatalf("按字符串 md5 竟然一样（%s），说明这条测试没盯住数字位那个坑", plain)
	}
}

// TestAbyssToken 用的是能直接换出 206 的那三条 token（1080p h264 的第 0/1/311 片）。
func TestAbyssToken(t *testing.T) {
	const size = 1634796856
	cases := map[int]string{
		0:   "QUk0MWhXcEhGNHJUWEU3Q2pxamxZQU5mcHozaER0cWxRQjRhUU44UDdjYTR1VnU5",
		1:   "QUk0MWhXcEhGNHJUWEU3Q2pxamxZQU5mcHozaER0cWxRQjRhUU44UDdjYTR1VnU4",
		311: "QUk0MWhXcEhGNHJUWEU3Q2pxamxZQU5mcHozaER0cWxRQjRhUU44UDdjYTR1VnUrdUVB",
	}
	m := &abyssMedia{MD5ID: 26709409, ResID: 5, Size: size, Host: "ygfi0yfyc10.sssrr.org"}
	for i, want := range cases {
		plain := fmt.Sprintf("/mp4/26709409/5/%d/%d/%d", size, abyssChunkSize, i)
		if got := abyssToken(plain, size); got != want {
			t.Errorf("第 %d 片 token = %s, want %s", i, got, want)
		}
		url := m.chunkURL(i)
		if !strings.Contains(url, want) {
			t.Errorf("第 %d 片地址没带上 token: %s", i, url)
		}
	}
	if got, want := m.chunks(), 312; got != want {
		t.Errorf("分片数 = %d, want %d", got, want)
	}
}

// TestAbyssConfigKey 是 media 那层的 key：这一次 JS 传的是**字符串**，走标准 md5。
func TestAbyssConfigKey(t *testing.T) {
	got := string(abyssConfigKey(1995, "N1N5-Y7-C", 26709409))
	if want := "cfa9d8d8af39c1bba859513c50b5a8d4"; got != want {
		t.Fatalf("abyssConfigKey = %s, want %s", got, want)
	}
}

// TestParseAbyssPage 覆盖两层：json 里那些**裸二进制字节**要抠得出来（标准库会把它换成
// U+FFFD，密文就废了），抠出来之后还要用 md5 的 ASCII key 解成 manifest。
func TestParseAbyssPage(t *testing.T) {
	const (
		userID = int64(42)
		slug   = "Ab-Cd-Ef"
		md5ID  = int64(1234567)
	)
	manifest := `{"mp4":{"sources":[` +
		`{"label":"360p","res_id":2,"size":1000,"codec":"h264","status":true,"sub":"aaa111"},` +
		`{"label":"1080p","res_id":5,"size":9000,"codec":"av1","status":true,"sub":"bbb222"},` +
		`{"label":"1080p","res_id":5,"size":8000,"codec":"h264","status":true,"sub":"ccc333"}` +
		`],"domains":["aaa111.sssrr.org","bbb222.sssrr.org","ccc333.sssrr.org"]}}`

	key := abyssConfigKey(userID, slug, md5ID)
	// CTR 是对称的：加密和解密是同一次运算。
	media, err := abyssCTR(key, []byte(manifest))
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	// 造一个和站点同样形态的 json：控制字符写成 \u00XX，非 UTF-8 字节原样落进去。
	page := fmt.Sprintf(`<html><script>const datas = "%s";</script></html>`,
		base64.StdEncoding.EncodeToString([]byte(
			fmt.Sprintf(`{"slug":"%s","md5_id":%d,"user_id":%d,"media":"%s"}`,
				slug, md5ID, userID, abyssJSONEscapeForTest(media)))))

	m, err := parseAbyssPage(page)
	if err != nil {
		t.Fatalf("parseAbyssPage: %v", err)
	}
	// 1080p 里挑 h264 那一档（同分辨率下浏览器兼容性最好）
	if m.ResID != 5 || m.Size != 8000 {
		t.Fatalf("挑错了档位: res_id=%d size=%d，应挑 1080p h264（size 8000）", m.ResID, m.Size)
	}
	if m.Host != "ccc333.sssrr.org" {
		t.Fatalf("域名 = %s, want ccc333.sssrr.org", m.Host)
	}
	if m.Height != 1080 {
		t.Fatalf("高度 = %d, want 1080", m.Height)
	}
}

// TestAbyssAltEmbed 盯老形态 /v/<code>/ 到 ?v=<code> 的换算。
func TestAbyssAltEmbed(t *testing.T) {
	got, ok := abyssAltEmbed("https://mov18plus.cloud/v/8UIvI13M70SY/")
	if !ok || got != "https://mov18plus.cloud/?v=8UIvI13M70SY" {
		t.Fatalf("abyssAltEmbed = %q,%v", got, ok)
	}
	if _, ok := abyssAltEmbed("https://mov18plus.cloud/?v=N1N5-Y7-C"); ok {
		t.Fatal("已经是新形态就不该换")
	}
	if _, ok := abyssAltEmbed("https://mov18plus.cloud/"); ok {
		t.Fatal("根路径不该被当成老形态")
	}
}

func TestAbyssJSONRawString(t *testing.T) {
	// 裸 0xF1 是非法 UTF-8；标准库会把这种字节换成 U+FFFD，这里必须原样拿回来。
	raw := []byte("{\"slug\":\"a\",\"media\":\"\\u0017\\u0007\xf1\\\"x\\\\y\"}")
	got, err := abyssJSONRawString(raw, "media")
	if err != nil {
		t.Fatalf("abyssJSONRawString: %v", err)
	}
	want := []byte{0x17, 0x07, 0xF1, '"', 'x', '\\', 'y'}
	if string(got) != string(want) {
		t.Fatalf("抠出来的字节 = %x, want %x", got, want)
	}
}

func TestParseByteRange(t *testing.T) {
	cases := []struct {
		raw      string
		total    int64
		from, to int64
		ok       bool
	}{
		{"bytes=0-1023", 5000, 0, 1023, true},
		{"bytes=1000-", 5000, 1000, 4999, true},
		{"bytes=-200", 5000, 4800, 4999, true},
		{"bytes=4000-99999", 5000, 4000, 4999, true},
		{"bytes=5000-", 5000, 0, 0, false},
		{"bytes=9-5", 5000, 0, 0, false},
		{"items=0-1", 5000, 0, 0, false},
	}
	for _, c := range cases {
		from, to, ok := parseByteRange(c.raw, c.total)
		if ok != c.ok || (ok && (from != c.from || to != c.to)) {
			t.Errorf("parseByteRange(%q) = (%d,%d,%v), want (%d,%d,%v)",
				c.raw, from, to, ok, c.from, c.to, c.ok)
		}
	}
}

// TestAbyssHost 盯「优先 sub 对得上的域名，否则按下标取模」这条规则。
func TestAbyssHost(t *testing.T) {
	domains := []string{"a.sssrr.org", "b.sssrr.org", "c.sssrr.org"}
	if got := abyssHost(domains, "b", 9000); got != "b.sssrr.org" {
		t.Fatalf("按 sub 挑域名 = %s, want b.sssrr.org", got)
	}
	if got := abyssHost(domains, "", 9000); got != domains[9000%3] {
		t.Fatalf("按下标挑域名 = %s, want %s", got, domains[9000%3])
	}
}

// plainMD5 是对照用：md5 一个**字符串**（不是数字位）。
func plainMD5(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// abyssJSONEscapeForTest 按站点的口径把二进制塞进 json 字符串：
// 控制字符写成 \u00XX，双引号和反斜杠转义，其余字节原样（包括非 UTF-8 的那些）。
func abyssJSONEscapeForTest(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == '"':
			sb.WriteString(`\"`)
		case c == '\\':
			sb.WriteString(`\\`)
		case c < 0x20:
			fmt.Fprintf(&sb, `\u%04x`, c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}
