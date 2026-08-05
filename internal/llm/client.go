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
	// NumCtx 是上下文窗口大小（token）。**必须显式给**：
	// Ollama 的默认值是 2048，与模型自身能力无关，超出部分**静默截断**——
	// 而截断从 prompt 开头切，最先丢掉的正是那句任务指令，于是模型答非所问
	// 却不报任何错。0 表示不设，交给服务端默认（不推荐）。
	NumCtx int
	// NoThink 关掉「思考」模式。
	//
	// thinking 模型（如 gemma4）会先生成一段推理再给答案，而这两段**共用**
	// NumPredict 预算：摘要给的 256 token 被推理吃光后，正式回答一个字都没轮到，
	// Ollama 返回 done_reason=length 且 response 为空串——上层看到的就是
	// 「模型返回空摘要」，完全看不出是预算被占了。
	//
	// 摘要要的是一句话，推理过程纯属浪费，所以直接关掉而不是调大预算：
	// 实测调大到 1024 确实能出，但会变成 247 字带小标题的长文，不是一句话。
	NoThink bool
}

// ChatRequest 是一轮带工具的对话请求。
//
// 用结构体而不是继续加形参：R11 在 Worker.Live 上栽过同一处——
// 参数越加越长，且都是 string/int，写反了编译器拦不住。
// 与 GenerateRequest 对称，两条路径看起来就是同一件事的两种形态。
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []ToolDef
	// NumCtx 与 GenerateRequest 同义：**必须显式给**。
	// R11 只修了 Generate 那条路径，Chat 一直吃服务端默认值，
	// 实测被截在 2051 token——而问答会累积多轮历史，截断从头部切，
	// 最先丢的正是系统提示与用户的问题。
	NumCtx int
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
	Chat(ctx context.Context, r ChatRequest) (ChatResponse, error)
}
