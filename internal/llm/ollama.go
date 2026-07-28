package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
}

type ollamaGenerateResp struct {
	Response string `json:"response"`
	Error    string `json:"error"`
}

func (o *Ollama) Generate(ctx context.Context, r GenerateRequest) (string, error) {
	body := ollamaGenerateReq{Model: r.Model, Prompt: r.Prompt, System: r.System, Stream: false}
	if r.NumPredict > 0 {
		body.Options = map[string]any{"num_predict": r.NumPredict}
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

func (o *Ollama) Chat(ctx context.Context, model string, msgs []Message, tools []ToolDef) (ChatResponse, error) {
	req := ollamaChatReq{Model: model, Stream: false}
	for _, m := range msgs {
		om := ollamaChatMsg{Role: m.Role, Content: m.Content, ToolName: m.ToolName}
		for _, tc := range m.ToolCalls {
			var otc ollamaToolCall
			otc.Function.Name = tc.Name
			otc.Function.Arguments = tc.Args
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		req.Messages = append(req.Messages, om)
	}
	for _, t := range tools {
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
