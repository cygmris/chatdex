package parser

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cygmris/chatdex/internal/model"
)

// Grok 解析 Grok CLI 的会话记录。
//
//	~/.grok/sessions/<百分号编码的 cwd>/<session-uuid>/
//	    chat_history.jsonl   正文
//	    summary.json         元数据（时间、标题、摘要、cwd）
//	    events.jsonl         事件流，每条带 ts
//	    subagents/<uuid>/meta.json
//
// 与前两个来源的三处结构性差异（都由实测语料得出）：
//
//  1. **正文里没有任何时间字段。** 时间戳从同目录的 events.jsonl 按 tool_call_id
//     取真值，取不到的在相邻真值间插值。这是 Parse 要收 path 的唯一理由。
//  2. **多一个 reasoning 类型。** content 恒为 null，原文在 encrypted_content
//     （加密不可读），能索引的只有 summary[].text —— 已经是压缩过的思考要点。
//  3. **子代理正文只在项目目录顶层存一份**，subagents/<uuid>/ 下只有 meta.json，
//     所以扫顶层即可，不会重复索引。
type Grok struct{ Home string }

func (Grok) Name() string { return string(model.SourceGrok) }

func (g Grok) root() string { return filepath.Join(g.Home, ".grok", "sessions") }

func (g Grok) Roots() []string { return []string{g.root()} }

// grokTranscript 是正文文件名。整个解析器只认这一个文件：
// 同目录下 updates.jsonl（单会话可达 22 MB）与 rewind_points.jsonl 是编辑器状态
// 与回滚点，不是对话内容；system_prompt.txt 是工具的提示词不是使用者的会话。
const grokTranscript = "chat_history.jsonl"

func (g Grok) Match(path string) bool {
	return filepath.Base(path) == grokTranscript &&
		strings.HasPrefix(path, g.root()+string(filepath.Separator))
}

// grokSummary 是 summary.json 里本解析器要用的部分。
type grokSummary struct {
	Info struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"info"`
	SessionSummary string `json:"session_summary"`
	GeneratedTitle string `json:"generated_title"`
	AgentName      string `json:"agent_name"`
	CreatedAt      string `json:"created_at"`
	LastActiveAt   string `json:"last_active_at"`
	UpdatedAt      string `json:"updated_at"`
}

// grokMessage 是 chat_history.jsonl 的一行。
type grokMessage struct {
	Type       string          `json:"type"`
	Content    json.RawMessage `json:"content"`
	Summary    json.RawMessage `json:"summary"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// grokIgnorable 是不产内容块的消息类型。
//
// system 是工具的提示词，不是使用者的会话；backend_tool_call 实测可读文本为 0。
var grokIgnorable = map[string]bool{
	"system":            true,
	"backend_tool_call": true,
}

func (g Grok) Meta(path string) (model.SessionMeta, error) {
	dir := filepath.Dir(path)
	m := model.SessionMeta{
		Source:     model.SourceGrok,
		FilePath:   path,
		SessionUID: filepath.Base(dir),
	}

	s, err := readGrokSummary(dir)
	if err != nil {
		// summary.json 读不到不是致命的：会话仍然索引，元数据退回从路径推。
		// 中断整轮的代价远大于少几个字段。
		slog.Warn("grok: 读不到 summary.json，元数据退回从路径推", "dir", dir, "err", err)
		m.ProjectPath = decodeGrokProject(filepath.Base(filepath.Dir(dir)))
		return m, nil
	}

	if s.Info.ID != "" {
		m.SessionUID = s.Info.ID
	}
	// 项目路径以 info.cwd 为准，**不用解码目录名**：目录名是百分号编码后的产物，
	// 文件里那份是原值。两者理论上一致，但只有一个是原始记录。
	if s.Info.CWD != "" {
		m.ProjectPath = s.Info.CWD
	} else {
		m.ProjectPath = decodeGrokProject(filepath.Base(filepath.Dir(dir)))
	}
	m.StartedAt = parseTime(s.CreatedAt)
	m.EndedAt = parseTime(s.LastActiveAt)
	if m.EndedAt == 0 {
		m.EndedAt = parseTime(s.UpdatedAt)
	}
	m.AgentLabel = s.AgentName
	m.ParentUID = grokParentOf(dir, m.SessionUID)
	return m, nil
}

// grokParentOf 找出这个会话的父会话 uuid，不是子代理则返回空。
//
// 🔴 **判据是结构事实：该 uuid 出不出现在同项目下别的会话的 subagents/ 里。**
//
// 不用 summary.json 的 agent_name —— 实测本机语料里它与主/子**完全对应**
// （41 个 general-purpose 恰好是全部 41 个子代理，30 个 grok-build-plan 恰好是
// 全部 30 个主会话，零例外）。**恰恰因为对得太齐才不能用**：agent_name 的语义是
// 「哪个 agent 配置在跑」，今天碰巧只有两种配置且一种只用于子代理；
// 明天多一个配置、或有人用 general-purpose 跑主会话，判据就悄悄反了，
// 而且不会有任何报错 —— 会话照常索引，只是主/子标反。
func grokParentOf(sessionDir, uid string) string {
	projectDir := filepath.Dir(sessionDir)
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return ""
	}
	self := filepath.Base(sessionDir)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == self {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectDir, e.Name(), "subagents", uid)); err == nil {
			return e.Name()
		}
	}
	return ""
}

