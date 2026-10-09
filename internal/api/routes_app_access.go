package api

import "net/http"

func (s *Server) handleCompanionApp(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "node not running")
		return
	}
	st, ok := b.CompanionApp(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "companion not running")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleDisconnectCompanionApp(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "node not running")
		return
	}
	if !b.DisconnectCompanionApp(r.PathValue("name")) {
		writeError(w, http.StatusNotFound, "app access is off for this companion")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
