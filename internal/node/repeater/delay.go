package repeater

import (
	"math"
	"math/rand/v2"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/hardware"
	"github.com/OwlShack/meshcore-go/node"
)

// Ports MyMesh::getRetransmitDelay / getDirectRetransmitDelay: rand[0, 5t] with t = airtime × factor.
func (r *Repeater) floodRelayDelay(_ *meshcore.Packet, airtimeMs uint32) time.Duration {
	cfg := r.cfgSnapshot()
	return relayDelay(airtimeMs, cfg.TxDelayFactorOr())
}

func (r *Repeater) directRelayDelay(_ *meshcore.Packet, airtimeMs uint32) time.Duration {
	cfg := r.cfgSnapshot()
	return relayDelay(airtimeMs, cfg.DirectTxDelayFactorOr())
}

func relayDelay(airtimeMs uint32, factor float64) time.Duration {
	t := uint32(float64(airtimeMs) * factor)
	return time.Duration(rand.IntN(int(5*t)+1)) * time.Millisecond
}

// rxDelay ports MyMesh::calcRxDelay ((base^(0.85-score) - 1) × airtime, off when base is 0 or the SF is unknown), plus the dedupe hold on every flood a dedupe peer didn't relay.
func (r *Repeater) rxDelay(pkt *meshcore.Packet, packetLen int, airtimeMs uint32) time.Duration {
	dedupe, peer := r.lastHopPeer(pkt)
	if dedupe {
		return 0
	}
	cfg := r.cfgSnapshot()
	var d time.Duration
	if base := cfg.RxDelayBaseOr(); base > 0 && r.sf != 0 {
		score := hardware.PacketScore(float64(pkt.SNR), r.sf, packetLen)
		// A strong packet scores past 0.85 and comes out negative, which means no delay; added to the hold it would shorten it.
		d = max(0, time.Duration((math.Pow(base, 0.85-score)-1)*float64(airtimeMs))*time.Millisecond)
	}
	if r.hasDedupe && !peer && !cfg.IsFwdDisabled() {
		d += time.Duration(cfg.DedupeHoldOr() * float64(airtimeMs) * float64(time.Millisecond))
	}
	return d
}

// extraAcks is the firmware getExtraAckTransmitCount (NodePrefs multi_acks).
func (r *Repeater) extraAcks() uint8 {
	cfg := r.cfgSnapshot()
	return uint8(cfg.MultiAcksOr())
}

// routeCounters returns the router's RX / dup counters since the last `clear stats`.
func (r *Repeater) routeCounters() node.RouteStats {
	if r.routeStats == nil {
		return node.RouteStats{}
	}
	s := r.routeStats()
	r.mu.Lock()
	b := r.statsBase
	r.mu.Unlock()
	s.FloodReceived -= b.FloodReceived
	s.DirectReceived -= b.DirectReceived
	s.FloodDuplicates -= b.FloodDuplicates
	s.DirectDuplicates -= b.DirectDuplicates
	s.FloodRelays -= b.FloodRelays
	s.DirectRelays -= b.DirectRelays
	s.Delivered -= b.Delivered
	return s
}
