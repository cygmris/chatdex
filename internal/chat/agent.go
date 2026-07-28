package chat

import (
	"context"
	"fmt"

	"github.com/cygmris/chatdex/internal/llm"
	"github.com/cygmris/chatdex/internal/mcpserver"
)

const (
	// defaultMaxRounds 是工具调用的轮次上限。
	defaultMaxRounds = 8
	// toolBudget 是累计工具结果的字节预算。触顶后不再追加，提示模型收敛。
	toolBudget = 24000
)

const systemPrompt = `你是 chatdex 的检索助手，帮用户在他自己的历史会话（Claude Code 与 Codex）里找东西。

工作方式：
- 先用 search_sessions 检索。关键词是**字面匹配**，命中为 0 时**必须换个说法再搜**：
  换同义词、换更短的词、去掉限定条件、或改用工具名/项目路径过滤。不要直接回答「没找到」。
- 拿不准某条是不是用户要找的，就用 get_session 读几条确认。
- 回答时必须列出具体的会话：会话 id、项目路径、原始文件绝对路径。
- 只根据检索到的内容回答，不要编造会话内容。
- 用中文回答，简洁。`

// Event 是过程中推给前端的一条事件。
//
// 需求 10.7 要求展示 LLM 实际执行了哪些检索——不展示的话，
// 用户无从判断它是搜错了方向还是确实没有。
type Event struct {
	Type  string         `json:"type"` // tool | tool_result | answer | note
	Tool  string         `json:"tool,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
	Hits  int            `json:"hits,omitempty"`
	Text  string         `json:"text,omitempty"`
	Round int            `json:"round,omitempty"`
}

// Agent 是多轮调工具的检索助手。
type Agent struct {
	LLM       llm.Client
	Model     string
	Tools     *mcpserver.Tools
	MaxRounds int
}

// Available 探测本地 LLM 此刻是否就绪。
//
// 端点配得对不等于服务起着——Ollama 随时可能没跑。需求 10.6 要的是
// 「入口置灰并说明原因」，所以状态查询必须真去探一下，而不是只看配置。
func (a *Agent) Available(ctx context.Context) bool { return a.LLM.Available(ctx) }

// Ask 回答一个问题，过程中的每一步通过 emit 推出去。
func (a *Agent) Ask(ctx context.Context, question string, emit func(Event)) error {
	rounds := a.MaxRounds
	if rounds <= 0 {
		rounds = defaultMaxRounds
	}
	msgs := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: question},
	}
	tools := toolDefs()
	budget := toolBudget

	for round := 1; round <= rounds; round++ {
		resp, err := a.LLM.Chat(ctx, a.Model, msgs, tools)
		if err != nil {
			return err
		}
		if len(resp.ToolCalls) == 0 {
			emit(Event{Type: "answer", Text: resp.Content, Round: round})
			return nil
		}

		msgs = append(msgs, llm.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			emit(Event{Type: "tool", Tool: tc.Name, Args: tc.Args, Round: round})

			out, hits, err := a.call(tc.Name, tc.Args)
			if err != nil {
				out = fmt.Sprintf(`{"error":%q}`, err.Error())
			}
			if len(out) > budget {
				// 显式告知模型被截断，而不是悄悄少给内容（需求 10.8）
				out = out[:max(budget, 0)] +
					`… [已达单次会话的检索结果预算，后续工具结果不再返回，请用现有信息作答]`
				budget = 0
			} else {
				budget -= len(out)
			}
			emit(Event{Type: "tool_result", Tool: tc.Name, Hits: hits, Round: round})
			msgs = append(msgs, llm.Message{Role: "tool", ToolName: tc.Name, Content: out})
		}

		if budget <= 0 {
			msgs = append(msgs, llm.Message{Role: "user",
				Content: "检索结果预算已用完，请用已有信息给出结论，并列出具体会话 id 与文件路径。"})
		}
	}

	// 轮次用尽：给出可解释的收敛，而不是死循环或空手而归
	emit(Event{Type: "note", Text: fmt.Sprintf("已达检索轮次上限（%d 轮），根据已有信息作答。", rounds)})
	msgs = append(msgs, llm.Message{Role: "user",
		Content: "已达检索轮次上限。请立刻用已有信息作答，不要再调用工具。"})
	resp, err := a.LLM.Chat(ctx, a.Model, msgs, nil) // 不给工具，逼它收口
	if err != nil {
		return err
	}
	emit(Event{Type: "answer", Text: resp.Content, Round: rounds})
	return nil
}
