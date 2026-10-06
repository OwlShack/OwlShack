package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"

	"github.com/OwlShack/OwlShack/internal/monitor"
	"github.com/OwlShack/OwlShack/internal/store"
)

// linkMonitorKey derives the synthetic 32-byte key a link monitor is polled under.
func linkMonitorKey(companionID int64, pathHashSize uint8, path []byte) []byte {
	h := sha256.New()
	fmt.Fprintf(h, "link|%d|%d|%x", companionID, pathHashSize, path)
	return h.Sum(nil)
}

type linkMonitorJSON struct {
	ID             int64  `json:"id"`
	Key            string `json:"key"`
	Companion      string `json:"companion"`
	Label          string `json:"label"`
	Path           string `json:"path"`
	PathHashSize   int    `json:"pathHashSize"`
	IntervalSecs   int    `json:"intervalSecs"`
	Enabled        bool   `json:"enabled"`
	IgnoreFirstHop bool   `json:"ignoreFirstHop"`
	RetrySecs      int    `json:"retrySecs"`
	MaxRetries     int    `json:"maxRetries"`
	HideLastSnr    bool   `json:"hideLastSnr"`
}

func (s *Server) toLinkMonitorJSON(ctx context.Context, l store.LinkMonitor) linkMonitorJSON {
	companion := ""
	if c, err := s.store.Companions.Get(ctx, l.CompanionID); err == nil && c != nil {
		companion = c.Name
	}
	return linkMonitorJSON{
		ID:             l.ID,
		Key:            hex.EncodeToString(l.Key),
		Companion:      companion,
		Label:          l.Label,
		Path:           hex.EncodeToString(l.Path),
		PathHashSize:   l.PathHashSize,
		IntervalSecs:   l.IntervalSecs,
		Enabled:        l.Enabled,
		IgnoreFirstHop: l.IgnoreFirstHop,
		RetrySecs:      l.RetrySecs,
		MaxRetries:     l.MaxRetries,
		HideLastSnr:    l.HideLastSnr,
	}
}

// validateLinkRetry keeps delay × maxRetries inside the poll interval; only a positive maxRetries retries.
func validateLinkRetry(intervalSecs, retrySecs, maxRetries int) error {
	if maxRetries <= 0 {
		return nil
	}
	effRetry := int64(retrySecs)
	if effRetry <= 0 {
		effRetry = monitor.DefaultRetrySecs
	}
	if span := effRetry * int64(maxRetries); span > int64(intervalSecs) {
		return fmt.Errorf("retry delay × max retries (%ds) must be less than or equal to the poll interval (%ds)", span, intervalSecs)
	}
	return nil
}

// handleListLinks returns every saved link monitor.
func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.LinkMonitors.List(r.Context())
	if err != nil {
		s.serverError(w, "failed to list link monitors", err)
		return
	}
	out := make([]linkMonitorJSON, 0, len(links))
	for _, l := range links {
		out = append(out, s.toLinkMonitorJSON(r.Context(), l))
	}
	writeJSON(w, http.StatusOK, out)
}

type addLinkRequest struct {
	Path         string `json:"path"`
	PathHashSize uint8  `json:"pathHashSize"`
	Label        string `json:"label"`
	IntervalSecs int    `json:"intervalSecs"`
	RetrySecs    int    `json:"retrySecs"`
	MaxRetries   int    `json:"maxRetries"`
}

