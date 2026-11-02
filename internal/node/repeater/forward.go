package repeater

import (
	"bytes"
	"encoding/hex"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/OwlShack/OwlShack/internal/config"
)

// Firmware max_loop_* tables indexed by path hash size; value N means drop once our own hash appears >= N times.
var (
	maxLoopMinimal  = [4]int{0, 4, 2, 1}
	maxLoopModerate = [4]int{0, 2, 1, 1}
	maxLoopStrict   = [4]int{0, 1, 1, 1}
)

// allowForward ports MyMesh::allowPacketForward; the router already handles dedup and path overflow, so only policy lives here.
func (r *Repeater) allowForward(pkt *meshcore.Packet) bool {
	if r.cfg.IsFwdDisabled() {
		return false
	}

	if pkt.IsRouteFlood() {
		// Firmware order: general cap, then the unscoped-only cap, then the advert cap.
		hops := int(pkt.PathHashCount())
		if hops >= r.cfg.FloodMaxOr() {
			return false
		}
		if pkt.RouteType() == meshcore.RouteTypeFlood && hops >= r.cfg.FloodMaxUnscopedOr() {
			return false
		}
		if pkt.PayloadType() == meshcore.PayloadTypeAdvert && hops >= r.cfg.FloodMaxAdvertOr() {
			return false
		}
		// Firmware recv_pkt_region: an unknown transport code, or a plain flood "*" denies, is not relayed.
		if r.node.Regions().FindFloodMatch(pkt) == nil {
			return false
		}

		if r.isLooped(pkt) {
			return false
		}

		// A relay peer already put this copy on air from beside us.
		if _, peer := r.lastHopPeer(pkt); peer {
			return false
		}
		if r.hasDedupe {
			r.backupRelays.Add(1)
		}
	}

	// Wire size ~ header + pathLen byte + path + payload.
	if r.airtime != nil {
		r.txAirtimeMs.Add(uint64(r.airtime(2 + len(pkt.Path) + len(pkt.Payload))))
	}
	r.fwdCount.Add(1)
	r.countTx(pkt.IsRouteFlood())
	return true
}

// loopThreshold returns how many times our own hash may appear before the packet counts as looped; 0 means no check.
func loopThreshold(level string, hashSize int) int {
	if hashSize < 1 || hashSize > 3 {
		return 0
	}
	switch level {
	case "minimal":
		return maxLoopMinimal[hashSize]
	case "moderate":
		return maxLoopModerate[hashSize]
	case "strict":
		return maxLoopStrict[hashSize]
	default: // "off"
		return 0
	}
}

// relayPeer is a config.RelayPeer decoded once for the packet path.
type relayPeer struct {
	pub    [32]byte
	dedupe bool
}

// parseRelayPeers skips a key that doesn't decode; Validate refuses those, so only a hand-built config has one.
func parseRelayPeers(peers []config.RelayPeer) (out []relayPeer, hasDedupe bool) {
	for _, p := range peers {
		b, err := hex.DecodeString(p.PubKey)
		if err != nil || len(b) != 32 {
			continue
		}
		rp := relayPeer{dedupe: p.Mode == config.RelayPeerDedupe}
		copy(rp.pub[:], b)
		out = append(out, rp)
		hasDedupe = hasDedupe || rp.dedupe
	}
	return out, hasDedupe
}

// lastHopPeer matches a flood's last relayer against the relay peers; a hash can match more than one, and dedupe wins.
func (r *Repeater) lastHopPeer(pkt *meshcore.Packet) (dedupe, found bool) {
	if len(r.relayPeers) == 0 || !pkt.IsRouteFlood() {
		return false, false
	}
	hashes := pkt.PathHashes()
	if len(hashes) == 0 {
		return false, false
	}
	h := hashes[len(hashes)-1]
	for _, p := range r.relayPeers {
		if bytes.Equal(h, p.pub[:len(h)]) {
			found = true
			dedupe = dedupe || p.dedupe
		}
	}
	return dedupe, found
}

// isLooped ports MyMesh::isLooped.
func (r *Repeater) isLooped(pkt *meshcore.Packet) bool {
	threshold := loopThreshold(r.cfg.LoopDetectOr(), int(pkt.PathHashSize()))
	if threshold == 0 {
		return false
	}

	id := r.node.Identity()
	n := 0
	for _, h := range pkt.PathHashes() {
		if id.IsHashMatch(h) {
			n++
		}
	}
	return n >= threshold
}
