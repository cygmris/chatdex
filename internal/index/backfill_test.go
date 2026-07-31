package index

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

func writeJSONL(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 回填必须幂等，且跑过之后不再付出扫描代价——否则每次启动白扫 17 秒。
func TestBackfillTitlesIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	st := openTemp(t)

	p1 := writeJSONL(t, dir, "a.jsonl",
		`{"type":"agent-name","agentName":"自动名"}`,
		`{"type":"custom-title","customTitle":"我起的"}`)
	p2 := writeJSONL(t, dir, "b.jsonl", `{"type":"mode","mode":"normal"}`)

	for _, p := range []string{p1, p2} {
		if _, err := st.UpsertSession(model.SessionMeta{
			Source: model.SourceClaude, SessionUID: filepath.Base(p), FilePath: p,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 源文件不存在的会话：必须跳过而不是让整轮失败
	if _, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "gone", FilePath: filepath.Join(dir, "没有这个文件.jsonl"),
	}); err != nil {
		t.Fatal(err)
	}

	n, err := st.BackfillTitles()
	if err != nil {
		t.Fatalf("回填报错（源文件缺失应跳过而非中断）: %v", err)
	}
	if n != 1 {
		t.Fatalf("命中 %d 个，期望 1", n)
	}

	var got string
	st.DB().QueryRow(`SELECT title FROM sessions WHERE file_path = ?`, p1).Scan(&got)
	if got != "我起的" {
		t.Errorf("title = %q，custom-title 应优先于 agent-name", got)
	}

	// 第二次：结果一致，且因为水位已记而根本不扫
	n2, err := st.BackfillTitles()
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("第二次仍扫了 %d 个 —— 水位没生效，每次启动都会白扫", n2)
	}
	st.DB().QueryRow(`SELECT title FROM sessions WHERE file_path = ?`, p1).Scan(&got)
	if got != "我起的" {
		t.Errorf("第二次之后 title 变成了 %q", got)
	}
}
