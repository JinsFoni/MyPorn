package platform

import (
	"strings"
	"unicode"
)

// 名字归一化：各站的 slug / 账号名都只认「小写字母数字」，
// 而艺人名里常带空格、变音符号和装饰，得先抹平才能拿来比对。

// foldRunes 是拉丁字母变音符号的折叠表：不折叠的话 Lucía 会变成 luca，
// 和站点上的 lucia 永远对不上。
var foldRunes = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'ă': "a", 'ą': "a",
	'ç': "c", 'ć': "c", 'č': "c",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i", 'į': "i",
	'ñ': "n", 'ń': "n", 'ň': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'ő': "o",
	'ś': "s", 'š': "s", 'ş': "s",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u", 'ů': "u", 'ű': "u",
	'ý': "y", 'ÿ': "y",
	'ź': "z", 'ż': "z", 'ž': "z",
	'ł': "l", 'ß': "ss", 'æ': "ae", 'œ': "oe", 'þ': "th", 'ð': "d",
}

// Slugify 把名字压成只含小写字母数字的 slug：折叠变音符号、丢掉其余字符。
// "Mini Diva" → "minidiva"，"Lucía Fernández" → "luciafernandez"。
func Slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if f, ok := foldRunes[r]; ok {
			b.WriteString(f)
			continue
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Tokens 把名字切成小写单词（同样折叠变音符号），用于判断两个名字是不是同一个人。
// 非拉丁文字原样保留，至少还能整词比较。
// "Mini Diva" → ["mini" "diva"]，"Mini  Diva | OF" → ["mini" "diva" "of"]。
func Tokens(name string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if f, ok := foldRunes[r]; ok {
			cur.WriteString(f)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}
