package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/OwlShack/OwlShack/internal/monitor"
	"github.com/OwlShack/OwlShack/internal/store"
)

// linkMonitorDefaultIntervalSecs is the cadence for a link monitor that doesn't override it.
const linkMonitorDefaultIntervalSecs = 900

// newMergedLister concatenates Listers because monitor.Service takes only one.
func newMergedLister(ls ...monitor.Lister) monitor.ListerFunc {
	return func(ctx context.Context) ([]monitor.Target, error) {
		var out []monitor.Target
		for _, l := range ls {
			t, err := l.Targets(ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, t...)
		}
		return out, nil
	}
}

// newLinkLister puts each row's synthetic key in Target.Pubkey; a link whose companion is mid-reload is skipped for the cycle.
func newLinkLister(reg *companionRegistry, db *store.Store) monitor.ListerFunc {
	return func(ctx context.Context) ([]monitor.Target, error) {
		links, err := db.LinkMonitors.ListEnabled(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing enabled link monitors: %w", err)
		}
		targets := make([]monitor.Target, 0, len(links))
		for _, l := range links {
			companion, err := db.Companions.Get(ctx, l.CompanionID)
			if err != nil || companion == nil {
				continue
			}
			interval := l.IntervalSecs
			if interval <= 0 {
				interval = linkMonitorDefaultIntervalSecs
			}
			targets = append(targets, monitor.Target{
				Pubkey:       l.Key,
				CompanionID:  companion.Name,
				Kind:         "link",
				IntervalSecs: int64(interval),
				RetrySecs:    int64(l.RetrySecs),
				MaxRetries:   l.MaxRetries,
			})
		}
		return targets, nil
	}
}

// linkCollector traces a monitored link; a timed-out trace is a measurement (packet loss), not a failed poll.
type linkCollector struct {
	reg *companionRegistry
	db  *store.Store
	log *slog.Logger
}

func newLinkCollector(reg *companionRegistry, db *store.Store, log *slog.Logger) *linkCollector {
	if log == nil {
		log = slog.Default()
	}
	return &linkCollector{reg: reg, db: db, log: log.With("component", "link-collector")}
}

// Collect already runs under monitor.Service's pollMu, which is the signal-test airtime lock: taking it again deadlocks.
func (lc *linkCollector) Collect(ctx context.Context, t monitor.Target) (*monitor.CollectResult, error) {
	lm, err := lc.db.LinkMonitors.GetByKey(ctx, t.Pubkey)
	if err != nil {
		return nil, fmt.Errorf("loading link monitor: %w", err)
	}
	if lm == nil {
		return nil, fmt.Errorf("link monitor not found")
	}
	c, ok := lc.reg.find(t.CompanionID)
	if !ok {
		return nil, fmt.Errorf("companion %q is not running", t.CompanionID)
	}

	out, err := c.RunTrace(ctx, lm.Path, uint8(lm.PathHashSize))
	if err != nil {
		return nil, fmt.Errorf("trace: %w", err)
	}

	name := lm.Label
	if name == "" {
		name = "link"
	}
	// A timed-out trace asks for a fast retry only when the user opted in (MaxRetries > 0).
	res := &monitor.CollectResult{Name: name, RetryFailure: lm.MaxRetries > 0 && !out.Complete}

	ok01 := 0.0
	if out.Complete {
		ok01 = 1
	}
	res.Readings = append(res.Readings, monitor.Reading{Metric: "success", Value: ok01})
	for i, snr := range out.HopSNRs {
		res.Readings = append(res.Readings, monitor.Reading{Metric: fmt.Sprintf("snr_hop%d", i+1), Value: snr})
	}
	if out.Complete {
		res.Readings = append(res.Readings, monitor.Reading{Metric: "elapsed_ms", Value: float64(out.Elapsed.Milliseconds())})
		if out.SNR != nil {
			res.Readings = append(res.Readings, monitor.Reading{Metric: "last_snr", Value: *out.SNR})
		}
	}
	return res, nil
}

var _ monitor.Collector = (*linkCollector)(nil)
