// Package llm 是摘要与聊天共用的唯一 LLM 抽象。
//
// 端点只允许回环地址（需求 10.5）。本地 LLM 是**可选依赖**：不可用时
// 索引与检索照常工作，只是缺摘要、聊天入口置灰（需求 10.6 / 11.6）。
//
// ⛔ 这里没有、也不得有 embedding 方法——需求 8 的向量检索本期门控。
package llm

import "context"

// Message 是一轮对话消息。
type Message struct {
	Role      string     `json:"role"` // system | user | assistant | tool
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"` // role=tool 时对应的工具名
}

// ToolDef 是提供给模型的工具定义。
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema
}

// ToolCall 是模型发起的一次工具调用。
type ToolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// GenerateRequest 是一次单轮生成（摘要用）。
type GenerateRequest struct {
	Model  string
	System string
	Prompt string
	// NumPredict 限制生成长度；0 表示用模型默认值。
	NumPredict int
}

// ChatResponse 是一轮对话的结果：要么是文本，要么是若干工具调用。
type ChatResponse struct {
	Content   string
	ToolCalls []ToolCall
}

// Client 是摘要与聊天共用的接口。将来若解除需求 8 的门控，
// 向量生成挂在同一抽象之后，索引与检索层不受影响。
type Client interface {
	// Available 探测本地 LLM 是否就绪。不可用不是错误，是功能降级。
	Available(ctx context.Context) bool
	// Generate 单轮生成，摘要用。
	Generate(ctx context.Context, req GenerateRequest) (string, error)
	// Chat 多轮对话（可带工具），聊天 agent 用。
	Chat(ctx context.Context, model string, msgs []Message, tools []ToolDef) (ChatResponse, error)
}
