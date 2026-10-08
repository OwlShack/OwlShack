package repeater

import (
	crand "crypto/rand"
	"encoding/binary"
	mrand "math/rand/v2"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
)

// Node discovery (firmware CTL_TYPE_NODE_DISCOVER_REQ/RESP): a zero-hop request whose matching responses become neighbours.

const (
	discoverReqFlags   = byte(meshcore.ControlSubTypeDiscoverReq << 4) // 0x80, prefix_only=0
	discoverWindow     = 60 * time.Second                              // collect responses this long (firmware)
	discoverTypeFilter = byte(1 << meshcore.AdvertTypeRepeater)        // discover repeaters
)

// sendDiscover broadcasts a zero-hop NODE_DISCOVER_REQ; control data is [filter:1][tag:4][since:4].
func (r *Repeater) sendDiscover() error {
	var tagB [4]byte
	_, _ = crand.Read(tagB[:])
	tag := binary.LittleEndian.Uint32(tagB[:])

	data := make([]byte, 9)
	data[0] = discoverTypeFilter
	binary.LittleEndian.PutUint32(data[1:5], tag)
	// data[5:9] = since (0)

	r.discover.Lock()
	r.discover.tag = tag
	r.discover.until = time.Now().Add(discoverWindow)
	r.discover.Unlock()

	ctl := meshcore.Control{Flags: discoverReqFlags, Data: data}
	payload, err := ctl.ToBytes()
	if err != nil {
		return err
	}
	// Zero-hop: direct route, no path — reaches direct neighbours only.
	return r.sendPkt(&meshcore.Packet{
		Header:  meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, 0),
		Payload: payload,
	}, nil, 0)
}

func (r *Repeater) SendDiscover() error { return r.sendDiscover() }

// handleControl ports firmware onControlDataRecv: answer discover requests, record responses matching our in-flight tag.
func (r *Repeater) handleControl(pkt *meshcore.Packet) {
	ctl, err := meshcore.ControlFromBytes(pkt.Payload)
	if err != nil || !pkt.IsRouteDirect() {
		return // firmware: only zero-hop DIRECT control packets are handled
	}
	switch ctl.SubType() {
	case meshcore.ControlSubTypeDiscoverReq:
		r.answerDiscover(pkt, ctl)
	case meshcore.ControlSubTypeDiscoverResp:
		r.recordDiscoverResp(pkt, ctl)
	}
}

// answerDiscover replies [flags=0x90|type][snr x4][tag:4][pubkey], gated on relaying being enabled and rate-limited 4/2min.
func (r *Repeater) answerDiscover(pkt *meshcore.Packet, ctl *meshcore.Control) {
	req, err := ctl.DiscoverRequest()
	if err != nil || req.TypeFilter&(1<<meshcore.AdvertTypeRepeater) == 0 {
		return // not asking about repeaters
	}
	if r.cfg.IsFwdDisabled() || !r.discoverLimiter.allow() {
		return
	}
	// ponytail: firmware's discovery_mod_timestamp freshness gate isn't modelled — we always answer.

	pub := r.node.Identity().PublicKeyBytes()
	data := make([]byte, 5, 5+len(pub))
	if pkt.HasSignalInfo {
		data[0] = byte(meshcore.SNRToWire(pkt.SNR))
	}
	binary.LittleEndian.PutUint32(data[1:5], req.Tag)
	data = append(data, pub[:]...)
	if req.PrefixOnly {
		data = data[:5+8] // firmware replies with an 8-byte pubkey prefix
	}
	payload, err := (&meshcore.Control{
		Flags: byte(meshcore.ControlSubTypeDiscoverResp<<4 | meshcore.AdvertTypeRepeater),
		Data:  data,
	}).ToBytes()
	if err != nil {
		return
	}

	// Widened ×4 because multiple nodes answer the same request (firmware getRetransmitDelay*4, tx_delay_factor 0.5).
	est := uint32(100) // ms fallback when no ToA estimator is available
	if r.airtime != nil {
		est = r.airtime(len(payload))
	}
	delay := time.Duration(mrand.IntN(int(5*est/2)+1)) * 4 * time.Millisecond
	if err := r.sendPkt(&meshcore.Packet{
		Header:  meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, 0),
		Payload: payload,
	}, nil, delay); err != nil {
		r.log.Error("discover response failed", "error", err)
	}
}

// recordDiscoverResp stores the neighbour at the SNR *we* heard it at, not the byte it reports (firmware putNeighbour).
func (r *Repeater) recordDiscoverResp(pkt *meshcore.Packet, ctl *meshcore.Control) {
	resp, err := ctl.DiscoverResponse()
	if err != nil || resp.NodeType != meshcore.AdvertTypeRepeater || len(resp.PubKey) < 32 {
		return
	}

	r.discover.Lock()
	active := r.discover.tag != 0 && resp.Tag == r.discover.tag && time.Now().Before(r.discover.until)
	r.discover.Unlock()
	if !active {
		return
	}

	var pub [32]byte
	copy(pub[:], resp.PubKey)
	if pub == r.node.Identity().Identity.PublicKey() {
		return // don't record ourselves
	}

	r.neighbors.Lock()
	name := ""
	if existing := r.neighbors.m[pub]; existing != nil {
		name = existing.name // keep a name already learned from an advert
	}
	r.neighbors.m[pub] = &neighbor{pubkey: pub, name: name, snr: float64(pkt.SNR), heard: time.Now()}
	r.neighbors.Unlock()
}
