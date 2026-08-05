package summary

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/search"
)

// 抽稀必须排除 tool_result 正文：它占语料 54% 的字节却几乎不含决策信息，
// 吃进去只会把上下文挤爆。
func TestDistillDropsToolResultBodies(t *testing.T) {
	msgs := []search.Message{
		{Kind: "user", Body: "做一个类似 TimeMachine 的管理工具"},
		{Kind: "assistant", Body: "先确认 restic 的快照仓库能不能增量能力"},
		{Kind: "tool_use", ToolName: "Bash", Body: `{"command":"rsync -av /a /b"}`},
		{Kind: "tool_result", ToolName: "Bash", Body: "这里是几十 KB 的构建日志输出，绝不能进摘要输入"},
		{Kind: "summary", Body: "旧的摘要，也不该被吃进去"},
	}
	got := Distill(msgs)

	if strings.Contains(got, "构建日志") {
		t.Error("抽稀吃进了 tool_result 正文")
	}
	if strings.Contains(got, "旧的摘要") {
		t.Error("抽稀吃进了旧摘要")
	}
	if !strings.Contains(got, "TimeMachine") || !strings.Contains(got, "restic") {
		t.Errorf("用户与助手的正文丢了: %q", got)
	}
	// 工具只留名字，不留入参
	if !strings.Contains(got, "Bash") {
		t.Error("工具名丢了")
	}
	if strings.Contains(got, "rsync -av") {
		t.Error("工具入参不该进摘要输入")
	}
}

// 同一个工具连续调用只记一次，避免「〔用了工具 Bash〕」刷屏。
func TestDistillCollapsesRepeatedTool(t *testing.T) {
	var msgs []search.Message
	for range 50 {
		msgs = append(msgs, search.Message{Kind: "tool_use", ToolName: "Bash"})
	}
	if n := strings.Count(Distill(msgs), "Bash"); n != 1 {
		t.Errorf("重复工具出现 %d 次，want 1", n)
	}
}

// 单条巨型消息不得吃掉整个预算。
func TestDistillClipsHugeMessage(t *testing.T) {
	huge := strings.Repeat("很长的一段话。", 5000)
	got := Distill([]search.Message{{Kind: "user", Body: huge}})
	if len([]rune(got)) > perMessageLimit+20 {
		t.Errorf("单条消息未被限长: %d 字", len([]rune(got)))
	}
}

var testBudget = BudgetFor(32768)

func TestSplitShortTextIsSingleChunk(t *testing.T) {
	cs := Split("短会话内容", testBudget)
	if len(cs) != 1 || cs[0].Elided {
		t.Errorf("短文本应只有一段且无省略标记: %+v", cs)
	}
}

// 预算必须由 num_ctx 推导，而不是写死。
//
// 此前 MaxInput=24000 字符与 num_ctx=8192 token 是两个各自为政的数：
// 24000 字符约 19000 token，是窗口的两倍多，**分了段仍然被截断**。
func TestBudgetDerivesFromNumCtx(t *testing.T) {
	small, big := BudgetFor(8192), BudgetFor(32768)
	if !(small.Single < big.Single) {
		t.Errorf("窗口大预算就该大：8192→%d, 32768→%d", small.Single, big.Single)
	}
	// 预算换算回 token 必须留在窗口内，否则又会被服务端静默截断
	for _, ctx := range []int{8192, 32768, 131072} {
		b := BudgetFor(ctx)
		if tok := int(float64(b.Single) * tokensPerRune); tok >= ctx {
			t.Errorf("num_ctx=%d 推出的预算 %d 字符约 %d token，没留余量", ctx, b.Single, tok)
		}
	}
	// Single 与 Chunk 必须相同——分段的目的就是让每段能完整进入一次调用
	if big.Single != big.Chunk {
		t.Errorf("Single 与 Chunk 应相同，得到 %d / %d", big.Single, big.Chunk)
	}
	// 非法值回退而不是算出 0 或负数
	for _, bad := range []int{0, -1, 100} {
		if b := BudgetFor(bad); b.Single <= 0 {
			t.Errorf("num_ctx=%d 推出非正预算 %d", bad, b.Single)
		}
	}
}

// ⭐ 本期的核心命题：超长会话的覆盖必须**摊在全文范围上**，而不是堆在两端。
//
// 此前的写法是「取前 6 段 + 后 6 段，中间全扔」。实测全库 62 个会话落在这一档，
// 最大的一个抽稀后 190 万字符，只有约 5% 进了摘要，且中间干了什么毫无痕迹。
//
// 断言怎么写才对，这里推翻过一次：第一版要求「每个标记都幸存」——那是**不可能**的，
// 内容是预算的几百倍时必然要丢东西，那条断言等于要求实现做不到的事。
// 正确的命题是「**全文的每一个区段都要有内容进来**」：把原文等分成 MaxCalls 个区，
// 要求每个区至少有一个标记幸存。丢中段的旧实现在中间那几个区上会全军覆没。
func TestSplitCoversEveryPartOfLongText(t *testing.T) {
	// 用小窗口，好让这点语料就能触发分组路径（大窗口下它一次就吃下了）
	b := BudgetFor(8192)
	const marks = MaxCalls * 20 // 远多于分组数，确保每个区里有若干标记
	var sb strings.Builder
	for i := 0; i < marks; i++ {
		sb.WriteString(fmt.Sprintf("〖标记%03d〗", i))
		sb.WriteString(strings.Repeat("内", 900))
	}
	cs := Split(sb.String(), b)

	if len(cs) != MaxCalls {
		t.Fatalf("段数 = %d, want %d —— 这条语料应当触发分组路径", len(cs), MaxCalls)
	}
	joined := ""
	for _, c := range cs {
		joined += c.Text
	}

	// 把原文等分成 MaxCalls 个区，逐区检查有没有内容幸存
	perRegion := marks / MaxCalls
	var empty []int
	for region := 0; region < MaxCalls; region++ {
		hit := false
		for k := region * perRegion; k < (region+1)*perRegion; k++ {
			if strings.Contains(joined, fmt.Sprintf("〖标记%03d〗", k)) {
				hit = true
				break
			}
		}
		if !hit {
			empty = append(empty, region)
		}
	}
	if len(empty) > 0 {
		t.Errorf("第 %v 区完全没有内容进入摘要 —— 中段被丢弃了（共 %d 区）", empty, MaxCalls)
	}
}

