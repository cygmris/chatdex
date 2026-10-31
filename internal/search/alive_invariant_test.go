package search_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sessions.alive 只描述「源文件还在不在磁盘上」。读索引的查询一律不得用它过滤——
// 内容在 blocks 里，与文件无关。R1 把它读成了「会话还算不算数」，alive=0 只有 3.9%
// 时看不出差别，涨到 57% 时就是半个索引消失在检索之外（R26）。
//
// 取样：本包全部非测试 .go + index/summarystore.go（摘要读的是 blocks，同属读索引）。
// 先剥注释再匹配——注释里合法地讨论这条规则，匹到注释会让干净代码也红（memory: mutation-sweep-discipline）。
func TestAliveIsNotAContentFilter(t *testing.T) {
	re := regexp.MustCompile(`alive\s*=\s*1`)
	files, _ := filepath.Glob("*.go")
	files = append(files, filepath.Join("..", "index", "summarystore.go"))
	sampled := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sampled++
		if loc := re.FindIndex(stripComments(string(src))); loc != nil {
			t.Errorf("%s 用 alive 过滤查询：alive 是文件状态不是内容状态，读索引的查询不得用它过滤。"+
				"要用它，说明你接下来要打开文件——那属于 index/backup 包。", f)
		}
	}
	if sampled < 3 {
		t.Fatalf("只取样到 %d 个文件，锚点失效", sampled)
	}

	// 阳性锚点：store.go 的 AlivePaths 是合法用法（它接下来要 stat 文件），正则必须能匹到它。
	// 匹不到说明正则或剥注释坏了，而不是代码干净——空样本永远「通过」。
	src, err := os.ReadFile(filepath.Join("..", "index", "store.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !re.Match(stripComments(string(src))) {
		t.Fatal("阳性锚点缺失：index/store.go 里应有 `alive = 1`（AlivePaths），正则没匹到——测试本身失效")
	}
}

// stripComments 去掉 Go 行注释、块注释与 SQL 字符串里的 `--` 行注释。
func stripComments(s string) []byte {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?m)--.*$`).ReplaceAllString(s, "")
	return []byte(s)
}
