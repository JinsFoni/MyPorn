package xhamster

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// xHamster 从 2023 年起不再把播放地址明文写进页面：xplayerSettings.sources 里
// 每条 url 都是一串十六进制密文，播放器在浏览器里解密后再用。规则全部在这个文件里。
//
// 密文结构（见 xplayer.js 里那段解码函数）：
//
//	byte 0     算法编号（1–7，每个号对应一个不同的 PRNG）
//	byte 1..4  种子，小端 32 位
//	byte 5..   payload，逐字节与 PRNG 输出异或
//
// 解出来的明文是「https://video-h.xhcdn.com/key=…,end=…/media=hls4/multi=…/_TPL_.h264.mp4.m3u8」
// 这样的 master 清单地址（_TPL_ 占位符由分段清单替换）；主机名第一段还要按页面上的
// hostMap 换成真正能连的节点（video-h → video7 这种）。
//
// 站点随时可能换算法；换了解密会失败，调用方要能退回 downloadDropdownComponent
// 里那批明文直链（只有 144p–720p，但能播）。

// errDecrypt 表示密文解不开（算法变了或格式变了）。
var errDecrypt = errors.New("xHamster 播放地址解密失败")

// decryptSource 解密一条 sources 里的密文，返回明文地址。明文不是可打印 ASCII 时按失败处理。
func decryptSource(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 12 || len(raw)%2 != 0 || !isHexString(raw) {
		return "", errDecrypt
	}
	buf, err := hex.DecodeString(raw)
	if err != nil {
		return "", errDecrypt
	}
	if len(buf) < 5 {
		return "", errDecrypt
	}
	seed := int32(uint32(buf[1]) | uint32(buf[2])<<8 | uint32(buf[3])<<16 | uint32(buf[4])<<24)
	next, err := newKeystream(buf[0], seed)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(buf)-5)
	for i, c := range buf[5:] {
		plain[i] = c ^ next()
	}
	out := string(plain)
	if !isPrintableASCII(out) {
		return "", errDecrypt
	}
	return out, nil
}

// isPrintableASCII 判断明文是否「像一条地址」：只允许可打印字符。
func isPrintableASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// newKeystream 按算法编号返回一个字节流生成器。
// 七个算法逐一照抄站点脚本里的位运算；JavaScript 的位运算是 32 位有符号回绕，
// 所以这里用 uint32 做加减与无符号右移（>>>），最后 i32 转回 int32 取低 8 位，语义才对得上。
func newKeystream(algo byte, seed int32) (func() byte, error) {
	i := seed
	switch algo {
	case 1: // LCG
		return func() byte {
			i = i32(uint32(imul(i, 1664525)) + 0x3c6ef35f)
			return byte(i)
		}, nil
	case 2: // xorshift32
		return func() byte {
			i ^= i << 13
			i ^= i32(uint32(i) >> 17)
			i ^= i << 5
			return byte(i)
		}, nil
	case 3: // 三轮混合哈希
		return func() byte {
			i = i32(uint32(i) + 0x9e3779b9)
			e := i ^ i32(uint32(i)>>16)
			e = imul(e, i32(0x85ebca77))
			e ^= i32(uint32(e) >> 13)
			e = imul(e, i32(0xc2b2ae3d))
			e ^= i32(uint32(e) >> 16)
			return byte(e)
		}, nil
	case 4:
		return func() byte {
			i = i32(uint32(i) + 0x6d2b79f5)
			e := i32(uint32(i)<<7 | uint32(i)>>25)
			e = i32(uint32(e) + 0x9e3779b9)
			e ^= i32(uint32(e) >> 11)
			e = imul(e, 0x27d4eb2d)
			return byte(e)
		}, nil
	case 5:
		return func() byte {
			i ^= i << 7
			i ^= i32(uint32(i) >> 9)
			i ^= i << 8
			i = i32(uint32(i) + 0xa5a5a5a5)
			return byte(i)
		}, nil
	case 6:
		return func() byte {
			i = i32(uint32(imul(i, 0x2c9277b5)) + 0xac564b05)
			v := uint32(i) ^ (uint32(i) >> 18)
			return byte(v >> ((uint32(i) >> 27) & 31))
		}, nil
	case 7:
		return func() byte {
			i = i32(uint32(i) + 0x9e3779b9)
			e := i ^ (i << 5)
			e = imul(e, 0x7feb352d)
			e ^= i32(uint32(e) >> 15)
			e = imul(e, i32(0x846ca68b))
			return byte(e)
		}, nil
	default:
		return nil, fmt.Errorf("%w: 未知算法 %d", errDecrypt, algo)
	}
}

// imul 等价于 JavaScript 的 Math.imul：32 位相乘取低 32 位。
func imul(a, b int32) int32 { return int32(int64(a) * int64(b)) }

// i32 把 32 位无符号值按补码转回有符号（站点脚本里的常量都写成无符号十六进制，
// 直接写 int32 字面量会因为超范围编不过）。
func i32(v uint32) int32 { return int32(v) }
