package httpapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cygmris/chatdex/internal/backup"
	"github.com/cygmris/chatdex/internal/httpapi"
	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/model"
	"github.com/cygmris/chatdex/internal/parser"
	"github.com/cygmris/chatdex/internal/search"
)

// fakeBackup 只实现取回这一路，其余方法给零值——本组测试要验的是
// handler 怎么处理取回的结果，不是 restic 本身（那在 internal/backup 里用真
// restic 测过了）。
type fakeBackup struct {
	content  string
	err      error
	lastAuto *backup.AutoResult
}

func (f fakeBackup) Available(context.Context) backup.Status {
	return backup.Status{Available: true}
}
func (f fakeBackup) Backup(context.Context) (backup.Result, error) { return backup.Result{}, nil }
func (f fakeBackup) Snapshots(context.Context) ([]backup.Snapshot, error) {
	return nil, nil
}
func (f fakeBackup) Coverage(context.Context, []backup.IndexedSession) (backup.Coverage, error) {
	return backup.Coverage{}, nil
}
func (f fakeBackup) Init(context.Context) error   { return nil }
func (f fakeBackup) LastAuto() *backup.AutoResult { return f.lastAuto }
func (f fakeBackup) Fetch(context.Context, string) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.content)), nil
}

// 索引里存的是被截断的工具结果，备份里的原件是完整的。
const (
	archivedFull = `{"type":"user","timestamp":"2026-01-01T00:00:00Z","sessionId":"u1","cwd":"/proj/alpha","message":{"role":"user","content":"做一个增量备份工具"}}
{"type":"user","timestamp":"2026-01-01T00:00:01Z","sessionId":"u1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"完整的工具结果——索引里这段被截断了，只有备份里的原件才有"}]}}
`
	indexedTail = "完整的工具结果——索引里这"
)

// 建一个带备份能力的服务：索引里那条工具结果是**截断**过的，
// 这样才能证明取回的内容确实来自备份、而不是又从索引读了一遍。
func newArchivedServer(t *testing.T, b httpapi.Backuper) *httptest.Server {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "projects", "-proj-alpha", "u1.jsonl")

	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := st.UpsertSession(model.SessionMeta{
		Source: model.SourceClaude, SessionUID: "u1", FilePath: path,
		ProjectPath: "/proj/alpha", StartedAt: 1000, EndedAt: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks := []model.Block{
		{Seq: 0, TS: 1000, Kind: model.KindUser, Body: "做一个增量备份工具"},
		{Seq: 1, TS: 1001, Kind: model.KindToolResult, ToolUseID: "t1",
			Truncated: true, RawBytes: 999, Body: indexedTail},
	}
	if err := st.AppendBlocks(id, blocks, index.Watermark{Size: 1, MTime: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	(&httpapi.Server{
		Engine: search.NewEngine(st.DB()), Store: st, Backup: b,
		Reg: parser.NewRegistry(parser.Claude{Home: home}),
	}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// 取回的必须是**备份里的原件**，不是索引里那份有损的副本。
//
// 这条是整个功能的意义所在：索引对工具结果故意截断（实测 706362 个块里
// 43637 个被截断），所以「已消失的会话能显示」光靠索引就成立——从备份读
// 唯一多出来的东西，就是这段被截掉的内容。断言不比对正文的话，一个直接
// 转发索引结果的实现也能全绿。
func TestArchivedReturnsOriginalNotTheTruncatedIndexCopy(t *testing.T) {
	srv := newArchivedServer(t, fakeBackup{content: archivedFull})

	var view search.SessionView
	if code := getJSON(t, srv.URL+"/api/session/1/archived", &view); code != 200 {
		t.Fatalf("状态码 = %d, want 200", code)
	}
	if len(view.Messages) != 2 {
		t.Fatalf("消息条数 = %d, want 2", len(view.Messages))
	}
	last := view.Messages[len(view.Messages)-1]
	if !strings.Contains(last.Body, "只有备份里的原件才有") {
		t.Errorf("拿到的还是索引里那份截断副本：%q", last.Body)
	}
	if last.Truncated {
		t.Error("从备份取回的原件不该标记为已截断")
	}

	// 对照：同一个会话走普通回读，拿到的应当是截断过的那份。
	// 没有这个对照，上面的断言无法排除「索引本来就没截断」这种可能。
	var live search.SessionView
	if code := getJSON(t, srv.URL+"/api/session/1", &live); code != 200 {
		t.Fatalf("普通回读状态码 = %d", code)
	}
	lastLive := live.Messages[len(live.Messages)-1]
	if !lastLive.Truncated || strings.Contains(lastLive.Body, "只有备份里的原件才有") {
		t.Fatalf("对照组失效：索引里那条本应是截断的，实得 truncated=%v body=%q",
			lastLive.Truncated, lastLive.Body)
	}
}

// 备份里也没有时要明确告知，不得显示空白或假装还在（需求 4.4）。
func TestArchivedSaysSoWhenNotInBackup(t *testing.T) {
	srv := newArchivedServer(t, fakeBackup{err: backup.ErrNotInBackup})

	resp, err := http.Get(srv.URL + "/api/session/1/archived")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("状态码 = %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	// 光断言 404 不够：会话不存在也是 404。要能看出说的是「备份里没有」。
	if !strings.Contains(string(body), "备份") {
		t.Errorf("没说清是「备份里也没有」：%s", body)
	}
}

// restic 挂了与「备份里没有」必须分开：前者是备份本身有问题（去修备份），
// 后者是这个会话真的没了（别再等了）。都报 404 会让人一直找不存在的问题。
func TestArchivedDistinguishesBrokenBackupFromMissingFile(t *testing.T) {
	srv := newArchivedServer(t, fakeBackup{err: errors.New("仓库打不开")})

	resp, err := http.Get(srv.URL + "/api/session/1/archived")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Error("restic 出错被当成了「备份里没有」——用户会去找一个不存在的问题")
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("状态码 = %d, want 500", resp.StatusCode)
	}
}

// 备份没启用时降级，不是 500 —— restic 是可选依赖（与本地 LLM 同一模式）。
func TestArchivedDegradesWhenBackupDisabled(t *testing.T) {
	srv := newArchivedServer(t, nil)

	resp, err := http.Get(srv.URL + "/api/session/1/archived")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("状态码 = %d, want 503", resp.StatusCode)
	}
}

// 自动备份失败必须能被界面看见。
//
// 需求 5.3：备份失败 SHALL 明确显示原因，**SHALL NOT 只记日志**。
// 手动那条路靠 HTTP 500 把原因带回去；自动那条路没人看着，只写 slog 的话，
// 一个每半小时失败一次的备份除了 journalctl 里没人看得见。
func TestAutoBackupFailureIsVisibleInStatus(t *testing.T) {
	srv := newArchivedServer(t, fakeBackup{
		lastAuto: &backup.AutoResult{Error: "仓库锁没释放"},
	})
	var got struct {
		LastAuto *backup.AutoResult `json:"last_auto"`
	}
	if code := getJSON(t, srv.URL+"/api/backup/status", &got); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if got.LastAuto == nil || got.LastAuto.Error != "仓库锁没释放" {
		t.Errorf("自动备份的失败原因没带到界面上：%+v", got.LastAuto)
	}
}
