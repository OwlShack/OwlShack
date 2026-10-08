package repeater

import (
	"bytes"
	"encoding/hex"
	"sort"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
)

// Stats is a live snapshot of the repeater's relay activity, for the API.
type Stats struct {
	Name             string   `json:"name"`
	PubKey           string   `json:"pubkey"`
	UptimeSecs       int64    `json:"uptimeSecs"`
	PacketsReceived  uint64   `json:"packetsReceived"`
	PacketsForwarded uint64   `json:"packetsForwarded"`
	TxQueueLen       int      `json:"txQueueLen"`
	Neighbors        int      `json:"neighbors"`
	Latitude         float64  `json:"latitude"`
	Longitude        float64  `json:"longitude"`
	LastSNR          *float64 `json:"lastSnr"`    // dB
	LastRSSI         *int     `json:"lastRssi"`   // dBm
	NoiseFloor       *int     `json:"noiseFloor"` // dBm
	BatteryMV        *int     `json:"batteryMv"`
	RxAirSecs        uint64   `json:"rxAirSecs"`
	TxAirSecs        uint64   `json:"txAirSecs"`
	FloodTx          uint64   `json:"floodTx"`
	DirectTx         uint64   `json:"directTx"`
	FloodRx          uint64   `json:"floodRx"`
	DirectRx         uint64   `json:"directRx"`
	FloodDups        uint64   `json:"floodDups"`
	DirectDups       uint64   `json:"directDups"`
}

// NeighborInfo is a directly-heard repeater, for the API neighbours list.
type NeighborInfo struct {
	PubKey  string  `json:"pubkey"`
	Name    string  `json:"name"`
	SNR     float64 `json:"snr"`
	SecsAgo int64   `json:"secsAgo"`
}

// batteryReading is nil on a host with no cell; the binary STATUS reply keeps its 0, fixed firmware layout.
func (r *Repeater) batteryReading() *int {
	if !r.haveBattery.Load() {
		return nil
	}
	batt := int(r.batteryMV.Load())
	return &batt
}

func (r *Repeater) Stats() Stats {
	r.mu.Lock()
	started := r.startedAt
	r.mu.Unlock()

	uptime := int64(0)
	if !started.IsZero() {
		uptime = int64(time.Since(started).Seconds())
	}

	r.neighbors.Lock()
	nCount := len(r.neighbors.m)
	r.neighbors.Unlock()

	rc := r.routeCounters()
	s := Stats{
		Name:             r.cfg.Name,
		PubKey:           hex.EncodeToString(r.node.Identity().PublicKeyBytes()),
		UptimeSecs:       uptime,
		PacketsReceived:  r.recvCount.Load(),
		PacketsForwarded: r.fwdCount.Load(),
		TxQueueLen:       r.node.TxQueueLen(),
		Neighbors:        nCount,
		RxAirSecs:        r.rxAirtimeMs.Load() / 1000,
		TxAirSecs:        r.txAirtimeMs.Load() / 1000,
		FloodTx:          r.sentFlood.Load(),
		DirectTx:         r.sentDirect.Load(),
		FloodRx:          rc.FloodReceived,
		DirectRx:         rc.DirectReceived,
		FloodDups:        rc.FloodDuplicates,
		DirectDups:       rc.DirectDuplicates,
	}
	if r.haveSignal.Load() {
		snr := float64(r.lastSNRx4.Load()) / 4
		rssi := int(r.lastRSSI.Load())
		s.LastSNR, s.LastRSSI = &snr, &rssi
	}
	if r.haveDeviceStats.Load() {
		nf := int(r.noiseFloor.Load())
		s.NoiseFloor, s.BatteryMV = &nf, r.batteryReading()
	}
	if r.cfg.Latitude != nil {
		s.Latitude = *r.cfg.Latitude
	}
	if r.cfg.Longitude != nil {
		s.Longitude = *r.cfg.Longitude
	}
	return s
}

// snapshotNeighbors copies the directly-heard neighbours under the lock, newest first; callers only format.
func (r *Repeater) snapshotNeighbors() []neighbor {
	r.neighbors.Lock()
	out := make([]neighbor, 0, len(r.neighbors.m))
	for _, n := range r.neighbors.m {
		out = append(out, *n)
	}
	r.neighbors.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].heard.After(out[j].heard) })
	return out
}

// Neighbors returns the directly-heard repeaters, newest first.
func (r *Repeater) Neighbors() []NeighborInfo {
	now := time.Now()
	list := r.snapshotNeighbors()
	out := make([]NeighborInfo, len(list))
	for i, n := range list {
		out[i] = NeighborInfo{
			PubKey:  hex.EncodeToString(n.pubkey[:]),
			Name:    n.name,
			SNR:     n.snr,
			SecsAgo: int64(now.Sub(n.heard).Seconds()),
		}
	}
	return out
}

// countTx mirrors the firmware Dispatcher: our own adverts and replies count, not just relays.
func (r *Repeater) countTx(flood bool) {
	if flood {
		r.sentFlood.Add(1)
		return
	}
	r.sentDirect.Add(1)
}

// sendPkt queues a packet we originate, counting it (and its airtime) first.
func (r *Repeater) sendPkt(pkt *meshcore.Packet, priority uint8, delay time.Duration) error {
	r.countTx(pkt.IsRouteFlood())
	if r.airtime != nil {
		r.txAirtimeMs.Add(uint64(r.airtime(2 + len(pkt.Path) + len(pkt.Payload))))
	}
	return r.node.SendPacketDelayed(pkt, priority, delay)
}

// removeNeighbor drops every neighbour whose key starts with the prefix, as firmware `neighbor.remove` does.
func (r *Repeater) removeNeighbor(prefix []byte) {
	r.neighbors.Lock()
	defer r.neighbors.Unlock()
	for k := range r.neighbors.m {
		if bytes.HasPrefix(k[:], prefix) {
			delete(r.neighbors.m, k)
		}
	}
}
