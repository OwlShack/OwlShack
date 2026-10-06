// Package monitor polls each node's registered Collector on a staggered schedule, persists readings and broadcasts them.
package monitor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/OwlShack/OwlShack/internal/store"
)

// WSTopicMetrics is the WebSocket topic live readings are broadcast on.
const WSTopicMetrics = "metrics"

// Poller-wide tuning; per-node cadence comes from Target.IntervalSecs instead.
const (
	pollTick = 30 * time.Second
	// requestTimeout is a generous backstop: each sub-request of a poll has its own tighter timeout.
	requestTimeout  = 2 * time.Minute
	defaultInterval = 6 * time.Hour
	// stagger gaps consecutive polls within a cycle to avoid bursts of RF collisions.
	stagger       = 30 * time.Second
	retention     = 90 * 24 * time.Hour
	pruneInterval = 6 * time.Hour
	// retryInterval re-attempts sooner than the full interval, so transient RF failures don't blank a node for hours.
	retryInterval = 5 * time.Minute
	// defaultMaxRetries bounds consecutive fast re-attempts before the node falls back to its normal interval.
	defaultMaxRetries = 3
)

// DefaultIntervalSecs is defaultInterval in seconds, exported so the UI derives its staleness threshold from it.
const DefaultIntervalSecs = int64(defaultInterval / time.Second)

// DefaultRetrySecs/DefaultMaxRetries are the poller defaults the API validates per-node overrides against.
const DefaultRetrySecs = int64(retryInterval / time.Second)
const DefaultMaxRetries = defaultMaxRetries

// Broadcaster is the subset of *api.Hub the monitor needs.
type Broadcaster interface {
	Broadcast(topic string, data any)
}

// Target identifies one node to poll and how. Supplied by the Lister each cycle.
type Target struct {
	Pubkey       []byte
	CompanionID  string   // which companion sends the requests
	Kind         string   // selects the Collector ("repeater", "sensor", …)
	IntervalSecs int64    // per-node cadence; 0 = use defaultInterval
	RetrySecs    int64    // re-attempt delay after a failed poll; 0 = default
	MaxRetries   int      // consecutive failed re-attempts before normal cadence; 0 = default, <0 = no retries
	Probes       []string // request bundles to run; nil/empty = all
}

// Lister supplies the current monitor targets; called once per cycle, so changes take effect on the next tick.
type Lister interface {
	Targets(ctx context.Context) ([]Target, error)
}

// ListerFunc adapts a function to the Lister interface.
type ListerFunc func(ctx context.Context) ([]Target, error)

func (f ListerFunc) Targets(ctx context.Context) ([]Target, error) { return f(ctx) }

// Reading is one decoded value; Channel is the CayenneLPP channel for sensor readings (0 when there is none).
type Reading struct {
	Metric  string
	Channel int
	Value   float64
}

// NeighborSample is one observed neighbour SNR a Collector reports.
type NeighborSample struct {
	Pubkey []byte
	SNR    *float64
}

// CollectResult is what a Collector returns for one poll of one node.
type CollectResult struct {
	// Name, if non-empty, updates the node's display name in node_state.
	Name      string
	Readings  []Reading
	Neighbors []NeighborSample
	// RetryFailure counts a poll that returned valid data as failed for retry scheduling (e.g. a trace that timed out).
	RetryFailure bool
}

// Collector polls one kind of node; implementations live in the app layer and register via Service.RegisterCollector.
type Collector interface {
	// Collect must honour ctx; an error marks the poll failed and leaves history untouched.
	Collect(ctx context.Context, t Target) (*CollectResult, error)
}

// Service is the poller engine.
type Service struct {
	st     *store.Store
	bc     Broadcaster
	log    *slog.Logger
	lister Lister

	mu         sync.RWMutex
	collectors map[string]Collector
	nextDue    map[string]time.Time // keyed by pubkey hex
	failures   map[string]int       // consecutive failed polls, keyed by pubkey hex

	// pollMu keeps only one collector round-trip on the air at a time.
	pollMu sync.Mutex
}

