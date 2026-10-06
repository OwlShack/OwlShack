package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OwlShack/OwlShack/internal/monitor"
	"github.com/OwlShack/OwlShack/internal/store"
)

// NodePoller is the monitor-service seam, implemented by *monitor.Service and installed via Server.SetPoller.
type NodePoller interface {
	PollNow(ctx context.Context, pubkey []byte) error
	Targets(ctx context.Context) ([]monitor.Target, error)
}

// Monitoring is toggled through the contact-metadata PATCH, so these endpoints are read-only.

// handleListMonitoredNodes lists the poller's target set, merged with the latest node_state snapshot.
func (s *Server) handleListMonitoredNodes(w http.ResponseWriter, r *http.Request) {
	poller := s.pollerRef()
	if poller == nil {
		writeError(w, http.StatusServiceUnavailable, "monitor not ready")
		return
	}
	targets, err := poller.Targets(r.Context())
	if err != nil {
		s.serverError(w, "failed to list monitored nodes", err)
		return
	}
	states, err := s.store.Metrics.ListNodeStates(r.Context())
	if err != nil {
		s.serverError(w, "failed to list node states", err)
		return
	}
	stateByKey := make(map[string]*store.NodeState, len(states))
	for i := range states {
		stateByKey[hex.EncodeToString(states[i].Pubkey)] = &states[i]
	}

	type nodeJSON struct {
		PubKey       string             `json:"pubkey"`
		CompanionID  string             `json:"companionId"`
		Kind         string             `json:"kind"`
		Name         string             `json:"name"`
		LastPollTS   int64              `json:"lastPollTs"`
		LastOkTS     int64              `json:"lastOkTs"`
		LastError    string             `json:"lastError,omitempty"`
		IntervalSecs int64              `json:"intervalSecs"`
		Metrics      map[string]float64 `json:"metrics"`
	}

	out := make([]nodeJSON, 0, len(targets))
	for _, t := range targets {
		key := hex.EncodeToString(t.Pubkey)
		// The target carries the contact's override, or 0 for the scheduler default.
		interval := monitor.DefaultIntervalSecs
		if t.IntervalSecs > 0 {
			interval = t.IntervalSecs
		}
		n := nodeJSON{
			PubKey:       key,
			CompanionID:  t.CompanionID,
			Kind:         t.Kind,
			IntervalSecs: interval,
			Metrics:      map[string]float64{},
		}
		if st := stateByKey[key]; st != nil {
			n.Name = st.Name
			n.LastPollTS = st.LastPollTS
			n.LastOkTS = st.LastOkTS
			n.LastError = st.LastError
			if st.State != "" {
				if err := json.Unmarshal([]byte(st.State), &n.Metrics); err != nil {
					s.log.Warn("corrupt node_state snapshot", "pubkey", key, "error", err)
				}
			}
		}
		// node_state.name is only a snapshot from the last poll, so read a link's label live.
		if t.Kind == "link" {
			if lm, err := s.store.LinkMonitors.GetByKey(r.Context(), t.Pubkey); err == nil && lm != nil && lm.Label != "" {
				n.Name = lm.Label
			} else if n.Name == "" {
				n.Name = "link"
			}
		} else if n.Name == "" {
			// node_state only learns the name from a successful poll, so an unpolled node needs a fallback.
			if p, err := s.store.Peers.GetByPubKey(r.Context(), t.Pubkey); err == nil && p != nil {
				n.Name = p.Name
			}
		}
		out = append(out, n)
	}
	// Stable order regardless of the lister's map iteration.
	slices.SortFunc(out, func(a, b nodeJSON) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.PubKey, b.PubKey)
	})
	writeJSON(w, http.StatusOK, out)
}

// neighborLinkFreshness: the default poll interval is 6h, so 24h tolerates a few missed cycles.
const neighborLinkFreshness = 24 * time.Hour

