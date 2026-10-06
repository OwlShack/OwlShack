package app

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/OwlShack/OwlShack/internal/client/repeater"
	"github.com/OwlShack/OwlShack/internal/monitor"
	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/store"
	"github.com/OwlShack/OwlShack/internal/telemetry"
)

// Per-request timeouts for a monitor poll; login is a flood round-trip, so it gets more.
const (
	monitorLoginTimeout = 15 * time.Second
	monitorReqTimeout   = 12 * time.Second
	// monitorRequestGap spaces round-trips within one poll; back-to-back requests collide on air.
	monitorRequestGap = 1500 * time.Millisecond
	// monitorProbeAttempts: RF round-trips drop silently, so one in-poll retry beats waiting a whole cycle.
	monitorProbeAttempts = 2
)

// gap pauses between sub-requests, returning early if the context is cancelled.
func gap(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(monitorRequestGap):
	}
}

// companionRegistry is a reload-surviving handle to the current companions, so collectors resolve by name instead of capturing a slice.
type companionRegistry struct {
	mu     sync.RWMutex
	byName map[string]*companion.Companion
}

func newCompanionRegistry() *companionRegistry {
	return &companionRegistry{byName: map[string]*companion.Companion{}}
}

func (r *companionRegistry) set(companions []*companion.Companion) {
	m := make(map[string]*companion.Companion, len(companions))
	for _, c := range companions {
		m[c.Name()] = c
	}
	r.mu.Lock()
	r.byName = m
	r.mu.Unlock()
}

// all returns the CURRENT set, which a reload replaces wholesale.
func (r *companionRegistry) all() []*companion.Companion {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*companion.Companion, 0, len(r.byName))
	for _, c := range r.byName {
		out = append(out, c)
	}
	return out
}

func (r *companionRegistry) find(name string) (*companion.Companion, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.byName[name]
	return c, ok
}

// newContactLister scans contacts for the monitor flag, deduped by pubkey so a node shared by two companions isn't polled twice.
func newContactLister(reg *companionRegistry, db *store.Store) monitor.ListerFunc {
	return func(ctx context.Context) ([]monitor.Target, error) {
		reg.mu.RLock()
		comps := make(map[string]*companion.Companion, len(reg.byName))
		for n, c := range reg.byName {
			comps[n] = c
		}
		reg.mu.RUnlock()

		seen := make(map[string]bool)
		var targets []monitor.Target
		for name, c := range comps {
			contacts, err := db.Contacts.List(ctx, c.ID())
			if err != nil {
				// Fail the listing rather than drop a companion's nodes, which would read as "not monitored".
				return nil, fmt.Errorf("listing contacts for %q: %w", name, err)
			}
			for _, ct := range contacts {
				if !ct.Metadata.Monitor {
					continue
				}
				key := hex.EncodeToString(ct.PeerPubKey)
				if seen[key] {
					continue
				}
				kind := monitorKind(c, ct)
				if kind == "" {
					continue
				}
				seen[key] = true
				targets = append(targets, monitor.Target{
					Pubkey:       ct.PeerPubKey,
					CompanionID:  name,
					Kind:         kind,
					IntervalSecs: ct.Metadata.MonitorIntervalSecs,
					RetrySecs:    ct.Metadata.MonitorRetrySecs,
					MaxRetries:   ct.Metadata.MonitorMaxRetries,
					Probes:       ct.Metadata.MonitorProbes,
				})
			}
		}
		return targets, nil
	}
}

// monitorKind trusts the advertised type over the isRepeater flag: companion firmware has no login handler, so that poll only times out.
func monitorKind(c *companion.Companion, ct store.Contact) string {
	var peerType string
	if c != nil {
		if id, err := meshcore.NewIdentityFromBytes(ct.PeerPubKey); err == nil {
			if p := c.Node().Peers().Lookup(id.PublicKey()); p != nil {
				peerType = strings.ToUpper(p.Type)
			}
		}
	}
	if peerType == "" {
		// Advert not heard yet: the contact row caches the type the operator or a past advert gave it.
		peerType = strings.ToUpper(ct.Type)
	}
	switch peerType {
	case "REPEATER":
		return "repeater"
	case "SENSOR":
		// Sensors answer sessionless GET_TELEMETRY_DATA for any ACL client; there is no status or neighbour data.
		return "companion"
	case "ROOM", "ROOM_SERVER":
		return "companion"
	case "CHAT", "COMPANION":
		return "companion"
	}
	if ct.Metadata.IsRepeater {
		return "repeater"
	}
	return ""
}

