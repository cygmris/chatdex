package parser

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cygmris/chatdex/internal/model"
)

// Codex 解析 Codex 的会话记录。
//
//	~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl
//
// 与 Claude Code 的差别（实测语料得出）：
//   - 消息在 payload.role，外层是 type: response_item
//   - 用户文本是 content[].input_text，助手文本是 content[].output_text
//   - 工具调用有两种：function_call（name + arguments）与 custom_tool_call（name + input）
//     两者都用 call_id 与各自的 *_output 配对（payload.id 不是配对键）
//   - 子代理在同一文件内，不另开文件
type Codex struct{ Home string }

func (Codex) Name() string { return string(model.SourceCodex) }

func (c Codex) root() string { return filepath.Join(c.Home, ".codex", "sessions") }

func (c Codex) Roots() []string { return []string{c.root()} }

func (c Codex) Match(path string) bool {
	return strings.HasSuffix(path, ".jsonl") &&
		strings.HasPrefix(filepath.Base(path), "rollout-") &&
		strings.HasPrefix(path, c.root()+string(filepath.Separator))
}

type codexRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		Output    json.RawMessage `json:"output"`
		SessionID string          `json:"session_id"`
		CWD       string          `json:"cwd"`
	} `json:"payload"`
}

type codexContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// codexIgnorableOuter 是不产内容块的外层记录类型。
//
// event_msg 尤其重要：它把 agent_message / user_message 又复述了一遍，
// 一并索引会让同一句话在库里出现两次、命中数虚高。
var codexIgnorableOuter = map[string]bool{
	"event_msg": true, "turn_context": true, "world_state": true,
	"compacted": true, "session_meta": true,
	"inter_agent_communication_metadata": true,
}

// codexIgnorablePayload 是 response_item 里不产块的负载类型。
var codexIgnorablePayload = map[string]bool{
	"reasoning": true, // 思考过程，不在需求 7.1 的索引范围内
}

func (c Codex) Meta(path string) (model.SessionMeta, error) {
	m := model.SessionMeta{
		Source:     model.SourceCodex,
		FilePath:   path,
		SessionUID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
	}
	f, err := os.Open(path) // 只读
	if err != nil {
		return m, err
	}
	defer f.Close()

	// session_meta 是首行，cwd 与 session_id 都在里面
	br := bufio.NewReaderSize(f, 1<<16)
	for i := 0; i < 20; i++ {
		line, err := br.ReadString('\n')
		if line == "" && err != nil {
			break
		}
		var r codexRecord
		if json.Unmarshal([]byte(line), &r) == nil {
			if r.Payload.CWD != "" {
				m.ProjectPath = r.Payload.CWD
			}
			if r.Payload.SessionID != "" {
				m.SessionUID = r.Payload.SessionID
			}
			if m.StartedAt == 0 {
				m.StartedAt = parseTime(r.Timestamp)
			}
		}
		if m.ProjectPath != "" && m.StartedAt != 0 {
			break
		}
		if err != nil {
			break
		}
	}
	return m, nil
}

func (c Codex) Parse(r io.Reader, start Cursor, emit func(model.Block) error) (Cursor, error) {
	cur := start
	toolNames := map[string]string{} // call_id -> 工具名
	sawUser := start.Offset > 0

	off, err := scanLines(r, start.Offset, func(line []byte) error {
		var rec codexRecord
		if json.Unmarshal(line, &rec) != nil {
			cur.Skipped++
			return nil
		}
		if rec.Type != "response_item" {
			if !codexIgnorableOuter[rec.Type] {
				slog.Warn("codex: 未知外层记录类型，已跳过", "type", rec.Type)
			}
			return nil
		}

		ts := parseTime(rec.Timestamp)
		b, ok := c.block(rec, ts, &sawUser, toolNames)
		if !ok {
			return nil
		}
		b.Seq = cur.Seq
		cur.Seq++
		return emit(b)
	})
	cur.Offset = off
	return cur, err
}

// block 把一条 response_item 转成至多一个内容块。
func (c Codex) block(rec codexRecord, ts int64, sawUser *bool, toolNames map[string]string) (model.Block, bool) {
	p := rec.Payload
	switch p.Type {
	case "message":
		// developer 角色是注入的指令，不是对话内容
		if p.Role != "user" && p.Role != "assistant" {
			return model.Block{}, false
		}
		kind := model.KindUser
		if p.Role == "assistant" {
			kind = model.KindAssistant
		}
		firstUser := p.Role == "user" && !*sawUser
		if p.Role == "user" {
			*sawUser = true
		}
		body := cleanText(flattenCodexText(p.Content), firstUser, p.SessionID)
		if body == "" {
			return model.Block{}, false
		}
		return model.Block{TS: ts, Kind: kind, Body: body}, true

	case "function_call", "custom_tool_call":
		toolNames[p.CallID] = p.Name
		body := p.Arguments
		if body == "" {
			body = p.Input
		}
		return model.Block{
			TS: ts, Kind: model.KindToolUse, ToolName: p.Name, ToolUseID: p.CallID, Body: body,
		}, body != ""

	case "function_call_output", "custom_tool_call_output":
		body := flattenCodexText(p.Output)
		return model.Block{
			TS: ts, Kind: model.KindToolResult,
			ToolName: toolNames[p.CallID], ToolUseID: p.CallID, Body: body,
		}, body != ""
	}

	if !codexIgnorablePayload[p.Type] {
		slog.Warn("codex: 未知 payload 类型，已跳过", "type", p.Type)
	}
	return model.Block{}, false
}

// flattenCodexText 把 content / output 展平成文本。
// 两者都可能是字符串，也可能是 [{type:"input_text"|"output_text"|"text", text:...}]。
func flattenCodexText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []codexContentItem
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var sb strings.Builder
	for _, it := range items {
		switch it.Type {
		case "input_text", "output_text", "text":
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(it.Text)
		}
	}
	return sb.String()
}
