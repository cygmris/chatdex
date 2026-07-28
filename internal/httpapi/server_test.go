package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