// repeaterCollector owns the session: it logs in when there is none, and re-logs in once when a stale session fails the status request.
type repeaterCollector struct {
	reg *companionRegistry
	db  *store.Store
	log *slog.Logger
}

func newRepeaterCollector(reg *companionRegistry, db *store.Store, log *slog.Logger) *repeaterCollector {
	if log == nil {
		log = slog.Default()
	}
	return &repeaterCollector{reg: reg, db: db, log: log.With("component", "repeater-collector")}
}

func (rc *repeaterCollector) Collect(ctx context.Context, t monitor.Target) (*monitor.CollectResult, error) {
	c, ok := rc.reg.find(t.CompanionID)
	if !ok {
		return nil, fmt.Errorf("companion %q is not running", t.CompanionID)
	}
	client := c.Repeaters()
	pubkeyHex := hex.EncodeToString(t.Pubkey)

	doStatus := probeEnabled(t.Probes, "status")
	doTelemetry := probeEnabled(t.Probes, "telemetry")
	doNeighbors := probeEnabled(t.Probes, "neighbors")

	hadSession := client.Session(pubkeyHex) != nil
	if !hadSession {
		if err := rc.login(ctx, client, t, c.ID()); err != nil {
			return nil, fmt.Errorf("login: %w", err)
		}
	}

	res := &monitor.CollectResult{Name: nodeName(c, t.Pubkey)}
	first := true // first radio probe drives stale-session recovery

	if doStatus {
		status, err := client.SendStatusReq(pubkeyHex, monitorReqTimeout)
		if err != nil && hadSession {
			// A pre-existing session went stale (reboot or expiry): drop it, re-login once, retry.
			client.Logout(pubkeyHex)
			if lerr := rc.login(ctx, client, t, c.ID()); lerr != nil {
				return nil, fmt.Errorf("re-login after stale session: %w", lerr)
			}
			status, err = client.SendStatusReq(pubkeyHex, monitorReqTimeout)
		}
		if err != nil {
			return nil, fmt.Errorf("status: %w", err)
		}
		res.Readings = append(res.Readings, statusReadings(status)...)
		first = false
	}

	// Telemetry and neighbours are best-effort: a failure must not drop the readings already collected.
	if doTelemetry {
		if !first {
			gap(ctx)
		}
		if tel, terr := retryProbe(ctx, rc.log, "telemetry", pubkeyHex, func() (*telemetry.Telemetry, error) {
			return client.SendTelemetryReq(pubkeyHex, monitorReqTimeout)
		}); terr == nil {
			res.Readings = append(res.Readings, telemetryReadings(tel)...)
		} else {
			rc.log.Debug("telemetry poll failed", "pubkey", pubkeyHex, "error", terr)
		}
		first = false
	}

	if doNeighbors {
		if !first {
			gap(ctx)
		}
		if nb, nerr := retryProbe(ctx, rc.log, "neighbors", pubkeyHex, func() (*repeater.Neighbors, error) {
			return client.SendNeighborsReq(pubkeyHex, 32, 0, monitorReqTimeout)
		}); nerr == nil {
			res.Readings = append(res.Readings, monitor.Reading{Metric: "neighbor_count", Value: float64(nb.TotalCount)})
			res.Neighbors = neighborSamples(nb)
		} else {
			rc.log.Debug("neighbors poll failed", "pubkey", pubkeyHex, "error", nerr)
		}
	}

	return res, nil
}

// retryProbe spaces retries by the request gap, so a retry doesn't collide with the request that just timed out.
func retryProbe[T any](ctx context.Context, log *slog.Logger, label, pubkeyHex string, fn func() (T, error)) (T, error) {
	var (
		zero T
		last error
	)
	for attempt := 1; attempt <= monitorProbeAttempts; attempt++ {
		v, err := fn()
		if err == nil {
			return v, nil
		}
		last = err
		if attempt < monitorProbeAttempts {
			log.Debug(label+" probe failed, retrying", "pubkey", pubkeyHex, "attempt", attempt, "error", err)
			gap(ctx)
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
		}
	}
	return zero, last
}

// probeEnabled treats an empty or nil probe set as "all".
func probeEnabled(probes []string, name string) bool {
	if len(probes) == 0 {
		return true
	}
	for _, p := range probes {
		if p == name {
			return true
		}
	}
	return false
}

// login uses the admin password stored on the contact, blank when the repeater has none.
func (rc *repeaterCollector) login(ctx context.Context, client *repeater.Client, t monitor.Target, companionID int64) error {
	password := ""
	if contact, err := rc.db.Contacts.Get(ctx, companionID, t.Pubkey); err == nil && contact != nil {
		password = contact.Metadata.RepeaterPassword
	}
	_, err := client.SendLogin(hex.EncodeToString(t.Pubkey), password, monitorLoginTimeout)
	return err
}

