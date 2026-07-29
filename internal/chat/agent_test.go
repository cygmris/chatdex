package chat_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/chat"
	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/llm"
	"github.com/cygmris/chatdex/internal/mcpserver"
	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"
)

// scriptedLLM 按脚本逐轮返回，便于精确验证 agent 的循环行为。
type scriptedLLM struct {
	steps []llm.ChatResponse
	seen  [][]llm.Message
	i     int
	// lastTools 记录最后一轮拿到的工具列表（验证收口时不再给工具）
	lastTools []llm.ToolDef
}

func (s *scriptedLLM) Available(context.Context) bool { return true }
func (s *scriptedLLM) Generate(context.Context, llm.GenerateRequest) (string, error) {
	return "", nil
}
func (s *scriptedLLM) Chat(_ context.Context, _ string, msgs []llm.Message, tools []llm.ToolDef) (llm.ChatResponse, error) {
	s.seen = append(s.seen, append([]llm.Message(nil), msgs...))
	s.lastTools = tools
	if s.i >= len(s.steps) {
		return llm.ChatResponse{Content: "（脚本用尽）"}, nil
	}
	r := s.steps[s.i]
	s.i++
	return r, nil
}

func newAgent(t *testing.T, steps ...llm.ChatResponse) (*chat.Agent, *scriptedLLM) {
	t.Helper()
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "u1", FilePath: "/sessions/u1.jsonl",
		ProjectPath: "/proj/alpha", StartedAt: 1000, EndedAt: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendBlocks(id, []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "做一个类似 TimeMachine 的管理工具"},
	}, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}

	f := &scriptedLLM{steps: steps}
	return &chat.Agent{
		LLM: f, Model: "test", MaxRounds: 8,
		Tools: &mcpserver.Tools{Engine: search.NewEngine(st.DB())},
	}, f
}

func collect(t *testing.T, a *chat.Agent, q string) []chat.Event {
	t.Helper()
	var evs []chat.Event
	if err := a.Ask(context.Background(), q, func(e chat.Event) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}
	return evs
}

// 需求 10.2：首轮无命中必须换个说法重试，不得直接答「没找到」。
// 这里验证 agent **允许并支持**这一行为：第二轮的工具调用确实带着不同的查询词发了出去。
func TestAgentRetriesWithRewrittenQuery(t *testing.T) {
	a, f := newAgent(t,
		llm.ChatResponse{ToolCalls: []llm.ToolCall{{Name: "search_sessions", Args: map[string]any{"query": "增量备份"}}}},
		llm.ChatResponse{ToolCalls: []llm.ToolCall{{Name: "search_sessions", Args: map[string]any{"query": "TimeMachine"}}}},
		llm.ChatResponse{Content: "找到了：会话 1，项目 /proj/alpha"},
	)
	evs := collect(t, a, "我记得做过一个增量备份的项目")

	var queries []string
	var hits []int
	for _, e := range evs {
		if e.Type == "tool" {
			queries = append(queries, e.Args["query"].(string))
		}
		if e.Type == "tool_result" {
			hits = append(hits, e.Hits)
		}
	}
	if len(queries) != 2 || queries[0] == queries[1] {
		t.Fatalf("应发出两次不同的查询: %v", queries)
	}
	if hits[0] != 0 {
		t.Errorf("第一次查询本应无命中，实得 %d", hits[0])
	}
	if hits[1] != 1 {
		t.Errorf("改写后的查询应命中 1 条，实得 %d", hits[1])
	}
	if evs[len(evs)-1].Type != "answer" {
		t.Errorf("最后一条事件应是答案: %+v", evs[len(evs)-1])
	}

	// 工具结果必须以 role=tool 回喂给模型，否则它看不到检索结果
	last := f.seen[len(f.seen)-1]
	var toolMsgs int
	for _, m := range last {
		if m.Role == "tool" {
			toolMsgs++
		}
	}
	if toolMsgs != 2 {
		t.Errorf("回喂的工具结果消息数 = %d, want 2", toolMsgs)
	}
}

// 需求 10.7：每一步搜了什么、命中几条，都要能推给前端展示。
func TestAgentEmitsWhatItSearched(t *testing.T) {
	a, _ := newAgent(t,
		llm.ChatResponse{ToolCalls: []llm.ToolCall{{Name: "search_sessions",
			Args: map[string]any{"query": "TimeMachine", "source": "claude"}}}},
		llm.ChatResponse{Content: "答案"},
	)
	evs := collect(t, a, "找找")

	var toolEv chat.Event
	for _, e := range evs {
		if e.Type == "tool" {
			toolEv = e
		}
	}
	if toolEv.Tool != "search_sessions" {
		t.Fatalf("未推送工具事件: %+v", evs)
	}
	if toolEv.Args["query"] != "TimeMachine" || toolEv.Args["source"] != "claude" {
		t.Errorf("未带上实际使用的查询与过滤条件: %+v", toolEv.Args)
	}
	if toolEv.Round != 1 {
		t.Errorf("轮次 = %d", toolEv.Round)
	}
}

