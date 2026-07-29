package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

func claudeFixture(t *testing.T) (Claude, string, string) {
	t.Helper()
	home, err := filepath.Abs("testdata/claudehome")
	if err != nil {
		t.Fatal(err)
	}
	c := Claude{Home: home}
	main := filepath.Join(home, ".claude/projects/-home-user-demo/11111111-2222-3333-4444-555555555555.jsonl")
	sub := filepath.Join(home, ".claude/projects/-home-user-demo/11111111-2222-3333-4444-555555555555/subagents/agent-aaa111.jsonl")
	return c, main, sub
}

func parseFile(t *testing.T, p Parser, path string, start Cursor) ([]model.Block, Cursor) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if start.Offset > 0 {
		if _, err := f.Seek(start.Offset, 0); err != nil {
			t.Fatal(err)
		}
	}
	var got []model.Block
	cur, err := p.Parse(f, start, func(b model.Block) error {
		got = append(got, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got, cur
}

func TestClaudeMatchAndMeta(t *testing.T) {
	c, main, sub := claudeFixture(t)

	if !c.Match(main) {
		t.Error("主会话文件未被认领")
	}
	if c.Match("/tmp/other.jsonl") {
		t.Error("不属于本解析器的路径被认领了")
	}

	m, err := c.Meta(main)
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != model.SourceClaude || m.SessionUID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("主会话元数据 = %+v", m)
	}
	// 项目路径优先取记录里的 cwd，而不是目录 slug 的歧义反解
	if m.ProjectPath != "/home/user/demo" {
		t.Errorf("ProjectPath = %q, want /home/user/demo", m.ProjectPath)
	}
	if m.StartedAt == 0 {
		t.Error("StartedAt 未解出")
	}
	if m.ParentUID != "" {
		t.Errorf("主会话不应有 ParentUID，得到 %q", m.ParentUID)
	}

	ms, err := c.Meta(sub)
	if err != nil {
		t.Fatal(err)
	}
	if ms.ParentUID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("子代理 ParentUID = %q", ms.ParentUID)
	}
	if ms.AgentLabel != "agent-aaa111" {
		t.Errorf("子代理 AgentLabel = %q", ms.AgentLabel)
	}
}

func TestClaudeParseBlocks(t *testing.T) {
	c, main, _ := claudeFixture(t)
	blocks, cur := parseFile(t, c, main, Cursor{})

	if cur.Skipped != 1 {
		t.Errorf("坏行跳过计数 = %d, want 1", cur.Skipped)
	}

	kinds := map[model.Kind]int{}
	for _, b := range blocks {
		kinds[b.Kind]++
	}
	want := map[model.Kind]int{
		model.KindUser: 1, model.KindAssistant: 1,
		model.KindToolUse: 2, model.KindToolResult: 2,
	}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("kind=%s 块数 = %d, want %d（全部块: %d）", k, kinds[k], n, len(blocks))
		}
	}

	// 首条 user：注入块剥掉，用户真正的提问必须完整保留
	if blocks[0].Kind != model.KindUser || blocks[0].Body != "做一个类似 TimeMachine 的 profile 管理工具" {
		t.Errorf("首条 user 块 = %+v", blocks[0])
	}

	// 工具调用：工具名与完整入参
	var toolUse model.Block
	for _, b := range blocks {
		if b.Kind == model.KindToolUse && b.ToolName == "Bash" {
			toolUse = b
		}
	}
	if toolUse.ToolUseID != "toolu_01" || !strings.Contains(toolUse.Body, "rsync -av") {
		t.Errorf("tool_use 块 = %+v", toolUse)
	}

	// 工具结果：关联回那次调用，并回填工具名（需求 7.6）
	var toolRes model.Block
	for _, b := range blocks {
		if b.Kind == model.KindToolResult && b.ToolUseID == "toolu_01" {
			toolRes = b
		}
	}
	if toolRes.ToolName != "Bash" || !strings.Contains(toolRes.Body, "sent 1,024 bytes") {
		t.Errorf("tool_result 块 = %+v", toolRes)
	}

	// 纯注入的那条 user 剥完为空，不得产生空块
	for _, b := range blocks {
		if strings.TrimSpace(b.Body) == "" {
			t.Errorf("产生了空块: %+v", b)
		}
	}

	// seq 连续
	for i, b := range blocks {
		if b.Seq != i {
			t.Errorf("块 %d 的 Seq = %d", i, b.Seq)
		}
	}
}

// 增量续读：从上次水位接着读，不重复产出，seq 接着排。
func TestClaudeIncrementalResume(t *testing.T) {
	c, main, _ := claudeFixture(t)
	all, fullCur := parseFile(t, c, main, Cursor{})

	// 取文件前半部分作为「上一轮读到的位置」
	data, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	half := int64(len(data) / 2)
	// 退到该位置之前最近的换行边界，模拟上一轮停在完整行处
	for half > 0 && data[half-1] != '\n' {
		half--
	}

	if fullCur.Offset != int64(len(data)) {
		t.Errorf("完整读取后 Offset = %d, 文件长度 = %d", fullCur.Offset, len(data))
	}

	rest, cur2 := parseFile(t, c, main, Cursor{Offset: half, Seq: 3})
	if cur2.Offset != int64(len(data)) {
		t.Errorf("续读后 Offset = %d, want %d", cur2.Offset, len(data))
	}
	if len(rest) == 0 || len(rest) >= len(all) {
		t.Errorf("续读产出 %d 块，全量 %d 块——续读应只产出后半部分", len(rest), len(all))
	}
	if rest[0].Seq != 3 {
		t.Errorf("续读的首块 Seq = %d, want 3", rest[0].Seq)
	}
}

// 半行不得计入水位：否则下一轮会从行中间续读，之后全错。
func TestClaudePartialLineNotCounted(t *testing.T) {
	c, _, _ := claudeFixture(t)
	complete := `{"type":"user","timestamp":"2026-07-28T10:00:00.000Z","message":{"role":"user","content":"完整的一行"}}` + "\n"
	partial := `{"type":"user","message":{"role":"user","content":"这一行还`

	var got []model.Block
	cur, err := c.Parse(strings.NewReader(complete+partial), Cursor{}, func(b model.Block) error {
		got = append(got, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cur.Offset != int64(len(complete)) {
		t.Errorf("Offset = %d, want %d（半行不得计入）", cur.Offset, len(complete))
	}
	if len(got) != 1 {
		t.Errorf("产出 %d 块, want 1", len(got))
	}
}

func TestClaudeSubagentParse(t *testing.T) {
	c, _, sub := claudeFixture(t)
	blocks, _ := parseFile(t, c, sub, Cursor{})
	if len(blocks) != 2 {
		t.Fatalf("子代理会话产出 %d 块, want 2", len(blocks))
	}
	if !strings.Contains(blocks[0].Body, "restic") {
		t.Errorf("子代理首块 = %q", blocks[0].Body)
	}
}
