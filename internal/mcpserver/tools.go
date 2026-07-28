// Package mcpserver 把检索能力暴露为 MCP 工具，供 agent 自查历史会话。
//
// 这里的 Tools 是**唯一一份工具实现**：MCP 端点与 dashboard 里的聊天 agent
// 共用它（需求 4.4 / 10.4），聊天侧只换一层 schema 描述，检索逻辑一行不重写。
package mcpserver

import (
	"strings"

	"github.com/cygmris/chatdex/internal/search"
)

// 单次返回的预算。超出一律**显式标注**截断，不静默丢弃（需求 4.3 / 10.8）。
const (
	maxSearchLimit  = 20
	snippetChars    = 200
	maxSessionLimit = 50
	sessionBudget   = 6000 // 字节
	maxBodyChars    = 1200 // 单条消息
)

// Tools 持有唯一的检索引擎。
type Tools struct{ Engine *search.Engine }

// ---- search_sessions ----

type SearchArgs struct {
	Query    string `json:"query" jsonschema:"关键词，中英皆可；多个词以空格分隔，按 AND 组合"`
	Kind     string `json:"kind,omitempty" jsonschema:"限定内容类型：user/assistant/tool_use/tool_result/summary"`
	ToolName string `json:"tool_name,omitempty" jsonschema:"限定工具名，如 Bash"`
	Source   string `json:"source,omitempty" jsonschema:"限定来源：claude 或 codex"`
	Project  string `json:"project,omitempty" jsonschema:"限定项目路径（含其子目录）"`
	From     int64  `json:"from,omitempty" jsonschema:"起始时间，unix 秒"`
	To       int64  `json:"to,omitempty" jsonschema:"结束时间，unix 秒"`
	Limit    int    `json:"limit,omitempty" jsonschema:"返回条数，最多 20"`
}

type SessionBrief struct {
	SessionID   int64  `json:"session_id"`
	Source      string `json:"source"`
	ProjectPath string `json:"project_path"`
	FilePath    string `json:"file_path"` // 原始文件绝对路径，便于用别的工具打开
	StartedAt   int64  `json:"started_at"`
	EndedAt     int64  `json:"ended_at"`
	MsgCount    int    `json:"msg_count"`
	Summary     string `json:"summary,omitempty"`
	Hits        int    `json:"hits"`
	BestSeq     int    `json:"best_seq"` // 最佳命中所在序号，可直接喂给 get_session
	BestKind    string `json:"best_kind,omitempty"`
	BestTool    string `json:"best_tool,omitempty"`
	Snippet     string `json:"snippet"`
}

type SearchOutput struct {
	Sessions []SessionBrief `json:"sessions"`
	NoMatch  bool           `json:"no_match"`
	Note     string         `json:"note,omitempty"`
}

func (t *Tools) SearchSessions(a SearchArgs) (SearchOutput, error) {
	limit := a.Limit
	if limit <= 0 || limit > maxSearchLimit {
		limit = maxSearchLimit
	}
	q := search.Query{
		Text: a.Query, ToolName: a.ToolName, Source: a.Source, Project: a.Project,
		From: a.From, To: a.To, Limit: limit,
	}
	if a.Kind != "" {
		q.Kinds = []string{a.Kind}
	}
	res, err := t.Engine.SearchSessions(q)
	if err != nil {
		return SearchOutput{}, err
	}

	out := SearchOutput{NoMatch: res.NoMatch}
	for _, s := range res.Sessions {
		out.Sessions = append(out.Sessions, SessionBrief{
			SessionID: s.ID, Source: s.Source, ProjectPath: s.ProjectPath, FilePath: s.FilePath,
			StartedAt: s.StartedAt, EndedAt: s.EndedAt, MsgCount: s.MsgCount, Summary: s.Summary,
			Hits: s.Hits, BestSeq: s.BestSeq, BestKind: s.BestKind, BestTool: s.BestTool,
			Snippet: clip(search.StripAll(s.Snippet), snippetChars),
		})
	}
	if res.NoMatch {
		out.Note = "无命中。这是字面匹配的结果，不会返回近似项——换个说法或放宽过滤条件再试。"
	}
	return out, nil
}

// ---- get_session ----

type GetSessionArgs struct {
	SessionID int64 `json:"session_id" jsonschema:"会话 id，取自 search_sessions 的结果"`
	FromSeq   int   `json:"from_seq,omitempty" jsonschema:"从第几条开始读，可用 search 返回的 best_seq"`
	Limit     int   `json:"limit,omitempty" jsonschema:"读取条数，最多 50"`
}

type MessageBrief struct {
	Seq       int    `json:"seq"`
	TS        int64  `json:"ts"`
	Kind      string `json:"kind"`
	ToolName  string `json:"tool_name,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Body      string `json:"body"`
}

type SessionOutput struct {
	SessionID   int64          `json:"session_id"`
	ProjectPath string         `json:"project_path"`
	FilePath    string         `json:"file_path"`
	Total       int            `json:"total"`
	Summary     string         `json:"summary,omitempty"`
	Messages    []MessageBrief `json:"messages"`
	Note        string         `json:"note,omitempty"`
}

func (t *Tools) GetSession(a GetSessionArgs) (SessionOutput, error) {
	limit := a.Limit
	if limit <= 0 || limit > maxSessionLimit {
		limit = maxSessionLimit
	}
	v, err := t.Engine.GetSession(a.SessionID, a.FromSeq, limit)
	if err != nil {
		return SessionOutput{}, err
	}

	out := SessionOutput{
		SessionID: v.ID, ProjectPath: v.ProjectPath, FilePath: v.FilePath,
		Total: v.Total, Summary: v.Summary,
	}
	budget := sessionBudget
	var notes []string
	for _, m := range v.Messages {
		body := clip(search.StripAll(m.Body), maxBodyChars)
		if len(body) > budget {
			notes = append(notes, "已达单次返回上限，后续消息未包含——用 from_seq 继续读")
			break
		}
		budget -= len(body)
		out.Messages = append(out.Messages, MessageBrief{
			Seq: m.Seq, TS: m.TS, Kind: m.Kind, ToolName: m.ToolName,
			Truncated: m.Truncated, Body: body,
		})
	}
	if last := out.lastSeq(); last >= 0 && last+1 < v.Total {
		notes = append(notes, "会话还有后续内容，用 from_seq 继续读")
	}
	out.Note = strings.Join(notes, "；")
	return out, nil
}

func (o SessionOutput) lastSeq() int {
	if len(o.Messages) == 0 {
		return -1
	}
	return o.Messages[len(o.Messages)-1].Seq
}

// ---- list_projects ----

type ProjectsOutput struct {
	Projects []search.ProjectStat `json:"projects"`
}

func (t *Tools) ListProjects() (ProjectsOutput, error) {
	ps, err := t.Engine.Projects()
	return ProjectsOutput{Projects: ps}, err
}

// clip 按字符数截断并显式标注——截断本身必须可见，否则 agent 会把残句当全文。
func clip(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…（已截断）"
}
