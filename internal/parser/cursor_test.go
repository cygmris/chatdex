package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cygmris/chatdex/internal/model"
)

const (
	cursorUID    = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	cursorParent = "11111111-1111-1111-1111-111111111111"
	cursorChild  = "22222222-2222-2222-2222-222222222222"
)

func cursorWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cursorTranscript(home, slug, uid string) string {
	return filepath.Join(home, ".cursor", "projects", slug, "agent-transcripts", uid, uid+".jsonl")
}

// 合成一行一条的会话。正文是假的，不从本机真实 transcript 拷。
const cursorSample = `{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Saturday, Aug 22, 2026, 8:34 AM (UTC+7)</timestamp>\n<user_query>hello cursor</user_query>"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Read","input":{"path":"/tmp/a"}}]}}
{"type":"turn_ended","status":"success"}
{"role":"user","message":{"content":[{"type":"text","text":"<available_subagent_types>catalog</available_subagent_types>"}]}}
not-json
{"role":"assistant","message":{"content":[{"type":"thinking","text":"secret-thought"}]}}
{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Sunday, Aug 23, 2026, 9:01 PM (UTC+7)</timestamp><user_query>second</user_query>"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"ack"}]}}
`

func TestCursorMatchOnlyAgentTranscripts(t *testing.T) {
	home := t.TempDir()
	p := CursorCLI{Home: home}
	ok := cursorTranscript(home, "slug", cursorUID)
	if !p.Match(ok) {
		t.Fatalf("Match 不认 agent-transcripts 下的 jsonl：%s", ok)
	}
	sub := filepath.Join(home, ".cursor", "projects", "slug", "agent-transcripts", cursorParent, "subagents", cursorChild+".jsonl")
	if !p.Match(sub) {
		t.Fatalf("Match 不认 subagents 下的 jsonl：%s", sub)
	}
	for _, bad := range []string{
		filepath.Join(home, ".cursor", "projects", "slug", "other.jsonl"),
		filepath.Join(home, ".cursor", "projects", "slug", "agent-transcripts", cursorUID, "meta.json"),
		filepath.Join(home, ".cursor", "chats", "hash", cursorUID, "store.db"),
	} {
		if p.Match(bad) {
			t.Errorf("不该索引：%s", bad)
		}
	}
}

