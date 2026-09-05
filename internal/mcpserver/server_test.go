package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/mcpserver"
	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newFixture(t *testing.T) (*search.Engine, int64) {
	t.Helper()
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "u1", FilePath: "/sessions/u1.jsonl",
		ProjectPath: "/proj/alpha", StartedAt: 1000, EndedAt: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks := []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "做一个增量备份工具"},
		{Seq: 1, TS: 1001, Kind: model.KindToolUse, ToolName: "Bash", Body: `{"command":"rsync -av /a /b"}`},
		{Seq: 2, TS: 1002, Kind: model.KindAssistant, Body: strings.Repeat("很长的回答内容。", 400)},
	}
	if err := st.AppendBlocks(id, blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	return search.NewEngine(st.DB()), id
}

func connect(t *testing.T, engine *search.Engine) *mcp.ClientSession {
	t.Helper()
	mux := http.NewServeMux()
	mcpserver.Register(mux, engine)
	// dashboard 的 catch-all 也挂上：验证 /mcp 不会被它抢走
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := c.Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func callJSON(t *testing.T, sess *mcp.ClientSession, name string, args any, out any) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("调用 %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s 返回错误: %+v", name, res.Content)
	}
	// 结构化结果优先；退回文本内容
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("解析 %s 的结构化结果: %v", name, err)
		}
		return
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if err := json.Unmarshal([]byte(tc.Text), out); err != nil {
				t.Fatalf("解析 %s 的文本结果: %v", name, err)
			}
			return
		}
	}
	t.Fatalf("%s 没有可解析的返回", name)
}

func TestToolsAreListed(t *testing.T) {
	engine, _ := newFixture(t)
	sess := connect(t, engine)

	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"search_sessions", "get_session", "list_projects"} {
		if !got[want] {
			t.Errorf("工具 %s 未注册，实得 %v", want, got)
		}
	}
}

// 结构化结果：agent 拿到的必须是可直接用的字段，而非需要二次解析的原始文本（需求 4.1）。
func TestSearchReturnsStructuredResult(t *testing.T) {
	engine, _ := newFixture(t)
	sess := connect(t, engine)

	var out mcpserver.SearchOutput
	callJSON(t, sess, "search_sessions", mcpserver.SearchArgs{Query: "增量备份"}, &out)
	if len(out.Sessions) != 1 {
		t.Fatalf("命中 %d 条", len(out.Sessions))
	}
	s := out.Sessions[0]
	if s.FilePath != "/sessions/u1.jsonl" || s.ProjectPath != "/proj/alpha" || s.SessionID == 0 {
		t.Errorf("结果字段不全: %+v", s)
	}
	if s.Snippet == "" {
		t.Error("缺片段")
	}

	var none mcpserver.SearchOutput
	callJSON(t, sess, "search_sessions", mcpserver.SearchArgs{Query: "根本不存在xyzzy"}, &none)
	if !none.NoMatch || none.Note == "" {
		t.Errorf("无命中未明确说明: %+v", none)
	}
}

// 需求 4.3：超预算必须截断并**显式标注**，不得静默截断。
func TestGetSessionTruncationIsExplicit(t *testing.T) {
	engine, id := newFixture(t)
	sess := connect(t, engine)

	var out mcpserver.SessionOutput
	callJSON(t, sess, "get_session", mcpserver.GetSessionArgs{SessionID: id}, &out)
	if out.Total != 3 {
		t.Errorf("Total = %d", out.Total)
	}
	var long mcpserver.MessageBrief
	for _, m := range out.Messages {
		if m.Seq == 2 {
			long = m
		}
	}
	if !strings.Contains(long.Body, "已截断") {
		t.Errorf("超长消息未标注截断: %q…", long.Body[:60])
	}
	if out.FilePath == "" {
		t.Error("未给出原始文件绝对路径")
	}
}

