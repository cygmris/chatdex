package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/model"
)

// 🔴 扫描落在一轮对话**中间**时，累积到一半的消息不能被当成完整消息发出去。
//
// updates.jsonl 是流式日志，一条消息拆成连续多行。若水位推到文件末尾，
// 半条消息会成块，而下一轮从新水位开始读剩下的半条 —— 两块**永久**接不回来
// （不像截断那样有 size < offset 触发重建，这里文件只是变长了）。
// 后果是搜一句跨越切点的话，两边都搜不到。
//
// 做法：水位停在当前未完结消息的**起始行**，那几 KB 下一轮重读。
func TestGrokWatermarkStopsAtUnfinishedTurn(t *testing.T) {
	full := []string{
		`{"timestamp":1,"method":"session/update","params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"先说完"}}}}`,
		`{"timestamp":2,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"turn_completed"}}}`,
		`{"timestamp":3,"method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"这句话"}}}}`,
		`{"timestamp":4,"method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"被扫描切开了"}}}}`,
		`{"timestamp":5,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"turn_completed"}}}`,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, grokTranscript)

	// 第一轮：只写到第 4 行（第二轮对话还没完结）
	partial := strings.Join(full[:4], "\n") + "\n"
	if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
		t.Fatal(err)
	}
	blocks1, cur1 := grokParseFile(t, path, Cursor{})

	// 未完结的那条不该成块
	for _, b := range blocks1 {
		if b.Kind == model.KindAssistant {
			t.Errorf("未完结的 assistant 消息被当成完整消息发出去了：%q", b.Body)
		}
	}
	// 水位必须停在第 3 行（那条消息的起点），而不是文件末尾
	startOfLine3 := int64(len(full[0]) + 1 + len(full[1]) + 1)
	if cur1.Offset != startOfLine3 {
		t.Fatalf("水位 = %d，想要 %d（未完结消息的起始行）；文件末尾是 %d",
			cur1.Offset, startOfLine3, len(partial))
	}

	// 第二轮：文件补完，从上次水位续读
	if err := os.WriteFile(path, []byte(strings.Join(full, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocks2, _ := grokParseFile(t, path, cur1)

	var assistants []string
	for _, b := range blocks2 {
		if b.Kind == model.KindAssistant {
			assistants = append(assistants, b.Body)
		}
	}
	if len(assistants) != 1 {
		t.Fatalf("assistant 块 = %d 个，想要 1：%q —— 消息被切成了两块，接不回来", len(assistants), assistants)
	}
	if assistants[0] != "这句话被扫描切开了" {
		t.Errorf("assistant 正文 = %q，想要「这句话被扫描切开了」", assistants[0])
	}
}

// grokParseFile 从指定水位解析一个 updates.jsonl，模拟扫描器的 seek + 续读。
func grokParseFile(t *testing.T, path string, start Cursor) ([]model.Block, Cursor) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if start.Offset > 0 {
		if _, err := f.Seek(start.Offset, 0); err != nil {
			t.Fatal(err)
		}
	}
	var got []model.Block
	cur, err := (Grok{}).Parse(f, path, start, func(b model.Block) error {
		got = append(got, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got, cur
}
