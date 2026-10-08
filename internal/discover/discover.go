// Package discover runs the firmware's zero-hop node discovery from whichever node we already have,
// so finding what is in radio range does not require running a repeater personality.
package discover

import (
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"slices"
	"sync"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"
)

// Firmware ADV_TYPE_* (AdvertDataHelpers.h); the request's filter byte is a bitmask over these.
// Only TypeRepeater and TypeSensor can actually answer: simple_room_server inherits the empty base
// Mesh::onControlDataRecv, and companion_radio hands the frame to its phone app without replying.
// The other two stay defined because a response carries its type and we still label what arrives.
const (
	TypeChat     = 1
	TypeRepeater = 2
	TypeRoom     = 3
	TypeSensor   = 4
)

// Window is how long responses are collected. Responders answer after a random delay widened x4
// (getRetransmitDelay*4) because many reply to one request; that worst case is ten times the
// packet's airtime, so this has to stay well clear of it on the slowest presets.
const Window = 30 * time.Second

// Sender is the part of a node this needs: any running node can carry the request.
type Sender interface {
	SendPacketDelayed(pkt *meshcore.Packet, priority uint8, delay time.Duration) error
	OnPacket(t byte, h node.PacketHandler)
	Identity() meshcore.LocalIdentity
}

// Result is one node that answered.
type Result struct {
	PubKey string  `json:"pubkey"`
	Type   int     `json:"type"`
	SNR    float64 `json:"snr"`
	// ReportedSNR is the responder's view of our signal, so a one-sided link shows as a gap between the two.
	ReportedSNR float64   `json:"reportedSnr"`
	Heard       time.Time `json:"heard"`
}

// Service holds at most one scan at a time; a second Start replaces the first.
type Service struct {
	send Sender
	log  *slog.Logger
	// onResult publishes each answer as it lands, so a 60s scan fills the page rather than ending with it.
	onResult func(Result)

	mu      sync.Mutex
	tag     uint32
	filter  byte
	until   time.Time
	results map[string]Result
}

func New(send Sender, log *slog.Logger, onResult func(Result)) *Service {
	s := &Service{send: send, log: log, onResult: onResult, results: map[string]Result{}}
	send.OnPacket(meshcore.PayloadTypeControl, s.handleControl)
	return s
}

// FilterFor builds the request's type bitmask. One request can ask for several types at once.
func FilterFor(types ...int) byte {
	var f byte
	for _, t := range types {
		f |= 1 << t
	}
	return f
}

// Start broadcasts a zero-hop NODE_DISCOVER_REQ and collects matching responses for Window.
// since is the firmware's freshness gate: a responder stays quiet unless its discovery info changed
// after it. Zero asks everyone.
func (s *Service) Start(filter byte, since time.Time) error {
	if filter == 0 {
		filter = FilterFor(TypeRepeater, TypeSensor)
	}
	var tagB [4]byte
	if _, err := crand.Read(tagB[:]); err != nil {
		return err
	}
	tag := binary.LittleEndian.Uint32(tagB[:])

	data := make([]byte, 9)
	data[0] = filter
	binary.LittleEndian.PutUint32(data[1:5], tag)
	if !since.IsZero() {
		binary.LittleEndian.PutUint32(data[5:9], uint32(since.Unix()))
	}

	s.mu.Lock()
	s.tag, s.filter, s.until = tag, filter, time.Now().Add(Window)
	s.results = map[string]Result{}
	s.mu.Unlock()

	payload, err := (&meshcore.Control{
		Flags: byte(meshcore.ControlSubTypeDiscoverReq << 4), // prefix_only=0: we want the full key
		Data:  data,
	}).ToBytes()
	if err != nil {
		return err
	}
	s.log.Info("discovery scan started", "filter", filter, "window", Window)
	// Zero-hop: direct route with no path, so only nodes hearing us on air can answer.
	return s.send.SendPacketDelayed(&meshcore.Packet{
		Header:  meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, 0),
		Payload: payload,
	}, node.PrioritySend, 0)
}

// State reports the current scan, whether or not it is still running.
func (s *Service) State() (running bool, endsAt time.Time, results []Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Result, 0, len(s.results))
	for _, r := range s.results {
		out = append(out, r)
	}
	// Map iteration is random; without this the table reshuffles every time the page reloads.
	slices.SortFunc(out, func(a, b Result) int { return b.Heard.Compare(a.Heard) })
	return s.tag != 0 && time.Now().Before(s.until), s.until, out
}

func (s *Service) handleControl(pkt *meshcore.Packet) {
	// Firmware handles control frames only when they arrived zero-hop.
	if !pkt.IsRouteDirect() {
		return
	}
	ctl, err := meshcore.ControlFromBytes(pkt.Payload)
	if err != nil || ctl.SubType() != meshcore.ControlSubTypeDiscoverResp {
		return
	}
	resp, err := ctl.DiscoverResponse()
	if err != nil || len(resp.PubKey) < 32 {
		return
	}

	s.mu.Lock()
	active := s.tag != 0 && resp.Tag == s.tag && time.Now().Before(s.until)
	wanted := s.filter&(1<<resp.NodeType) != 0
	s.mu.Unlock()
	if !active || !wanted {
		return
	}

	var pub [32]byte
	copy(pub[:], resp.PubKey)
	if pub == s.send.Identity().Identity.PublicKey() {
		return
	}

	// Our own measurement, not the byte the responder reports: that one is its view of us.
	r := Result{
		PubKey:      hex.EncodeToString(pub[:]),
		Type:        int(resp.NodeType),
		SNR:         float64(pkt.SNR),
		ReportedSNR: float64(resp.SNR),
		Heard:       time.Now(),
	}

	s.mu.Lock()
	_, seen := s.results[r.PubKey]
	s.results[r.PubKey] = r
	s.mu.Unlock()
	if !seen && s.onResult != nil {
		s.onResult(r)
	}
}
