package search

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFrontLoadBringsFirstHitIntoView(t *testing.T) {
	long := strings.Repeat("字", 60) + "\n" + strings.Repeat("字", 20) + "\n\n" + strings.Repeat("字", 10)
	snip := "…" + long + HitOpen + "事故" + HitClose + "物料存放的地方"
	got := frontLoad(snip)
	lead := got[:strings.Index(got, HitOpen)]
	// 判据写字面值，不引用常量：引用常量的话常量改成 400 这条也照样绿（变异实测不咬）
	if n := utf8.RuneCountInString(lead); n > 41 { // 40 字 + 补上的 '…'
		t.Errorf("命中前还有 %d 个字，应 ≤ 41", n)
	}
	if c := strings.Count(lead, "\n"); c > 1 {
		t.Errorf("命中前还有 %d 个换行，应 ≤ 1", c)
	}
	if !strings.HasPrefix(got, "…") || strings.HasPrefix(got, "……") {
		t.Errorf("裁掉前文后应恰好一个省略号开头，实际 %q", got[:6])
	}
	if !strings.HasSuffix(got, HitOpen+"事故"+HitClose+"物料存放的地方") {
		t.Errorf("命中及其后文不得改动，实际 %q", got)
	}
}

// 单独钉字数规则：前文一个换行都没有，只有字数超限——换行规则帮不上忙，
// 字数上限放宽（变异实测 400）这条就得红。
func TestFrontLoadTrimsLongSingleLineLead(t *testing.T) {
	snip := strings.Repeat("字", 100) + HitOpen + "事故" + HitClose + "后文"
	got := frontLoad(snip)
	lead := got[:strings.Index(got, HitOpen)]
	if n := utf8.RuneCountInString(lead); n != 41 || !strings.HasPrefix(lead, "…") {
		t.Errorf("前文应恰好 '…'+40 字，实际 %d 字：%q", n, lead)
	}
}

func TestFrontLoadLeavesShortOrUnmarkedAlone(t *testing.T) {
	for _, s := range []string{
		"前面几个字" + HitOpen + "事故" + HitClose + "后文",
		"没有任何标记的片段，纯按过滤条件命中时就是这样",
		"一行\n" + HitOpen + "x" + HitClose,
	} {
		if got := frontLoad(s); got != s {
			t.Errorf("本不该动：%q → %q", s, got)
		}
	}
}
