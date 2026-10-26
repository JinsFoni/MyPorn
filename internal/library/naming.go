// Package library 负责本地库落盘：命名模板、路径安全、磁盘空间、
// Emby 规范元数据（nfo / 图片）与 ffmpeg 封装。
package library

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultTemplate 是默认命名模板（只保留标题），符合 FR-25。
const DefaultTemplate = "{title}"

// MaxNameRunes 是单个路径段（文件名/文件夹名）的长度上限，见 FR-26。
const MaxNameRunes = 120

// NameContext 提供模板变量取值。
type NameContext struct {
	Title      string // 影片标题
	Date       string // 发布日期 YYYY-MM-DD
	Performer  string // 主艺人名
	Platform   string // 平台显示名，如 Pornhub
	ID         string // 平台侧 id，便于回溯
	Resolution string // 例如 2160p
}

// tokens 同时支持英文与中文变量名，方便用户按习惯书写模板。
var tokens = []struct {
	keys []string
	get  func(NameContext) string
}{
	{[]string{"title", "标题"}, func(c NameContext) string { return c.Title }},
	{[]string{"date", "发布日期"}, func(c NameContext) string { return c.Date }},
	{[]string{"performer", "artist", "艺人"}, func(c NameContext) string { return c.Performer }},
	{[]string{"platform", "平台"}, func(c NameContext) string { return c.Platform }},
	{[]string{"id"}, func(c NameContext) string { return c.ID }},
	{[]string{"resolution", "分辨率"}, func(c NameContext) string { return c.Resolution }},
}

// unknownToken 匹配残留的 {xxx} 占位符，渲染后直接清掉。
var unknownToken = regexp.MustCompile(`\{[^{}]{0,32}\}`)

// RenderName 渲染命名模板并做清洗；结果一定非空、可安全用作单个路径段。
func RenderName(tmpl string, c NameContext) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultTemplate
	}
	out := tmpl
	for _, t := range tokens {
		val := Sanitize(t.get(c), MaxNameRunes)
		for _, k := range t.keys {
			out = strings.ReplaceAll(out, "{"+k+"}", val)
		}
	}
	out = unknownToken.ReplaceAllString(out, "")
	// 模板可能被渲染成 " -  - " 这类空壳，收尾再清一次。
	out = Sanitize(out, MaxNameRunes)
	if out == "" {
		out = Sanitize(c.ID, MaxNameRunes)
	}
	if out == "" {
		out = "untitled"
	}
	return out
}

// invalidChars 是需要替换掉的文件系统保留字符（Windows 最严格，统一按它处理）。
var invalidChars = regexp.MustCompile(`[\\/:*?"<>|]+`)

var multiSpace = regexp.MustCompile(`\s+`)

// Sanitize 清洗单个路径段：替换非法字符、折叠空白、去首尾空格与点、限长。
func Sanitize(s string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = MaxNameRunes
	}
	// 控制字符先干掉（换行会直接破坏路径）。
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == unicode.ReplacementChar {
			return ' '
		}
		return r
	}, s)
	s = invalidChars.ReplaceAllString(s, " ")
	s = multiSpace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ". ")
	// 路径段不能是 . 或 ..
	if s == "" || s == "." || s == ".." {
		return ""
	}
	if utf8.RuneCountInString(s) > maxRunes {
		runes := []rune(s)
		s = strings.TrimSpace(string(runes[:maxRunes]))
		s = strings.Trim(s, ". ")
	}
	return s
}

// MoviePath 是一部影片在库中的落盘位置。
type MoviePath struct {
	RelDir    string // 相对库根的目录，如 "Pornhub/Mini Diva/Some Title"
	AbsDir    string // 绝对目录
	VideoName string // 影片文件名（不含扩展名），与目录同名，Emby 电影规范
}

// BuildMoviePath 组合 {库根}/{平台}/{艺人}/{影片名}/，并校验不越出库根。
func BuildMoviePath(root, platformName, performerName, movieName string) (MoviePath, error) {
	if strings.TrimSpace(root) == "" {
		return MoviePath{}, errPath("库根目录为空")
	}
	segments := []string{
		Sanitize(platformName, MaxNameRunes),
		Sanitize(performerName, MaxNameRunes),
		Sanitize(movieName, MaxNameRunes),
	}
	for i, seg := range segments {
		if seg == "" {
			// 艺人名可能为空（影片没有关联艺人），用平台内固定占位，避免层级塌陷。
			segments[i] = "Unknown"
		}
	}
	rel := filepath.Join(segments...)
	abs := filepath.Join(root, rel)

	// 双保险：即使清洗有漏网之鱼，也确保最终路径仍在库根内。
	if !WithinRoot(root, abs) {
		return MoviePath{}, errPath("渲染出的路径越出库根: " + rel)
	}
	return MoviePath{RelDir: rel, AbsDir: abs, VideoName: segments[2]}, nil
}

// WithinRoot 判断 target 是否位于 root 之内（防目录穿越，见非功能需求 §安全）。
func WithinRoot(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}

type errPath string

func (e errPath) Error() string { return string(e) }
