package summary

import (
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/search"
)

// 抽稀必须排除 tool_result 正文：它占语料 54% 的字节却几乎不含决策信息，
// 吃进去只会把上下文挤爆。
func TestDistillDropsToolResultBodies(t *testing.T) {
	msgs := []search.Message{
		{Kind: "user", Body: "做一个类似 timemachine 的管理工具"},
		{Kind: "assistant", Body: "先确认 restic 的 profile 持久化能力"},
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
	if !strings.Contains(got, "timemachine") || !strings.Contains(got, "restic") {
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

func TestSplitShortTextIsSingleChunk(t *testing.T) {
	cs := Split("短会话内容")
	if len(cs) != 1 || cs[0].ElidedBefore {
		t.Errorf("短文本应只有一段且无省略标记: %+v", cs)
	}
}

// 超长会话必须走 map-reduce 且段数有界——否则 16MB 的会话会打爆本地模型。
func TestSplitLongTextIsBoundedAndMarksElision(t *testing.T) {
	// 造一段远超 12 段的文本
	long := strings.Repeat("内", ChunkSize*40)
	cs := Split(long)

	if len(cs) != MaxChunks {
		t.Fatalf("段数 = %d, want %d", len(cs), MaxChunks)
	}
	elided := 0
	for _, c := range cs {
		if c.ElidedBefore {
			elided++
		}
	}
	if elided != 1 {
		t.Errorf("省略标记出现 %d 次，want 1（正好在后半段的开头）", elided)
	}
	if !cs[MaxChunks/2].ElidedBefore {
		t.Error("省略标记不在后半段的第一段上")
	}
}

// 24000 字符是单次/分段的分界线。
func TestSplitBoundary(t *testing.T) {
	if got := len(Split(strings.Repeat("字", MaxInput))); got != 1 {
		t.Errorf("恰好 %d 字应走单次路径，实得 %d 段", MaxInput, got)
	}
	if got := len(Split(strings.Repeat("字", MaxInput+1))); got < 2 {
		t.Errorf("超过 %d 字应分段，实得 %d 段", MaxInput, got)
	}
}

// 「用概念词重写」是摘要能填平词汇鸿沟的关键，提示词里必须写明。
func TestPromptsRequireConceptualRewrite(t *testing.T) {
	_, single := SinglePrompt("内容")
	if !strings.Contains(single, "概念词") || !strings.Contains(single, "timemachine") {
		t.Error("单次提示词缺少「用概念词重写」的要求与示例")
	}
	_, reduce := ReducePrompt([]string{"提要一", "提要二"})
	if !strings.Contains(reduce, "概念词") {
		t.Error("汇总提示词缺少「用概念词重写」的要求")
	}
	if !strings.Contains(reduce, "提要一") || !strings.Contains(reduce, "提要二") {
		t.Error("汇总提示词没带上各段提要")
	}
}

func TestMapPromptCarriesElisionNote(t *testing.T) {
	_, p := MapPrompt(Chunk{Text: "内容", ElidedBefore: true}, 6, 12)
	if !strings.Contains(p, "跳过") {
		t.Error("分段提示词未说明中段被跳过")
	}
	_, p2 := MapPrompt(Chunk{Text: "内容"}, 0, 12)
	if strings.Contains(p2, "跳过") {
		t.Error("未省略的段落不该带跳过说明")
	}
}
