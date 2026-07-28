package search

import (
	"strings"
	"unicode"
)

// Sep 是插入在 CJK 字符之间的分隔符。
//
// FTS5 的 unicode61 分词器把一整串 CJK 当作**一个** token，中文因此无法按词检索。
// 在相邻 CJK 字符间插入一个控制字符即可让它切成单字 token：unicode61 把控制字符
// 视为分隔符。选 U+0001 而不是空格是为了**无损**——Strip 能逐字节还原原文，
// 于是索引与展示可以共用同一份正文，省掉一份 GB 级的拷贝。
const Sep = '\x01'

// cjkRanges 覆盖需要按单字切分的表意/音节文字。
var cjkRanges = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x3040, Hi: 0x30ff, Stride: 1}, // 平假名 + 片假名
		{Lo: 0x3400, Hi: 0x4dbf, Stride: 1}, // CJK 扩展 A
		{Lo: 0x4e00, Hi: 0x9fff, Stride: 1}, // CJK 统一表意文字
		{Lo: 0xac00, Hi: 0xd7af, Stride: 1}, // 韩文音节
		{Lo: 0xf900, Hi: 0xfaff, Stride: 1}, // CJK 兼容表意文字
	},
	R32: []unicode.Range32{
		{Lo: 0x20000, Hi: 0x2ffff, Stride: 1}, // CJK 扩展 B 及以后
	},
}

func isCJK(r rune) bool { return unicode.Is(cjkRanges, r) }

// tokenRune 判断一个字符是否会被 unicode61 计入 token 内部。
// unicode61 以「字母或数字」为 token 字符，其余一律视为分隔符——
// 那些位置本来就会断词，无须再插分隔符。
func tokenRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// split 在需要断词处插入 sep：相邻两个 token 字符中只要有一个是 CJK，就断开。
// 于是「增量备份」切成单字，而「TimeMachine」保持整词。
func split(s string, sep rune) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/4)
	var prev rune
	first := true
	for _, r := range s {
		if r == Sep {
			continue // 剔除原文中已有的标记，保证 Strip 可逆
		}
		if !first && tokenRune(prev) && tokenRune(r) && (isCJK(prev) || isCJK(r)) {
			b.WriteRune(sep)
		}
		b.WriteRune(r)
		prev, first = r, false
	}
	return b.String()
}

// NormalizeIndex 把正文转成入库形态：CJK 之间插入 Sep。
func NormalizeIndex(s string) string { return split(s, Sep) }

// NormalizeQuery 把查询词转成与索引侧一致的分词形态。
// 查询串不进入存储，用空格分隔即可——与 Sep 经 unicode61 分词后完全等价。
func NormalizeQuery(s string) string { return split(s, ' ') }

// Strip 去掉 Sep，还原可展示的原文。
func Strip(s string) string {
	if !strings.ContainsRune(s, Sep) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == Sep {
			return -1
		}
		return r
	}, s)
}
