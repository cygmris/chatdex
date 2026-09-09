package parser

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cygmris/chatdex/internal/model"
)

// Grok 解析 Grok CLI 的会话记录。
//
//	~/.grok/sessions/<百分号编码的 cwd>/<session-uuid>/
//	    updates.jsonl        正文（ACP 流式协议日志）
//	    chat_history.jsonl   **刻意不认**，见下
//	    summary.json         元数据（时间、标题、摘要、cwd）
//	    compaction/segment_*.md  被压缩掉那段的摘要
//	    subagents/<uuid>/meta.json
//
// 🔴 **为什么正文取 updates.jsonl 而不是看起来更像正文的 chat_history.jsonl**：
// Grok CLI 会**原地压缩** chat_history.jsonl —— 把已经过去的一整段对话换成
// compaction/ 下的一段摘要。索引侧看到 size < offset 判为「文件被截断」而重建，
// 行为完全正确，结果是原文静默蒸发。实测缺口：prompt_history.jsonl 记录的
// 1279 条提问里，chat_history 只剩 196 条（15%），1054 条（82%）只存在于
// updates.jsonl，可恢复率 99.9%。
//
// ⚠️ R23 的注释曾写着「updates.jsonl 与 rewind_points.jsonl 是编辑器状态与回滚点，
// 不是对话内容」—— **那句话是错的，而且正是它让 R23 排除了唯一完整的那份记录**。
// rewind_points.jsonl 才是回滚点；updates.jsonl 是完整的对话流。
//
// 与前两个来源的三处结构性差异（都由实测语料得出）：
//
//  1. **一条消息拆成连续多行**（流式 chunk），要按类型拼接；因此水位只能推进到
//     最后一次 flush 处，否则扫描落在一轮中间会把消息**永久**切成两块。
//  2. **多一个 reasoning 类型**（agent_thought_chunk）。这里是明文，与 R23 时代
//     chat_history 里那个 content 恒为 null、原文加密的 reasoning 不是一回事。
//  3. **子代理正文只在项目目录顶层存一份**，subagents/<uuid>/ 下只有 meta.json，
//     所以扫顶层即可，不会重复索引。
type Grok struct{ Home string }

func (Grok) Name() string { return string(model.SourceGrok) }

func (g Grok) root() string { return filepath.Join(g.Home, ".grok", "sessions") }

func (g Grok) Roots() []string { return []string{g.root()} }

// grokTranscript 是正文文件名。整个解析器只认这一个文件。
const grokTranscript = "updates.jsonl"

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

// grokUpdate 是 updates.jsonl 的一行。
//
// method 有 "session/update" 与 "_x.ai/session/update" 两种，**都要收**
// （turn_completed 只出现在后者）。判据一律用 update.sessionUpdate，不用 method。
type grokUpdate struct {
	Timestamp int64 `json:"timestamp"`
	Params    struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content"`
			ToolCallID    string          `json:"toolCallId"`
			Title         string          `json:"title"`
			Status        string          `json:"status"`
			RawInput      json.RawMessage `json:"rawInput"`
			RawOutput     json.RawMessage `json:"rawOutput"`
			Meta          struct {
				Tool struct {
					Name string `json:"name"`
				} `json:"x.ai/tool"`
			} `json:"_meta"`
		} `json:"update"`
	} `json:"params"`
}

// grokChunkKind 是「要累积成正文」的三种 chunk 及其对应的块类型。
var grokChunkKind = map[string]model.Kind{
	"user_message_chunk":  model.KindUser,
	"agent_message_chunk": model.KindAssistant,
	"agent_thought_chunk": model.KindReasoning,
}

