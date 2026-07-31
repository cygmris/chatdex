// Package model 定义 chatdex 的内部统一消息模型。
//
// 这里的类型刻意不认识任何一种 JSONL 结构：解析器负责把 Claude Code 与 Codex
// 的两套格式翻译成这些类型，索引与检索层只依赖这些类型。
package model

// Source 是会话的来源工具。
type Source string

const (
	SourceClaude Source = "claude"
	SourceCodex  Source = "codex"
)

// Kind 是一个内容块的类型，用于检索时按类型收窄。
type Kind string

const (
	KindUser       Kind = "user"
	KindAssistant  Kind = "assistant"
	KindToolUse    Kind = "tool_use"
	KindToolResult Kind = "tool_result"
	KindSummary    Kind = "summary"
)

// SessionMeta 是一个会话的元数据。
//
// 子代理会话（Claude Code 的 <uuid>/subagents/agent-*.jsonl）也是独立的 SessionMeta，
// 通过 ParentUID 关联到父会话。
type SessionMeta struct {
	Source      Source
	SessionUID  string // uuid（Claude）/ rollout id（Codex）
	ParentUID   string // 子代理指向父会话的 SessionUID；主会话为空
	AgentLabel  string // 子代理名，主会话为空
	FilePath    string // 原始文件绝对路径，回读时展示给用户
	ProjectPath string
	StartedAt   int64 // unix 秒
	EndedAt     int64
	MsgCount    int
	Title       string // 用户 /rename 的名字或 agent 自动生成；Codex 侧恒为空
}

// Block 是一条可检索的内容块。
type Block struct {
	Seq       int   // 会话内序号，用于回读定位与命中跳转
	TS        int64 // unix 秒
	Kind      Kind
	ToolName  string // ToolUse / ToolResult 时非空
	ToolUseID string // 把 ToolResult 关联回它那次 ToolUse
	Truncated bool   // 正文被截断（超阈值或非文本）
	RawBytes  int    // 截断前的原始体积
	Body      string // 正文；写库前由 index 层做 CJK 归一化
}
