package parser

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

func codexFixture(t *testing.T) (Codex, string) {
	t.Helper()
	home, err := filepath.Abs("testdata/codexhome")
	if err != nil {
		t.Fatal(err)
	}
	return Codex{Home: home}, filepath.Join(home,
		".codex/sessions/2026/07/28/rollout-2026-07-28T14-11-56-aaaa1111-bbbb-2222-cccc-333344445555.jsonl")
}

func TestCodexMatchAndMeta(t *testing.T) {
	c, path := codexFixture(t)

	if !c.Match(path) {
		t.Error("会话文件未被认领")
	}
	if c.Match(filepath.Join(c.root(), "2026/07/28/other.jsonl")) {
		t.Error("非 rollout- 前缀的文件被认领了")
	}

	m, err := c.Meta(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != model.SourceCodex {
		t.Errorf("Source = %q", m.Source)
	}
	// session_meta 里的 session_id 优先于文件名
	if m.SessionUID != "aaaa1111-bbbb-2222-cccc-333344445555" {
		t.Errorf("SessionUID = %q", m.SessionUID)
	}
	if m.ProjectPath != "/home/user/workflow/demo" {
		t.Errorf("ProjectPath = %q", m.ProjectPath)
	}
	if m.StartedAt == 0 {
		t.Error("StartedAt 未解出")
	}
}

func TestCodexParseBlocks(t *testing.T) {
	c, path := codexFixture(t)
	blocks, cur := parseFile(t, c, path, Cursor{})

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
			t.Errorf("kind=%s 块数 = %d, want %d（共 %d 块）", k, kinds[k], n, len(blocks))
		}
	}

	// 首条 user：AGENTS.md 注入块剥掉，提问保留
	if blocks[0].Kind != model.KindUser || blocks[0].Body != "解包刚下载的二进制，用无头模式做全面分析" {
		t.Errorf("首条 user 块 = %+v", blocks[0])
	}
	// assistant 走的是 output_text，不是 input_text
	if blocks[1].Kind != model.KindAssistant || !strings.Contains(blocks[1].Body, "binary-ninja-re") {
		t.Errorf("assistant 块 = %+v", blocks[1])
	}

	// 两种工具调用都要覆盖，且经 call_id 与各自的 output 配对
	byID := map[string][]model.Block{}
	for _, b := range blocks {
		if b.ToolUseID != "" {
			byID[b.ToolUseID] = append(byID[b.ToolUseID], b)
		}
	}
	for _, id := range []string{"call_AAA", "call_BBB"} {
		if len(byID[id]) != 2 {
			t.Errorf("%s 应有一次调用 + 一次结果，实得 %d 条", id, len(byID[id]))
		}
	}
	if byID["call_AAA"][0].ToolName != "memory_search" || !strings.Contains(byID["call_AAA"][0].Body, "增量备份") {
		t.Errorf("function_call 块 = %+v", byID["call_AAA"][0])
	}
	// 工具结果回填了工具名（需求 7.6）
	if byID["call_BBB"][1].ToolName != "exec" || !strings.Contains(byID["call_BBB"][1].Body, "sent 1,024 bytes") {
		t.Errorf("custom_tool_call_output 块 = %+v", byID["call_BBB"][1])
	}
	// custom_tool_call 的入参在 input 字段而非 arguments
	if !strings.Contains(byID["call_BBB"][0].Body, "rsync -av") {
		t.Errorf("custom_tool_call 块 = %+v", byID["call_BBB"][0])
	}
}

// event_msg 会把 agent_message 再复述一遍；索引它会让同一句话在库里出现两次、
// 命中数虚高——而「按命中数排序」正是本项目要避免的失败模式。
func TestCodexSkipsDuplicateEventMsg(t *testing.T) {
	c, path := codexFixture(t)
	blocks, _ := parseFile(t, c, path, Cursor{})

	n := 0
	for _, b := range blocks {
		if strings.Contains(b.Body, "binary-ninja-re") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("助手那句话出现了 %d 次，want 1（event_msg 不得重复索引）", n)
	}
}

// developer 角色是注入的指令，不是对话内容。
func TestCodexSkipsDeveloperRole(t *testing.T) {
	c, path := codexFixture(t)
	blocks, _ := parseFile(t, c, path, Cursor{})
	for _, b := range blocks {
		if strings.Contains(b.Body, "注入的开发者指令") {
			t.Errorf("developer 角色的内容被索引了: %+v", b)
		}
	}
}
