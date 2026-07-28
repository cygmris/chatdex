package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/parser"
	"github.com/cygmris/chatdex/internal/search"
)

// 建一个假 HOME，里面放 Claude 格式的会话文件。
func newScanner(t *testing.T) (*Scanner, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude/projects/-home-demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Scanner{
		Store: st,
		Reg:   parser.NewRegistry(parser.Claude{Home: home}, parser.Codex{Home: home}),
		Cfg:   DefaultConfig(),
	}, home
}

func sessionPath(home, name string) string {
	return filepath.Join(home, ".claude/projects/-home-demo", name)
}

func userLine(ts, text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"cwd":"/home/demo","sessionId":"s1","message":{"role":"user","content":%q}}`+"\n", ts, text)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func countBlocks(t *testing.T, s *Scanner, filePath string) int {
	t.Helper()
	var n int
	err := s.Store.DB().QueryRow(`SELECT COUNT(*) FROM blocks b JOIN sessions s ON s.id=b.session_id WHERE s.file_path = ?`, filePath).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// 情形一 + 二：首次索引后再扫一遍，无变化则跳过、不重复写入。
func TestScanAppendThenSkip(t *testing.T) {
	s, home := newScanner(t)
	p := sessionPath(home, "aaa.jsonl")
	writeFile(t, p, userLine("2026-07-28T10:00:00.000Z", "第一句"))

	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep.FilesIndexed != 1 || rep.BlocksAdded != 1 {
		t.Fatalf("首次扫描 = %+v", rep)
	}

	rep2, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep2.FilesIndexed != 0 {
		t.Errorf("无变化的第二轮不应重新索引: %+v", rep2)
	}
	if n := countBlocks(t, s, p); n != 1 {
		t.Errorf("块数 = %d, want 1（不得重复写入）", n)
	}
}

// 情形三：文件增长 → 只索引新增部分，seq 接着排。
func TestScanIncrementalAppend(t *testing.T) {
	s, home := newScanner(t)
	p := sessionPath(home, "bbb.jsonl")
	writeFile(t, p, userLine("2026-07-28T10:00:00.000Z", "第一句"))
	if _, err := s.ScanOnce(); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(userLine("2026-07-28T10:01:00.000Z", "第二句"))
	f.Close()

	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep.BlocksAdded != 1 {
		t.Errorf("增量应只新增 1 块，实得 %d", rep.BlocksAdded)
	}
	if n := countBlocks(t, s, p); n != 2 {
		t.Errorf("总块数 = %d, want 2", n)
	}
	var seqs []int
	rows, _ := s.Store.DB().Query(`SELECT seq FROM blocks ORDER BY seq`)
	defer rows.Close()
	for rows.Next() {
		var q int
		rows.Scan(&q)
		seqs = append(seqs, q)
	}
	if len(seqs) != 2 || seqs[0] != 0 || seqs[1] != 1 {
		t.Errorf("seq 未接着排: %v", seqs)
	}
}

// 情形四：文件被截断（size < offset）→ 整个会话从 0 重建。
func TestScanRebuildOnTruncate(t *testing.T) {
	s, home := newScanner(t)
	p := sessionPath(home, "ccc.jsonl")
	writeFile(t, p, userLine("2026-07-28T10:00:00.000Z", "旧的第一句")+userLine("2026-07-28T10:01:00.000Z", "旧的第二句"))
	if _, err := s.ScanOnce(); err != nil {
		t.Fatal(err)
	}

	writeFile(t, p, userLine("2026-07-28T11:00:00.000Z", "重写后的唯一一句"))
	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rebuilt != 1 {
		t.Errorf("应触发重建，Report = %+v", rep)
	}
	if n := countBlocks(t, s, p); n != 1 {
		t.Errorf("重建后块数 = %d, want 1（旧内容必须清掉）", n)
	}
	var body string
	s.Store.DB().QueryRow(`SELECT body FROM blocks`).Scan(&body)
	if !strings.Contains(search.Strip(body), "重写后") {
		t.Errorf("重建后残留旧内容: %q", body)
	}
}

// 情形五：size 不变但 mtime 变了（原地改写）→ 同样从 0 重建。
func TestScanRebuildOnInPlaceRewrite(t *testing.T) {
	s, home := newScanner(t)
	p := sessionPath(home, "ddd.jsonl")
	writeFile(t, p, userLine("2026-07-28T10:00:00.000Z", "原内容"))
	if _, err := s.ScanOnce(); err != nil {
		t.Fatal(err)
	}

	// 等长替换，size 不变
	writeFile(t, p, userLine("2026-07-28T10:00:00.000Z", "新内容"))
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}

	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rebuilt != 1 {
		t.Errorf("size 相同但 mtime 变了，应触发重建。Report = %+v", rep)
	}
	var body string
	s.Store.DB().QueryRow(`SELECT body FROM blocks`).Scan(&body)
	if !strings.Contains(search.Strip(body), "新内容") {
		t.Errorf("原地改写后内容未更新: %q", body)
	}
}

// 情形六：文件消失 → 标记失效，检索不得再指向它。
func TestScanMarksDeletedFileDead(t *testing.T) {
	s, home := newScanner(t)
	p := sessionPath(home, "eee.jsonl")
	writeFile(t, p, userLine("2026-07-28T10:00:00.000Z", "会被删掉的会话"))
	if _, err := s.ScanOnce(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}

	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep.MarkedDead != 1 {
		t.Errorf("应标记 1 个失效会话，Report = %+v", rep)
	}
	var alive int
	s.Store.DB().QueryRow(`SELECT alive FROM sessions WHERE file_path = ?`, p).Scan(&alive)
	if alive != 0 {
		t.Error("会话未被标记为失效")
	}
}

// 需求 7.4：超阈值的工具结果只索引前 N 字节，并记录截断前体积。
func TestApplyPolicyTruncatesToolResult(t *testing.T) {
	s, _ := newScanner(t)
	s.Cfg.ToolResultCap = 100

	long := strings.Repeat("构建日志很长。", 200)
	got := s.applyPolicy(model.Block{Kind: model.KindToolResult, Body: long})
	if !got.Truncated {
		t.Error("未标记截断")
	}
	if got.RawBytes != len(long) {
		t.Errorf("RawBytes = %d, want %d", got.RawBytes, len(long))
	}
	if len(got.Body) > 100 {
		t.Errorf("截断后长度 = %d, want <= 100", len(got.Body))
	}
	// 不得切断多字节字符
	for _, r := range got.Body {
		if r == '�' {
			t.Fatal("截断切坏了 UTF-8 字符")
		}
	}

	// user / assistant / tool_use 不受截断策略影响
	keep := s.applyPolicy(model.Block{Kind: model.KindUser, Body: long})
	if keep.Truncated || keep.Body != long {
		t.Error("非工具结果的块被截断了")
	}
}

// 需求 7.5：二进制 / base64 内容只留元数据。
func TestApplyPolicySkipsNonText(t *testing.T) {
	s, _ := newScanner(t)

	cases := map[string]string{
		"含 NUL":  "前面正常\x00后面是二进制",
		"base64": strings.Repeat("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR", 40),
	}
	for name, body := range cases {
		got := s.applyPolicy(model.Block{Kind: model.KindToolResult, ToolName: "Read", Body: body})
		if got.Body != "" {
			t.Errorf("%s: 正文未被跳过", name)
		}
		if !got.Truncated || got.RawBytes != len(body) {
			t.Errorf("%s: 元数据未保留 truncated=%v raw=%d", name, got.Truncated, got.RawBytes)
		}
		if got.ToolName != "Read" {
			t.Errorf("%s: 工具名丢了", name)
		}
	}
}

// 需求 7.7 的开关：关闭工具结果正文索引后只留元数据。
func TestToolResultBodySwitch(t *testing.T) {
	s, _ := newScanner(t)
	s.Cfg.ToolResultBody = false
	got := s.applyPolicy(model.Block{Kind: model.KindToolResult, ToolName: "Bash", Body: "普通输出"})
	if got.Body != "" || !got.Truncated || got.RawBytes == 0 {
		t.Errorf("关闭正文索引后 = %+v", got)
	}
}

// 超体积上限时停止新增，但绝不删除已有数据。
func TestSizeCapStopsIndexingWithoutDeleting(t *testing.T) {
	s, home := newScanner(t)
	writeFile(t, sessionPath(home, "f1.jsonl"), userLine("2026-07-28T10:00:00.000Z", "第一个会话"))
	if _, err := s.ScanOnce(); err != nil {
		t.Fatal(err)
	}
	before := countBlocks(t, s, sessionPath(home, "f1.jsonl"))

	s.Cfg.MaxBytes = 1 // 立即触顶
	writeFile(t, sessionPath(home, "f2.jsonl"), userLine("2026-07-28T10:00:00.000Z", "第二个会话"))
	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if !rep.SizeCapped {
		t.Error("未标记触顶")
	}
	if got := countBlocks(t, s, sessionPath(home, "f1.jsonl")); got != before {
		t.Errorf("触顶后已有数据被动了: %d -> %d", before, got)
	}
}

// 坏行计数要报上来，供排查。
func TestScanCountsSkippedLines(t *testing.T) {
	s, home := newScanner(t)
	p := sessionPath(home, "ggg.jsonl")
	writeFile(t, p, userLine("2026-07-28T10:00:00.000Z", "正常一行")+"这不是 JSON\n")

	rep, err := s.ScanOnce()
	if err != nil {
		t.Fatal(err)
	}
	if rep.LinesSkipped != 1 {
		t.Errorf("坏行计数 = %d, want 1", rep.LinesSkipped)
	}
	if rep.BlocksAdded != 1 {
		t.Errorf("坏行不应中断整个文件，块数 = %d", rep.BlocksAdded)
	}
}

// 只读铁律：扫描不得修改任何会话原始文件。
func TestScanNeverWritesSourceFiles(t *testing.T) {
	s, home := newScanner(t)
	p := sessionPath(home, "hhh.jsonl")
	content := userLine("2026-07-28T10:00:00.000Z", "只读校验")
	writeFile(t, p, content)
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.ScanOnce(); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("原始会话文件被改动了")
	}
	got, _ := os.ReadFile(p)
	if string(got) != content {
		t.Error("原始会话文件内容被改动了")
	}
}