// grokIgnorable 是不产内容块的 sessionUpdate 类型。
//
// turn_completed 在别处单独处理（它要触发 flush），不列在这里。
//
// 这份清单**全部来自实测**，不是猜的：先只列样本里见过的几种，跑一次全量索引
// （247 个会话 / 1.0 GB），再按 `grok: 未知 sessionUpdate 类型` 的告警补齐。
// 实测该轮告警 1501 条、10 个类型，就是下面第二组。
// 未列出的类型仍会告警并跳过 —— Grok CLI 在演进，新类型必然出现，
// 告警是发现它们的唯一入口，所以不要用通配把这条路堵死。
var grokIgnorable = map[string]bool{
	// 第一组：设计阶段从样本里量到的
	"hook_execution":    true,
	"plan":              true,
	"goal_updated":      true,
	"task_backgrounded": true,
	"task_completed":    true,
	"retry_state":       true,

	// 第二组：全量索引后按告警补齐（括号内是那一轮的条数）
	"hook_annotation":        true, // 422
	"session_recap":          true, // 202
	"subagent_spawned":       true, // 198
	"subagent_finished":      true, // 198
	"auto_compact_started":   true, // 157
	"compaction_checkpoint":  true, // 151
	"auto_compact_completed": true, // 151
	"current_mode_update":    true, // 10
	"auto_compact_cancelled": true, // 5
	"image_dropped":          true, // 4
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

// grokAcc 是正在累积的一条消息。流式 chunk 一行一片，要拼回一条。
type grokAcc struct {
	kind    model.Kind
	body    strings.Builder
	ts      int64
	startAt int64 // 这条消息**第一行**的偏移量，决定水位能推到哪
	live    bool
}

func (g Grok) Parse(r io.Reader, path string, start Cursor, emit func(model.Block) error) (Cursor, error) {
	cur := start
	sum, _ := readGrokSummary(filepath.Dir(path))

	var acc grokAcc
	var lastTS int64

	// out 发一个块并推进序号。边读边发，不再把整份 blocks 收进内存——
	// 实测最大的一个会话过滤截断后仍有 51.8 MB / 38421 块。
	out := func(b model.Block) error {
		b.Seq = cur.Seq
		cur.Seq++
		return emit(b)
	}
	flush := func() error {
		if !acc.live {
			return nil
		}
		body := strings.TrimSpace(acc.body.String())
		k, ts := acc.kind, acc.ts
		acc = grokAcc{}
		if body == "" {
			return nil
		}
		return out(model.Block{Kind: k, TS: ts, Body: body})
	}

	off, err := scanLinesAt(r, start.Offset, func(line []byte, at int64) error {
		var rec grokUpdate
		if json.Unmarshal(line, &rec) != nil {
			cur.Skipped++
			return nil
		}
		u := rec.Params.Update
		// 缺 timestamp 时沿用上一个已知值——**不回退到插值**，那套已随本次改动删除。
		ts := rec.Timestamp
		if ts > 0 {
			lastTS = ts
		} else {
			ts = lastTS
		}

		if k, ok := grokChunkKind[u.SessionUpdate]; ok {
			if acc.live && acc.kind != k {
				if err := flush(); err != nil {
					return err
				}
			}
			if !acc.live {
				acc.kind, acc.ts, acc.startAt, acc.live = k, ts, at, true
			}
			acc.body.WriteString(grokText(u.Content))
			return nil
		}

		switch u.SessionUpdate {
		case "turn_completed":
			return flush()

		case "tool_call":
			if err := flush(); err != nil {
				return err
			}
			name := u.Meta.Tool.Name
			if name == "" {
				name = u.Title
			}
			return out(model.Block{
				Kind: model.KindToolUse, ToolName: name, ToolUseID: u.ToolCallID,
				TS: ts, Body: string(u.RawInput),
			})

		case "tool_call_update":
			// 🔴 靠有无 status 分流。实测 28664 条里 14307 条没有 status
			// （那是进度更新，携带的是 rawInput 的回显），14357 条有
			// （completed 14211 / failed 146）。不分流会把每个工具结果记两遍，
			// 且其中一遍是输入不是输出。
			if u.Status == "" {
				return nil
			}
			if err := flush(); err != nil {
				return err
			}
			return out(model.Block{
				Kind: model.KindToolResult, ToolUseID: u.ToolCallID,
				TS: ts, Body: string(u.RawOutput),
			})
		}

		if !grokIgnorable[u.SessionUpdate] {
			slog.Warn("grok: 未知 sessionUpdate 类型，已跳过", "type", u.SessionUpdate)
		}
		return nil
	})
	if err != nil {
		return cur, err
	}

	// 🔴 水位只推到「当前这条未完结消息的起点」，不是文件末尾。
	//
	// 扫描随时可能落在一轮对话中间。若水位推到末尾，累积到一半的那条消息会
	// 被当成完整消息发出去，而下一轮从新水位开始读剩下的半条——两块**永久**
	// 接不回来。宁可每轮重读几 KB。其余两个来源一行一条消息，没有这个问题，
	// 所以代码里找不到先例。
	cur.Offset = off
	if acc.live {
		cur.Offset = acc.startAt
	}
	if sum.GeneratedTitle != "" {
		cur.Title = sum.GeneratedTitle
	}
	return cur, nil
}

// grokText 从一个 chunk 的 content 字段里取出文本。
//
// 实测形状恒为 {"type":"text","text":"…"}。另兼容裸字符串——代价一行，
// 而 ACP 是演进中的协议，这个字段换形状不会有任何报错，只会静默变成空正文。
func grokText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Text != "" {
		return obj.Text
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}
