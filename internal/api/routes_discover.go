package api

import (
	"net/http"
)

// handleDiscoveryState reports the current scan. A backend that is not up yet, or has no node to
// send from, errors rather than returning an empty result set that reads as "nothing out there".
func (s *Server) handleDiscoveryState(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "node not running")
		return
	}
	state, ok := b.DiscoveryState()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "no node to discover from: create a companion first")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleStartDiscovery broadcasts a zero-hop discovery. Returns the scan immediately: responders
// answer over the next minute, so the results arrive on the websocket, not in this reply.
func (s *Server) handleStartDiscovery(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "node not running")
		return
	}
	var in struct {
		Types []int `json:"types"`
	}
	if r.ContentLength > 0 {
		if err := readJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}
	state, ok := b.StartDiscovery(in.Types)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "no node to discover from: create a companion first")
		return
	}
	writeJSON(w, http.StatusAccepted, state)
}

// handleRegionScanState reports the current or last Discover nearby run.
func (s *Server) handleRegionScanState(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "node not running")
		return
	}
	writeJSON(w, http.StatusOK, b.RegionScanState())
}

// handleStartRegionScan starts Discover nearby, or returns the run already going; poll the state for progress.
func (s *Server) handleStartRegionScan(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "node not running")
		return
	}
	state, err := b.StartRegionScan()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, state)
}
