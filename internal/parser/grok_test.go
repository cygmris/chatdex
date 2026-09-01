package parser

import (
	"os"
	"path/filepath"
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

// reasoning 的正文在 summary[].text，**不是 content**。
//
// content 恒为 null、原文在 encrypted_content 里且加密不可读。只看 content
// 会得出「reasoning 是空的」这个错误结论 —— 规划阶段实测时就差点这么判。
func TestGrokReasoningReadsSummaryNotContent(t *testing.T) {
	blocks, _, _ := grokParse(t, grokMain)

	var reasoning []model.Block
	for _, b := range blocks {
		if b.Kind == model.KindReasoning {
			reasoning = append(reasoning, b)
		}
	}
	if len(reasoning) == 0 {
		t.Fatal("一条 reasoning 块都没有 —— testdata 里明明有两条 reasoning 消息")
	}
	for _, b := range reasoning {
		if b.Body == "" {
			t.Error("reasoning 块正文为空 —— 说明读的是 content（恒 null）而不是 summary[].text")
		}
	}
}

// system 与 backend_tool_call 不产块：前者是工具的提示词不是会话，后者实测无可读文本。
func TestGrokSkipsNonConversationTypes(t *testing.T) {
	blocks, _, _ := grokParse(t, grokMain)
	for _, b := range blocks {
		if b.Kind != model.KindUser && b.Kind != model.KindAssistant &&
			b.Kind != model.KindReasoning && b.Kind != model.KindToolUse &&
			b.Kind != model.KindToolResult {
			t.Errorf("出现了不该有的块类型：%q", b.Kind)
		}
		if b.Body == "" && b.Kind != model.KindToolUse {
			t.Errorf("%s 块正文为空", b.Kind)
		}
	}
}

// 工具块拿到的必须是 events.jsonl 里的**真时间戳**，不是插值。
//
// 配了对照：把 events.jsonl 移走之后，同一个块只能落到插值，
// 两者必须不同 —— 否则这条断言证明不了「真值优先」真的在起作用。
func TestGrokToolBlocksUseRealTimestampsFromEvents(t *testing.T) {
	blocks, _, _ := grokParse(t, grokMain)

	var withReal int
	for _, b := range blocks {
		if b.Kind == model.KindToolResult || b.Kind == model.KindToolUse {
			if b.TS == 0 {
				t.Errorf("工具块没有时间戳：%+v", b.ToolUseID)
			}
			withReal++
		}
	}
	if withReal == 0 {
		t.Fatal("一个工具块都没有 —— testdata 取样坏了，下面的对照失去意义")
	}

	// 对照：events.jsonl 不在时，同一批块的时间戳应当改变（落到插值）
	g := Grok{Home: grokHome(t)}
	dir := filepath.Join(g.root(), "%2Ftmp%2Fdemo", grokMain)
	ev := filepath.Join(dir, "events.jsonl")
	hidden := ev + ".hidden"
	if err := os.Rename(ev, hidden); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(hidden, ev) })

	noEvents, _, _ := grokParse(t, grokMain)
	same := 0
	for i := range blocks {
		if i < len(noEvents) && blocks[i].TS == noEvents[i].TS {
			same++
		}
	}
	if same == len(blocks) {
		t.Error("移走 events.jsonl 后时间戳一个都没变 —— 说明根本没在用 events 里的真值")
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

// Match 只认 chat_history.jsonl —— 同目录下 updates.jsonl（单会话可达 22 MB）
// 与 rewind_points.jsonl 是编辑器状态，不是对话内容。
func TestGrokMatchOnlyTranscript(t *testing.T) {
	g := Grok{Home: grokHome(t)}
	base := filepath.Join(g.root(), "%2Ftmp%2Fdemo", grokMain)
	for _, name := range []string{"updates.jsonl", "rewind_points.jsonl", "events.jsonl", "summary.json"} {
		if g.Match(filepath.Join(base, name)) {
			t.Errorf("Match 认了 %s —— 它不是对话正文", name)
		}
	}
	if !g.Match(filepath.Join(base, grokTranscript)) {
		t.Error("Match 不认 chat_history.jsonl")
	}
	// 根目录之外的同名文件不算
	if g.Match("/tmp/elsewhere/chat_history.jsonl") {
		t.Error("Match 认了根目录之外的同名文件")
	}
}
