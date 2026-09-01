package summary

import (
	"strings"
	"testing"
)

// 超长摘要必须断在标点处并带 `…`，而不是硬切在词中间。
//
// 实测那 212 条被硬切的摘要长这样：`/home/<user>/workspace/<proj>/websi`、
// `-observability.md，408行；关键发现：Hoo` —— 断在半个路径、半个词上。
func TestTidyBreaksAtPunctuationAndMarksIt(t *testing.T) {
	long := func(n int, tail string) string {
		return strings.Repeat("甲", n) + tail
	}

	cases := []struct {
		name    string
		in      string
		wantEnd string
		trunc   bool
	}{
		{
			name:    "含标点：断在标点后",
			in:      long(100, "。") + strings.Repeat("乙", 60),
			wantEnd: "。…",
			trunc:   true,
		},
		{
			name:    "整段无标点：退回硬切但仍带省略号",
			in:      long(300, ""),
			wantEnd: "…",
			trunc:   true,
		},
		{
			// 阳性对照：不超长的**不得**加省略号。
			// 少了这条，一个「无脑加 …」的实现也能通过上面两条。
			name:    "不超长：原样返回，不加省略号",
			in:      "在 chatdex 里修好了覆盖率跨快照判定。",
			wantEnd: "。",
			trunc:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tidy(tc.in)
			if !strings.HasSuffix(got, tc.wantEnd) {
				t.Errorf("结尾 = %q, want 以 %q 结尾", lastRunes(got, 8), tc.wantEnd)
			}
			if tc.trunc != strings.HasSuffix(got, "…") {
				t.Errorf("是否带省略号 = %v, want %v —— 「模型只写这么多」与「被系统截了」"+
					"必须分得开", strings.HasSuffix(got, "…"), tc.trunc)
			}
			// 长度不变式：含省略号也不得超上限
			if n := len([]rune(got)); n > maxSummaryChars {
				t.Errorf("长度 = %d, 超过上限 %d", n, maxSummaryChars)
			}
			// 断点必须落在标点上（无标点那条除外）
			if tc.trunc && strings.Contains(tc.in, "。") {
				body := strings.TrimSuffix(got, "…")
				if !strings.HasSuffix(body, "。") {
					t.Errorf("断在了非标点处：%q", lastRunes(body, 8))
				}
			}
		})
	}
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
