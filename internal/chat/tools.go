// Package chat 是 dashboard 里的自然语言检索助手。
//
// 它**不实现任何检索逻辑**：工具体全部转调 mcpserver.Tools，
// 与 MCP 端点是同一份实现（需求 10.4）。这里只多做两件事：
// 把 schema 描述翻译成模型能读的形式，以及守住轮次与上下文预算。
package chat

import (
	"encoding/json"
	"fmt"

	"github.com/cygmris/chatdex/internal/llm"
	"github.com/cygmris/chatdex/internal/mcpserver"
)

// toolDefs 是给模型看的工具声明。
//
// 刻意只有三个：工具越多，小模型越容易挑错、越容易在无关的分支上耗掉轮次。
func toolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: "search_sessions",
			Description: "按关键词检索历史会话，返回会话 id、项目路径、命中片段与最佳命中位置。" +
				"关键词是字面匹配，没命中就换个说法再试，不要直接回答找不到。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":     map[string]any{"type": "string", "description": "关键词，中英皆可，多个词空格分隔"},
					"kind":      map[string]any{"type": "string", "description": "可选：user/assistant/tool_use/tool_result/summary"},
					"tool_name": map[string]any{"type": "string", "description": "可选：工具名，如 Bash"},
					"source":    map[string]any{"type": "string", "description": "可选：claude 或 codex"},
					"project":   map[string]any{"type": "string", "description": "可选：项目路径"},
					"limit":     map[string]any{"type": "integer", "description": "可选：返回条数，最多 20"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "get_session",
			Description: "读取某个会话的消息，用来确认它是不是要找的那段。可用 from_seq 从命中位置开始读。",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{"type": "integer", "description": "会话 id"},
					"from_seq":   map[string]any{"type": "integer", "description": "从第几条开始读"},
					"limit":      map[string]any{"type": "integer", "description": "读几条，最多 50"},
				},
				"required": []string{"session_id"},
			},
		},
		{
			Name:        "list_projects",
			Description: "列出索引里的项目路径与会话数，用来收窄检索范围。",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// call 执行一次工具调用，返回给模型看的 JSON 文本与命中数（供前端展示）。
// applyScope 把用户划的范围**强制覆盖**到工具参数上。
//
// 只有这一处做注入。不能只写进提示词指望模型自觉——`list_projects` 在工具集里
// 躺了两期，实测模型一次都没主动调用过（R13 的「模型能做不等于模型会做」）。
// 范围错了更糟：agent 首轮没命中会自己改写查询重试，于是它会**在错误的范围里
// 反复改写**，用户看到「搜了三轮都没找到」，而东西就在隔壁项目。
//
// 只约束检索类工具。get_session 已经定位到具体会话了，再拿范围挡它没有意义，
// 而且会让「点开搜索结果」这条路径莫名其妙地失败。
//
// 返回新 map 而不是就地改：调用方还要把原始参数留在对话历史里，
// 就地改会让历史与实际执行悄悄不一致。
func applyScope(name string, args map[string]any, scope Scope) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	if name == "search_sessions" && scope.Project != "" {
		out["project"] = scope.Project
	}
	return out
}

func (a *Agent) call(name string, args map[string]any) (string, int, error) {
	switch name {
	case "search_sessions":
		var sa mcpserver.SearchArgs
		if err := remarshal(args, &sa); err != nil {
			return "", 0, err
		}
		out, err := a.Tools.SearchSessions(sa)
		if err != nil {
			return "", 0, err
		}
		return toJSON(out), len(out.Sessions), nil

	case "get_session":
		var ga mcpserver.GetSessionArgs
		if err := remarshal(args, &ga); err != nil {
			return "", 0, err
		}
		out, err := a.Tools.GetSession(ga)
		if err != nil {
			return "", 0, err
		}
		return toJSON(out), len(out.Messages), nil

	case "list_projects":
		out, err := a.Tools.ListProjects()
		if err != nil {
			return "", 0, err
		}
		return toJSON(out), len(out.Projects), nil
	}
	return "", 0, fmt.Errorf("未知工具 %q", name)
}

// remarshal 把模型给的松散参数转成具体类型。
// 模型偶尔会把数字写成字符串，这里统一交给 encoding/json 处理，转不了就报错让它重试。
func remarshal(args map[string]any, out any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(b)
}
