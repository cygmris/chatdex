package search

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

var roundTripSamples = []string{
	"讨论基于 restic 做类似 TimeMachine 的增量备份项目",
	"rsync -av --delete /src/ user@host:/dst/",
	"混排：把 tool_result 截断到 4096 字节\n第二行\t带制表符",
	"日本語のテキストと한국어 텍스트",
	"TimeMachine的增量备份",
	"",
	"純中文沒有空格的一整段話",
}

func TestStripReversesNormalizeIndex(t *testing.T) {
	for _, s := range roundTripSamples {
		if got := Strip(NormalizeIndex(s)); got != s {
			t.Errorf("往返不等\n原文: %q\n还原: %q", s, got)
		}
	}
}

func TestNormalizeSplitsCJKKeepsASCIIWords(t *testing.T) {
	got := NormalizeQuery("TimeMachine的增量备份 rsync")
	want := "TimeMachine 的 指 纹 浏 览 器 rsync"
	if got != want {
		t.Errorf("NormalizeQuery = %q, want %q", got, want)
	}
	// 索引侧与查询侧只差分隔符本身，切分位置必须完全一致。
	if strings.ReplaceAll(NormalizeIndex("TimeMachine的增量备份 rsync"), string(Sep), " ") != want {
		t.Error("索引侧与查询侧切分位置不一致")
	}
}

func TestPreExistingSepIsDropped(t *testing.T) {
	if strings.ContainsRune(Strip(NormalizeIndex("a\x01b")), Sep) {
		t.Error("原文中已有的 Sep 未被剔除")
	}
}

// 这条是本任务的核心验收：索引侧与查询侧的归一化必须在**真实 FTS5 表**上互相对得上。
// 归一化不一致是这个模式最常见的 bug，用 mock 验证不出来。
func TestIndexAndQueryAgreeOnRealFTS5(t *testing.T) {
	db, err := sql.Open("sqlite", "file:normalize_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE b USING fts5(body, tokenize="unicode61 remove_diacritics 2")`); err != nil {
		t.Fatal(err)
	}

	docs := []string{
		"讨论基于 restic 做类似 TimeMachine 的增量备份项目",
		"用 rsync -av 部署到服务器，顺便清理旧构建产物",
		"完全无关的一段话，讲的是数据库连接池配置",
	}
	for _, d := range docs {
		if _, err := db.Exec(`INSERT INTO b(body) VALUES (?)`, NormalizeIndex(d)); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		query string
		want  int
	}{
		{"增量备份", 1},   // 中文整词
		{"浏览器", 1},     // 中文子串：必须能命中「增量备份」内部
		{"restic", 1}, // 英文
		{"rsync", 1},    // 命令名
		{"连接池", 1},     // 另一篇的中文
		{"根本没有这个词", 0}, // 无命中必须真的是 0
	}
	for _, c := range cases {
		var n int
		q := `"` + NormalizeQuery(c.query) + `"` // 短语查询：保证 CJK 单字按序相邻
		if err := db.QueryRow(`SELECT count(*) FROM b WHERE b MATCH ?`, q).Scan(&n); err != nil {
			t.Fatalf("查询 %q: %v", c.query, err)
		}
		if n != c.want {
			t.Errorf("查询 %q 命中 %d 条，期望 %d", c.query, n, c.want)
		}
	}
}

// snippet() 返回的高亮片段必须能去掉标记后直接展示。
func TestSnippetIsDisplayableAfterStrip(t *testing.T) {
	db, err := sql.Open("sqlite", "file:snippet_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE b USING fts5(body, tokenize="unicode61 remove_diacritics 2")`); err != nil {
		t.Fatal(err)
	}
	const doc = "讨论基于 restic 做类似 TimeMachine 的增量备份项目"
	if _, err := db.Exec(`INSERT INTO b(body) VALUES (?)`, NormalizeIndex(doc)); err != nil {
		t.Fatal(err)
	}
	var sn string
	q := `"` + NormalizeQuery("增量备份") + `"`
	// 注意：CJK 被切成单字后每个字都是一个 token，snippet 的 token 预算要按字算，
	// 给小了会把高亮短语从中间截断。检索层取片段时同理。
	if err := db.QueryRow(`SELECT snippet(b,0,'[',']','…',64) FROM b WHERE b MATCH ?`, q).Scan(&sn); err != nil {
		t.Fatal(err)
	}
	sn = Strip(sn)
	if strings.ContainsRune(sn, Sep) {
		t.Error("片段仍含 Sep 标记")
	}
	if !strings.Contains(sn, "[增量备份]") {
		t.Errorf("片段未正确高亮整词: %q", sn)
	}
}
