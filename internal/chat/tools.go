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