// handleAddLink saves a new monitored link — a path polled on a schedule.
func (s *Server) handleAddLink(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	companionID, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	var req addLinkRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PathHashSize != 1 && req.PathHashSize != 2 && req.PathHashSize != 4 {
		writeError(w, http.StatusBadRequest, "pathHashSize must be 1, 2, or 4")
		return
	}
	pathBytes, err := hex.DecodeString(req.Path)
	if err != nil || len(pathBytes) == 0 {
		writeError(w, http.StatusBadRequest, "path must be valid, non-empty hex")
		return
	}
	if len(pathBytes)%int(req.PathHashSize) != 0 {
		writeError(w, http.StatusBadRequest, "path length must be divisible by pathHashSize")
		return
	}
	intervalSecs := req.IntervalSecs
	if intervalSecs <= 0 {
		intervalSecs = 900
	}
	if intervalSecs < 60 || intervalSecs > 86400 {
		writeError(w, http.StatusBadRequest, "intervalSecs must be between 60 and 86400")
		return
	}
	if req.RetrySecs < 0 || req.RetrySecs > 86400 {
		writeError(w, http.StatusBadRequest, "retrySecs must be between 0 and 86400")
		return
	}
	if req.MaxRetries < -1 || req.MaxRetries > 100 {
		writeError(w, http.StatusBadRequest, "maxRetries must be -1 (no retries), 0 (default), or up to 100")
		return
	}
	if err := validateLinkRetry(intervalSecs, req.RetrySecs, req.MaxRetries); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	lm := &store.LinkMonitor{
		Key:            linkMonitorKey(companionID, req.PathHashSize, pathBytes),
		CompanionID:    companionID,
		Label:          req.Label,
		Path:           pathBytes,
		PathHashSize:   int(req.PathHashSize),
		IntervalSecs:   intervalSecs,
		Enabled:        true,
		RetrySecs:      req.RetrySecs,
		MaxRetries:     req.MaxRetries,
		IgnoreFirstHop: true,
		HideLastSnr:    true,
	}
	var createErr error
	s.store.WriteSync(func() {
		createErr = s.store.LinkMonitors.Create(context.Background(), lm)
	})
	if createErr != nil {
		writeError(w, http.StatusConflict, "this path is already monitored")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": lm.ID, "key": hex.EncodeToString(lm.Key)})
}

func (s *Server) linkMonitorByID(w http.ResponseWriter, r *http.Request) (*store.LinkMonitor, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid link id")
		return nil, false
	}
	lm, err := s.store.LinkMonitors.GetByID(r.Context(), id)
	if err != nil {
		s.serverError(w, "failed to load link monitor", err)
		return nil, false
	}
	if lm == nil {
		writeError(w, http.StatusNotFound, "link monitor not found")
		return nil, false
	}
	return lm, true
}

type updateLinkRequest struct {
	Label          *string `json:"label"`
	IntervalSecs   *int    `json:"intervalSecs"`
	Enabled        *bool   `json:"enabled"`
	IgnoreFirstHop *bool   `json:"ignoreFirstHop"`
	RetrySecs      *int    `json:"retrySecs"`
	MaxRetries     *int    `json:"maxRetries"`
	HideLastSnr    *bool   `json:"hideLastSnr"`
}

// handleUpdateLink takes effect on the monitor engine's next scheduling tick, without a restart.
func (s *Server) handleUpdateLink(w http.ResponseWriter, r *http.Request) {
	lm, ok := s.linkMonitorByID(w, r)
	if !ok {
		return
	}
	var req updateLinkRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IntervalSecs != nil && (*req.IntervalSecs < 60 || *req.IntervalSecs > 86400) {
		writeError(w, http.StatusBadRequest, "intervalSecs must be between 60 and 86400")
		return
	}
	if req.RetrySecs != nil && (*req.RetrySecs < 0 || *req.RetrySecs > 86400) {
		writeError(w, http.StatusBadRequest, "retrySecs must be between 0 and 86400")
		return
	}
	if req.MaxRetries != nil && (*req.MaxRetries < -1 || *req.MaxRetries > 100) {
		writeError(w, http.StatusBadRequest, "maxRetries must be -1 (no retries), 0 (default), or up to 100")
		return
	}
	// Validate the merged post-patch values, not just the fields this request touches.
	mergedInterval := lm.IntervalSecs
	if req.IntervalSecs != nil {
		mergedInterval = *req.IntervalSecs
	}
	mergedRetrySecs := lm.RetrySecs
	if req.RetrySecs != nil {
		mergedRetrySecs = *req.RetrySecs
	}
	mergedMaxRetries := lm.MaxRetries
	if req.MaxRetries != nil {
		mergedMaxRetries = *req.MaxRetries
	}
	if err := validateLinkRetry(mergedInterval, mergedRetrySecs, mergedMaxRetries); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var uerr error
	s.store.WriteSync(func() {
		uerr = s.store.LinkMonitors.Update(context.Background(), lm.ID, req.Label, req.IntervalSecs, req.Enabled, req.IgnoreFirstHop, req.RetrySecs, req.MaxRetries, req.HideLastSnr)
	})
	if uerr != nil {
		s.serverError(w, "failed to update link monitor", uerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteLink removes a link monitor and its node_state/node_metrics rows.
func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	lm, ok := s.linkMonitorByID(w, r)
	if !ok {
		return
	}
	var derr error
	s.store.WriteSync(func() {
		if err := s.store.LinkMonitors.Delete(context.Background(), lm.ID); err != nil {
			derr = err
			return
		}
		derr = s.store.LinkMonitors.DeleteNodeStateAndMetrics(context.Background(), lm.Key)
	})
	if derr != nil {
		s.serverError(w, "failed to delete link monitor", derr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
