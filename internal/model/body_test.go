package model

import "testing"

// 身高：站点给厘米就以站点为准，只给英制才换算，认不出来的不许瞎猜。
func TestParseHeightCm(t *testing.T) {
	cases := map[string]int{
		`5' 6" (167cm)`:      167, // 括号里的厘米是站点口径，换算会差 1cm
		`5 ft 6 in (167 cm)`: 167,
		`167cm`:              167,
		`167`:                167,
		`5' 6"`:              168, // 66in * 2.54 = 167.64
		`5'7"`:               170, // 67in * 2.54 = 170.18
		`5'`:                 152, // 60in
		`1.67 m`:             0,   // 没写清单位的不猜
		`very tall`:          0,
		`16`:                 0, // 两位数当厘米太矮，当英尺太离谱
		`999cm`:              0, // 超出人高范围
	}
	for in, want := range cases {
		if got := parseHeightCm(in); got != want {
			t.Errorf("parseHeightCm(%q) = %d，期望 %d", in, got, want)
		}
	}
}

// 三围：英制换厘米，公制原样；顺序必须是 胸/腰/臀。
func TestParseMeasurements(t *testing.T) {
	cases := []struct {
		in                string
		bust, waist, hips int
	}{
		{"35-25-36", 89, 64, 91},        // 英制（Mini Diva 那组）
		{"89-64-91", 89, 64, 91},        // 公制原样
		{"89 - 64 - 91 cm", 89, 64, 91}, // 明确写了 cm
		{"35-25-36 in", 89, 64, 91},     // 明确写了 in
		{"34B-24-35", 86, 61, 89},       // 罩杯字母不影响取数
		{"35-25", 0, 0, 0},              // 凑不够三个数就不收
		{"", 0, 0, 0},
	}
	for _, c := range cases {
		bust, waist, hips := parseMeasurements(c.in)
		if bust != c.bust || waist != c.waist || hips != c.hips {
			t.Errorf("parseMeasurements(%q) = %d/%d/%d，期望 %d/%d/%d",
				c.in, bust, waist, hips, c.bust, c.waist, c.hips)
		}
	}
}

// ParseBody 要从资料行里认出这两行并把它们摘掉，其余行顺序不许动。
func TestParseBodyConsumesRows(t *testing.T) {
	rows := []ProfileField{
		{Label: "感情状态", Value: "单身"},
		{Label: "三围", Value: "35-25-36"},
		{Label: "身高", Value: `5' 6" (167cm)`},
		{Label: "体重", Value: "111lbs. (50kg)"},
	}
	body, kept := ParseBody(rows)
	want := Body{HeightCm: 167, Bust: 89, Waist: 64, Hips: 91}
	if body != want {
		t.Errorf("身材 = %+v，期望 %+v", body, want)
	}
	if len(kept) != 2 || kept[0].Label != "感情状态" || kept[1].Label != "体重" {
		t.Errorf("身高/三围应从资料行里摘掉，其余保持原顺序，实际 %+v", kept)
	}
}

// 认不出来的行原样保留：宁可照抄站点的写法，也不要让它无声消失。
func TestParseBodyKeepsUnparsedRows(t *testing.T) {
	rows := []ProfileField{
		{Label: "身高", Value: "1.67 m"},
		{Label: "三围", Value: "保密"},
	}
	body, kept := ParseBody(rows)
	if !body.Empty() {
		t.Errorf("认不出来就该是空的，实际 %+v", body)
	}
	if len(kept) != 2 {
		t.Errorf("认不出来的行要留着，实际 %d 行: %+v", len(kept), kept)
	}
}

// 只有身高：三围仍留在资料行里（只摘掉真正被用上的那一项）。
func TestParseBodyPartial(t *testing.T) {
	rows := []ProfileField{
		{Label: "身高", Value: "167cm"},
		{Label: "三围", Value: "未知"},
	}
	body, kept := ParseBody(rows)
	if body.HeightCm != 167 || body.Bust != 0 {
		t.Errorf("只该认出身高，实际 %+v", body)
	}
	if len(kept) != 1 || kept[0].Label != "三围" {
		t.Errorf("三围没认出来应该留着，实际 %+v", kept)
	}
}
