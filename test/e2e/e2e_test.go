// Package e2e 起一个真实的 chatdex 进程，把用户会走的路径从头到尾走一遍。
//
// 与各包的单测互补：这里验证的是「装在一起还成不成立」——
// 路由冲突、双端口一致性、只读铁律、LLM 缺席时的降级，都只有整体跑起来才看得出。
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type env struct {
	uiPort, apiPort int
	home            string
	dbPath          string
	claudeFile      string
}

// start 构建并启动真实二进制，返回环境信息。
func start(t *testing.T) env {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "chatdex")

	build := exec.Command("go", "build", "-o", bin, "github.com/cygmris/chatdex/cmd/chatdex")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("构建失败: %v", err)
	}

	home := filepath.Join(dir, "home")
	projDir := filepath.Join(home, ".claude/projects/-home-demo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeFile := filepath.Join(projDir, "sess.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","timestamp":"2026-07-28T10:00:00.000Z","cwd":"/home/demo","sessionId":"s1","message":{"role":"user","content":"<system-reminder>注入的 CLAUDE.md</system-reminder>做一个类似 TimeMachine 的增量备份工具"}}`,
		`{"type":"assistant","timestamp":"2026-07-28T10:00:05.000Z","cwd":"/home/demo","sessionId":"s1","message":{"role":"assistant","content":[{"type":"text","text":"先确认 restic 的快照仓库能不能增量"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"rsync -av /src /dst"}}]}}`,
		`{"type":"user","timestamp":"2026-07-28T10:00:06.000Z","cwd":"/home/demo","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"sent 1024 bytes"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(claudeFile, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	e := env{uiPort: freePort(t), apiPort: freePort(t), home: home,
		dbPath: filepath.Join(dir, "index.db"), claudeFile: claudeFile}

	cfgPath := filepath.Join(dir, "config.json")
	// LLM 指向一个没有服务的回环端口：验证「本地 LLM 是可选依赖」这条
	cfg := fmt.Sprintf(`{"home":%q,"db_path":%q,"ports":{"ui":%d,"api":%d},
	  "scan":{"interval_sec":1},"llm":{"endpoint":"http://127.0.0.1:11499"}}`,
		home, e.dbPath, e.uiPort, e.apiPort)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "serve", "-config", cfgPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	waitUntil(t, 20*time.Second, func() bool {
		var st struct {
			Blocks int `json:"blocks"`
		}
		return getJSON(e.apiPort, "/api/stats", &st) == 200 && st.Blocks > 0
	}, "服务未在 20s 内索引到内容")
	return e
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(msg)
}

func getJSON(port int, path string, out any) int {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// 用户主路径：搜索 → 拿到片段与跳转锚点 → 回读会话 → 看到原始文件绝对路径。
func TestSearchThenRead(t *testing.T) {
	e := start(t)

	var res struct {
		Sessions []struct {
			ID       int64  `json:"id"`
			FilePath string `json:"file_path"`
			Snippet  string `json:"snippet"`
			BestSeq  int    `json:"best_seq"`
		} `json:"sessions"`
		NoMatch bool `json:"no_match"`
	}
	if code := getJSON(e.apiPort, "/api/search?q="+url.QueryEscape("增量备份"), &res); code != 200 {
		t.Fatalf("检索状态码 = %d", code)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("命中 %d 条，want 1", len(res.Sessions))
	}
	hit := res.Sessions[0]
	if hit.FilePath != e.claudeFile {
		t.Errorf("原始文件路径 = %q, want %q", hit.FilePath, e.claudeFile)
	}
	if hit.Snippet == "" {
		t.Error("缺命中片段")
	}

	var view struct {
		Total    int `json:"total"`
		Messages []struct {
			Seq  int    `json:"seq"`
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"messages"`
	}
	if code := getJSON(e.apiPort, fmt.Sprintf("/api/session/%d", hit.ID), &view); code != 200 {
		t.Fatalf("回读状态码 = %d", code)
	}
	if view.Total != 4 { // user + assistant + tool_use + tool_result
		t.Errorf("消息数 = %d, want 4", view.Total)
	}
	// 注入的 CLAUDE.md 被剥离，用户真正说的话保留
	if !strings.Contains(view.Messages[0].Body, "TimeMachine") {
		t.Errorf("首条消息 = %q", view.Messages[0].Body)
	}
	if strings.Contains(view.Messages[0].Body, "注入的") {
		t.Error("注入内容未被剥离")
	}
}

