package llm

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 对真实本机 Ollama 的联通性验证。没起 Ollama 就跳过——它是可选依赖。
func TestLiveOllamaGenerate(t *testing.T) {
	c, err := NewOllama("http://127.0.0.1:11434")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if !c.Available(ctx) {
		t.Skip("本机 Ollama 未运行，跳过（本地 LLM 是可选依赖）")
	}

	start := time.Now()
	out, err := c.Generate(ctx, GenerateRequest{
		Model:      "qwen2.5:7b-instruct",
		System:     "只用一句中文回答，不超过 20 字。",
		Prompt:     "用一句话说明 rsync 是做什么的。",
		NumPredict: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("耗时 %v，输出: %s", time.Since(start).Round(time.Millisecond), strings.TrimSpace(out))
	if strings.TrimSpace(out) == "" {
		t.Error("返回为空")
	}
}
