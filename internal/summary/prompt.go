// Package summary 为会话生成一句话摘要，并作为普通文本写入 FTS5。
//
// 摘要不只是给人看的：它用**概念词重写原文**，于是搜「增量备份」能命中
// 原文只写了「类似 TimeMachine 的管理工具」的会话——这正是需求 8（向量语义检索）
// 被降为门控的依据，纯文本手段填平了词汇鸿沟。
package summary

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/cygmris/chatdex/internal/search"
)

const (
	// MaxCalls 是一条会话最多分几段，也就是最多几次 map 调用（再加 1 次 reduce）。
	// 有了它，16 MB / 5000+ 条的会话也是有界成本（≤13 次调用）。
	MaxCalls = 12
	// perMessageLimit 防止单条巨型消息吃掉整个预算。
	perMessageLimit = 1500

	// tokensPerRune 是中文语料的保守 token 密度（实测 8420 字符 → 6528 token，
	// 约 0.775）。取 0.8 是往「少喂」的一侧偏——喂少了摘要略粗，
	// 喂多了会被服务端静默截断，后者的代价大得多。
	tokensPerRune = 0.8
	// ctxReserve 是留给系统提示、输出预算与安全余量的 token 数。
	ctxReserve = 1024
	// minSample 是取样时单段至少保留的字符数；再少就没有信息量了。
	minSample = 120
	// minNumCtx 是推导预算时能接受的最小上下文；比这更小说明配置有问题。
	minNumCtx = 2048
	// fallbackNumCtx 是 num_ctx 非法时回退用的值。
	fallbackNumCtx = 8192
)

// Budget 是由上下文窗口推导出的字符预算。
//
// **不再写死。** 此前 MaxInput=24000 字符与 ChunkSize=8000 字符是两个独立常量，
// 而 num_ctx 是第三个数（R11 之前甚至不存在，服务端默认 2048）——三者从未对齐，
// 结果是「分了段仍然被截断」：单段 8000 字符约 6400 token，加上系统提示与
// 256 token 输出预算，已经压着 8192 的上限。
//
// Single 与 Chunk 相同是刻意的：分段的唯一目的就是让每段能完整进入一次调用，
// 给分段再单独定一个更小的值只会增加调用次数而没有任何收益。
type Budget struct {
	Single int // 单次调用能吃下的抽稀文本字符数
	Chunk  int // 分段大小；与 Single 相同
}

// BudgetFor 由上下文窗口推导字符预算。
//
// num_ctx 非法（0、负数、小到放不下提示词）时回退到 fallbackNumCtx 并记 warn——
// 配置填错不该让摘要退化成「悄悄只看开头一点点」。
func BudgetFor(numCtx int) Budget {
	if numCtx < minNumCtx {
		slog.Warn("num_ctx 过小或未设，回退默认值推导摘要预算",
			"num_ctx", numCtx, "回退到", fallbackNumCtx)
		numCtx = fallbackNumCtx
	}
	runes := int(float64(numCtx-ctxReserve) / tokensPerRune)
	return Budget{Single: runes, Chunk: runes}
}

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
	// Elided 表示这一组内部发生过省略（内容多到一次调用装不下）。
	// 省略必须留痕迹：提示词里会写明，最终摘要因此知道自己没看全。
	Elided bool
}

// Split 把抽稀后的文本切成待摘要的段。
//
// 三种情形：
//  1. 短会话 → 一段，一次调用；
//  2. 段数不超过 MaxCalls → 每段一次调用，内容全覆盖；
//  3. 段数超过 MaxCalls → **均分成 MaxCalls 组**，每组取样后进入一次调用。
//
// 第三种此前的写法是「取前 6 段 + 后 6 段，中间全扔」。实测全库有 62 个会话
// 落在这一档，最大的一个抽稀后 190 万字符——按那个写法只有约 5% 的内容进了摘要，
// 而且**中间那段干了什么完全没有痕迹**。改成均分分组之后，
// 每一段都属于某个组、都有机会被看到，覆盖率提到约 25%，且首尾中都有。
//
// 组内超出预算的部分会被截断并标记 Elided —— 省略仍然存在（内容确实太多了），
// 但它是**有痕迹的省略**，最终摘要因此知道「还有没看全的部分」。
func Split(text string, b Budget) []Chunk {
	r := []rune(text)
	if len(r) <= b.Single {
		return []Chunk{{Text: text}}
	}

	var all []string
	for i := 0; i < len(r); i += b.Chunk {
		all = append(all, string(r[i:min(i+b.Chunk, len(r))]))
	}
	if len(all) <= MaxCalls {
		out := make([]Chunk, len(all))
		for i, c := range all {
			out[i] = Chunk{Text: c}
		}
		return out
	}

	// 均分成 MaxCalls 组。用「按序号算边界」而不是「每组固定 n 段」，
	// 免得段数不能整除时最后一组特别胖或特别瘦。
	out := make([]Chunk, 0, MaxCalls)
	for g := 0; g < MaxCalls; g++ {
		lo := g * len(all) / MaxCalls
		hi := (g + 1) * len(all) / MaxCalls
		out = append(out, sampleGroup(all[lo:hi], b.Chunk))
	}
	return out
}

