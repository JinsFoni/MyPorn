package httpapi

import (
	"testing"
)

func TestSmallWidthParam(t *testing.T) {
	cases := map[string]int{
		"48":   48,
		" 64 ": 64,
		"8":    8,
		"256":  256,
		"":     0,
		"7":    0, // 太小，糊了也没意义
		"512":  0, // 太大，那就不是「小图」了
		"abc":  0,
		"-48":  0,
	}
	for in, want := range cases {
		if got := smallWidthParam(in); got != want {
			t.Errorf("smallWidthParam(%q) = %d, 期望 %d", in, got, want)
		}
	}
}
