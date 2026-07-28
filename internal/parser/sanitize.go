package parser

import (
	"log/slog"
	"strings"
)

// firstUserFallbackLimit 是首条 user 消息剥离注入块后的兜底截断阈值。
const firstUserFallbackLimit = 2000

// ⚠️ 注入的 CLAUDE.md / AGENTS.md **不是**独立的一条消息，而是混在首条真实
// 用户消息里。整条丢弃会把用户真正说的话一起丢掉，而「用户说了什么」恰恰是
// 需求 1.4（role=user 过滤）与 9.2（时间线辨认文本）赖以工作的东西。
//
// 所以这里的规则是**剥离注入块、保留其余**。

const (
	reminderOpen  = "<system-reminder>"
	reminderClose = "</system-reminder>"

	agentsOpen  = "# AGENTS.md instructions"
	agentsClose = "</INSTRUCTIONS>"
)

// StripInjected 剥掉注入的指令块，保留其余正文。
//
//   - Claude Code 用 <system-reminder>…</system-reminder> 包裹注入的 CLAUDE.md、
//     hook 输出与记忆注入
//   - Codex 首条 input_text 以 "# AGENTS.md instructions" 起、"</INSTRUCTIONS>" 止
func StripInjected(s string) string {
	s = stripBlocks(s, reminderOpen, reminderClose)
	s = stripBlocks(s, agentsOpen, agentsClose)
	return strings.TrimSpace(s)
}

// stripBlocks 反复剥掉 open…closeTag 之间的内容（含标记本身）。
// 找得到 open 但找不到 close 时，剥到字符串末尾——注入块被截断的情形下，
// 后面的内容也已不可信。
func stripBlocks(s, open, closeTag string) string {
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], closeTag)
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+len(closeTag):]
	}
}

// CleanFirstUser 处理首条 user 消息：先剥注入块，剥完仍然过长则兜底截断。
//
// 返回的 truncated 表示走了兜底路径——那说明出现了未知形态的注入，
// 值得记 warning 而不是静默丢弃（需求 2.5）。
func CleanFirstUser(s, filePath string) (text string, truncated bool) {
	text = StripInjected(s)
	if len(text) <= firstUserFallbackLimit {
		return text, false
	}
	slog.Warn("首条 user 消息剥离注入块后仍超长，按兜底阈值截断",
		"file", filePath, "len", len(text), "limit", firstUserFallbackLimit)
	return truncateAtRune(text, firstUserFallbackLimit), true
}

// truncateAtRune 按字节上限截断，但不切断多字节字符。
func truncateAtRune(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8Start(s[limit]) {
		limit--
	}
	return s[:limit]
}

// utf8Start 判断一个字节是否是 UTF-8 字符的首字节。
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
