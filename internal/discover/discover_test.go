package discover

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"
)

type fakeNode struct {
	id   meshcore.LocalIdentity
	sent []*meshcore.Packet
	h    func(*meshcore.Packet)
}

func (f *fakeNode) SendPacketDelayed(pkt *meshcore.Packet, _ uint8, _ time.Duration) error {
	f.sent = append(f.sent, pkt)
	return nil
}
func (f *fakeNode) OnPacket(_ byte, h node.PacketHandler) { f.h = h }
func (f *fakeNode) Identity() meshcore.LocalIdentity      { return f.id }

func newFake(t *testing.T) (*fakeNode, *Service, *[]Result) {
	t.Helper()
	var seed [ed25519.SeedSize]byte
	seed[0] = 7
	f := &fakeNode{id: meshcore.NewLocalIdentityFromSeed(seed)}
	got := &[]Result{}
	s := New(f, slog.New(slog.NewTextHandler(io.Discard, nil)), func(r Result) { *got = append(*got, r) })
	return f, s, got
}

// resp builds a DISCOVER_RESP exactly as the firmware lays it out: flags carry the node type in the
// low nibble, then [snr x4][tag:4][pubkey].
func resp(t *testing.T, nodeType byte, tag uint32, pub []byte, snrQuarterDB int8) *meshcore.Packet {
	t.Helper()
	data := make([]byte, 5, 5+len(pub))
	data[0] = byte(snrQuarterDB)
	binary.LittleEndian.PutUint32(data[1:5], tag)
	data = append(data, pub...)
	payload, err := (&meshcore.Control{
		Flags: byte(meshcore.ControlSubTypeDiscoverResp<<4) | nodeType,
		Data:  data,
	}).ToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return &meshcore.Packet{
		Header:        meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, 0),
		Payload:       payload,
		SNR:           -3.25,
		HasSignalInfo: true,
	}
}

