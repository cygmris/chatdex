package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cygmris/chatdex/internal/httpapi"
	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/search"
)

func newServer(t *testing.T) (*httptest.Server, *index.Store) {
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
	}
	if err := st.AppendBlocks(id, blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	(&httpapi.Server{Engine: search.NewEngine(st.DB()), Store: st}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("解析 %s 的响应: %v", url, err)
		}
	}
	return resp.StatusCode
}

func TestSearchEndpoint(t *testing.T) {
	srv, _ := newServer(t)

	var res search.Result
	if code := getJSON(t, srv.URL+"/api/search?q=增量备份", &res); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("命中 %d 条", len(res.Sessions))
	}
	if res.Sessions[0].FilePath != "/sessions/u1.jsonl" {
		t.Errorf("未返回原始文件绝对路径: %+v", res.Sessions[0])
	}

	// 过滤条件经 URL 参数传入
	var filtered search.Result
	getJSON(t, srv.URL+"/api/search?q=rsync&tool=Bash&kind=tool_use&source=claude&project=/proj/alpha", &filtered)
	if len(filtered.Sessions) != 1 {
		t.Errorf("组合过滤命中 %d 条", len(filtered.Sessions))
	}

	// 无命中要明确说明，不能拿近似结果冒充
	var empty search.Result
	getJSON(t, srv.URL+"/api/search?q=完全不存在的词xyzzy", &empty)
	if !empty.NoMatch {
		t.Error("无命中未置 no_match")
	}
}

func TestSessionEndpoint(t *testing.T) {
	srv, _ := newServer(t)

	var view search.SessionView
	if code := getJSON(t, srv.URL+"/api/session/1", &view); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if view.Total != 2 || len(view.Messages) != 2 {
		t.Errorf("回读 = %+v", view)
	}
	if view.Messages[0].Seq != 0 || view.Messages[1].Seq != 1 {
		t.Error("消息未按 seq 正序")
	}

	if code := getJSON(t, srv.URL+"/api/session/9999", nil); code != 404 {
		t.Errorf("不存在的会话状态码 = %d, want 404", code)
	}
	if code := getJSON(t, srv.URL+"/api/session/abc", nil); code != 400 {
		t.Errorf("非法 id 状态码 = %d, want 400", code)
	}
}