// sampleGroup 把一组段压进一次调用的预算。
//
// **按段均摊配额，不是取整组的首尾。** 第一版写的是「取 joined 的前一半 + 后一半」，
// 单测当场咬住：60 段分 12 组、每组 5 段时，只有每组的头尾两段进得去，
// 48/60 的位置完全没有痕迹——那不过是把「丢中段」缩小到了组内，同一个毛病。
//
// 均摊之后每一段都贡献自己的开头（话题通常在段首交代），
// 于是「这条会话中间干了什么」不再整块消失。
func sampleGroup(segs []string, budget int) Chunk {
	if len(segs) == 0 {
		return Chunk{}
	}
	joined := strings.Join(segs, "\n")
	if len([]rune(joined)) <= budget {
		return Chunk{Text: joined}
	}
	// 每段能分到多少字符；给分隔标记留一点
	quota := budget/len(segs) - 8
	if quota < minSample {
		// 段太多，均摊到每段只剩几十个字就没意义了：改为等距抽取若干段，
		// 每段仍给 minSample。抽不到的段会被省略——**但这是有痕迹的省略**。
		keep := max(1, budget/minSample)
		picked := make([]string, 0, keep)
		for i := 0; i < keep; i++ {
			picked = append(picked, segs[i*len(segs)/keep])
		}
		segs, quota = picked, minSample
	}
	parts := make([]string, 0, len(segs))
	for _, sg := range segs {
		r := []rune(sg)
		if len(r) > quota {
			parts = append(parts, string(r[:quota])+"〔…省略…〕")
		} else {
			parts = append(parts, sg)
		}
	}
	return Chunk{Text: strings.Join(parts, "\n〔…中间省略…〕\n"), Elided: true}
}

// 提示词。刻意要求「用概念词重写」——这是摘要能填平词汇鸿沟的关键。
//
// ⚠️ **要求里不要举带具体名词的例子。** 原先写的是「例如原文说『类似
// TimeMachine 的管理工具』，应写成『增量备份工具』」，结果小模型把
// 「增量备份工具」当成可用词汇，抄进了 pprof、FTS5、certbot、健康探针
// 这些毫不相关的会话摘要里（7b 上泄漏率 9%，12b 上 3082 条里泄漏 1 条）。
// 示范规则可以，但示范词会被当成素材。
const (
	systemSingle = `你在为一段 AI 编码助手的会话写检索用摘要。只输出摘要本身，不要复述提示词，不要加任何前后缀。`

	tmplSingle = `以下是一段 AI 编码助手会话的节选。把「在哪个项目里做了什么、结论是什么」
揉进**一句**中文话里。

要求：
- 一句话，不超过 60 字
- 不要分点、不要小标题、不要加粗，不要复述这里的任何提问方式
- 不要以「好的」「明白了」「我已经」这类对话口吻开头，直接说这段会话做了什么
- 不要照抄会话里的原话，尤其不要把助手最后那段回复当成摘要
- 把具体的产品名/命令名换成它所属的品类词，让搜品类也能命中
- 摘要里只能出现这段会话里真实存在的事物，不要写入本提示词中的任何词语
- 只输出这一句话本身

会话节选：
%s`

	systemMap = `你在为一段很长的会话做分段提要，供后续汇总。只输出提要本身。`

	tmplMap = `这是一段长会话的第 %d 段（共 %d 段）%s。用 1-2 句中文说明这一段在做什么、得到什么结论。

段落内容：
%s`

	tmplReduce = `以下是同一段会话各段的提要，按时间先后排列。把「在哪个项目里做了什么、
结论是什么」揉进**一句**中文话里。

要求：
- 一句话，不超过 60 字
- 不要分点、不要小标题、不要加粗，不要复述这里的任何提问方式
- 不要以「好的」「明白了」「我已经」这类对话口吻开头，直接说这段会话做了什么
- 不要照抄会话里的原话，尤其不要把助手最后那段回复当成摘要
- 把具体的产品名/命令名换成它所属的品类词，让搜品类也能命中
- 摘要里只能出现这些提要里真实存在的事物，不要写入本提示词中的任何词语
- 只输出这一句话本身

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
	if c.Elided {
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
