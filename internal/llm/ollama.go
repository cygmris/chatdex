package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Ollama 是本地 Ollama 的客户端实现。
type Ollama struct {
	endpoint string
	http     *http.Client
}

// NewOllama 建立客户端。端点非回环时**直接失败**，不留逃生口（需求 10.5）。
func NewOllama(endpoint string) (*Ollama, error) {
	if err := requireLoopback(endpoint); err != nil {
		return nil, err
	}
	return &Ollama{
		endpoint: endpoint,
		// 本地 7B 生成一段摘要可能要几十秒，超时给足
		http: &http.Client{Timeout: 10 * time.Minute},
	}, nil
}

func (o *Ollama) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.endpoint+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type ollamaGenerateReq struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	System  string         `json:"system,omitempty"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
	// Think 是指针：nil 表示不带这个字段（保持模型默认），false 表示显式关掉。
	// 用值类型的话，零值 false 会给所有模型都塞上 think:false，
	// 而老版本 Ollama 不认识这个字段。
	Think *bool `json:"think,omitempty"`
}

type ollamaGenerateResp struct {
	Response string `json:"response"`
	Error    string `json:"error"`
}

func (o *Ollama) Generate(ctx context.Context, r GenerateRequest) (string, error) {
	body := ollamaGenerateReq{Model: r.Model, Prompt: r.Prompt, System: r.System, Stream: false}
	body.Options = buildOptions(r.NumCtx, r.NumPredict, r.Model,
		len([]rune(r.System))+len([]rune(r.Prompt)))
	if r.NoThink {
		no := false
		body.Think = &no
	}
	var out ollamaGenerateResp
	if err := o.post(ctx, "/api/generate", body, &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("ollama: %s", out.Error)
	}
	return out.Response, nil
}

type ollamaChatReq struct {
	Model    string           `json:"model"`
	Messages []ollamaChatMsg  `json:"messages"`
	Tools    []ollamaToolSpec `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
	Options  map[string]any   `json:"options,omitempty"`
}

type ollamaChatMsg struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolName  string           `json:"tool_name,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

type ollamaToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type ollamaChatResp struct {
	Message ollamaChatMsg `json:"message"`
	Error   string        `json:"error"`
}

func (o *Ollama) Chat(ctx context.Context, r ChatRequest) (ChatResponse, error) {
	req := ollamaChatReq{Model: r.Model, Stream: false}
	est := 0
	for _, m := range r.Messages {
		est += len([]rune(m.Content))
	}
	req.Options = buildOptions(r.NumCtx, 0, r.Model, est)
	for _, m := range r.Messages {
		om := ollamaChatMsg{Role: m.Role, Content: m.Content, ToolName: m.ToolName}
		for _, tc := range m.ToolCalls {
			var otc ollamaToolCall
			otc.Function.Name = tc.Name
			otc.Function.Arguments = tc.Args
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		req.Messages = append(req.Messages, om)
	}
	for _, t := range r.Tools {
		var ot ollamaToolSpec
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Schema
		req.Tools = append(req.Tools, ot)
	}

	var out ollamaChatResp
	if err := o.post(ctx, "/api/chat", req, &out); err != nil {
		return ChatResponse{}, err
	}
	if out.Error != "" {
		return ChatResponse{}, fmt.Errorf("ollama: %s", out.Error)
	}
	res := ChatResponse{Content: out.Message.Content}
	for _, tc := range out.Message.ToolCalls {
		res.ToolCalls = append(res.ToolCalls, ToolCall{Name: tc.Function.Name, Args: tc.Function.Arguments})
	}
	return res, nil
}

func (o *Ollama) post(ctx context.Context, path string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.http.Do(req)
	if err != nil {
		return fmt.Errorf("本地 LLM 不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("本地 LLM 返回 %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ModelInfo 是本地可用的一个模型。
type ModelInfo struct {
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

// Models 列出本地可用于**文本生成**的模型。
//
// 刻意过滤掉 embedding-only 的模型（如 bge-m3）：它们不能写摘要也不能聊天，
// 出现在选项里只会让人选中之后一头雾水。
func (o *Ollama) Models(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.endpoint+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("本地 LLM 不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("本地 LLM 返回 %s", resp.Status)
	}
	var out struct {
		Models []ModelInfo `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var usable []ModelInfo
	for _, m := range out.Models {
		for _, c := range m.Capabilities {
			if c == "completion" {
				usable = append(usable, m)
				break
			}
		}
	}
	return usable, nil
}

// buildOptions 装配 Ollama 的 options。**两条路径共用这一处。**
//
// R11 只给 Generate 加了 num_ctx，Chat 是另一个函数、另一个端点，
// 没有任何东西提示它也需要——于是同一个坑隔一天就在问答链路上重演了一次
// （实测被截在 2051 token）。抽成一处之后，「加一个请求参数」只有一个地方可改。
//
// 返回 nil 表示不带 options 字段（保持服务端原样）。
func buildOptions(numCtx, numPredict int, model string, estTokens int) map[string]any {
	opts := map[string]any{}
	if numPredict > 0 {
		opts["num_predict"] = numPredict
	}
	if numCtx > 0 {
		opts["num_ctx"] = numCtx
		// 粗估：中文一字约一 token，是上界估计。超了只告警不拦截——
		// 估算本来就不准，不该因为估算把能跑的请求挡回去。
		if estTokens > numCtx {
			slog.Warn("prompt 可能超出上下文窗口，服务端会静默截断",
				"估算token", estTokens, "num_ctx", numCtx, "model", model)
		}
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}
