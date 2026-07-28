// Package summary 为会话生成一句话摘要，并作为普通文本写入 FTS5。
//
// 摘要不只是给人看的：它用**概念词重写原文**，于是搜「增量备份」能命中
// 原文只写了「类似 timemachine 的管理工具」的会话——这正是需求 8（向量语义检索）
// 被降为门控的依据，纯文本手段填平了词汇鸿沟。
package summary

import (
	"fmt"
	"strings"

	"github.com/cygmris/chatdex/internal/search"
)

const (
	// MaxInput 是单次摘要能吃下的字符数，超出走 map-reduce。
	MaxInput = 24000
	// ChunkSize 是分段摘要的每段大小。
	ChunkSize = 8000
	// MaxChunks 是分段数上限：前一半 + 后一半，中段跳过。
	// 有了它，16 MB / 5000+ 条的会话也是有界成本（≤13 次调用）。
	MaxChunks = 12
	// perMessageLimit 防止单条巨型消息吃掉整个预算。
	perMessageLimit = 1500
)

// Distill 从会话消息里抽稀出用于摘要的文本。
//
// **不吃 tool_result 正文**：它占语料字节的 54%，却几乎不含决策信息。
// 工具只保留名字——「用过哪些工具」有信息量，「工具吐了什么」没有。
func Distill(msgs []search.Message) string {
	var sb strings.Builder
	var lastTool string
	for _, m := range msgs {
		switch m.Kind {
		case "user":
			writeLine(&sb, "我：", m.Body)
		case "assistant":
			writeLine(&sb, "助手：", m.Body)
		case "tool_use":
			if m.ToolName != "" && m.ToolName != lastTool {
				fmt.Fprintf(&sb, "〔用了工具 %s〕\n", m.ToolName)
				lastTool = m.ToolName
			}
		}
		// tool_result / summary 一律跳过
	}
	return strings.TrimSpace(sb.String())
}

func writeLine(sb *strings.Builder, prefix, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	sb.WriteString(prefix)
	sb.WriteString(clip(body, perMessageLimit))
	sb.WriteString("\n")
}

func clip(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

// Chunk 是一段待摘要的内容。
type Chunk struct {
	Text string
	// ElidedBefore 表示这一段之前有被跳过的中段（超长会话的取样）。
	ElidedBefore bool
}

// Split 把抽稀后的文本切成待摘要的段。
// 短会话返回一段；超长会话取前 MaxChunks/2 段 + 后 MaxChunks/2 段，中段跳过。
func Split(text string) []Chunk {
	r := []rune(text)
	if len(r) <= MaxInput {
		return []Chunk{{Text: text}}
	}

	var all []string
	for i := 0; i < len(r); i += ChunkSize {
		end := min(i+ChunkSize, len(r))
		all = append(all, string(r[i:end]))
	}
	if len(all) <= MaxChunks {
		out := make([]Chunk, len(all))
		for i, c := range all {
			out[i] = Chunk{Text: c}
		}
		return out
	}

	half := MaxChunks / 2
	out := make([]Chunk, 0, MaxChunks)
	for _, c := range all[:half] {
		out = append(out, Chunk{Text: c})
	}
	for i, c := range all[len(all)-half:] {
		out = append(out, Chunk{Text: c, ElidedBefore: i == 0})
	}
	return out
}

// 提示词。刻意要求「用概念词重写」——这是摘要能填平词汇鸿沟的关键。
const (
	systemSingle = `你在为一段 AI 编码助手的会话写检索用摘要。只输出摘要本身，不要复述提示词，不要加任何前后缀。`

	tmplSingle = `以下是一段 AI 编码助手会话的节选。用一句中文概括：做了什么 / 涉及哪个项目 / 结论是什么。

要求：
- 不超过 60 字
- 用概念词而非原文措辞（例如原文说「类似 timemachine 的管理工具」，应写成「增量备份工具」）
- 只输出这一句话

会话节选：
%s`

	systemMap = `你在为一段很长的会话做分段提要，供后续汇总。只输出提要本身。`

	tmplMap = `这是一段长会话的第 %d 段（共 %d 段）%s。用 1-2 句中文说明这一段在做什么、得到什么结论。

段落内容：
%s`

	tmplReduce = `以下是同一段会话各段的提要，按时间先后排列。把它们汇总成一句中文摘要：做了什么 / 涉及哪个项目 / 结论是什么。

要求：
- 不超过 60 字
- 用概念词而非原文措辞（例如「类似 timemachine 的管理工具」应写成「增量备份工具」）
- 只输出这一句话

各段提要：
%s`
)

// SinglePrompt 是短会话的一次性摘要提示词。
func SinglePrompt(text string) (system, prompt string) {
	return systemSingle, fmt.Sprintf(tmplSingle, text)
}

// MapPrompt 是分段提要的提示词。
func MapPrompt(c Chunk, idx, total int) (system, prompt string) {
	note := ""
	if c.ElidedBefore {
		note = "（此前有中间段落因过长被跳过）"
	}
	return systemMap, fmt.Sprintf(tmplMap, idx+1, total, note, c.Text)
}

// ReducePrompt 把各段提要汇总成一句话。
func ReducePrompt(parts []string) (system, prompt string) {
	var sb strings.Builder
	for i, p := range parts {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, strings.TrimSpace(p))
	}
	return systemSingle, fmt.Sprintf(tmplReduce, sb.String())
}
