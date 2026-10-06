// Package signaltest runs repeatable trace-route tests: a fixed number of traces at a fixed interval over a saved path.
package signaltest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/OwlShack/OwlShack/internal/store"
)

// ErrAlreadyRunning is returned by Begin when a test is already active; the API layer maps it to 409.
var ErrAlreadyRunning = errors.New("a signal test is already running")

// ValidationError marks a Begin failure as a bad request, so the API layer can map it to 400 without string-matching.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func validationErrorf(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// WSTopic is the WebSocket topic live run/status events are broadcast on.
const WSTopic = "signaltest"

// Bounds enforced by Begin and mirrored in the API handler.
const (
	MinIntervalSecs = 5
	MaxIntervalSecs = 3600
	MaxCount        = 1000
)

// traceTimeout backstops a hung companion lookup; RunTrace's own silence window is the primary timeout.
const traceTimeout = 30 * time.Second

// maxConsecutiveTracerErrors aborts a test as "interrupted" rather than recording an hour of failed runs against an unreachable companion.
const maxConsecutiveTracerErrors = 5

// TraceResult is one trace attempt's outcome, adapted from companion.TraceOutcome by the Tracer implementation.
type TraceResult struct {
	HopSNRs   []float64
	SNR       *float64
	ElapsedMs int64
	Complete  bool
}

// Tracer runs one blocking trace on a named companion; implemented in internal/app over the companion registry.
type Tracer func(ctx context.Context, companionName string, path []byte, pathHashSize uint8) (*TraceResult, error)

// Broadcaster is the subset of *api.Hub the runner needs.
type Broadcaster interface {
	Broadcast(topic string, data any)
}

// Params describes a test to start.
type Params struct {
	CompanionID   int64  // surrogate id for storage
	CompanionName string // runtime lookup key for the Tracer
	Label         string
	Path          []byte
	PathHashSize  uint8
	Count         int
	IntervalSecs  int
}

// Service is the test runner; one test is active at a time, since interleaved tests would pollute each other's stats.
type Service struct {
	st      *store.Store
	bc      Broadcaster
	tracer  Tracer
	airtime *sync.Mutex
	log     *slog.Logger

	mu       sync.Mutex
	baseCtx  context.Context
	cancel   context.CancelFunc
	activeID int64
}

func New(st *store.Store, bc Broadcaster, tracer Tracer, airtime *sync.Mutex, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		st:      st,
		bc:      bc,
		tracer:  tracer,
		airtime: airtime,
		log:     log.With("component", "signaltest"),
	}
}

// Start records the base context for run goroutines and marks tests left "running" by a previous process as "interrupted".
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()

	now := time.Now().Unix()
	s.st.WriteSync(func() {
		if err := s.st.SignalTests.MarkInterrupted(context.Background(), now); err != nil {
			s.log.Error("marking interrupted signal tests", "error", err)
		}
	})
}

// Active returns the id of the currently running test, or 0 if none.
func (s *Service) Active() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeID
}

// Begin validates params, creates the signal_tests row, and launches the run loop.
func (s *Service) Begin(ctx context.Context, p Params) (int64, error) {
	if len(p.Path) == 0 {
		return 0, validationErrorf("path is required")
	}
	if p.PathHashSize != 1 && p.PathHashSize != 2 && p.PathHashSize != 4 {
		return 0, validationErrorf("pathHashSize must be 1, 2, or 4")
	}
	if len(p.Path)%int(p.PathHashSize) != 0 {
		return 0, validationErrorf("path length is not divisible by pathHashSize")
	}
	if p.Count < 1 || p.Count > MaxCount {
		return 0, validationErrorf("count must be between 1 and %d", MaxCount)
	}
	if p.IntervalSecs < MinIntervalSecs || p.IntervalSecs > MaxIntervalSecs {
		return 0, validationErrorf("intervalSecs must be between %d and %d", MinIntervalSecs, MaxIntervalSecs)
	}

	s.mu.Lock()
	if s.activeID != 0 {
		s.mu.Unlock()
		return 0, ErrAlreadyRunning
	}
	baseCtx := s.baseCtx
	s.mu.Unlock()
	if baseCtx == nil {
		return 0, fmt.Errorf("signal test service not started")
	}

	test := &store.SignalTest{
		CompanionID:  p.CompanionID,
		Label:        p.Label,
		Path:         p.Path,
		PathHashSize: int(p.PathHashSize),
		Count:        p.Count,
		IntervalSecs: p.IntervalSecs,
		StartedAt:    time.Now().Unix(),
	}
	var createErr error
	s.st.WriteSync(func() {
		createErr = s.st.SignalTests.Create(context.Background(), test)
	})
	if createErr != nil {
		return 0, fmt.Errorf("creating signal test: %w", createErr)
	}

	runCtx, cancel := context.WithCancel(baseCtx)
	s.mu.Lock()
	s.activeID = test.ID
	s.cancel = cancel
	s.mu.Unlock()

	go s.run(runCtx, test.ID, p)

	return test.ID, nil
}

