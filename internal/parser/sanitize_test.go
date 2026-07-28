package parser

import (
	"strings"
	"testing"
)

// 真实形态：Claude Code 把注入的 CLAUDE.md 包在 <system-reminder> 里，
// 与用户真正的提问同处首条 user 消息。剥离必须保住提问。
func TestStripInjectedKeepsRealQuestion_Claude(t *testing.T) {
	const question = "用 specloop 把 chatdex 从 Design 一路做到实现完成，spec 名 session-search"
	msg := "<system-reminder>\n# claudeMd\n" +
		strings.Repeat("## 核心編碼紀律\n- 不確定就問\n", 200) +
		"\n</system-reminder>\n" + question

	got := StripInjected(msg)
	if got != question {
		t.Errorf("剥离结果不是原提问:\ngot  = %q\nwant = %q", got, question)
	}
}

// 真实形态：Codex 首条 input_text 以 "# AGENTS.md instructions" 开头、
// "</INSTRUCTIONS>" 结束，其后才是用户提问。
func TestStripInjectedKeepsRealQuestion_Codex(t *testing.T) {
	const question = "解包我们刚才下载的二进制，调用 bn 无头模式做全面分析"
	msg := "# AGENTS.md instructions\n\n<INSTRUCTIONS>\n# Codex 全域設定\n" +
		strings.Repeat("- 預設使用繁體中文回覆\n", 300) +
		"</INSTRUCTIONS>\n\n" + question

	got := StripInjected(msg)
	if got != question {
		t.Errorf("剥离结果不是原提问:\ngot  = %q\nwant = %q", got, question)
	}
}

// 多个 system-reminder 块（hook 输出、记忆注入）都要剥干净，中间的正文要留。
func TestStripInjectedHandlesMultipleBlocks(t *testing.T) {
	msg := "<system-reminder>注入一</system-reminder>前半句" +
		"<system-reminder>注入二</system-reminder>后半句"
	if got := StripInjected(msg); got != "前半句后半句" {
		t.Errorf("got = %q", got)
	}
}

// 纯注入内容剥完为空 —— 调用方据此不产 block。
func TestStripInjectedPureInjectionBecomesEmpty(t *testing.T) {
	if got := StripInjected("<system-reminder>只有注入</system-reminder>\n\n  "); got != "" {
		t.Errorf("got = %q, want 空", got)
	}
}

// 注入块被截断（有开标记没有闭标记）时，剥到末尾——后面的内容已不可信。
func TestStripInjectedUnclosedBlock(t *testing.T) {
	if got := StripInjected("正文在前<system-reminder>被截断的注入"); got != "正文在前" {
		t.Errorf("got = %q", got)
	}
}

// 不含注入标记的普通长消息不得被误伤：兜底截断只在剥离后仍超阈值时生效，
// 且必须真的是「超长」才截，不能因为「首条消息」这个身份就砍。
func TestCleanFirstUserDoesNotHurtNormalMessage(t *testing.T) {
	msg := "这是一段正常的用户提问，" + strings.Repeat("说明细节。", 50)
	got, truncated := CleanFirstUser(msg, "/tmp/x.jsonl")
	if truncated {
		t.Error("正常长度的消息不应触发兜底截断")
	}
	if got != msg {
		t.Error("正常消息内容被改动了")
	}
}

func TestCleanFirstUserFallbackTruncates(t *testing.T) {
	msg := strings.Repeat("未知形态的注入内容，剥不掉。", 500) // 远超 2000 字节
	got, truncated := CleanFirstUser(msg, "/tmp/x.jsonl")
	if !truncated {
		t.Fatal("超长消息未触发兜底截断")
	}
	if len(got) > firstUserFallbackLimit {
		t.Errorf("截断后长度 %d 超过阈值 %d", len(got), firstUserFallbackLimit)
	}
	// 不得从多字节字符中间切断
	if !isValidUTF8(got) {
		t.Error("截断切坏了 UTF-8 字符")
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestRegistryDispatchesByMatch(t *testing.T) {
	a := fakeParser{name: "claude", suffix: ".claude.jsonl", roots: []string{"/a"}}
	b := fakeParser{name: "codex", suffix: ".codex.jsonl", roots: []string{"/b"}}
	r := NewRegistry(a, b)

	if p := r.For("/x/y.codex.jsonl"); p == nil || p.Name() != "codex" {
		t.Errorf("For 未派给 codex: %v", p)
	}
	if p := r.For("/x/y.txt"); p != nil {
		t.Errorf("无人认领的文件应返回 nil，得到 %v", p.Name())
	}
	if roots := r.Roots(); len(roots) != 2 {
		t.Errorf("Roots = %v", roots)
	}
}