func readGrokSummary(dir string) (grokSummary, error) {
	var s grokSummary
	b, err := os.ReadFile(filepath.Join(dir, "summary.json")) // 只读
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// decodeGrokProject 把 %2Fhome%2Fuser%2F… 还原成路径。
//
// 只在 summary.json 拿不到 info.cwd 时兜底用。手写而不用 url.QueryUnescape：
// 后者会把 '+' 解成空格，而路径里的 '+' 是字面量。
func decodeGrokProject(enc string) string {
	var b strings.Builder
	for i := 0; i < len(enc); i++ {
		if enc[i] == '%' && i+2 < len(enc) {
			var v int
			if _, err := fmtSscanHex(enc[i+1:i+3], &v); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(enc[i])
	}
	return b.String()
}

func fmtSscanHex(s string, v *int) (int, error) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			n = n*16 + int(c-'0')
		case c >= 'a' && c <= 'f':
			n = n*16 + int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			n = n*16 + int(c-'A') + 10
		default:
			return 0, errNotHex
		}
	}
	*v = n
	return 1, nil
}

var errNotHex = os.ErrInvalid

// grokTimes 是从 events.jsonl 取到的 tool_call_id → unix 秒。
type grokTimes map[string]int64

// readGrokEvents 建 tool_call_id → ts 映射。
//
// 实测：chat_history 里的 tool id 在 events 里**100% 找得到**带 ts 的记录，
// 而 tool_result 占块数的 55% —— 所以超过一半的块拿得到**真时间戳**，
// 其余的插值段落跨不过一次工具调用。
//
// 代价：全量 events.jsonl 合计 53.4 MB，只有 chat_history 的 1.1 倍。
func readGrokEvents(dir string) grokTimes {
	f, err := os.Open(filepath.Join(dir, "events.jsonl")) // 只读
	if err != nil {
		return nil
	}
	defer f.Close()

	out := grokTimes{}
	var rec struct {
		TS         string `json:"ts"`
		ToolCallID string `json:"tool_call_id"`
		CallID     string `json:"call_id"`
	}
	_, _ = scanLines(f, 0, func(line []byte) error {
		rec = struct {
			TS         string `json:"ts"`
			ToolCallID string `json:"tool_call_id"`
			CallID     string `json:"call_id"`
		}{}
		if json.Unmarshal(line, &rec) != nil || rec.TS == "" {
			return nil
		}
		id := rec.ToolCallID
		if id == "" {
			id = rec.CallID
		}
		if id == "" {
			return nil
		}
		if ts := parseTime(rec.TS); ts > 0 {
			// 同一个 id 可能有 started / completed 两条，取最早的那个
			if old, ok := out[id]; !ok || ts < old {
				out[id] = ts
			}
		}
		return nil
	})
	return out
}

func (g Grok) Parse(r io.Reader, path string, start Cursor, emit func(model.Block) error) (Cursor, error) {
	cur := start
	dir := filepath.Dir(path)
	times := readGrokEvents(dir)
	sum, _ := readGrokSummary(dir)
	lo, hi := parseTime(sum.CreatedAt), parseTime(sum.LastActiveAt)

	// 先全部收进来再统一定时间：插值需要知道后一个真时间戳在哪，
	// 而那要读到后面的行才知道。会话最大 1.3 MB，一次性收下没有压力。
	var pending []model.Block
	var known []int // pending 里拿到真时间戳的下标

	off, err := scanLines(r, start.Offset, func(line []byte) error {
		var msg grokMessage
		if json.Unmarshal(line, &msg) != nil {
			cur.Skipped++
			return nil
		}
		if grokIgnorable[msg.Type] {
			return nil
		}
		for _, b := range g.blocks(msg, times) {
			if b.TS > 0 {
				known = append(known, len(pending))
			}
			pending = append(pending, b)
		}
		return nil
	})
	if err != nil {
		return cur, err
	}

	fillGrokTimes(pending, known, lo, hi)
	for i := range pending {
		pending[i].Seq = cur.Seq
		cur.Seq++
		if e := emit(pending[i]); e != nil {
			return cur, e
		}
	}
	cur.Offset = off
	if sum.GeneratedTitle != "" {
		cur.Title = sum.GeneratedTitle
	}
	return cur, nil
}