// handleListNeighborLinks merges the two one-way neighbour views into one entry, dropping unlocatable ends.
func (s *Server) handleListNeighborLinks(w http.ResponseWriter, r *http.Request) {
	since := time.Now().Add(-neighborLinkFreshness).Unix()
	rows, err := s.store.Metrics.ListLatestNeighbors(r.Context(), since)
	if err != nil {
		s.serverError(w, "failed to query neighbor links", err)
		return
	}

	type neighborLinkJSON struct {
		APubKey string   `json:"aPubkey"`
		AName   string   `json:"aName"`
		ALat    int32    `json:"aLat"`
		ALon    int32    `json:"aLon"`
		BPubKey string   `json:"bPubkey"`
		BName   string   `json:"bName"`
		BLat    int32    `json:"bLat"`
		BLon    int32    `json:"bLon"`
		SNRAtoB *float64 `json:"snrAtoB,omitempty"`
		SNRBtoA *float64 `json:"snrBtoA,omitempty"`
		TS      int64    `json:"ts"`
	}

	links := make(map[string]*neighborLinkJSON)
	for _, n := range rows {
		from, err := s.store.Peers.GetByPubKey(r.Context(), n.Pubkey)
		if err != nil {
			s.serverError(w, "failed to resolve neighbor link observer", err)
			return
		}
		if from == nil || !from.HasLocation() {
			continue
		}
		to, err := s.store.Peers.FindByPrefix(r.Context(), n.NeighborPubkey)
		if err != nil {
			s.serverError(w, "failed to resolve neighbor link peer", err)
			return
		}
		if to == nil || !to.HasLocation() {
			continue
		}

		fromHex := hex.EncodeToString(from.PubKey)
		toHex := hex.EncodeToString(to.PubKey)
		if fromHex == toHex {
			continue // can't happen, but guard against a degenerate self-link
		}

		aHex, bHex := fromHex, toHex
		aPeer, bPeer := from, to
		fromIsA := true
		if bHex < aHex {
			aHex, bHex = bHex, aHex
			aPeer, bPeer = bPeer, aPeer
			fromIsA = false
		}
		key := aHex + "|" + bHex

		link, ok := links[key]
		if !ok {
			link = &neighborLinkJSON{
				APubKey: aHex,
				AName:   aPeer.Name,
				ALat:    aPeer.Lat,
				ALon:    aPeer.Lon,
				BPubKey: bHex,
				BName:   bPeer.Name,
				BLat:    bPeer.Lat,
				BLon:    bPeer.Lon,
			}
			links[key] = link
		}
		if n.TS > link.TS {
			link.TS = n.TS
		}
		// n.SNR is how well the observer heard the neighbour, so it is the to→from direction.
		if fromIsA {
			link.SNRBtoA = n.SNR
		} else {
			link.SNRAtoB = n.SNR
		}
	}

	out := make([]neighborLinkJSON, 0, len(links))
	for _, l := range links {
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b neighborLinkJSON) int {
		if c := strings.Compare(strings.ToLower(a.AName), strings.ToLower(b.AName)); c != 0 {
			return c
		}
		if c := strings.Compare(strings.ToLower(a.BName), strings.ToLower(b.BName)); c != 0 {
			return c
		}
		return strings.Compare(a.APubKey, b.APubKey)
	})
	writeJSON(w, http.StatusOK, out)
}

// handleListNodeMetricNames returns the distinct metric names recorded for a node.
func (s *Server) handleListNodeMetricNames(w http.ResponseWriter, r *http.Request) {
	pubkey, err := hex.DecodeString(r.PathValue("pubkey"))
	if err != nil || len(pubkey) == 0 {
		writeError(w, http.StatusBadRequest, "invalid pubkey hex")
		return
	}
	names, err := s.store.Metrics.ListMetricNames(r.Context(), pubkey)
	if err != nil {
		s.serverError(w, "failed to query node metrics", err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, names)
}

// handleNodeHistory takes metric (required), from/to (unix seconds) and bucket (seconds, default 300).
func (s *Server) handleNodeHistory(w http.ResponseWriter, r *http.Request) {
	pubkey, err := hex.DecodeString(r.PathValue("pubkey"))
	if err != nil || len(pubkey) == 0 {
		writeError(w, http.StatusBadRequest, "invalid pubkey hex")
		return
	}
	q := r.URL.Query()
	metric := q.Get("metric")
	if metric == "" {
		writeError(w, http.StatusBadRequest, "metric query parameter required")
		return
	}

	from := parseInt64(q.Get("from"), 0)
	to := parseInt64(q.Get("to"), 1<<62)
	bucket := parseInt64(q.Get("bucket"), 300)

	points, err := s.store.Metrics.QueryHistory(r.Context(), pubkey, metric, from, to, bucket)
	if err != nil {
		s.serverError(w, "failed to query node history", err)
		return
	}
	if points == nil {
		points = []store.HistoryPoint{}
	}
	writeJSON(w, http.StatusOK, points)
}

// handlePollNode blocks for the RF round-trip; fresh metrics arrive on the "metrics" WS topic.
func (s *Server) handlePollNode(w http.ResponseWriter, r *http.Request) {
	pubkey, err := hex.DecodeString(r.PathValue("pubkey"))
	if err != nil || len(pubkey) == 0 {
		writeError(w, http.StatusBadRequest, "invalid pubkey hex")
		return
	}
	poller := s.pollerRef()
	if poller == nil {
		writeError(w, http.StatusServiceUnavailable, "monitor not ready")
		return
	}
	if err := poller.PollNow(r.Context(), pubkey); err != nil {
		s.log.Error("manual node poll", "error", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func parseInt64(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}