// 组内确实装不下时，省略必须留痕迹，不能静默。
func TestSplitMarksElisionWhenGroupOverflows(t *testing.T) {
	b := BudgetFor(8192)
	long := strings.Repeat("内", b.Chunk*MaxCalls*3)
	cs := Split(long, b)
	elided := 0
	for _, c := range cs {
		if c.Elided {
			elided++
		}
		if c.Elided && !strings.Contains(c.Text, "省略") {
			t.Error("标了 Elided 却没在正文里留下省略痕迹")
		}
	}
	if elided == 0 {
		t.Error("内容远超预算却没有任何一段标记省略")
	}
}

// 单次 / 分段的分界线由预算决定，不再是写死的 24000。
func TestSplitBoundary(t *testing.T) {
	b := BudgetFor(32768)
	if got := len(Split(strings.Repeat("字", b.Single), b)); got != 1 {
		t.Errorf("恰好 %d 字应走单次路径，实得 %d 段", b.Single, got)
	}
	if got := len(Split(strings.Repeat("字", b.Single+1), b)); got < 2 {
		t.Errorf("超过 %d 字应分段，实得 %d 段", b.Single, got)
	}
}

// 「用概念词重写」是摘要能填平词汇鸿沟的关键，提示词里必须写明。
//
// 只断言**要求在**，不断言有示例——原先还要求出现 "TimeMachine"，
// 而那个示例词正是被模型抄进不相关摘要的东西
// （见 TestPromptCarriesNoBorrowableNouns）。两条断言曾直接打架。
func TestPromptsRequireConceptualRewrite(t *testing.T) {
	_, single := SinglePrompt("内容")
	if !strings.Contains(single, "品类词") {
		t.Error("单次提示词缺少「换成品类词」的要求")
	}
	_, reduce := ReducePrompt([]string{"提要一", "提要二"})
	if !strings.Contains(reduce, "品类词") {
		t.Error("汇总提示词缺少「换成品类词」的要求")
	}
	if !strings.Contains(reduce, "提要一") || !strings.Contains(reduce, "提要二") {
		t.Error("汇总提示词没带上各段提要")
	}
}

func TestMapPromptCarriesElisionNote(t *testing.T) {
	_, p := MapPrompt(Chunk{Text: "内容", Elided: true}, 6, 12)
	if !strings.Contains(p, "跳过") {
		t.Error("分段提示词未说明中段被跳过")
	}
	_, p2 := MapPrompt(Chunk{Text: "内容"}, 0, 12)
	if strings.Contains(p2, "跳过") {
		t.Error("未省略的段落不该带跳过说明")
	}
}

// 提示词的「要求」部分里不得出现具体名词做示范。
//
// 原先写的是「例如原文说『类似 TimeMachine 的管理工具』，应写成『增量备份工具』」，
// 结果小模型把「增量备份工具」当成可用词汇，抄进了 pprof、FTS5、certbot、
// 健康探针这些毫不相关的会话摘要里（qwen2.5:7b 上泄漏率 9%，
// gemma4:12b 上 3082 条真实摘要里泄漏 1 条）。
//
// **示范规则可以，示范词会被当成素材。**
func TestPromptCarriesNoBorrowableNouns(t *testing.T) {
	_, single := SinglePrompt("占位")
	_, reduce := ReducePrompt([]string{"占位"})

	// 这些是曾经泄漏过的、以及同类「一看就能直接抄进摘要」的具体名词
	for _, w := range []string{"增量备份工具", "TimeMachine"} {
		for name, p := range map[string]string{"单段": single, "汇总": reduce} {
			if strings.Contains(p, w) {
				t.Errorf("%s提示词里出现了可直接抄用的具体名词 %q —— 小模型会把它写进不相关的摘要", name, w)
			}
		}
	}

	// 斜杠枚举会被当成**输出格式**照抄：曾产出「做了压测/项目 压力测试
	// 结论 …」乃至整篇带小标题的 Markdown（3157 条真实摘要里 2 条）。
	for _, w := range []string{"做了什么 /", "/ 涉及哪个项目", "结论是什么。"} {
		for name, p := range map[string]string{"单段": single, "汇总": reduce} {
			if strings.Contains(p, w) {
				t.Errorf("%s提示词里有斜杠枚举 %q —— 模型会把它当成输出格式照抄", name, w)
			}
		}
	}

	// 必须显式禁止分点/小标题，否则模型会输出整篇 Markdown 而不是一句话；
	// 也必须禁止抄原话，否则会把助手最后一段回复当摘要（实测 41/3157）
	for name, p := range map[string]string{"单段": single, "汇总": reduce} {
		if !strings.Contains(p, "不要分点") {
			t.Errorf("%s提示词没有禁止分点 —— 实测有摘要变成了带小标题的整篇文档", name)
		}
		if !strings.Contains(p, "不要照抄会话里的原话") {
			t.Errorf("%s提示词没有禁止抄原话 —— 实测 41 条摘要是助手回复的前 120 字", name)
		}
	}
}
