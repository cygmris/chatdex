// Package httpapi 暴露检索能力的 JSON API。
//
// 所有查询一律走 search.Engine，handler 里不得写 SQL——dashboard、MCP 与聊天
// 三端必须共用同一套检索逻辑（需求 4.4）。
package httpapi

import (
	"context"
	"encoding/json"
	"github.com/cygmris/chatdex/internal/backup"
	"io"
	"net/http"

	"github.com/cygmris/chatdex/internal/chat"

	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/parser"
	"github.com/cygmris/chatdex/internal/search"
)

// Summarizer 是摘要后台任务的控制面，由 internal/summary 实现。
// 这里用最小接口而不是直接依赖具体类型：LLM 不可用时它可以是 nil。
type Summarizer interface {
	Pause()
	Resume()
	Paused() bool
}

// Chatter 是聊天助手的最小接口。为 nil 表示本地 LLM 不可用，入口置灰。
// Backuper 是备份能力的最小接口。可为 nil —— restic 是可选依赖，
// 不可用时备份入口置灰而索引与检索完全不受影响。
type Backuper interface {
	Available(ctx context.Context) backup.Status
	Backup(ctx context.Context) (backup.Result, error)
	Snapshots(ctx context.Context) ([]backup.Snapshot, error)
	Coverage(ctx context.Context, sessions []backup.IndexedSession) (backup.Coverage, error)
	Fetch(ctx context.Context, path string) (io.ReadCloser, error)
	LastAuto() *backup.AutoResult
	Init(ctx context.Context) error
}

type Chatter interface {
	Available(ctx context.Context) bool
	Ask(ctx context.Context, question string, scope chat.Scope, emit func(chat.Event)) error
}

// Server 持有各端共用的依赖。
type Server struct {
	Engine *search.Engine
	Store  *index.Store
	// Summary / Chat 可为 nil（本地 LLM 不可用时不启用，功能降级而非报错）。
	Summary Summarizer
	Chat    Chatter
	// ChatUnavailableReason 在 Chat 为 nil 时说明原因，前端要显示给用户。
	ChatUnavailableReason string
	// Config 可为 nil（理论上不会，留给测试）。
	Config ConfigStore
	// Backup 可为 nil（restic 是可选依赖，与 Chat 同一模式）。
	Backup Backuper
	// Reg 用于解析从备份里取回的原件。与索引用的是同一套解析器——
	// 备份里的原件和源目录里的文件是同一种东西，两套解析必然漂移。
	Reg *parser.Registry
}

// Register 把 API 路由挂到 mux 上。
//
// 同一个 mux 会被 :5021（前端）与 :5022（API+MCP）两个 listener 共用，
// 于是页面对自己的 origin 发请求即可，不需要 CORS，也不必反代跳一次。
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/session/{id}", s.handleSession)
	mux.HandleFunc("GET /api/session/{id}/children", s.handleChildren)
	mux.HandleFunc("GET /api/session/{id}/archived", s.handleArchived)
	mux.HandleFunc("GET /api/timeline", s.handleTimeline)
	mux.HandleFunc("GET /api/projects", s.handleProjects)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("POST /api/summary/pause", s.handleSummaryPause)
	mux.HandleFunc("POST /api/summary/resume", s.handleSummaryResume)
	mux.HandleFunc("POST /api/summary/retry", s.handleSummaryRetry)
	mux.HandleFunc("GET /api/summary/progress", s.handleSummaryProgress)
	mux.HandleFunc("GET /api/backup/status", s.handleBackupStatus)
	mux.HandleFunc("POST /api/backup/run", s.handleBackupRun)
	mux.HandleFunc("POST /api/backup/init", s.handleBackupInit)
	mux.HandleFunc("GET /api/backup/coverage", s.handleBackupCoverage)
	mux.HandleFunc("GET /api/backup/snapshots", s.handleBackupSnapshots)
	mux.HandleFunc("GET /api/chat/status", s.handleChatStatus)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/llm/models", s.handleModels)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