// New builds a Service. Register a Collector per node kind, then call Start.
func New(st *store.Store, bc Broadcaster, lister Lister, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		st:         st,
		bc:         bc,
		log:        log.With("component", "monitor"),
		lister:     lister,
		collectors: make(map[string]Collector),
		nextDue:    make(map[string]time.Time),
		failures:   make(map[string]int),
	}
}

// rescheduleAfter sets the next due time: a failed poll retries fast up to maxRetries times, then resumes the normal interval.
func (s *Service) rescheduleAfter(key string, t Target, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	interval := defaultInterval
	if t.IntervalSecs > 0 {
		interval = time.Duration(t.IntervalSecs) * time.Second
	}
	if ok {
		delete(s.failures, key)
	} else {
		s.failures[key]++
		// MaxRetries: 0 = poller default, negative (the UI's "None" sends -1) = no fast re-attempt.
		maxRetries := defaultMaxRetries
		if t.MaxRetries < 0 {
			maxRetries = 0
		} else if t.MaxRetries > 0 {
			maxRetries = t.MaxRetries
		}
		if s.failures[key] <= maxRetries {
			interval = retryInterval
			if t.RetrySecs > 0 {
				interval = time.Duration(t.RetrySecs) * time.Second
			}
		} else {
			if maxRetries > 0 {
				s.log.Info("monitor giving up after consecutive failures, resuming normal interval",
					"pubkey", key, "failures", s.failures[key], "next", interval)
			}
			delete(s.failures, key)
		}
	}
	s.nextDue[key] = time.Now().Add(interval)
}

// Targets returns the nodes whose monitor toggle is on right now.
func (s *Service) Targets(ctx context.Context) ([]Target, error) {
	return s.lister.Targets(ctx)
}

// AirtimeLock lets other radio-driving services serialize against polls; lock per RF operation, never for a whole test run.
func (s *Service) AirtimeLock() *sync.Mutex {
	return &s.pollMu
}

// RegisterCollector wires a Collector for a node kind; re-registering a kind replaces it.
func (s *Service) RegisterCollector(kind string, c Collector) {
	s.mu.Lock()
	s.collectors[kind] = c
	s.mu.Unlock()
}

// Start launches the scheduler and prune goroutines and returns immediately.
func (s *Service) Start(ctx context.Context) {
	s.log.Info("node monitoring started", "defaultInterval", defaultInterval, "stagger", stagger, "retention", retention)
	go s.scheduleLoop(ctx)
	go s.pruneLoop(ctx)
}

// scheduleLoop polls due monitors sequentially so one cycle never overlaps the next.
func (s *Service) scheduleLoop(ctx context.Context) {
	s.runCycle(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollTick):
			s.runCycle(ctx)
		}
	}
}

func (s *Service) runCycle(ctx context.Context) {
	targets, err := s.lister.Targets(ctx)
	if err != nil {
		s.log.Error("listing monitor targets", "error", err)
		return
	}

	now := time.Now()
	first := true
	for _, t := range targets {
		key := hex.EncodeToString(t.Pubkey)
		s.mu.RLock()
		due := s.nextDue[key]
		s.mu.RUnlock()
		if !due.IsZero() && now.Before(due) {
			continue
		}

		if !first && stagger > 0 {
			if !sleepCtx(ctx, stagger) {
				return
			}
		}
		first = false

		s.pollMu.Lock()
		retryFailure, err := s.poll(ctx, t)
		s.pollMu.Unlock()

		s.rescheduleAfter(key, t, err == nil && !retryFailure)
	}
}

