package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/search"
	"github.com/cygmris/chatdex/internal/summary"
)

// parseQuery 把 URL 参数映射成 search.Query。
func parseQuery(r *http.Request) search.Query {
	v := r.URL.Query()
	q := search.Query{
		Text:     v.Get("q"),
		ToolName: v.Get("tool"),
		Source:   v.Get("source"),
		Project:  v.Get("project"),
		Agent:    v.Get("agent"),
	}
	if k := v.Get("kind"); k != "" {
		for _, s := range strings.Split(k, ",") {
			if s = strings.TrimSpace(s); s != "" {
				q.Kinds = append(q.Kinds, s)
			}
		}
	}
	q.From = atoi64(v.Get("from"))
	q.To = atoi64(v.Get("to"))
	q.Limit = int(atoi64(v.Get("limit")))
	q.Offset = int(atoi64(v.Get("offset")))
	return q
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	res, err := s.Engine.SearchSessions(parseQuery(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 空结果下发 [] 而不是 null，与 /api/timeline 一致：
	// null 会让前端每个用到它的地方各写一次判空，漏一处就是运行时报错。
	if res.Sessions == nil {
		res.Sessions = []search.SessionHit{}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "会话 id 非法")
		return
	}
	v := r.URL.Query()
	view, err := s.Engine.GetSession(id, int(atoi64(v.Get("from"))), int(atoi64(v.Get("limit"))))
	if err != nil {
		writeErr(w, http.StatusNotFound, "会话不存在")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleChildren 下发某会话的子代理列表。
//
// 走一个独立端点而不是塞进 GetSession：绝大多数会话没有子代理（61 / 3207 有），
// 为了那 2% 让每次回读都多查一次不划算。回读页只在 child_count > 0 时才来取。
func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "会话 id 非法")
		return
	}
	items, total, err := s.Engine.Children(id, int(atoi64(r.URL.Query().Get("limit"))))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []search.SessionHit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// handleSummaryRetry 把失败的摘要任务放回队列。
//
// 失败任务现在不会被自动复活（那正是本期修掉的饥饿 bug），所以必须有一个
// 显式入口，否则失败即永久。
func (s *Server) handleSummaryRetry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID int64 `json:"session_id"`
		All       bool  `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.All {
		n, err := s.Store.RetryAllFailed()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"retried": n})
		return
	}
	if req.SessionID <= 0 {
		writeErr(w, http.StatusBadRequest, "缺少 session_id")
		return
	}
	ok, err := s.Store.RetrySummary(req.SessionID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		// 没有这条失败任务：可能 id 不存在，也可能它并不处于 failed 状态。
		// 两者对使用者是同一件事——「没有可重试的东西」，不是服务器出错。
		writeErr(w, http.StatusNotFound, "没有该会话的失败任务")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"retried": 1})
}

// handleSummaryProgress 一次给全摘要生成的运行态。
//
// 做成一个端点而不是让前端拼几处：进度页要的每一项都来自同一时刻的快照，
// 分几次取会看到互相矛盾的数字（计数是新的、吞吐是旧的）。
func (s *Server) handleSummaryProgress(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.SummaryProgress()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	lastHour, last24h, err := s.Store.Throughput()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	items, total, err := s.Store.Failures(50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []index.Failure{}
	}

	out := map[string]any{
		"counts":       p,
		"recent":       map[string]int{"last_hour": lastHour, "last_24h": last24h},
		"failures":     items,
		"failed_total": total,
		"paused":       s.Summary != nil && s.Summary.Paused(),
	}
	// ETA 算不出来时下发 null 而不是 0：「算不出」与「马上完成」是两回事。
	// 这是决策 18 那条纪律——「不知道」不能压成「没问题」。
	if lastHour > 0 && p.Pending+p.Running > 0 {
		out["eta_seconds"] = (p.Pending + p.Running) * 3600 / lastHour
	} else {
		out["eta_seconds"] = nil
	}
	if s.Chat != nil {
		out["llm_ready"] = s.Chat.Available(r.Context())
	}
	if s.Config != nil {
		c := s.Config.Current()
		win := map[string]any{"conf": c.Summary.Window, "enabled": c.Summary.Enabled}
		in, next, err := summary.InWindow(c.Summary.Window, time.Now())
		win["in"] = in
		if err != nil {
			win["invalid"] = err.Error() // 填错了要说出来，不能只是按不限跑
		} else if !in {
			win["next_at"] = next.Unix()
		}
		out["window"] = win
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	// 与检索共用同一套过滤条件解析（需求 9.4）
	gs, err := s.Engine.Timeline(parseQuery(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if gs == nil {
		gs = []search.ProjectGroup{}
	}
	writeJSON(w, http.StatusOK, gs)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Engine.Projects()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

// statsResponse 在索引统计之上带出摘要进度，供 dashboard 顶部展示。
type statsResponse struct {
	index.Stats
	// Summary 是指针：取不到时下发 null，而不是零值。
	// 零值会在前端渲染成 0/0 的进度条——一个看起来像「一条摘要都没生成」的错值，
	// 且没有任何信号。null 让前端能区分「进度是 0」与「取不到进度」。
	Summary  *index.Progress `json:"summary"`
	Paused   bool            `json:"summary_paused"`
	LLMReady bool            `json:"llm_ready"`
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// llm_ready 要如实反映「此刻能不能用」，不是「配置是否合法」——
	// 前端拿它决定摘要进度条怎么显示，报个乐观值只会误导。
	out := statsResponse{Stats: st}
	if s.Chat != nil {
		out.LLMReady = s.Chat.Available(r.Context())
	}
	if p, err := s.Store.SummaryProgress(); err != nil {
		slog.Warn("读取摘要进度失败，本次下发 null", "err", err)
	} else {
		out.Summary = &p
	}
	if s.Summary != nil {
		out.Paused = s.Summary.Paused()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSummaryPause(w http.ResponseWriter, r *http.Request) {
	if s.Summary == nil {
		writeErr(w, http.StatusServiceUnavailable, "摘要任务未启用（本地 LLM 不可用）")
		return
	}
	s.Summary.Pause()
	writeJSON(w, http.StatusOK, map[string]bool{"paused": true})
}

func (s *Server) handleSummaryResume(w http.ResponseWriter, r *http.Request) {
	if s.Summary == nil {
		writeErr(w, http.StatusServiceUnavailable, "摘要任务未启用（本地 LLM 不可用）")
		return
	}
	s.Summary.Resume()
	writeJSON(w, http.StatusOK, map[string]bool{"paused": false})
}