// 时间线与检索共用过滤条件。
func TestTimeline(t *testing.T) {
	e := start(t)

	var gs []struct {
		ProjectPath string `json:"project_path"`
		Total       int    `json:"total"`
		Sessions    []struct {
			Label string `json:"label"`
		} `json:"sessions"`
	}
	if code := getJSON(e.apiPort, "/api/timeline", &gs); code != 200 {
		t.Fatalf("时间线状态码 = %d", code)
	}
	if len(gs) != 1 || gs[0].ProjectPath != "/home/demo" {
		t.Fatalf("时间线 = %+v", gs)
	}
	// 没有摘要（LLM 不可用）时退回首条用户消息
	if !strings.Contains(gs[0].Sessions[0].Label, "TimeMachine") {
		t.Errorf("辨识文字 = %q", gs[0].Sessions[0].Label)
	}

	// 过滤：来源不匹配则为空
	var none []any
	getJSON(e.apiPort, "/api/timeline?source=codex", &none)
	if len(none) != 0 {
		t.Errorf("source=codex 应无结果: %+v", none)
	}
}

// MCP 端点可用，且与 HTTP API 走同一份索引。
func TestMCPEndpoint(t *testing.T) {
	e := start(t)

	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	sess, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d/mcp", e.apiPort)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	tools, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 3 {
		t.Errorf("工具数 = %d, want 3", len(tools.Tools))
	}

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_sessions", Arguments: map[string]any{"query": "rsync"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var out struct {
		Sessions []struct {
			FilePath string `json:"file_path"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].FilePath != e.claudeFile {
		t.Errorf("MCP 检索结果 = %+v", out.Sessions)
	}
	// 给 agent 的文本不得含内部控制标记
	if strings.ContainsAny(string(b), "\x01\x02\x03") {
		t.Error("MCP 返回含内部控制标记")
	}
}

// 需求 10.6：本地 LLM 不可用时，仅聊天置灰，其余照常。
func TestChatDegradesGracefully(t *testing.T) {
	e := start(t)

	var st struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}
	if code := getJSON(e.apiPort, "/api/chat/status", &st); code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	if st.Available {
		t.Error("LLM 不可达时不该报可用")
	}
	if st.Reason == "" {
		t.Error("未说明不可用的原因")
	}

	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/chat", e.apiPort),
		"application/json", strings.NewReader(`{"question":"随便问"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("聊天状态码 = %d, want 503", resp.StatusCode)
	}

	// 检索必须照常
	var res struct {
		Sessions []any `json:"sessions"`
	}
	if code := getJSON(e.apiPort, "/api/search?q=rsync", &res); code != 200 || len(res.Sessions) != 1 {
		t.Errorf("LLM 缺席影响了检索: code=%d 命中=%d", code, len(res.Sessions))
	}
}

// 两个端口返回一致，且 dashboard 与 MCP 路由共存。
func TestBothPortsAndRoutes(t *testing.T) {
	e := start(t)

	get := func(port int, path string) (int, string) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	_, viaAPI := get(e.apiPort, "/api/search?q=rsync")
	_, viaUI := get(e.uiPort, "/api/search?q=rsync")
	if viaAPI != viaUI {
		t.Error("两个端口返回不一致")
	}
	if code, body := get(e.uiPort, "/"); code != 200 || !strings.Contains(body, "chatdex") {
		t.Errorf("dashboard 异常 code=%d", code)
	}
	if code, _ := get(e.apiPort, "/mcp"); code == 404 {
		t.Error("/mcp 被静态文件服务器抢走了")
	}
}

// 只读铁律：跑完整个流程，原始会话文件一字未动。
func TestSourceFilesUntouched(t *testing.T) {
	e := start(t)

	before, err := os.Stat(e.claudeFile)
	if err != nil {
		t.Fatal(err)
	}
	beforeContent, err := os.ReadFile(e.claudeFile)
	if err != nil {
		t.Fatal(err)
	}

	// 走一遍完整流程
	getJSON(e.apiPort, "/api/search?q=rsync", nil)
	getJSON(e.apiPort, "/api/session/1", nil)
	getJSON(e.apiPort, "/api/timeline", nil)
	getJSON(e.apiPort, "/api/stats", nil)
	time.Sleep(1500 * time.Millisecond) // 让后台扫描再跑一轮

	after, err := os.Stat(e.claudeFile)
	if err != nil {
		t.Fatal(err)
	}
	afterContent, err := os.ReadFile(e.claudeFile)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Error("原始会话文件的 size/mtime 被改动了")
	}
	if string(afterContent) != string(beforeContent) {
		t.Error("原始会话文件内容被改动了")
	}
}

// 安全：索引库权限必须是 0600（它是一份集中的凭证副本）。
func TestIndexPermissions(t *testing.T) {
	e := start(t)
	for _, p := range []string{e.dbPath, e.dbPath + "-wal", e.dbPath + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s 权限 = %o, want 600", filepath.Base(p), got)
		}
	}
}
