package api

import (
	"net/http"
	"time"

	"github.com/OwlShack/OwlShack/internal/buildinfo"
)

// handleHealth is 200 whenever the process is alive: an unreachable OwlShack already fails the request, so the code is not spent on a second opinion.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var info HealthInfo
	if b := s.backendRef(); b != nil {
		info = b.Health()
	} else {
		// Up but nothing wired yet — real during startup and after a failed reload, and not to be read as healthy.
		info.Problems = []string{"no backend installed yet"}
	}

	if info.Problems == nil {
		info.Problems = []string{} // a JSON null would break $count(problems) in a monitor's query
	}
	info.Status = "ok"
	if len(info.Problems) > 0 {
		info.Status = "degraded"
	}
	info.Version = buildinfo.Version
	info.UptimeSecs = int64(time.Since(s.startedAt).Seconds())

	writeJSON(w, http.StatusOK, info)
}