// poll runs one Collector call and persists/broadcasts the result; callers must hold s.pollMu.
func (s *Service) poll(ctx context.Context, t Target) (retryFailure bool, err error) {
	s.mu.RLock()
	collector := s.collectors[t.Kind]
	s.mu.RUnlock()

	pollTS := time.Now().Unix()
	state := store.NodeState{
		Pubkey:      t.Pubkey,
		CompanionID: t.CompanionID,
		Kind:        t.Kind,
		LastPollTS:  pollTS,
	}

	if collector == nil {
		state.LastError = "no collector registered for kind " + t.Kind
		s.log.Debug("no collector for target", "kind", t.Kind, "pubkey", hex.EncodeToString(t.Pubkey))
		s.persistState(state)
		return false, fmt.Errorf("%s", state.LastError)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	res, err := collector.Collect(reqCtx, t)
	cancel()
	if err != nil {
		state.LastError = err.Error()
		s.log.Warn("monitor poll failed", "kind", t.Kind, "pubkey", hex.EncodeToString(t.Pubkey), "error", err)
		s.persistState(state)
		return false, err
	}

	state.LastOkTS = pollTS
	if res.Name != "" {
		state.Name = res.Name
	}

	metrics := make([]store.Metric, 0, len(res.Readings))
	snapshot := make(map[string]float64, len(res.Readings))
	for _, r := range res.Readings {
		metrics = append(metrics, store.Metric{
			TS:      pollTS,
			Pubkey:  t.Pubkey,
			Metric:  r.Metric,
			Channel: r.Channel,
			Value:   r.Value,
		})
		snapshot[r.Metric] = r.Value
	}
	if blob, mErr := json.Marshal(snapshot); mErr == nil {
		state.State = string(blob)
	}

	neighbors := make([]store.Neighbor, 0, len(res.Neighbors))
	for _, n := range res.Neighbors {
		neighbors = append(neighbors, store.Neighbor{
			TS:             pollTS,
			Pubkey:         t.Pubkey,
			NeighborPubkey: n.Pubkey,
			SNR:            n.SNR,
		})
	}

	// The poll ctx is cancelled when poll() returns, so the async closure must not capture it.
	s.st.WriteAsync(func() {
		if err := s.st.Metrics.RecordMetrics(context.Background(), metrics); err != nil {
			s.log.Error("recording metrics", "error", err)
		}
		if err := s.st.Metrics.RecordNeighbors(context.Background(), neighbors); err != nil {
			s.log.Error("recording neighbors", "error", err)
		}
		if err := s.st.Metrics.UpsertNodeState(context.Background(), &state); err != nil {
			s.log.Error("upserting node state", "error", err)
		}
	})

	if s.bc != nil {
		s.bc.Broadcast(WSTopicMetrics, map[string]any{
			"pubkey":      hex.EncodeToString(t.Pubkey),
			"companionId": t.CompanionID,
			"kind":        t.Kind,
			"name":        state.Name,
			"ts":          pollTS,
			"metrics":     snapshot,
		})
	}
	return res.RetryFailure, nil
}

// PollNow polls one node out of band and resets its schedule; it errors if the node isn't monitored or a poll is in flight.
func (s *Service) PollNow(ctx context.Context, pubkey []byte) error {
	if !s.pollMu.TryLock() {
		return fmt.Errorf("a poll is already in progress; try again shortly")
	}
	defer s.pollMu.Unlock()

	targets, err := s.lister.Targets(ctx)
	if err != nil {
		return fmt.Errorf("listing monitor targets: %w", err)
	}
	key := hex.EncodeToString(pubkey)
	var target *Target
	for i := range targets {
		if hex.EncodeToString(targets[i].Pubkey) == key {
			target = &targets[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("node is not currently monitored")
	}

	retryFailure, pollErr := s.poll(ctx, *target)
	s.rescheduleAfter(key, *target, pollErr == nil && !retryFailure)
	return pollErr
}

// persistState records a poll failure without clobbering the last-known-good snapshot.
func (s *Service) persistState(state store.NodeState) {
	s.st.WriteAsync(func() {
		if err := s.st.Metrics.MarkPollFailure(context.Background(), &state); err != nil {
			s.log.Error("recording poll failure", "error", err)
		}
	})
}

// pruneLoop periodically drops time-series rows older than the retention window.
func (s *Service) pruneLoop(ctx context.Context) {
	if !sleepCtx(ctx, time.Minute) {
		return
	}
	for {
		s.prune()
		select {
		case <-ctx.Done():
			return
		case <-time.After(pruneInterval):
		}
	}
}

func (s *Service) prune() {
	cutoff := time.Now().Add(-retention).Unix()
	s.st.WriteAsync(func() {
		removed, err := s.st.Metrics.PruneMetrics(context.Background(), cutoff)
		if err != nil {
			s.log.Error("pruning metrics", "error", err)
			return
		}
		if removed > 0 {
			s.log.Info("pruned old metrics", "rows", removed, "cutoff", cutoff)
		}
	})
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