// 需求 4.4：MCP 与 dashboard 必须复用同一索引与同一查询接口。
func TestMCPAndEngineAgree(t *testing.T) {
	engine, _ := newFixture(t)
	sess := connect(t, engine)

	var viaMCP mcpserver.SearchOutput
	callJSON(t, sess, "search_sessions", mcpserver.SearchArgs{Query: "rsync"}, &viaMCP)

	viaEngine, err := engine.SearchSessions(search.Query{Text: "rsync", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(viaMCP.Sessions) != len(viaEngine.Sessions) {
		t.Fatalf("结果条数不一致：MCP %d vs Engine %d", len(viaMCP.Sessions), len(viaEngine.Sessions))
	}
	for i := range viaMCP.Sessions {
		if viaMCP.Sessions[i].SessionID != viaEngine.Sessions[i].ID {
			t.Errorf("第 %d 条会话不一致：%d vs %d", i, viaMCP.Sessions[i].SessionID, viaEngine.Sessions[i].ID)
		}
	}
}

func TestListProjects(t *testing.T) {
	engine, _ := newFixture(t)
	sess := connect(t, engine)

	var out mcpserver.ProjectsOutput
	callJSON(t, sess, "list_projects", struct{}{}, &out)
	if len(out.Projects) != 1 || out.Projects[0].ProjectPath != "/proj/alpha" {
		t.Errorf("项目列表 = %+v", out.Projects)
	}
}

// 给 agent 的文本必须是纯文本：高亮哨兵是 dashboard 的渲染细节，
// 实测本地模型会把 \x02 \x03 原样抄进答案里给用户看。
func TestAgentFacingTextHasNoControlMarkers(t *testing.T) {
	engine, id := newFixture(t)
	sess := connect(t, engine)

	hasCtrl := func(s string) bool {
		return strings.ContainsAny(s, "\x01\x02\x03")
	}

	var out mcpserver.SearchOutput
	callJSON(t, sess, "search_sessions", mcpserver.SearchArgs{Query: "增量备份"}, &out)
	for _, s := range out.Sessions {
		if hasCtrl(s.Snippet) {
			t.Errorf("片段含内部控制标记: %q", s.Snippet)
		}
	}

	var v mcpserver.SessionOutput
	callJSON(t, sess, "get_session", mcpserver.GetSessionArgs{SessionID: id}, &v)
	for _, m := range v.Messages {
		if hasCtrl(m.Body) {
			t.Errorf("消息正文含内部控制标记: %q", m.Body[:min(60, len(m.Body))])
		}
	}
}

// last_seq 只在「best 之后还有命中」时出现——相等就省略，别给零信息量的字段。
// 两个分支都要覆盖：省略那半靠不存在来表达，最容易在重构里悄悄失效。
func TestLastSeqOnlyWhenThereIsMoreAfter(t *testing.T) {
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "u2", FilePath: "/sessions/u2.jsonl",
		ProjectPath: "/proj/beta", StartedAt: 1000, EndedAt: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendBlocks(id, []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "住宅 住宅 住宅 出口判定为住宅"},
		{Seq: 1, TS: 1001, Kind: model.KindAssistant, Body: "先按这个写"},
		{Seq: 2, TS: 1002, Kind: model.KindToolResult, Body: "订正：住宅属误判，实测机房"},
		{Seq: 3, TS: 1003, Kind: model.KindAssistant, Body: "另说一件无关的事情"},
	}, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	sess := connect(t, search.NewEngine(st.DB()))

	var two mcpserver.SearchOutput
	callJSON(t, sess, "search_sessions", mcpserver.SearchArgs{Query: "住宅"}, &two)
	if len(two.Sessions) != 1 {
		t.Fatalf("应命中 1 个会话，实得 %d", len(two.Sessions))
	}
	// 🔴 这条守的是一个已经踩过的回归：last_seq 若在主聚合里写成 MAX(b.seq)，
	// 就会破坏 SQLite「bare column 取自 min/max 那一行」的前提（该保证要求全查询
	// 恰好一个 min/max），于是 best_seq 会被悄悄换成 max(seq) 那一行。
	// 实测那次 best 从 0 变成 2，而全量测试仍然退 0——所以必须显式断言 best_seq。
	if two.Sessions[0].BestSeq != 0 {
		t.Errorf("最相关的是词频更高的首块（seq=0），实得 best_seq=%d —— "+
			"检查 last_seq 是否又被写回主聚合的 MAX()", two.Sessions[0].BestSeq)
	}
	if two.Sessions[0].LastSeq != 2 {
		t.Errorf("后面还有命中时应给出 last_seq=2，实得 %d", two.Sessions[0].LastSeq)
	}

	var one mcpserver.SearchOutput
	callJSON(t, sess, "search_sessions", mcpserver.SearchArgs{Query: "无关"}, &one)
	if len(one.Sessions) != 1 {
		t.Fatalf("应命中 1 个会话，实得 %d", len(one.Sessions))
	}
	if one.Sessions[0].LastSeq != 0 {
		t.Errorf("只有一处命中时应省略 last_seq，实得 %d", one.Sessions[0].LastSeq)
	}
}
