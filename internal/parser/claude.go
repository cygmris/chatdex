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

// Claude 解析 Claude Code 的会话记录。
//
//	主会话   ~/.claude/projects/<slug>/<uuid>.jsonl
//	子代理   ~/.claude/projects/<slug>/<uuid>/subagents/agent-*.jsonl
//
// 消息走 message.role，content 为字符串或列表。
type Claude struct{ Home string }

func (Claude) Name() string { return string(model.SourceClaude) }

func (c Claude) root() string { return filepath.Join(c.Home, ".claude", "projects") }

func (c Claude) Roots() []string { return []string{c.root()} }

func (c Claude) Match(path string) bool {
	return strings.HasSuffix(path, ".jsonl") && strings.HasPrefix(path, c.root()+string(filepath.Separator))
}

// claudeRecord 只声明本项目关心的字段，其余一律忽略。
type claudeRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// claudeContentBlock 是 content 列表里的一项。
type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// ignorableTypes 是已知的非消息记录：跳过它们不算「未知结构」，不该刷 warning。
var ignorableTypes = map[string]bool{
	"attachment": true, "mode": true, "custom-title": true, "agent-name": true,
	"last-prompt": true, "file-history-delta": true, "file-history-snapshot": true,
	"queue-operation": true, "system": true, "summary": true,
}

// Meta 解出会话元数据。项目路径优先取记录里的 cwd——目录 slug 把路径分隔符
// 和原本就存在的连字符都编码成了 '-'，反解有歧义。
func (c Claude) Meta(path string) (model.SessionMeta, error) {
	m := model.SessionMeta{
		Source:     model.SourceClaude,
		FilePath:   path,
		SessionUID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
	}
	if dir := filepath.Dir(path); filepath.Base(dir) == "subagents" {
		m.ParentUID = filepath.Base(filepath.Dir(dir))
		m.AgentLabel = m.SessionUID
	}
	m.ProjectPath = unslug(projectSlug(path, c.root()))

	f, err := os.Open(path) // 只读
	if err != nil {
		return m, err
	}
	defer f.Close()

	// 头几十行里必定出现 cwd 与首个 timestamp
	br := bufio.NewReaderSize(f, 1<<16)
	for i := 0; i < 50; i++ {
		line, err := br.ReadString('\n')
		if line == "" && err != nil {
			break
		}
		var r claudeRecord
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		if r.CWD != "" {
			m.ProjectPath = r.CWD
		}
		if m.StartedAt == 0 {
			m.StartedAt = parseTime(r.Timestamp)
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

// projectSlug 取 root 之下的第一段目录名。
func projectSlug(path, root string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	if i := strings.IndexRune(rel, filepath.Separator); i > 0 {
		return rel[:i]
	}
	return ""
}

// unslug 把 "-home-user-workflow-chatdex" 还原成 "/home/user/workflow/chatdex"。
// 原目录名里本就含连字符时会还原错——所以 Meta 优先用记录里的 cwd，这里只是兜底。
func unslug(slug string) string {
	if slug == "" {
		return ""
	}
	return strings.ReplaceAll(slug, "-", "/")
}

func (c Claude) Parse(r io.Reader, start Cursor, emit func(model.Block) error) (Cursor, error) {
	cur := start
	toolNames := map[string]string{} // tool_use_id -> 工具名，供 tool_result 回填
	// 续读时首条 user 消息早已处理过，不能再走兜底截断路径
	sawUser := start.Offset > 0

	off, err := scanLines(r, start.Offset, func(line []byte) error {
		var rec claudeRecord
		if json.Unmarshal(line, &rec) != nil {
			cur.Skipped++ // 坏行跳过，不中断整个文件
			return nil
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			if !ignorableTypes[rec.Type] {
				slog.Warn("claude: 未知记录类型，已跳过", "type", rec.Type)
			}
			return nil
		}
		ts := parseTime(rec.Timestamp)
		firstUser := rec.Type == "user" && !sawUser
		if rec.Type == "user" {
			sawUser = true // 即便这条剥完为空，也算见过首条 user
		}

		for _, b := range c.blocks(rec, ts, firstUser, toolNames) {
			b.Seq = cur.Seq
			if err := emit(b); err != nil {
				return err
			}
			cur.Seq++
		}
		return nil
	})
	cur.Offset = off
	return cur, err
}

// blocks 把一条记录展开成若干内容块。
func (c Claude) blocks(rec claudeRecord, ts int64, firstUser bool, toolNames map[string]string) []model.Block {
	kind := model.KindUser
	if rec.Type == "assistant" {
		kind = model.KindAssistant
	}

	// content 可能是字符串
	var text string
	if json.Unmarshal(rec.Message.Content, &text) == nil {
		if body := cleanText(text, firstUser, rec.SessionID); body != "" {
			return []model.Block{{TS: ts, Kind: kind, Body: body}}
		}
		return nil
	}

	var items []claudeContentBlock
	if json.Unmarshal(rec.Message.Content, &items) != nil {
		slog.Warn("claude: content 既不是字符串也不是列表，已跳过", "session", rec.SessionID)
		return nil
	}

	var out []model.Block
	for _, it := range items {
		switch it.Type {
		case "text":
			if body := cleanText(it.Text, firstUser, rec.SessionID); body != "" {
				out = append(out, model.Block{TS: ts, Kind: kind, Body: body})
			}
		case "tool_use":
			toolNames[it.ID] = it.Name
			out = append(out, model.Block{
				TS: ts, Kind: model.KindToolUse, ToolName: it.Name, ToolUseID: it.ID,
				Body: string(it.Input),
			})
		case "tool_result":
			body := flattenContent(it.Content)
			if body == "" {
				continue
			}
			out = append(out, model.Block{
				TS: ts, Kind: model.KindToolResult,
				ToolName: toolNames[it.ToolUseID], ToolUseID: it.ToolUseID,
				Body: body,
			})
		}
		// thinking / image 等类型不在需求 7.1 的索引范围内，静默跳过
	}
	return out
}

// cleanText 剥离注入指令块；首条 user 消息额外走兜底截断。
func cleanText(s string, firstUser bool, sessionID string) string {
	if firstUser {
		out, _ := CleanFirstUser(s, sessionID)
		return out
	}
	return StripInjected(s)
}

// flattenContent 把 tool_result 的 content 展平成文本：可能是字符串，
// 也可能是 [{type:"text",text:...}] 这样的列表。
func flattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []claudeContentBlock
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var sb strings.Builder
	for _, it := range items {
		if it.Type == "text" {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(it.Text)
		}
	}
	return sb.String()
}
