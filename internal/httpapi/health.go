package httpapi

import (
	"net/http"

	"github.com/cygmris/chatdex/internal/version"
)

type healthIndex struct {
	OK       bool   `json:"ok"`
	Path     string `json:"path,omitempty"`
	Sessions int    `json:"sessions,omitempty"`
	Blocks   int    `json:"blocks,omitempty"`
	DBBytes  int64  `json:"db_bytes,omitempty"`
	Error    string `json:"error,omitempty"`
}

type healthResponse struct {
	OK      bool           `json:"ok"`
	Status  string         `json:"status"`
	Version string         `json:"version"`
	Commit  string         `json:"commit"`
	Ports   map[string]int `json:"ports"`
	Index   healthIndex    `json:"index"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ver := s.Version
	if ver == "" {
		ver = version.Version
	}
	commit := s.Commit
	if commit == "" {
		commit = version.Commit
	}
	out := healthResponse{
		OK:      true,
		Status:  "ok",
		Version: ver,
		Commit:  commit,
		Ports:   map[string]int{"ui": s.UIPort, "api": s.APIPort},
		Index:   healthIndex{Path: s.IndexPath},
	}
	if s.Store == nil {
		out.OK = false
		out.Status = "degraded"
		out.Index.Error = "索引库未打开"
		writeJSON(w, http.StatusServiceUnavailable, out)
		return
	}
	st, err := s.Store.Stats()
	if err != nil {
		out.OK = false
		out.Status = "degraded"
		out.Index.Error = err.Error()
		writeJSON(w, http.StatusServiceUnavailable, out)
		return
	}
	out.Index.OK = true
	out.Index.Sessions = st.Sessions
	out.Index.Blocks = st.Blocks
	out.Index.DBBytes = st.DBBytes
	writeJSON(w, http.StatusOK, out)
}
