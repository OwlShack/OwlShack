package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/OwlShack/OwlShack/internal/signaltest"
	"github.com/OwlShack/OwlShack/internal/store"
)

// SignalTester is the signal-test-runner seam, implemented by *signaltest.Service.
type SignalTester interface {
	Begin(ctx context.Context, p signaltest.Params) (int64, error)
	Cancel(id int64) error
	Active() int64
}

type startSignalTestRequest struct {
	Path         string `json:"path"`
	PathHashSize uint8  `json:"pathHashSize"`
	Count        int    `json:"count"`
	IntervalSecs int    `json:"intervalSecs"`
	Label        string `json:"label"`
}

// handleStartSignalTest 409s if a test is already active: only one runs at a time process-wide.
func (s *Server) handleStartSignalTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	tester := s.signalTesterRef()
	if tester == nil {
		writeError(w, http.StatusServiceUnavailable, "signal test runner not configured")
		return
	}
	companionID, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	var req startSignalTestRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pathBytes, err := hex.DecodeString(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "path must be valid hex")
		return
	}

	id, err := tester.Begin(r.Context(), signaltest.Params{
		CompanionID:   companionID,
		CompanionName: name,
		Label:         req.Label,
		Path:          pathBytes,
		PathHashSize:  req.PathHashSize,
		Count:         req.Count,
		IntervalSecs:  req.IntervalSecs,
	})
	if err != nil {
		var verr *signaltest.ValidationError
		switch {
		case errors.As(err, &verr):
			writeError(w, http.StatusBadRequest, verr.Error())
		case errors.Is(err, signaltest.ErrAlreadyRunning):
			writeError(w, http.StatusConflict, err.Error())
		default:
			s.serverError(w, "failed to start signal test", err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

type signalTestJSON struct {
	ID           int64  `json:"id"`
	Companion    string `json:"companion"`
	Label        string `json:"label"`
	Notes        string `json:"notes"`
	Path         string `json:"path"`
	PathHashSize int    `json:"pathHashSize"`
	Count        int    `json:"count"`
	IntervalSecs int    `json:"intervalSecs"`
	Status       string `json:"status"`
	StartedAt    int64  `json:"startedAt"`
	FinishedAt   int64  `json:"finishedAt"`
	RunsDone     int    `json:"runsDone"`
	OKCount      int    `json:"okCount"`
}

func (s *Server) toSignalTestJSON(ctx context.Context, t store.SignalTest) signalTestJSON {
	companion := ""
	if c, err := s.store.Companions.Get(ctx, t.CompanionID); err == nil && c != nil {
		companion = c.Name
	}
	return signalTestJSON{
		ID:           t.ID,
		Companion:    companion,
		Label:        t.Label,
		Notes:        t.Notes,
		Path:         hex.EncodeToString(t.Path),
		PathHashSize: t.PathHashSize,
		Count:        t.Count,
		IntervalSecs: t.IntervalSecs,
		Status:       t.Status,
		StartedAt:    t.StartedAt,
		FinishedAt:   t.FinishedAt,
		RunsDone:     t.RunsDone,
		OKCount:      t.OKCount,
	}
}

// handleListSignalTests returns saved tests newest first; ?companion=name filters to one companion.
func (s *Server) handleListSignalTests(w http.ResponseWriter, r *http.Request) {
	var companionID int64
	if name := r.URL.Query().Get("companion"); name != "" {
		id, ok := s.companionID(r.Context(), w, name)
		if !ok {
			return
		}
		companionID = id
	}

	tests, err := s.store.SignalTests.List(r.Context(), companionID)
	if err != nil {
		s.serverError(w, "failed to list signal tests", err)
		return
	}
	out := make([]signalTestJSON, 0, len(tests))
	for _, t := range tests {
		out = append(out, s.toSignalTestJSON(r.Context(), t))
	}
	writeJSON(w, http.StatusOK, out)
}

type signalTestRunJSON struct {
	Seq       int       `json:"seq"`
	SentAt    int64     `json:"sentAt"`
	OK        bool      `json:"ok"`
	HopSNRs   []float64 `json:"hopSNRs"`
	SNR       *float64  `json:"snr,omitempty"`
	ElapsedMs int64     `json:"elapsedMs"`
}

type signalTestDetailJSON struct {
	signalTestJSON
	Runs  []signalTestRunJSON `json:"runs"`
	Stats signaltest.Stats    `json:"stats"`
}

func (s *Server) signalTestByID(w http.ResponseWriter, r *http.Request) (*store.SignalTest, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid test id")
		return nil, false
	}
	t, err := s.store.SignalTests.Get(r.Context(), id)
	if err != nil {
		s.serverError(w, "failed to load signal test", err)
		return nil, false
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "signal test not found")
		return nil, false
	}
	return t, true
}

// handleGetSignalTest returns a test with its run history and computed per-hop stats.
func (s *Server) handleGetSignalTest(w http.ResponseWriter, r *http.Request) {
	t, ok := s.signalTestByID(w, r)
	if !ok {
		return
	}
	runs, err := s.store.SignalTests.ListRuns(r.Context(), t.ID)
	if err != nil {
		s.serverError(w, "failed to list signal test runs", err)
		return
	}

	runsJSON := make([]signalTestRunJSON, 0, len(runs))
	for _, run := range runs {
		var hopSNRs []float64
		_ = json.Unmarshal([]byte(run.HopSNRs), &hopSNRs)
		if hopSNRs == nil {
			hopSNRs = []float64{}
		}
		runsJSON = append(runsJSON, signalTestRunJSON{
			Seq: run.Seq, SentAt: run.SentAt, OK: run.OK,
			HopSNRs: hopSNRs, SNR: run.SNR, ElapsedMs: run.ElapsedMs,
		})
	}

	writeJSON(w, http.StatusOK, signalTestDetailJSON{
		signalTestJSON: s.toSignalTestJSON(r.Context(), *t),
		Runs:           runsJSON,
		Stats:          signaltest.ComputeStats(runs),
	})
}

// handleCancelSignalTest cancels the active test if its id matches.
func (s *Server) handleCancelSignalTest(w http.ResponseWriter, r *http.Request) {
	tester := s.signalTesterRef()
	if tester == nil {
		writeError(w, http.StatusServiceUnavailable, "signal test runner not configured")
		return
	}
	t, ok := s.signalTestByID(w, r)
	if !ok {
		return
	}
	if err := tester.Cancel(t.ID); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type updateSignalTestRequest struct {
	Label *string `json:"label"`
	Notes *string `json:"notes"`
}

// handleUpdateSignalTest renames/annotates a saved test.
func (s *Server) handleUpdateSignalTest(w http.ResponseWriter, r *http.Request) {
	t, ok := s.signalTestByID(w, r)
	if !ok {
		return
	}
	var req updateSignalTestRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var uerr error
	s.store.WriteSync(func() {
		uerr = s.store.SignalTests.UpdateLabelNotes(context.Background(), t.ID, req.Label, req.Notes)
	})
	if uerr != nil {
		s.serverError(w, "failed to update signal test", uerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleDeleteSignalTest refuses to delete the currently running test.
func (s *Server) handleDeleteSignalTest(w http.ResponseWriter, r *http.Request) {
	t, ok := s.signalTestByID(w, r)
	if !ok {
		return
	}
	if t.Status == store.SignalTestStatusRunning {
		writeError(w, http.StatusConflict, "cancel the test before deleting it")
		return
	}
	var derr error
	s.store.WriteSync(func() {
		derr = s.store.SignalTests.Delete(context.Background(), t.ID)
	})
	if derr != nil {
		s.serverError(w, "failed to delete signal test", derr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
