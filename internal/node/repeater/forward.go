package repeater

import meshcore "github.com/OwlShack/meshcore-go"

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

		if r.isLooped(pkt) {
			return false
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