func TestCursorParseExtractsQueryAndCarriesStamp(t *testing.T) {
	home := t.TempDir()
	p := CursorCLI{Home: home}
	path := cursorTranscript(home, "no-such-slug-zzzz", cursorUID)
	cursorWrite(t, path, cursorSample)

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got []model.Block
	cur, err := p.Parse(f, path, Cursor{}, func(b model.Block) error {
		got = append(got, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	zone := time.FixedZone("UTC+7", 7*3600)
	first := time.Date(2026, time.August, 22, 8, 34, 0, 0, zone).Unix()
	second := time.Date(2026, time.August, 23, 21, 1, 0, 0, zone).Unix()

	want := []model.Block{
		{Seq: 0, TS: first, Kind: model.KindUser, Body: "hello cursor"},
		{Seq: 1, TS: first, Kind: model.KindAssistant, Body: "hi"},
		{Seq: 2, TS: first, Kind: model.KindToolUse, ToolName: "Read", Body: `{"path":"/tmp/a"}`},
		{Seq: 3, TS: second, Kind: model.KindUser, Body: "second"},
		{Seq: 4, TS: second, Kind: model.KindAssistant, Body: "ack"},
	}
	if len(got) != len(want) {
		t.Fatalf("块数 = %d，想要 %d：%+v", len(got), len(want), got)
	}
	for i := range want {
		g := got[i]
		w := want[i]
		if g.Seq != w.Seq || g.TS != w.TS || g.Kind != w.Kind || g.Body != w.Body || g.ToolName != w.ToolName || g.ToolUseID != "" {
			t.Errorf("块 %d = %+v，想要 %+v（ToolUseID 应为空）", i, g, w)
		}
	}
	for _, b := range got {
		if strings.Contains(b.Body, "user_query") || strings.Contains(b.Body, "timestamp") || strings.Contains(b.Body, "catalog") || strings.Contains(b.Body, "secret-thought") {
			t.Errorf("不该进索引的文本出现了：%q", b.Body)
		}
		if b.Kind == model.KindToolResult {
			t.Errorf("jsonl 没有 tool_result，却产出了 %+v", b)
		}
	}
	if cur.Skipped != 1 {
		t.Errorf("Skipped = %d，想要 1（只有 not-json 那一行；turn_ended 与未知 content 类型不计）", cur.Skipped)
	}
}

func TestCursorStampOffsets(t *testing.T) {
	zone7 := time.FixedZone("UTC+7", 7*3600)
	want7 := time.Date(2026, time.August, 22, 8, 34, 0, 0, zone7).Unix()
	for _, s := range []string{
		"Saturday, Aug 22, 2026, 8:34 AM (UTC+7)",
		"Saturday, Aug 22, 2026, 8:34 AM (UTC+07)",
		"Saturday, Aug 22, 2026, 8:34 AM (UTC+07:00)",
	} {
		if got := parseCursorStamp(s); got != want7 {
			t.Errorf("%q → %d，想要 %d", s, got, want7)
		}
	}
	want5 := time.Date(2026, time.August, 22, 8, 34, 0, 0, time.FixedZone("UTC-5", -5*3600)).Unix()
	if got := parseCursorStamp("Saturday, Aug 22, 2026, 8:34 AM (UTC-5)"); got != want5 {
		t.Errorf("UTC-5 → %d，想要 %d", got, want5)
	}
	want530 := time.Date(2026, time.August, 22, 8, 34, 0, 0, time.FixedZone("UTC-05:30", -(5*3600+30*60))).Unix()
	if got := parseCursorStamp("Saturday, Aug 22, 2026, 8:34 AM (UTC-05:30)"); got != want530 {
		t.Errorf("UTC-05:30 → %d，想要 %d", got, want530)
	}
	for _, s := range []string{
		"Saturday, Aug 22, 2026, 8:34 AM",
		"not a date (UTC+7)",
		"Saturday, Aug 22, 2026, 8:34 AM (UTC+15)",
	} {
		if got := parseCursorStamp(s); got != 0 {
			t.Errorf("%q → %d，解析不了应当是 0", s, got)
		}
	}
}

func TestCursorSubagentParentIsTheDirectory(t *testing.T) {
	home := t.TempDir()
	p := CursorCLI{Home: home}
	path := filepath.Join(home, ".cursor", "projects", "no-such-slug-zzzz", "agent-transcripts", cursorParent, "subagents", cursorChild+".jsonl")
	cursorWrite(t, path, `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>child</user_query>"}]}}`)
	m, err := p.Meta(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != model.SourceCursor || m.SessionUID != cursorChild || m.ParentUID != cursorParent || m.AgentLabel != "" {
		t.Errorf("meta = %+v，想要 source=cursor uid=%s parent=%s label 空", m, cursorChild, cursorParent)
	}
}

func TestCursorMetaPrefersSideFileOverSlug(t *testing.T) {
	home := t.TempDir()
	base := cursorRoundTripBase(t)
	proj := filepath.Join(base, "only")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	slug := cursorTestSlug(proj)
	p := CursorCLI{Home: home}
	path := cursorTranscript(home, slug, cursorUID)
	cursorWrite(t, path, `{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Saturday, Aug 22, 2026, 8:34 AM (UTC+7)</timestamp><user_query>q</user_query>"}]}}`)

	sideDir := filepath.Join(home, ".cursor", "chats", "abc", cursorUID)
	if err := os.MkdirAll(sideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"schemaVersion":1,"createdAtMs":1792002494397,"updatedAtMs":1792002500000,"title":"Cursor CLI Setup","cwd":"/from-meta","hasConversation":true}`
	if err := os.WriteFile(filepath.Join(sideDir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := p.Meta(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.ProjectPath != "/from-meta" {
		t.Errorf("ProjectPath = %q，想要 /from-meta —— slug 能反解到 %s，也不该盖过 meta.json 的 cwd", m.ProjectPath, proj)
	}
	if m.StartedAt != 1792002494397/1000 || m.EndedAt != 1792002500000/1000 {
		t.Errorf("时间 = %d/%d，想要 createdAtMs、updatedAtMs 除以 1000", m.StartedAt, m.EndedAt)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cur, err := p.Parse(f, path, Cursor{}, func(model.Block) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if cur.Title != "Cursor CLI Setup" {
		t.Errorf("Title = %q，想要 meta.json 的 title", cur.Title)
	}
}

func TestCursorMissingMetaLeavesUnknownSlugEmpty(t *testing.T) {
	home := t.TempDir()
	p := CursorCLI{Home: home}
	path := cursorTranscript(home, "no-such-slug-zzzz", cursorUID)
	cursorWrite(t, path, `{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Saturday, Aug 22, 2026, 8:34 AM (UTC+7)</timestamp><user_query>q</user_query>"}]}}`)
	m, err := p.Meta(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.ProjectPath != "" {
		t.Errorf("ProjectPath = %q，slug 对不上任何目录时应当留空", m.ProjectPath)
	}
	want := time.Date(2026, time.August, 22, 8, 34, 0, 0, time.FixedZone("UTC+7", 7*3600)).Unix()
	if m.StartedAt != want {
		t.Errorf("StartedAt = %d，没有 meta.json 时应当用正文里的第一枚时间戳 %d", m.StartedAt, want)
	}
}

func TestCursorSlugUsesTheOnlyExistingDirectory(t *testing.T) {
	base := cursorRoundTripBase(t)
	slash := filepath.Join(base, "foo", "bar")
	hyphen := filepath.Join(base, "foo-bar")
	if err := os.MkdirAll(slash, 0o755); err != nil {
		t.Fatal(err)
	}
	slug := cursorTestSlug(slash)
	if cursorTestSlug(hyphen) != slug {
		t.Fatalf("两条路径的 slug 不一样：%q vs %q —— 这个测试不再有鉴别力", cursorTestSlug(slash), cursorTestSlug(hyphen))
	}
	if got := cursorProjectFromSlug(slug); got != slash {
		t.Errorf("只存在斜杠路径时 = %q，想要 %s", got, slash)
	}
	if err := os.MkdirAll(hyphen, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := cursorProjectFromSlug(slug); got != "" {
		t.Errorf("斜杠路径和连字符路径都在时 = %q，应当留空", got)
	}
	if err := os.RemoveAll(slash); err != nil {
		t.Fatal(err)
	}
	if got := cursorProjectFromSlug(slug); got != hyphen {
		t.Errorf("只存在连字符路径时 = %q，想要 %s", got, hyphen)
	}
}

func TestCursorSlugWithTooManyHyphensStaysEmpty(t *testing.T) {
	slug := strings.Repeat("a-", cursorSlugHyphenCap+1) + "z"
	if got := cursorProjectFromSlug(slug); got != "" {
		t.Fatalf("连字符超过上限仍返回了 %q", got)
	}
}

// cursorRoundTripBase 要一个不含 `-` 和 `.` 的临时目录。
// slug 把这两个字符都折成 `-`，父路径里本来就有的话，反解对不上我们造的目录。
func cursorRoundTripBase(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	if strings.ContainsAny(base, "-.") {
		t.Skipf("临时目录 %s 含 - 或 .，slug 无法往返", base)
	}
	return base
}

func cursorTestSlug(p string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(strings.TrimPrefix(p, "/"))
}