// blocks 把一条消息展开成若干内容块。
func (g Grok) blocks(msg grokMessage, times grokTimes) []model.Block {
	var out []model.Block
	switch msg.Type {
	case "user", "assistant":
		if txt := grokText(msg.Content); txt != "" {
			k := model.KindUser
			if msg.Type == "assistant" {
				k = model.KindAssistant
			}
			out = append(out, model.Block{Kind: k, Body: txt})
		}
	case "reasoning":
		// ⚠️ 正文在 summary[].text，**不是 content** —— content 恒为 null，
		// 原文在 encrypted_content 里且加密不可读。只量 content 会得出
		// 「reasoning 是空的」这个错误结论。
		if txt := grokText(msg.Summary); txt != "" {
			out = append(out, model.Block{Kind: model.KindReasoning, Body: txt})
		}
	case "tool_result":
		out = append(out, model.Block{
			Kind: model.KindToolResult, ToolUseID: msg.ToolCallID,
			TS: times[msg.ToolCallID], Body: grokText(msg.Content),
		})
	default:
		slog.Warn("grok: 未知消息类型，已跳过", "type", msg.Type)
	}
	// 工具调用挂在 assistant 消息上，单独成块
	for _, tc := range msg.ToolCalls {
		out = append(out, model.Block{
			Kind: model.KindToolUse, ToolName: tc.Function.Name, ToolUseID: tc.ID,
			TS: times[tc.ID], Body: tc.Function.Arguments,
		})
	}
	return out
}

// grokText 从 content / summary 里抽出可读文本。
//
// 两种形状都要认：字符串，或 [{type, text}] 数组。
func grokText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var items []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var parts []string
	for _, it := range items {
		if it.Text != "" {
			parts = append(parts, it.Text)
		} else if it.Content != "" {
			parts = append(parts, it.Content)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// fillGrokTimes 给没拿到真时间戳的块补时间。
//
// 🔴 **补出来的是推导值，不是原始记录。** Grok 的正文里根本没有每条消息的时间，
// 这些数是「夹在前后两个真时间戳之间按序均分」算出来的 —— 与 Claude/Codex 的
// ts 不是同一种东西，别把它当成会话里真实发生的时刻。
//
// 之所以还值得算：时间线与日期过滤只需要「落在正确的那一天」，而实测
// tool_result 占 55% 且 tool id 100% 能对上，插值段落跨不过一次工具调用。
func fillGrokTimes(blocks []model.Block, known []int, lo, hi int64) {
	if len(blocks) == 0 {
		return
	}
	if len(known) == 0 {
		// 一个真时间戳都没有：整段在会话起止之间均分
		spread(blocks, 0, len(blocks)-1, lo, hi)
		return
	}
	sort.Ints(known)
	// 头部：第一个真值之前
	if first := known[0]; first > 0 {
		spread(blocks, 0, first-1, lo, blocks[first].TS)
	}
	// 中间：每两个真值之间
	for i := 0; i+1 < len(known); i++ {
		a, b := known[i], known[i+1]
		if b-a > 1 {
			spread(blocks, a+1, b-1, blocks[a].TS, blocks[b].TS)
		}
	}
	// 尾部：最后一个真值之后
	if last := known[len(known)-1]; last < len(blocks)-1 {
		spread(blocks, last+1, len(blocks)-1, blocks[last].TS, hi)
	}
}

// spread 把 [from, to] 这段的时间在 (lo, hi] 之间均分。
func spread(blocks []model.Block, from, to int, lo, hi int64) {
	if from > to {
		return
	}
	if lo <= 0 {
		lo = hi
	}
	if hi <= 0 {
		hi = lo
	}
	n := int64(to - from + 2)
	step := int64(0)
	if hi > lo && n > 0 {
		step = (hi - lo) / n
	}
	for i := from; i <= to; i++ {
		blocks[i].TS = lo + step*int64(i-from+1)
	}
}
