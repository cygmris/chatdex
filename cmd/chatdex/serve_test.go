package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 起一个真实的 serve 进程内实例，返回 UI / API 两个端口。
func startServe(t *testing.T) (uiPort, apiPort int, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude/projects/-home-demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","timestamp":"2026-07-28T10:00:00.000Z","cwd":"/home/demo","sessionId":"s1","message":{"role":"user","content":"部署时用了 rsync 同步目录"}}` + "\n"
	if err := os.WriteFile(filepath.Join(home, ".claude/projects/-home-demo/a.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	uiPort, apiPort = freePort(t), freePort(t)
	dbPath = filepath.Join(dir, "index.db")
	cfgPath := filepath.Join(dir, "config.json")
	cfg := fmt.Sprintf(`{"home":%q,"db_path":%q,"ports":{"ui":%d,"api":%d},"scan":{"interval_sec":1}}`,
		home, dbPath, uiPort, apiPort)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	go func() { _ = runServe([]string{"-config", cfgPath}) }()
	waitReady(t, apiPort)
	return uiPort, apiPort, dbPath
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

func waitReady(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/stats", port))
		if err == nil {
			r.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("服务在 15s 内未就绪（端口 %d）", port)
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

// 两个端口都必须只绑回环——索引库是一份集中的凭证副本，不可暴露到局域网。
func TestListenersAreLoopbackOnly(t *testing.T) {
	uiPort, apiPort, _ := startServe(t)

	lan := lanIP(t)
	if lan == "" {
		t.Skip("本机没有非回环 IPv4，跳过局域网可达性检查")
	}
	for _, p := range []int{uiPort, apiPort} {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", lan, p), 2*time.Second)
		if err == nil {
			c.Close()
			t.Errorf("端口 %d 可从局域网地址 %s 连上——只监听 127.0.0.1 的约束被破坏了", p, lan)
		}
	}
}

func lanIP(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

// 同一查询从 :5021（前端口）与 :5022（API 口）必须得到相同结果——
// 两个 listener 共用同一个 mux，页面因此同源、不需要 CORS。
func TestBothPortsServeSameAPI(t *testing.T) {
	uiPort, apiPort, _ := startServe(t)

	waitIndexed(t, apiPort)

	_, viaAPI := get(t, fmt.Sprintf("http://127.0.0.1:%d/api/search?q=rsync", apiPort))
	_, viaUI := get(t, fmt.Sprintf("http://127.0.0.1:%d/api/search?q=rsync", uiPort))
	if viaAPI != viaUI {
		t.Errorf("两个端口返回不一致:\nAPI: %s\nUI : %s", viaAPI, viaUI)
	}
	if !strings.Contains(viaAPI, "rsync") {
		t.Errorf("未检索到刚索引的内容: %s", viaAPI)
	}

	// 前端口还要能出静态页面
	code, body := get(t, fmt.Sprintf("http://127.0.0.1:%d/", uiPort))
	if code != 200 || !strings.Contains(body, "chatdex") {
		t.Errorf("dashboard 未就绪 code=%d", code)
	}
}

// 后台扫描循环会自己把索引补齐（需求 5.2）。
func waitIndexed(t *testing.T, apiPort int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_, body := get(t, fmt.Sprintf("http://127.0.0.1:%d/api/stats", apiPort))
		var st struct {
			Blocks int `json:"blocks"`
		}
		if json.Unmarshal([]byte(body), &st) == nil && st.Blocks > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("后台扫描 15s 内未索引到任何内容")
}

// 第二个实例必须在**碰索引库之前**就退出：双开会把索引写坏，
// 而写坏的索引不是重启能修的。
func TestSecondInstanceRefusedBeforeTouchingIndex(t *testing.T) {
	_, apiPort, dbPath := startServe(t)
	waitIndexed(t, apiPort)

	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := fmt.Sprintf(`{"home":%q,"db_path":%q,"ports":{"ui":%d,"api":%d}}`,
		dir, dbPath, freePort(t), apiPort) // 复用已被占用的 API 端口
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	err = runServe([]string{"-config", cfgPath})
	if err == nil {
		t.Fatal("第二个实例竟然启动成功了")
	}
	if !strings.Contains(err.Error(), "已在运行") {
		t.Errorf("错误信息应说明「已在运行」，实际: %v", err)
	}

	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("第二个实例在退出前动了索引库")
	}
}

func TestStatsEndpoint(t *testing.T) {
	_, apiPort, _ := startServe(t)
	waitIndexed(t, apiPort)

	code, body := get(t, fmt.Sprintf("http://127.0.0.1:%d/api/stats", apiPort))
	if code != 200 {
		t.Fatalf("状态码 = %d", code)
	}
	var st struct {
		Sessions int   `json:"sessions"`
		Blocks   int   `json:"blocks"`
		DBBytes  int64 `json:"db_bytes"`
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.Sessions == 0 || st.Blocks == 0 || st.DBBytes == 0 {
		t.Errorf("统计为空: %s", body)
	}
}

// index 子命令必须与 serve 争同一把锁：两个写者同时扫同一个库会各写一份块，
// 事务只保证各自原子、不会互相察觉。
func TestIndexRefusesWhileServiceRunning(t *testing.T) {
	_, apiPort, dbPath := startServe(t)
	waitIndexed(t, apiPort)

	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := fmt.Sprintf(`{"home":%q,"db_path":%q,"ports":{"ui":%d,"api":%d}}`,
		dir, dbPath, freePort(t), apiPort)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	err = runIndex([]string{"-config", cfgPath, "-quiet"})
	if err == nil {
		t.Fatal("服务在跑时 index 子命令竟然执行成功了")
	}
	if !strings.Contains(err.Error(), "服务正在运行") {
		t.Errorf("错误信息应说明服务正在运行，实际: %v", err)
	}

	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Error("被拒绝的 index 仍然动了索引库")
	}
}
