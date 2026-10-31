package parser

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cygmris/chatdex/internal/model"
)

// CursorCLI 解析 Cursor CLI 的会话记录。
//
//	主会话  ~/.cursor/projects/<slug>/agent-transcripts/<uuid>/<uuid>.jsonl
//	子代理  ~/.cursor/projects/<slug>/agent-transcripts/<uuid>/subagents/<uuid>.jsonl
//
// 一行一条消息，role 为 user / assistant。user 正文包在
// <timestamp>…</timestamp> 与 <user_query>…</user_query> 里，时间戳不在 JSON 字段上。
//
// 工具调用只有 tool_use（name + input），没有 tool_result。
// 工具结果在 ~/.cursor/chats/<md5(cwd)>/<uuid>/store.db 里，而且大量 blob 是加密的
// （meta 里有 blobEncryptionKey）。那份库不是这条解析器的正文来源。
//
// 类型名不叫 Cursor：parser.Cursor 已经是解析进度。
type CursorCLI struct{ Home string }

func (CursorCLI) Name() string { return string(model.SourceCursor) }

func (c CursorCLI) root() string { return filepath.Join(c.Home, ".cursor", "projects") }

func (c CursorCLI) Roots() []string { return []string{c.root()} }

func (c CursorCLI) Match(path string) bool {
	if filepath.Ext(path) != ".jsonl" {
		return false
	}
	root := c.root() + string(filepath.Separator)
	if !strings.HasPrefix(path, root) {
		return false
	}
	needle := string(filepath.Separator) + "agent-transcripts" + string(filepath.Separator)
	return strings.Contains(path, needle)
}

type cursorMetaFile struct {
	CreatedAtMs int64  `json:"createdAtMs"`
	UpdatedAtMs int64  `json:"updatedAtMs"`
	Title       string `json:"title"`
	CWD         string `json:"cwd"`
}

type cursorLine struct {
	Role    string `json:"role"`
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type cursorPart struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

var (
	cursorStampRE = regexp.MustCompile(`(?s)<timestamp>(.*?)</timestamp>`)
	cursorQueryRE = regexp.MustCompile(`(?s)<user_query>\s*(.*?)\s*</user_query>`)
)

func (c CursorCLI) Meta(path string) (model.SessionMeta, error) {
	m := model.SessionMeta{
		Source:     model.SourceCursor,
		FilePath:   path,
		SessionUID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
	}
	if filepath.Base(filepath.Dir(path)) == "subagents" {
		m.ParentUID = filepath.Base(filepath.Dir(filepath.Dir(path)))
	}
	if side := c.readSideMeta(m.SessionUID); side.CWD != "" || side.CreatedAtMs != 0 {
		m.ProjectPath = side.CWD
		m.StartedAt = msToSec(side.CreatedAtMs)
		m.EndedAt = msToSec(side.UpdatedAtMs)
	}
	if m.ProjectPath == "" {
		m.ProjectPath = cursorProjectFromSlug(projectSlug(path, c.root()))
	}
	if m.StartedAt == 0 {
		m.StartedAt = cursorFirstStamp(path)
	}
	return m, nil
}

// readSideMeta 读 ~/.cursor/chats/<hash>/<uid>/meta.json。
//
// hash 是 cwd 的 md5，这边还不知道 cwd，所以按会话 uuid 在 chats 下找一层。
// 找不到就当没有：标题和 cwd 退回空，项目路径再试 slug。
func (c CursorCLI) readSideMeta(uid string) cursorMetaFile {
	var side cursorMetaFile
	chats := filepath.Join(c.Home, ".cursor", "chats")
	entries, err := os.ReadDir(chats)
	if err != nil {
		return side
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(chats, e.Name(), uid, "meta.json"))
		if err != nil {
			continue
		}
		_ = json.Unmarshal(b, &side)
		return side
	}
	return side
}

// cursorSlugHyphenCap 是反解时愿意枚举的连字符个数。
//
// 每个 `-` 都可能原来是 `/` 或 `-`，要试 2^n 种。超过这个数就放弃，
// 不在扫描时对一个目录名做十几万次 Stat。
const cursorSlugHyphenCap = 16

// cursorProjectFromSlug 只在反解结果恰好一个、且那个目录真实存在时才用。
//
// Cursor 的 slug 没有 Claude 那种前导 `-`：绝对路径先去掉根斜杠，
// 再把剩下的 `/` 和 `.` 都换成 `-`。`.` 折进去之后回不来。
// `-` 分不清原来是连字符还是斜杠——两种目录都存在时留空，
// 不把猜出来的路径写进索引。一个都对不上也留空。
func cursorProjectFromSlug(slug string) string {
	if slug == "" || strings.HasPrefix(slug, ".") {
		return ""
	}
	n := strings.Count(slug, "-")
	if n > cursorSlugHyphenCap {
		return ""
	}
	chars := []byte(slug)
	idxs := make([]int, 0, n)
	for i, c := range chars {
		if c == '-' {
			idxs = append(idxs, i)
		}
	}
	var hit string
	for mask := 0; mask < 1<<n; mask++ {
		buf := append([]byte(nil), chars...)
		for bit, i := range idxs {
			if mask>>bit&1 == 1 {
				buf[i] = '/'
			}
		}
		p := "/" + string(buf)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		if hit != "" {
			return ""
		}
		hit = p
	}
	return hit
}

