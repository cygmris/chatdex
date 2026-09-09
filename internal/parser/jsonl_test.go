package parser

import (
	"strings"
	"testing"
)

// TestScanLinesAtOffsets 钉住 scanLinesAt 交给回调的是**该行起始**的偏移量。
//
// 变异对照：把实现里的 at 改成取行尾（即 offset += 之后再取），本用例会红
// —— 第一行的期望值 0 是唯一不会被两种写法同时满足的。
func TestScanLinesAtOffsets(t *testing.T) {
	// 第三行故意留半行（无换行符），验证它既不回调也不计入水位
	in := "aa\nbbbb\ncc"
	var gotLines []string
	var gotAt []int64
	end, err := scanLinesAt(strings.NewReader(in), 0, func(line []byte, at int64) error {
		gotLines = append(gotLines, string(line))
		gotAt = append(gotAt, at)
		return nil
	})
	if err != nil {
		t.Fatalf("scanLinesAt: %v", err)
	}
	if want := []string{"aa", "bbbb"}; len(gotLines) != 2 || gotLines[0] != want[0] || gotLines[1] != want[1] {
		t.Fatalf("回调收到的行 = %v，想要 %v（末尾半行不应回调）", gotLines, want)
	}
	// "aa\n" 起于 0 长 3；"bbbb\n" 起于 3 长 5
	if gotAt[0] != 0 || gotAt[1] != 3 {
		t.Fatalf("行起始偏移 = %v，想要 [0 3]", gotAt)
	}
	if end != 8 {
		t.Fatalf("水位 = %d，想要 8（末尾半行 \"cc\" 不计入）", end)
	}
}

// TestScanLinesAtStartOffset 起始 offset 不为 0 时，行偏移量要接着算。
func TestScanLinesAtStartOffset(t *testing.T) {
	var gotAt []int64
	end, err := scanLinesAt(strings.NewReader("x\nyy\n"), 100, func(_ []byte, at int64) error {
		gotAt = append(gotAt, at)
		return nil
	})
	if err != nil {
		t.Fatalf("scanLinesAt: %v", err)
	}
	if gotAt[0] != 100 || gotAt[1] != 102 {
		t.Fatalf("行起始偏移 = %v，想要 [100 102]", gotAt)
	}
	if end != 105 {
		t.Fatalf("水位 = %d，想要 105", end)
	}
}

// TestScanLinesDelegates scanLines 改成 delegate 之后，语义必须一字不变
// —— Claude/Codex 两个解析器依赖它。
func TestScanLinesDelegates(t *testing.T) {
	var got []string
	end, err := scanLines(strings.NewReader("a\n\n  \nb\nhalf"), 0, func(line []byte) error {
		got = append(got, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("scanLines: %v", err)
	}
	// 空行与纯空白行不回调（TrimSpace 后为空）
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("回调收到的行 = %v，想要 [a b]", got)
	}
	if end != 8 {
		t.Fatalf("水位 = %d，想要 8（末尾半行 \"half\" 不计入）", end)
	}
}