// Cancel stops the active test if its id matches.
func (s *Service) Cancel(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeID == 0 || s.activeID != id {
		return fmt.Errorf("test %d is not currently running", id)
	}
	s.cancel()
	return nil
}

func (s *Service) run(ctx context.Context, testID int64, p Params) {
	status := store.SignalTestStatusDone
	consecutiveErrors := 0

	for seq := 1; seq <= p.Count; seq++ {
		sentAt := time.Now()

		if ctx.Err() != nil {
			status = store.SignalTestStatusCancelled
			break
		}

		s.airtime.Lock()
		traceCtx, cancel := context.WithTimeout(ctx, traceTimeout)
		res, err := s.tracer(traceCtx, p.CompanionName, p.Path, p.PathHashSize)
		cancel()
		s.airtime.Unlock()

		var run store.SignalTestRun
		run.TestID = testID
		run.Seq = seq
		run.SentAt = sentAt.Unix()

		if err != nil {
			consecutiveErrors++
			run.HopSNRs = "[]"
			s.log.Warn("signal test trace failed", "testId", testID, "seq", seq, "error", err)
			if consecutiveErrors >= maxConsecutiveTracerErrors {
				s.persistRun(&run)
				s.broadcastRun(testID, p.Count, &run)
				status = store.SignalTestStatusInterrupted
				break
			}
		} else {
			consecutiveErrors = 0
			run.OK = res.Complete
			run.ElapsedMs = res.ElapsedMs
			run.SNR = res.SNR
			if blob, mErr := json.Marshal(res.HopSNRs); mErr == nil {
				run.HopSNRs = string(blob)
			} else {
				run.HopSNRs = "[]"
			}
		}

		s.persistRun(&run)
		s.broadcastRun(testID, p.Count, &run)

		if seq == p.Count {
			break
		}
		if ctx.Err() != nil {
			status = store.SignalTestStatusCancelled
			break
		}
		deadline := sentAt.Add(time.Duration(p.IntervalSecs) * time.Second)
		if !sleepUntil(ctx, deadline) {
			status = store.SignalTestStatusCancelled
			break
		}
	}

	if ctx.Err() != nil && status == store.SignalTestStatusDone {
		status = store.SignalTestStatusCancelled
	}

	finishedAt := time.Now().Unix()
	s.st.WriteAsync(func() {
		if err := s.st.SignalTests.SetStatus(context.Background(), testID, status, finishedAt); err != nil {
			s.log.Error("setting signal test status", "error", err)
		}
	})
	if s.bc != nil {
		s.bc.Broadcast(WSTopic, map[string]any{
			"action":     "status",
			"testId":     testID,
			"status":     status,
			"finishedAt": finishedAt,
		})
	}

	s.mu.Lock()
	if s.activeID == testID {
		s.activeID = 0
		s.cancel = nil
	}
	s.mu.Unlock()
}

func (s *Service) persistRun(run *store.SignalTestRun) {
	// Copy by value into the closure — run is stack-local to the caller's loop iteration.
	r := *run
	s.st.WriteAsync(func() {
		if err := s.st.SignalTests.InsertRun(context.Background(), &r); err != nil {
			s.log.Error("inserting signal test run", "error", err)
		}
	})
}

func (s *Service) broadcastRun(testID int64, count int, run *store.SignalTestRun) {
	if s.bc == nil {
		return
	}
	var hopSNRs []float64
	_ = json.Unmarshal([]byte(run.HopSNRs), &hopSNRs)
	if hopSNRs == nil {
		hopSNRs = []float64{}
	}
	s.bc.Broadcast(WSTopic, map[string]any{
		"action":    "run",
		"testId":    testID,
		"seq":       run.Seq,
		"count":     count,
		"ok":        run.OK,
		"hopSNRs":   hopSNRs,
		"snr":       run.SNR,
		"elapsedMs": run.ElapsedMs,
		"sentAt":    run.SentAt,
	})
}

// sleepUntil blocks until deadline or ctx cancellation, returning false on cancellation.
func sleepUntil(ctx context.Context, deadline time.Time) bool {
	d := time.Until(deadline)
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