func sentTag(t *testing.T, pkt *meshcore.Packet) uint32 {
	t.Helper()
	ctl, err := meshcore.ControlFromBytes(pkt.Payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := ctl.DiscoverRequest()
	if err != nil {
		t.Fatal(err)
	}
	return req.Tag
}

// The request must go out zero-hop, or it is answered by nodes we cannot actually hear.
func TestStart_SendsZeroHopRequestWithTheTypeFilter(t *testing.T) {
	f, s, _ := newFake(t)
	if err := s.Start(FilterFor(TypeRepeater, TypeSensor), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 {
		t.Fatalf("sent %d packets, want 1", len(f.sent))
	}
	pkt := f.sent[0]
	if !pkt.IsRouteDirect() {
		t.Error("request was not zero-hop; a flooded discover would be answered by nodes out of radio range")
	}
	ctl, err := meshcore.ControlFromBytes(pkt.Payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := ctl.DiscoverRequest()
	if err != nil {
		t.Fatal(err)
	}
	if req.TypeFilter != 1<<TypeRepeater|1<<TypeSensor {
		t.Errorf("filter = %#x, want repeaters and sensors (%#x)", req.TypeFilter, 1<<TypeRepeater|1<<TypeSensor)
	}
	if req.PrefixOnly {
		t.Error("prefix_only set: the responder would send 8 key bytes and we need the whole key")
	}
}

// A response is only ours if the tag matches. Without that check a neighbouring client's scan
// silently fills our results with nodes we never asked about.
func TestHandle_IgnoresAnotherScansTag(t *testing.T) {
	f, s, got := newFake(t)
	if err := s.Start(FilterFor(TypeRepeater), time.Time{}); err != nil {
		t.Fatal(err)
	}
	pub := make([]byte, 32)
	pub[0] = 0xAA
	f.h(resp(t, TypeRepeater, sentTag(t, f.sent[0])+1, pub, 40))
	if len(*got) != 0 {
		t.Errorf("recorded %d results for a foreign tag, want 0", len(*got))
	}
}

// The filter is advisory on the wire: a responder can answer with a type we did not ask for.
func TestHandle_DropsATypeTheScanDidNotAskFor(t *testing.T) {
	f, s, got := newFake(t)
	if err := s.Start(FilterFor(TypeSensor), time.Time{}); err != nil {
		t.Fatal(err)
	}
	tag := sentTag(t, f.sent[0])
	pub := make([]byte, 32)
	pub[0] = 0xBB
	f.h(resp(t, TypeRepeater, tag, pub, 40))
	if len(*got) != 0 {
		t.Fatalf("a repeater answered a sensor-only scan and was recorded: %+v", *got)
	}
	pub[0] = 0xCC
	f.h(resp(t, TypeSensor, tag, pub, 40))
	if len(*got) != 1 {
		t.Fatalf("the sensor was not recorded, got %d results", len(*got))
	}
}

// Both SNRs are real dB. The library converts the quarter-dB wire form at ingest, so dividing here
// again would report a link four times better than it is.
func TestHandle_RecordsBothDirectionsInRealDB(t *testing.T) {
	f, s, got := newFake(t)
	if err := s.Start(FilterFor(TypeRepeater), time.Time{}); err != nil {
		t.Fatal(err)
	}
	pub := make([]byte, 32)
	pub[0] = 0xDD
	f.h(resp(t, TypeRepeater, sentTag(t, f.sent[0]), pub, 26)) // 26 quarter-dB = 6.5 dB
	if len(*got) != 1 {
		t.Fatalf("got %d results, want 1", len(*got))
	}
	r := (*got)[0]
	if r.SNR != -3.25 {
		t.Errorf("SNR = %v, want -3.25: what we measured on the inbound packet", r.SNR)
	}
	if r.ReportedSNR != 6.5 {
		t.Errorf("ReportedSNR = %v, want 6.5 dB; 1.625 means it was divided by 4 twice", r.ReportedSNR)
	}
	if r.PubKey != hex.EncodeToString(pub) {
		t.Errorf("PubKey = %q, want the responder's full key", r.PubKey)
	}
}

// Our own node answers a broadcast it hears; recording it would show every operator a phantom neighbour.
func TestHandle_SkipsOurself(t *testing.T) {
	f, s, got := newFake(t)
	if err := s.Start(FilterFor(TypeRepeater), time.Time{}); err != nil {
		t.Fatal(err)
	}
	self := f.id.Identity.PublicKey()
	f.h(resp(t, TypeRepeater, sentTag(t, f.sent[0]), self[:], 40))
	if len(*got) != 0 {
		t.Errorf("recorded ourselves as a neighbour: %+v", *got)
	}
}

// A flooded control frame is not zero-hop, so it says nothing about radio range.
func TestHandle_IgnoresARoutedResponse(t *testing.T) {
	f, s, got := newFake(t)
	if err := s.Start(FilterFor(TypeRepeater), time.Time{}); err != nil {
		t.Fatal(err)
	}
	pub := make([]byte, 32)
	pub[0] = 0xEE
	pkt := resp(t, TypeRepeater, sentTag(t, f.sent[0]), pub, 40)
	pkt.Header = meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeControl, 0)
	f.h(pkt)
	if len(*got) != 0 {
		t.Errorf("recorded a routed response as a direct neighbour: %+v", *got)
	}
}

// The same node answering twice must not fire the live-update callback twice.
func TestHandle_PublishesEachNodeOnce(t *testing.T) {
	f, s, got := newFake(t)
	if err := s.Start(FilterFor(TypeRepeater), time.Time{}); err != nil {
		t.Fatal(err)
	}
	tag := sentTag(t, f.sent[0])
	pub := make([]byte, 32)
	pub[0] = 0xFF
	f.h(resp(t, TypeRepeater, tag, pub, 40))
	f.h(resp(t, TypeRepeater, tag, pub, 44))
	if len(*got) != 1 {
		t.Errorf("published %d times for one node, want 1", len(*got))
	}
	_, _, results := s.State()
	if len(results) != 1 {
		t.Errorf("State() has %d results for one node, want 1", len(results))
	}
}

// The results live in a map, so without an explicit order the table reshuffles on every page load.
func TestState_OrdersNewestFirst(t *testing.T) {
	f, s, _ := newFake(t)
	if err := s.Start(FilterFor(TypeRepeater), time.Time{}); err != nil {
		t.Fatal(err)
	}
	tag := sentTag(t, f.sent[0])
	for _, b := range []byte{0x11, 0x22, 0x33} {
		pub := make([]byte, 32)
		pub[0] = b
		f.h(resp(t, TypeRepeater, tag, pub, 40))
	}
	_, _, results := s.State()
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for i := 1; i < len(results); i++ {
		if results[i-1].Heard.Before(results[i].Heard) {
			t.Fatalf("result %d is older than the one after it; the map order leaked through", i-1)
		}
	}
	if results[0].PubKey != hex.EncodeToString(append([]byte{0x33}, make([]byte, 31)...)) {
		t.Errorf("first result is %s, want the last node to answer", results[0].PubKey[:4])
	}
}