// 轮次上限必须硬性生效：给出可解释的收敛，而不是无限循环。
func TestAgentStopsAtRoundLimit(t *testing.T) {
	// 脚本永远只会调工具，agent 必须自己刹车
	steps := make([]llm.ChatResponse, 20)
	for i := range steps {
		steps[i] = llm.ChatResponse{ToolCalls: []llm.ToolCall{
			{Name: "search_sessions", Args: map[string]any{"query": "TimeMachine"}}}}
	}
	a, f := newAgent(t, steps...)
	a.MaxRounds = 3
	evs := collect(t, a, "找找")

	toolCalls := 0
	var note, answer bool
	for _, e := range evs {
		switch e.Type {
		case "tool":
			toolCalls++
		case "note":
			note = true
			if !strings.Contains(e.Text, "上限") {
				t.Errorf("收口说明未提到轮次上限: %q", e.Text)
			}
		case "answer":
			answer = true
		}
	}
	if toolCalls != 3 {
		t.Errorf("工具调用 %d 次，应被 MaxRounds=3 卡住", toolCalls)
	}
	if !note || !answer {
		t.Errorf("到上限后应给出说明并作答: note=%v answer=%v", note, answer)
	}
	// 最后一次请求不给工具，逼模型收口
	if len(f.lastTools) != 0 {
		t.Errorf("收口那轮不该再给工具，实得 %d 个", len(f.lastTools))
	}
}

// 需求 10.8：超预算要显式标注，不得静默丢弃。
func TestAgentMarksBudgetExhaustion(t *testing.T) {
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// 造够多的会话，让一次 get_session 的返回撑破预算
	for i := range 30 {
		id, err := st.UpsertSession(model.SessionMeta{
			Source: model.SourceClaude, SessionUID: string(rune('a' + i)),
			FilePath: "/s/" + string(rune('a'+i)) + ".jsonl", ProjectPath: "/p",
		})
		if err != nil {
			t.Fatal(err)
		}
		blocks := make([]model.Block, 0, 50)
		for j := range 50 {
			blocks = append(blocks, model.Block{Seq: j, Kind: model.KindUser,
				Body: strings.Repeat("很长的内容 TimeMachine ", 60)})
		}
		if err := st.AppendBlocks(id, blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}

	steps := make([]llm.ChatResponse, 8)
	for i := range steps {
		steps[i] = llm.ChatResponse{ToolCalls: []llm.ToolCall{
			{Name: "get_session", Args: map[string]any{"session_id": i + 1, "limit": 50}}}}
	}
	f := &scriptedLLM{steps: steps}
	a := &chat.Agent{LLM: f, Model: "test", MaxRounds: 8,
		Tools: &mcpserver.Tools{Engine: search.NewEngine(st.DB())}}

	if err := a.Ask(context.Background(), "读点东西", func(chat.Event) {}); err != nil {
		t.Fatal(err)
	}

	var marked bool
	for _, conv := range f.seen {
		for _, m := range conv {
			if m.Role == "tool" && strings.Contains(m.Content, "预算") {
				marked = true
			}
		}
	}
	if !marked {
		t.Error("超预算时未显式标注，模型会以为拿到的是完整结果")
	}
}

// 工具体必须复用 mcpserver 的实现（需求 10.4）：结果与直接调 MCP 工具一致。
func TestAgentToolsMatchMCPTools(t *testing.T) {
	a, _ := newAgent(t,
		llm.ChatResponse{ToolCalls: []llm.ToolCall{{Name: "search_sessions", Args: map[string]any{"query": "TimeMachine"}}}},
		llm.ChatResponse{Content: "答案"},
	)
	evs := collect(t, a, "找找")

	direct, err := a.Tools.SearchSessions(mcpserver.SearchArgs{Query: "TimeMachine"})
	if err != nil {
		t.Fatal(err)
	}
	var viaAgent int
	for _, e := range evs {
		if e.Type == "tool_result" {
			viaAgent = e.Hits
		}
	}
	if viaAgent != len(direct.Sessions) {
		t.Errorf("agent 侧命中 %d，直接调工具命中 %d —— 两边不是同一份实现",
			viaAgent, len(direct.Sessions))
	}
}
