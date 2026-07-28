package llm

import (
	"context"
	"strings"
	"testing"
)

// 负例是这条约束的全部意义所在：远端端点必须**构造失败**。
// 「只允许本地」如果只写在文档里而代码不报错，它就不存在。
func TestRemoteEndpointsAreRejected(t *testing.T) {
	remote := []string{
		"https://api.openai.com",
		"https://api.anthropic.com/v1",
		"http://192.168.1.10:11434",
		"http://10.0.0.5:11434",
		"http://ollama.internal:11434", // 主机名可能解析到任何地方，不赌 DNS
		"http://0.0.0.0:11434",
		"http://[fd00::1]:11434",
	}
	for _, ep := range remote {
		if _, err := NewOllama(ep); err == nil {
			t.Errorf("%s 竟然被接受了 —— 会话内容含明文凭证，这条不可放宽", ep)
		} else if !strings.Contains(err.Error(), "回环") {
			t.Errorf("%s 的报错未说明原因: %v", ep, err)
		}
	}
}

func TestLoopbackEndpointsAreAccepted(t *testing.T) {
	for _, ep := range []string{
		"http://127.0.0.1:11434",
		"http://127.0.0.1:11434/",
		"http://localhost:11434",
		"http://[::1]:11434",
	} {
		if _, err := NewOllama(ep); err != nil {
			t.Errorf("%s 应被接受: %v", ep, err)
		}
	}
}

func TestMalformedEndpointsAreRejected(t *testing.T) {
	for _, ep := range []string{"", "not a url", "ftp://127.0.0.1", "http://"} {
		if _, err := NewOllama(ep); err == nil {
			t.Errorf("%q 应被拒绝", ep)
		}
	}
}

// LLM 不可用不是错误，是功能降级：Available 返回 false，不 panic。
func TestAvailableIsFalseWhenNothingListening(t *testing.T) {
	c, err := NewOllama("http://127.0.0.1:1") // 几乎不可能有服务
	if err != nil {
		t.Fatal(err)
	}
	if c.Available(context.Background()) {
		t.Error("端口上没有服务却报告可用")
	}
}
