package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

const (
	grokMain = "01a04b18-e0e6-7b50-a67d-bf458dca32c2"
	grokSub  = "01a04c75-75be-7843-b3cd-b3e5e8921e18"
)

// grokHome 把 testdata/grok-home 当成 $HOME，里面是真实布局的 .grok/sessions/…
//
// 用真目录而不是软链：软链要么写绝对路径（换台机器就断），要么写相对路径
// （git 能存但 Windows 上未必好使）。testdata 本来就不参与包发现，
// 里面放点开头的目录没有问题。
func grokHome(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "grok-home")
}

func grokParse(t *testing.T, uid string) ([]model.Block, Cursor, model.SessionMeta) {
	t.Helper()
	g := Grok{Home: grokHome(t)}
	path := filepath.Join(g.root(), "%2Ftmp%2Fdemo", uid, grokTranscript)
	if !g.Match(path) {
		t.Fatalf("Match 不认这个文件：%s", path)
	}
	meta, err := g.Meta(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got []model.Block
	cur, err := g.Parse(f, path, Cursor{}, func(b model.Block) error {
		got = append(got, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got, cur, meta
}

// 元数据取自 summary.json，项目路径以 info.cwd 为准。
func TestGrokMetaComesFromSummaryJSON(t *testing.T) {
	_, cur, m := grokParse(t, grokMain)

	if m.Source != model.SourceGrok {
		t.Errorf("Source = %q, want grok", m.Source)
	}
	if m.SessionUID != grokMain {
		t.Errorf("SessionUID = %q", m.SessionUID)
	}
	// 🔴 testdata 刻意让两个来源**不一致**：目录名 %2Ftmp%2Fdemo 解码为 /tmp/demo，
	// 而 summary.json 的 info.cwd 是 /tmp/real-project。
	// 这是唯一能分开「取 info.cwd」与「解码目录名」的取样 —— 两者一致时，
	// 两种实现给出同一个答案，断言就没有鉴别力（本期变异扫描当场抓到过这一点）。
	if m.ProjectPath != "/tmp/real-project" {
		t.Errorf("ProjectPath = %q, want /tmp/real-project —— 应取 info.cwd（原值），"+
			"而不是解码目录名（编码后的产物）", m.ProjectPath)
	}
	if m.StartedAt == 0 || m.EndedAt == 0 || m.EndedAt < m.StartedAt {
		t.Errorf("时间不对：started=%d ended=%d", m.StartedAt, m.EndedAt)
	}
	if cur.Title != "演示会话标题" {
		t.Errorf("Title = %q —— 应取 summary.json 的 generated_title", cur.Title)
	}
}

// 连续同类 chunk 拼成**一块**，遇类型变化断开。
//
// 这是 updates.jsonl 与前两个来源最根本的差别：一条消息拆成连续多行。
// 不拼接的话，「帮我查一下昨天的备份」会变成两条谁也搜不到的碎片。
func TestGrokConsecutiveChunksMergeIntoOneBlock(t *testing.T) {
	blocks, _, _ := grokParse(t, grokMain)

	var users []model.Block
	for _, b := range blocks {
		if b.Kind == model.KindUser {
			users = append(users, b)
		}
	}
	if len(users) != 2 {
		t.Fatalf("user 块 = %d 个，想要 2（「帮我查一下昨天的备份」拼成一块 + 「没有了」）：%+v", len(users), users)
	}
	if users[0].Body != "帮我查一下昨天的备份" {
		t.Errorf("第一条 user = %q，想要「帮我查一下昨天的备份」—— 两行 chunk 应拼成一块", users[0].Body)
	}
	// 类型变化断开：thought 与后面的 agent_message 不能糊在一起
	for _, b := range blocks {
		if b.Kind == model.KindReasoning && strings.Contains(b.Body, "我去查快照") {
			t.Errorf("reasoning 块吞掉了后面的 assistant 正文：%q —— 类型变化时没断开", b.Body)
		}
	}
}

// turn_completed 断开累积。
//
// 两条 agent_message 中间隔着一个 turn_completed，必须是两块。
// 若只按「类型变化」断开，它们会被拼成一块，而那是两轮对话的回答。
func TestGrokTurnCompletedBreaksAccumulator(t *testing.T) {
	blocks, _, _ := grokParse(t, grokMain)

	var assistants []string
	for _, b := range blocks {
		if b.Kind == model.KindAssistant {
			assistants = append(assistants, b.Body)
		}
	}
	if len(assistants) != 3 {
		t.Fatalf("assistant 块 = %d 个，想要 3（「我去查快照。」/「一共三个快照。」/「还需要别的吗？」）：%q", len(assistants), assistants)
	}
	if assistants[1] != "一共三个快照。" || assistants[2] != "还需要别的吗？" {
		t.Errorf("assistant 块 = %q —— 后两条被 turn_completed 隔开，不该拼在一起", assistants)
	}
}

// 🔴 tool_call_update 靠**有无 status** 分流。
//
// 实测真实语料里 28664 条 tool_call_update，14307 条没有 status（那是进度更新，
// 携带的是 rawInput 的回显），14357 条有（completed/failed）。不分流会把每个
// 工具结果记两遍，且其中一遍是输入不是输出 —— 而两遍都能搜到，看不出异常。
func TestGrokToolCallUpdateSplitsOnStatus(t *testing.T) {
	blocks, _, _ := grokParse(t, grokMain)

	var uses, results []model.Block
	for _, b := range blocks {
		switch b.Kind {
		case model.KindToolUse:
			uses = append(uses, b)
		case model.KindToolResult:
			results = append(results, b)
		}
	}
	if len(uses) != 1 {
		t.Fatalf("tool_use 块 = %d 个，想要 1：%+v", len(uses), uses)
	}
	// testdata 里 call-1 有两条 tool_call_update：一条无 status（进度）、一条有（结果）
	if len(results) != 1 {
		t.Fatalf("tool_result 块 = %d 个，想要 1 —— 无 status 的那条是进度更新，不该产块：%+v",
			len(results), results)
	}
	if !strings.Contains(results[0].Body, "3 snapshots") {
		t.Errorf("tool_result 正文 = %q，想要含 rawOutput 的 stdout", results[0].Body)
	}
	if strings.Contains(results[0].Body, "restic snapshots") {
		t.Errorf("tool_result 正文 = %q —— 这是 rawInput 的回显，说明取错了字段", results[0].Body)
	}
	if uses[0].ToolName != "run_terminal_command" {
		t.Errorf("ToolName = %q，想要 run_terminal_command（取自 _meta.\"x.ai/tool\".name）", uses[0].ToolName)
	}
	if uses[0].ToolUseID != "call-1" || results[0].ToolUseID != "call-1" {
		t.Errorf("toolCallId 没对上：use=%q result=%q", uses[0].ToolUseID, results[0].ToolUseID)
	}
}

// 时间戳直取每行的 timestamp；缺失时沿用上一个已知值，**不插值**。
func TestGrokTimestampsAreVerbatim(t *testing.T) {
	blocks, _, _ := grokParse(t, grokMain)

	for _, b := range blocks {
		if b.TS == 0 {
			t.Errorf("%s 块没有时间戳：%q", b.Kind, b.Body)
		}
	}
	// 第一条 user 起于第一行 chunk，时间戳应当**逐字**等于那一行的 1700000020
	for _, b := range blocks {
		if b.Kind == model.KindUser && b.Body == "帮我查一下昨天的备份" {
			if b.TS != 1700000020 {
				t.Errorf("TS = %d，想要 1700000020（逐字取自该消息第一行）", b.TS)
			}
		}
		// testdata 里「没有了」那一行**故意没有 timestamp**，应沿用上一个已知值
		if b.Kind == model.KindUser && b.Body == "没有了" {
			if b.TS != 1700000090 {
				t.Errorf("缺 timestamp 的块 TS = %d，想要 1700000090（沿用上一个已知值）", b.TS)
			}
		}
	}
}

// 未知 sessionUpdate 类型不产块、也不报错 —— Grok CLI 在演进，新类型必然出现。
func TestGrokUnknownTypeProducesNoBlock(t *testing.T) {
	blocks, _, _ := grokParse(t, grokMain)
	for _, b := range blocks {
		if strings.Contains(b.Body, "不该产块") {
			t.Errorf("未知 sessionUpdate 类型产出了块：%+v", b)
		}
		switch b.Kind {
		case model.KindUser, model.KindAssistant, model.KindReasoning,
			model.KindToolUse, model.KindToolResult:
		default:
			t.Errorf("出现了不该有的块类型：%q", b.Kind)
		}
	}
}

// 🔴 子代理判据是结构事实（出现在别人的 subagents/ 里），不是 agent_name。
//
// 这份 testdata 里子会话的 agent_name 是 `grok-build-plan` —— **与真实语料里
// 「子代理都是 general-purpose」的相关性正好相反**。这是唯一能分开两种判据的取样：
// 按结构判会认出它是子代理，按 agent_name 判则会把它当成主会话。
func TestGrokSubagentIsDetectedByStructureNotAgentName(t *testing.T) {
	_, _, sub := grokParse(t, grokSub)
	if sub.ParentUID != grokMain {
		t.Errorf("ParentUID = %q, want %q —— 判据应是「出现在 %s 的 subagents/ 里」，"+
			"而不是 agent_name（这条样本的 agent_name 刻意与相关性相反）",
			sub.ParentUID, grokMain, grokMain)
	}
	// agent_name 只填 AgentLabel（那是它的本义），不参与主/子判定
	if sub.AgentLabel != "grok-build-plan" {
		t.Errorf("AgentLabel = %q, want grok-build-plan", sub.AgentLabel)
	}

	_, _, main := grokParse(t, grokMain)
	if main.ParentUID != "" {
		t.Errorf("主会话的 ParentUID = %q, want 空", main.ParentUID)
	}
	// 对照：两者的 agent_name 相同，而主/子结论不同 —— 证明判据不是它
	if main.AgentLabel != sub.AgentLabel {
		t.Fatalf("对照失效：主子的 agent_name 不同（%q vs %q），"+
			"这条测试就分不出两种判据了", main.AgentLabel, sub.AgentLabel)
	}
}

// 🔴 Match 只认 updates.jsonl，**刻意不认 chat_history.jsonl**。
//
// 后者会被 Grok CLI 原地压缩，索引它等于让内容随压缩静默消失
// —— 这正是本次改造要根治的缺陷，不是一个可以「顺手也支持一下」的选项。
func TestGrokMatchOnlyTranscript(t *testing.T) {
	g := Grok{Home: grokHome(t)}
	base := filepath.Join(g.root(), "%2Ftmp%2Fdemo", grokMain)
	for _, name := range []string{"chat_history.jsonl", "rewind_points.jsonl", "events.jsonl", "summary.json"} {
		if g.Match(filepath.Join(base, name)) {
			t.Errorf("Match 认了 %s —— 正文只在 updates.jsonl", name)
		}
	}
	if !g.Match(filepath.Join(base, grokTranscript)) {
		t.Error("Match 不认 updates.jsonl")
	}
	// 根目录之外的同名文件不算
	if g.Match("/tmp/elsewhere/updates.jsonl") {
		t.Error("Match 认了根目录之外的同名文件")
	}
}