// nodeName returns "" when the name is unknown, in which case node_state keeps its previous one.
func nodeName(c *companion.Companion, pubkey []byte) string {
	id, err := meshcore.NewIdentityFromBytes(pubkey)
	if err != nil {
		return ""
	}
	if p := c.Node().Peers().Lookup(id.PublicKey()); p != nil {
		return p.Name
	}
	return ""
}

// statusReadings: everything lands on channel 0; SNR is already real dB, RSSI and noise are dBm.
func statusReadings(s *repeater.Status) []monitor.Reading {
	return []monitor.Reading{
		{Metric: "battery_mv", Value: float64(s.BatteryMV)},
		{Metric: "queue_len", Value: float64(s.QueueLen)},
		{Metric: "noise_floor", Value: float64(s.NoiseFloor)},
		{Metric: "last_rssi", Value: float64(s.LastRSSI)},
		{Metric: "last_snr", Value: s.LastSNR},
		{Metric: "packets_recv", Value: float64(s.PacketsRecv)},
		{Metric: "packets_sent", Value: float64(s.PacketsSent)},
		{Metric: "tx_air_secs", Value: float64(s.TxAirSecs)},
		{Metric: "rx_air_secs", Value: float64(s.RxAirSecs)},
		{Metric: "uptime", Value: float64(s.UptimeSecs)},
		{Metric: "flood_tx", Value: float64(s.FloodTx)},
		{Metric: "direct_tx", Value: float64(s.DirectTx)},
		{Metric: "flood_rx", Value: float64(s.FloodRx)},
		{Metric: "direct_rx", Value: float64(s.DirectRx)},
		{Metric: "err_events", Value: float64(s.ErrEvents)},
		{Metric: "recv_errors", Value: float64(s.RecvErrors)},
		{Metric: "direct_dups", Value: float64(s.DirectDups)},
		{Metric: "flood_dups", Value: float64(s.FloodDups)},
		{Metric: "chan_util", Value: s.ChanUtil},
	}
}

// telemetryReadings only maps an already-decoded payload; LPP decode, naming and keying live in internal/telemetry.
func telemetryReadings(t *telemetry.Telemetry) []monitor.Reading {
	metrics := t.Metrics()
	out := make([]monitor.Reading, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, monitor.Reading{Metric: m.Key, Channel: m.Channel, Value: m.Value})
	}
	return out
}

// neighborSamples converts the firmware's x4 quarter-dB SNR to the real dB we store.
func neighborSamples(nb *repeater.Neighbors) []monitor.NeighborSample {
	out := make([]monitor.NeighborSample, 0, len(nb.Neighbors))
	for _, n := range nb.Neighbors {
		prefix, err := hex.DecodeString(n.PubkeyPrefix)
		if err != nil {
			continue
		}
		snr := n.SNR
		out = append(out, monitor.NeighborSample{Pubkey: prefix, SNR: &snr})
	}
	return out
}

// companionCollector gathers telemetry only: companions answer the sessionless telemetry request but no admin requests.
type companionCollector struct {
	reg *companionRegistry
	log *slog.Logger
}

func newCompanionCollector(reg *companionRegistry, log *slog.Logger) *companionCollector {
	if log == nil {
		log = slog.Default()
	}
	return &companionCollector{reg: reg, log: log.With("component", "companion-collector")}
}

func (cc *companionCollector) Collect(ctx context.Context, t monitor.Target) (*monitor.CollectResult, error) {
	c, ok := cc.reg.find(t.CompanionID)
	if !ok {
		return nil, fmt.Errorf("companion %q is not running", t.CompanionID)
	}
	res := &monitor.CollectResult{Name: nodeName(c, t.Pubkey)}

	// Telemetry is all a companion answers.
	if !probeEnabled(t.Probes, "telemetry") {
		return res, nil
	}

	pubkeyHex := hex.EncodeToString(t.Pubkey)
	tel, err := retryProbe(ctx, cc.log, "telemetry", pubkeyHex, func() (*telemetry.Telemetry, error) {
		return c.Repeaters().SendContactTelemetryReq(pubkeyHex, monitorReqTimeout)
	})
	if err != nil {
		return nil, fmt.Errorf("telemetry: %w", err)
	}
	res.Readings = telemetryReadings(tel)
	return res, nil
}

var (
	_ monitor.Collector = (*repeaterCollector)(nil)
	_ monitor.Collector = (*companionCollector)(nil)
)