func TestHealthEndpoint(t *testing.T) {
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	mux := http.NewServeMux()
	(&httpapi.Server{
		Engine:    search.NewEngine(st.DB()),
		Store:     st,
		Version:   "test-ver",
		Commit:    "abc123",
		UIPort:    5021,
		APIPort:   5022,
		IndexPath: "index.db",
	}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var h struct {
		OK      bool           `json:"ok"`
		Status  string         `json:"status"`
		Version string         `json:"version"`
		Commit  string         `json:"commit"`
		Ports   map[string]int `json:"ports"`
		Index   struct {
			OK bool `json:"ok"`
		} `json:"index"`
	}
	if code := getJSON(t, srv.URL+"/api/health", &h); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if !h.OK || h.Status != "ok" || h.Version != "test-ver" || h.Commit != "abc123" {
		t.Errorf("health = %+v", h)
	}
	if h.Ports["ui"] != 5021 || h.Ports["api"] != 5022 {
		t.Errorf("ports = %v", h.Ports)
	}
	if !h.Index.OK {
		t.Error("索引应可打开")
	}
}

func TestStatsAndProjectsEndpoints(t *testing.T) {
	srv, _ := newServer(t)

	var st index.Stats
	if code := getJSON(t, srv.URL+"/api/stats", &st); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if st.Sessions != 1 || st.Blocks != 2 || st.DBBytes == 0 {
		t.Errorf("Stats = %+v", st)
	}

	var ps []search.ProjectStat
	getJSON(t, srv.URL+"/api/projects", &ps)
	if len(ps) != 1 || ps[0].ProjectPath != "/proj/alpha" {
		t.Errorf("Projects = %+v", ps)
	}
}

// 摘要进度取不到时必须下发 null，不能下发零值。
//
// 零值会被前端算成 done=0/total=0 → pending=0 → 进度条直接隐藏，
// 看起来像「摘要都跑完了」。这类错误没有任何其它信号：接口 200、
// 字段齐全、日志干净，只有结论是反的。
//
// 这里断言的是**契约**（字段可空、正常时非空），失败注入在前端那侧验。
func TestStatsSummaryIsNullable(t *testing.T) {
	srv, _ := newServer(t)

	var raw map[string]any
	if code := getJSON(t, srv.URL+"/api/stats", &raw); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	v, ok := raw["summary"]
	if !ok {
		t.Fatal("/api/stats 没有 summary 字段")
	}
	// 正常路径下必须是对象而不是 null——否则这个字段永远是 null，
	// 「可空」就成了掩盖问题的借口
	if v == nil {
		t.Error("正常情况下 summary 不该是 null")
	}
	if _, isObj := v.(map[string]any); !isObj {
		t.Errorf("summary 应当是对象，实际 %T", v)
	}
}

// 回读页按类型过滤 + 用 seq 定位：fixture 是 seq0 user、seq1 tool_use。
func TestSessionEndpointFiltersKindsAndLocatesSeq(t *testing.T) {
	srv, _ := newServer(t)

	var view search.SessionView
	if code := getJSON(t, srv.URL+"/api/session/1?kinds=user&seq=1", &view); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if len(view.Messages) != 1 || view.Messages[0].Kind != "user" {
		t.Errorf("kinds=user 应只剩那条 user，实际 %+v", view.Messages)
	}
	if view.Total != 1 || view.TotalAll != 2 {
		t.Errorf("Total=%d TotalAll=%d, want 1 / 2", view.Total, view.TotalAll)
	}
	// seq=1 那条是 tool_use，被隐藏了：页面要据此提示「命中的那条被隐藏」
	if view.TargetKind != "tool_use" {
		t.Errorf("TargetKind = %q, want tool_use", view.TargetKind)
	}
	if view.KindCounts["tool_use"] != 1 || view.KindCounts["user"] != 1 {
		t.Errorf("KindCounts = %v", view.KindCounts)
	}
}

// /api/tools 给过滤栏的工具下拉当选项：按块数降序、同数按名，并缓存 10 分钟——
// 全库聚合实测 0.68 s（2.36M 块，blocks 没有 tool_name 索引），每次打开下拉都算一次不可接受。
func TestToolsEndpointSortsAndCaches(t *testing.T) {
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	seed := func(uid string, blocks []model.Block) {
		t.Helper()
		id, err := st.UpsertSession(model.SessionMeta{
			Source: model.SourceClaude, SessionUID: uid, FilePath: "/sessions/" + uid + ".jsonl",
			ProjectPath: "/proj/alpha", StartedAt: 1000, EndedAt: 2000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.AppendBlocks(id, blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	seed("t1", []model.Block{{Seq: 0, Kind: model.KindToolUse, ToolName: "Bash", Body: "{}"}})
	seed("t2", []model.Block{
		{Seq: 0, Kind: model.KindToolUse, ToolName: "Edit", Body: "{}"},
		{Seq: 1, Kind: model.KindToolUse, ToolName: "Edit", Body: "{}"},
	})

	now := time.Unix(1_700_000_000, 0)
	mux := http.NewServeMux()
	(&httpapi.Server{Engine: search.NewEngine(st.DB()), Store: st, Now: func() time.Time { return now }}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var got []search.ToolStat
	if code := getJSON(t, srv.URL+"/api/tools", &got); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	want := []search.ToolStat{{ToolName: "Edit", Blocks: 2}, {ToolName: "Bash", Blocks: 1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tools = %v, want %v（块数降序）", got, want)
	}

	// 库里多了一个工具：10 分钟内仍回旧列表（缓存），过期后才看到它
	seed("t3", []model.Block{
		{Seq: 0, Kind: model.KindToolUse, ToolName: "Grep", Body: "{}"},
		{Seq: 1, Kind: model.KindToolUse, ToolName: "Grep", Body: "{}"},
		{Seq: 2, Kind: model.KindToolUse, ToolName: "Grep", Body: "{}"},
	})
	now = now.Add(9 * time.Minute)
	getJSON(t, srv.URL+"/api/tools", &got)
	if len(got) != 2 {
		t.Errorf("9 分钟内应仍是缓存的 2 项，实际 %v", got)
	}
	now = now.Add(2 * time.Minute)
	getJSON(t, srv.URL+"/api/tools", &got)
	if len(got) != 3 || got[0].ToolName != "Grep" {
		t.Errorf("11 分钟后应重算并以 Grep(3) 居首，实际 %v", got)
	}
}
