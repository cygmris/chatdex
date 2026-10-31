package search

import (
	"strings"
	"unicode/utf8"
)

// 片段里命中标记之前最多保留多少个字、多少个换行。
//
// FTS5 的 snippet() 只保证窗口里**有**命中，不保证命中在前面：实测一个 325 字的块，
// 64 token 的窗口把唯一命中放在第 4 行末尾，而结果卡片的 CSS 只露 3 行
// （`.hit .snip` max-height 4.8em）——高亮被裁掉，卡片上看不出它为什么命中。
// 片段是「为什么匹配」的证据，证据必须在第一眼能看到的位置。
const snippetLeadRunes = 40
const snippetLeadNewlines = 1

// frontLoad 把第一个命中标记之前的上下文压到可见范围内：超出的部分换成 '…'。
// 没有标记（如纯按过滤条件命中）或标记本来就靠前时原样返回。
func frontLoad(snip string) string {
	i := strings.Index(snip, HitOpen)
	if i < 0 {
		return snip
	}
	lead, rest := snip[:i], snip[i:]
	cut := false
	if utf8.RuneCountInString(lead) > snippetLeadRunes {
		r := []rune(lead)
		lead = string(r[len(r)-snippetLeadRunes:])
		cut = true
	}
	for strings.Count(lead, "\n") > snippetLeadNewlines {
		lead = lead[strings.IndexByte(lead, '\n')+1:]
		cut = true
	}
	if cut {
		lead = "…" + strings.TrimLeft(lead, "…")
	}
	return lead + rest
}
