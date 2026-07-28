package httpapi

import (
	"net/http"
	"strconv"
	"strings"

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

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Engine.Projects()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Stats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}
