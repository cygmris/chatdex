package httpapi

import (
	"net/http"

	"github.com/cygmris/chatdex/internal/backup"
)

// 备份相关端点。restic 是**可选依赖**：Backup 为 nil 或不可用时，
// 这些端点如实说明原因，而索引、检索、摘要、问一问完全不受影响
// （与 /api/chat/status 同一模式）。

func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	if s.Backup == nil {
		writeJSON(w, http.StatusOK, backup.Status{Reason: "备份功能未启用"})
		return
	}
	st := s.Backup.Available(r.Context())
	// 自动备份失败没人看着——需求 5.3 要求明确显示原因而不是只记日志，
	// 所以把最后一次自动备份的结果一起带出去，界面上跟手动那次并排显示。
	writeJSON(w, http.StatusOK, struct {
		backup.Status
		LastAuto *backup.AutoResult `json:"last_auto,omitempty"`
	}{st, s.Backup.LastAuto()})
}

func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	if s.Backup == nil {
		writeErr(w, http.StatusServiceUnavailable, "备份功能未启用")
		return
	}
	res, err := s.Backup.Backup(r.Context())
	if err != nil {
		// 备份失败只是备份失败——不影响索引与检索，所以是 200 里带 error
		// 还是 5xx？用 5xx：前端据此显示红色状态，而不是把一次失败画成成功。
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleBackupInit(w http.ResponseWriter, r *http.Request) {
	if s.Backup == nil {
		writeErr(w, http.StatusServiceUnavailable, "备份功能未启用")
		return
	}
	if err := s.Backup.Init(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBackupSnapshots 下发快照列表。
//
// chatdex **不另存一份**快照历史——`restic snapshots` 就是权威记录，
// 存两份必然漂移（design「明确不做」里写死的）。
func (s *Server) handleBackupSnapshots(w http.ResponseWriter, r *http.Request) {
	if s.Backup == nil {
		writeErr(w, http.StatusServiceUnavailable, "备份功能未启用")
		return
	}
	snaps, err := s.Backup.Snapshots(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if snaps == nil {
		snaps = []backup.Snapshot{}
	}
	writeJSON(w, http.StatusOK, snaps)
}

func (s *Server) handleBackupCoverage(w http.ResponseWriter, r *http.Request) {
	if s.Backup == nil {
		writeErr(w, http.StatusServiceUnavailable, "备份功能未启用")
		return
	}
	// 会话清单由这一层从索引取，backup 包不认识 SQL——
	// 与「解析器认识 JSONL、索引层不认识」是同一条分层纪律。
	// 查询走 Engine 而不是在 handler 里写 SQL（见本包包注释）。
	files, err := s.Engine.AllSessionFiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sessions := make([]backup.IndexedSession, 0, len(files))
	for _, f := range files {
		sessions = append(sessions, backup.IndexedSession{Path: f.Path, Alive: f.Alive})
	}
	cov, err := s.Backup.Coverage(r.Context(), sessions)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cov)
}
