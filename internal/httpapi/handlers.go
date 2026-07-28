package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/cygmris/chatdex/internal/index"
	"github.com/cygmris/chatdex/internal/search"
)

// parseQuery 把 URL 参数映射成 search.Query。
func parseQuery(r *http.Request) search.Query {
	v := r.URL.Query()
	q := search.Query{
		Text:     v.Get("q"),
		ToolName: v.Get("tool"),
		Source:   v.Get("source"),
		Project:  v.Get("project"),
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
	Summary  index.Progress `json:"summary"`
	Paused   bool           `json:"summary_paused"`
	LLMReady bool           `json:"llm_ready"`
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
	if p, err := s.Store.SummaryProgress(); err == nil {
		out.Summary = p
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
