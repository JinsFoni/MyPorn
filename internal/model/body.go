package model

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Body 是艺人的身材数据：身高 + 三围，统一归一到公制厘米。
//
// 站点的资料行本来就带这些数字，但写法五花八门：身高可能是 `5' 6" (167cm)`、
// `5 ft 6 in` 或 `167 cm`，三围可能是 `35-25-36`（英寸）也可能是 `89-64-91`（厘米）。
// 换算放在抓取这一层做一次，API 和前端就只认厘米，不用各自猜单位。
// 某项为 0 表示站点没给、或者没认出来。
type Body struct {
	HeightCm int `json:"heightCm,omitempty"`
	Bust     int `json:"bust,omitempty"`
	Waist    int `json:"waist,omitempty"`
	Hips     int `json:"hips,omitempty"`
}

// Empty 报告四项都没解析出来（这时不写库、页面上也不出「身材」那一行）。
func (b Body) Empty() bool {
	return b.HeightCm == 0 && b.Bust == 0 && b.Waist == 0 && b.Hips == 0
}

var (
	// 身高优先认站点自己给的厘米值：`5' 6" (167cm)` 里的括号才是站点口径，换算会差一厘米。
	heightCmRe   = regexp.MustCompile(`(\d{2,3})\s*(?:cm|厘米|公分)`)
	heightFtInRe = regexp.MustCompile(`(\d)\s*(?:'|′|ft|feet)\s*(\d{1,2})`)
	heightFtRe   = regexp.MustCompile(`(\d)\s*(?:'|′|ft|feet)\s*$`)
	bodyNumberRe = regexp.MustCompile(`\d+(?:\.\d+)?`)
)

// ParseBody 从资料行里认出「身高」「三围」，归一成公制。
//
// 返回的 rows 去掉了已经变成 Body 的那两行——同一条数据不该在一屏里出现两次
// （页头一行 `167cm · B89 · W64 · H91`，资料区块里就不必再列一遍）。
// 认不出来的时候原行原样保留：宁可照抄站点的写法，也不要让它无声消失。
func ParseBody(rows []ProfileField) (Body, []ProfileField) {
	var b Body
	kept := make([]ProfileField, 0, len(rows))
	for _, r := range rows {
		label := strings.ToLower(strings.TrimSpace(r.Label))
		if strings.Contains(label, "身高") || strings.Contains(label, "height") {
			if cm := parseHeightCm(r.Value); cm > 0 {
				b.HeightCm = cm
				continue
			}
		}
		if strings.Contains(label, "三围") || strings.Contains(label, "measurement") ||
			strings.Contains(label, "尺寸") {
			if bust, waist, hips := parseMeasurements(r.Value); bust+waist+hips > 0 {
				b.Bust, b.Waist, b.Hips = bust, waist, hips
				continue
			}
		}
		kept = append(kept, r)
	}
	return b, kept
}

// parseHeightCm 把各种身高写法换成厘米，认不出来返回 0。
func parseHeightCm(raw string) int {
	s := strings.ToLower(strings.TrimSpace(raw))
	if m := heightCmRe.FindStringSubmatch(s); m != nil {
		return saneHeight(atoiOrZero(m[1]))
	}
	if m := heightFtInRe.FindStringSubmatch(s); m != nil {
		return saneHeight(cmFromInches(atoiOrZero(m[1])*12 + atoiOrZero(m[2])))
	}
	if m := heightFtRe.FindStringSubmatch(s); m != nil {
		return saneHeight(cmFromInches(atoiOrZero(m[1]) * 12))
	}
	// 光秃秃一个数字：三位数当厘米（`167`），其余当没写清单位，不猜。
	if n := atoiOrZero(s); n >= 100 {
		return saneHeight(n)
	}
	return 0
}

// parseMeasurements 解析三围，返回厘米制的胸/腰/臀，认不出来返回 0。
//
// 单位判断顺序：写了 cm/厘米 就是厘米；写了 in/英寸/引号就是英寸；
// 都没写就看量级——三围的数字在英制下都不超过 60（35-25-36），公制下都超过（89-64-91）。
func parseMeasurements(raw string) (bust, waist, hips int) {
	s := strings.ToLower(raw)
	nums := bodyNumberRe.FindAllString(s, -1)
	if len(nums) < 3 {
		return 0, 0, 0
	}
	vals := [3]float64{}
	for i := range vals {
		if f, err := strconv.ParseFloat(nums[i], 64); err == nil {
			vals[i] = f
		} else {
			return 0, 0, 0
		}
	}

	inches := false
	switch {
	case strings.Contains(s, "cm") || strings.Contains(s, "厘米") || strings.Contains(s, "公分"):
		inches = false
	case strings.Contains(s, `"`) || strings.Contains(s, `″`) || strings.Contains(s, "in") ||
		strings.Contains(s, "英寸"):
		inches = true
	default:
		inches = vals[0] < 60 && vals[1] < 60 && vals[2] < 60
	}

	out := [3]int{}
	for i, v := range vals {
		if inches {
			out[i] = cmFromInches(int(math.Round(v)))
		} else {
			out[i] = int(math.Round(v))
		}
	}
	bust, waist, hips = out[0], out[1], out[2]
	if bust < 60 || bust > 170 {
		bust = 0
	}
	if waist < 45 || waist > 150 {
		waist = 0
	}
	if hips < 60 || hips > 180 {
		hips = 0
	}
	return bust, waist, hips
}

// cmFromInches 英寸换算厘米（四舍五入到整厘米）。
func cmFromInches(in int) int {
	if in <= 0 {
		return 0
	}
	return int(math.Round(float64(in) * 2.54))
}

// saneHeight 让明显不对的数字进不了库（比如把「浏览量」当成了身高）。
func saneHeight(cm int) int {
	if cm < 120 || cm > 220 {
		return 0
	}
	return cm
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