func cursorFirstStamp(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var ts int64
	_, _ = scanLines(f, 0, func(line []byte) error {
		if ts != 0 {
			return nil
		}
		var rec cursorLine
		if json.Unmarshal(line, &rec) != nil || rec.Role != "user" {
			return nil
		}
		var parts []cursorPart
		if json.Unmarshal(rec.Message.Content, &parts) != nil {
			return nil
		}
		for _, part := range parts {
			if part.Type != "text" {
				continue
			}
			if m := cursorStampRE.FindStringSubmatch(part.Text); m != nil {
				if t := parseCursorStamp(m[1]); t != 0 {
					ts = t
				}
			}
		}
		return nil
	})
	return ts
}

func (c CursorCLI) Parse(r io.Reader, path string, start Cursor, emit func(model.Block) error) (Cursor, error) {
	cur := start
	uid := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if side := c.readSideMeta(uid); strings.TrimSpace(side.Title) != "" {
		cur.Title = side.Title
	}
	var lastTS int64

	off, err := scanLines(r, start.Offset, func(line []byte) error {
		var rec cursorLine
		if json.Unmarshal(line, &rec) != nil {
			cur.Skipped++
			return nil
		}
		if rec.Type == "turn_ended" {
			return nil
		}
		if rec.Role != "user" && rec.Role != "assistant" {
			if rec.Type != "" {
				slog.Warn("cursor: 未知记录类型，已跳过", "type", rec.Type)
			} else {
				cur.Skipped++
			}
			return nil
		}
		var parts []cursorPart
		if json.Unmarshal(rec.Message.Content, &parts) != nil {
			cur.Skipped++
			return nil
		}
		kind := model.KindAssistant
		if rec.Role == "user" {
			kind = model.KindUser
		}
		for _, part := range parts {
			switch part.Type {
			case "text":
				body, ts := cursorText(part.Text, rec.Role)
				if ts != 0 {
					lastTS = ts
				}
				if body == "" {
					continue
				}
				if err := emit(model.Block{Seq: cur.Seq, TS: lastTS, Kind: kind, Body: body}); err != nil {
					return err
				}
				cur.Seq++
			case "tool_use":
				body := strings.TrimSpace(string(part.Input))
				if body == "null" {
					body = ""
				}
				if err := emit(model.Block{
					Seq: cur.Seq, TS: lastTS, Kind: model.KindToolUse,
					ToolName: part.Name, Body: body,
				}); err != nil {
					return err
				}
				cur.Seq++
			default:
				if part.Type != "" {
					slog.Warn("cursor: 未知 content 类型，已跳过", "type", part.Type)
				}
			}
		}
		return nil
	})
	cur.Offset = off
	return cur, err
}

// cursorText 取出该索引的正文，并尽量带上这一条的时间。
//
// user 消息的真正提问在 <user_query> 里。没有这个标签的 user 文本是
// 注入的目录（available_subagent_types 等），不进索引。
func cursorText(s, role string) (body string, ts int64) {
	if m := cursorStampRE.FindStringSubmatch(s); m != nil {
		ts = parseCursorStamp(m[1])
	}
	if role != "user" {
		return strings.TrimSpace(s), ts
	}
	ms := cursorQueryRE.FindAllStringSubmatch(s, -1)
	if len(ms) == 0 {
		return "", ts
	}
	var b strings.Builder
	for i, m := range ms {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimSpace(m[1]))
	}
	return strings.TrimSpace(b.String()), ts
}

// parseCursorStamp 解析 "Saturday, Aug 22, 2026, 8:34 AM (UTC+7)"。
//
// 括号里的偏移是 UTC+7 这种写法，不是 Go 能直接吃的 -07:00。
// 日期部分按无时区解析，再把钟面放进那个偏移。
func parseCursorStamp(s string) int64 {
	s = strings.TrimSpace(s)
	i := strings.LastIndex(s, " (UTC")
	if i < 0 || !strings.HasSuffix(s, ")") {
		return 0
	}
	head := s[:i]
	off := strings.TrimSuffix(s[i+len(" (UTC"):], ")")
	t, err := time.Parse("Monday, Jan 2, 2006, 3:04 PM", head)
	if err != nil {
		return 0
	}
	sec, ok := parseUTCOffset(off)
	if !ok {
		return 0
	}
	loc := time.FixedZone("UTC"+off, sec)
	t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc)
	return t.Unix()
}

// parseUTCOffset 接受 +7、+07、+07:00、-5、-05:30。
func parseUTCOffset(s string) (int, bool) {
	if s == "" || (s[0] != '+' && s[0] != '-') {
		return 0, false
	}
	sign := 1
	if s[0] == '-' {
		sign = -1
	}
	rest := s[1:]
	hour, min := 0, 0
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		h, ok1 := atoi(rest[:i])
		m, ok2 := atoi(rest[i+1:])
		if !ok1 || !ok2 {
			return 0, false
		}
		hour, min = h, m
	} else {
		h, ok := atoi(rest)
		if !ok {
			return 0, false
		}
		hour = h
	}
	if hour > 14 || min > 59 {
		return 0, false
	}
	return sign * (hour*3600 + min*60), true
}

func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func msToSec(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return ms / 1000
}
