package chat

import (
	"context"
	"fmt"
	"github.com/cygmris/chatdex/internal/search"
	"strings"

	"github.com/cygmris/chatdex/internal/llm"
	"github.com/cygmris/chatdex/internal/mcpserver"
)

const (
	// defaultMaxRounds 是工具调用的轮次上限。
	defaultMaxRounds = 8
	// toolBudget 是累计工具结果的字节预算。触顶后不再追加，提示模型收敛。
	toolBudget = 24000
)

// maxPromptProjects 是塞进系统提示的项目条数上限。
// 实测本机 9 个项目，20 条足够；不限量的话项目多起来会撑爆上下文。
const maxPromptProjects = 20

// systemPrompt 按当前范围与项目清单拼装。
//
// **项目清单直接塞进提示，不指望模型自己去调 list_projects** ——
// 那个工具在工具集里躺了两期，实测一次都没被主动调用过。
// 同时写明当前生效的范围，否则模型会对「怎么搜不到别的项目」感到困惑，
// 进而在同一个范围里反复改写查询。
func (a *Agent) systemPrompt(scope Scope) string {
	var sb strings.Builder
	sb.WriteString(basePrompt)

	if scope.Project != "" {
		fmt.Fprintf(&sb, "\n\n**本次检索范围已被限定在项目 %s（含其子目录）**，"+
			"其它项目的会话搜不到是正常的，不要因此反复改写查询。", scope.Project)
	}

	if a.Projects != nil {
		if ps := a.Projects(); len(ps) > 0 {
			if len(ps) > maxPromptProjects {
				ps = ps[:maxPromptProjects]
			}
			sb.WriteString("\n\n可用的项目（用户说项目名时对应到这里的路径）：")
			for _, p := range ps {
				fmt.Fprintf(&sb, "\n- %s（%d 个会话）", p.ProjectPath, p.Sessions)
			}
		}
	}
	return sb.String()
}

const basePrompt = `你是 chatdex 的检索助手，帮用户在他自己的历史会话（Claude Code 与 Codex）里找东西。

工作方式：
- 先用 search_sessions 检索。关键词是**字面匹配**，命中为 0 时**必须换个说法再搜**：
  换同义词、换更短的词、去掉限定条件、或改用工具名/项目路径过滤。不要直接回答「没找到」。
- 拿不准某条是不是用户要找的，就用 get_session 读几条确认。
- 回答时必须列出具体的会话：会话 id、项目路径、原始文件绝对路径。
- 只根据检索到的内容回答，不要编造会话内容。
- 用中文回答，简洁。`

// Scope 是本次问答的检索范围。
//
// 空 Project 表示全部项目——不划范围是更常见的用法，而且划错范围会让 agent
// 在错误的范围里反复改写查询（重试机制会把这个错误放大）。
type Scope struct {
	Project string
}

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
	LLM   llm.Client
	Tools *mcpserver.Tools
	// Live 返回当前生效的配置（需求 4.3 的热生效）。
	//
	// 返回结构体而不是多返回值：R11 在 summary.Worker.Live 上栽过同一处——
	// 元组每加一项就要改全部调用点，且都是 string/int，写反了编译器拦不住。
	Live func() Settings
	// Projects 返回当前项目清单，用于拼进系统提示。可为 nil（测试）。
	// 与 Live 一样是回调而不是字段——拷成字段就变成需重启才能刷新的了。
	Projects func() []search.ProjectStat
	// Model / MaxRounds 是 Live 未接时的回退（测试用）。
	Model     string
	MaxRounds int
}

// Settings 是 agent 每轮重新读取的配置。
type Settings struct {
	Model     string
	MaxRounds int
	// NumCtx 与摘要共用同一个 llm.num_ctx——实测 8192→32768 在两个模型上
	// 分别只多 408 / 433 MiB，分开设省不下什么却多一个会再次失配的旋钮。
	NumCtx int
}

func (a *Agent) numCtx() int { return a.cfg().NumCtx }

func (a *Agent) cfg() Settings {
	if a.Live != nil {
		return a.Live()
	}
	return Settings{Model: a.Model, MaxRounds: a.MaxRounds}
}

// Available 探测本地 LLM 此刻是否就绪。
//
// 端点配得对不等于服务起着——Ollama 随时可能没跑。需求 10.6 要的是
// 「入口置灰并说明原因」，所以状态查询必须真去探一下，而不是只看配置。
func (a *Agent) Available(ctx context.Context) bool { return a.LLM.Available(ctx) }

// Ask 回答一个问题，过程中的每一步通过 emit 推出去。
func (a *Agent) Ask(ctx context.Context, question string, scope Scope, emit func(Event)) error {
	set := a.cfg()
	model, rounds := set.Model, set.MaxRounds
	if rounds <= 0 {
		rounds = defaultMaxRounds
	}
	msgs := []llm.Message{
		{Role: "system", Content: a.systemPrompt(scope)},
		{Role: "user", Content: question},
	}
	tools := toolDefs()
	budget := toolBudget

	for round := 1; round <= rounds; round++ {
		resp, err := a.LLM.Chat(ctx, llm.ChatRequest{
			Model: model, Messages: msgs, Tools: tools, NumCtx: a.numCtx()})
		if err != nil {
			return err
		}
		if len(resp.ToolCalls) == 0 {
			emit(Event{Type: "answer", Text: resp.Content, Round: round})
			return nil
		}

		msgs = append(msgs, llm.Message{Role: "assistant", Content: resp.Content, ToolCalls: resp.ToolCalls})
		for _, tc := range resp.ToolCalls {
			// 先应用范围再发事件：事件流是给用户看「它到底搜了什么」的，
			// 发模型给的参数就等于展示了一个**没真正执行过**的检索条件。
			args := applyScope(tc.Name, tc.Args, scope)
			emit(Event{Type: "tool", Tool: tc.Name, Args: args, Round: round})

			out, hits, err := a.call(tc.Name, args)
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
	resp, err := a.LLM.Chat(ctx, llm.ChatRequest{
		Model: model, Messages: msgs, NumCtx: a.numCtx()}) // 不给工具，逼它收口
	if err != nil {
		return err
	}
	emit(Event{Type: "answer", Text: resp.Content, Round: rounds})
	return nil
}
