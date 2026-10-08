package meshpath

import (
	"crypto/rand"
	"sync"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"
)

type recordingRadio struct {
	mu   sync.Mutex
	sent [][]byte
}

func (r *recordingRadio) SendData(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, append([]byte(nil), b...))
	return nil
}
func (r *recordingRadio) SetDataHandler(func(*meshcore.Packet))               {}
func (r *recordingRadio) SetRawDataHandler(func([]byte, float32, int8, bool)) {}
func (r *recordingRadio) AddOutboundHandler(func([]byte))                     {}
func (r *recordingRadio) Close() error                                        { return nil }

// Our own sends queue as the firmware's do: a direct request ahead of a flood and an advert, a trace last, whatever order they were sent in.
func TestSend_FirmwarePriorities(t *testing.T) {
	radio := &recordingRadio{}
	id, _ := meshcore.GenerateLocalIdentity(rand.Reader)
	n := node.New(id, radio)
	defer n.Stop()

	pkt := func(route, typ byte, path []byte, pathLen byte) *meshcore.Packet {
		return &meshcore.Packet{Header: meshcore.MakeHeader(route, typ, 0), PathLength: pathLen, Path: path, Payload: make([]byte, 20)}
	}
	scope := meshcore.NewRegion("nz")
	delay := 100 * time.Millisecond
	sends := []*meshcore.Packet{
		pkt(meshcore.RouteTypeDirect, meshcore.PayloadTypeTrace, nil, 0),
		pkt(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, nil, 0),
		pkt(meshcore.RouteTypeFlood, meshcore.PayloadTypeReq, nil, 0),
		pkt(meshcore.RouteTypeDirect, meshcore.PayloadTypeReq, []byte{0xab, 0xcd}, meshcore.MakePathLen(2, 1)),
	}
	for _, p := range sends {
		if err := Send(n, p, scope, delay); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		radio.mu.Lock()
		got := len(radio.sent)
		radio.mu.Unlock()
		if got == len(sends) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	radio.mu.Lock()
	defer radio.mu.Unlock()
	var order []string
	for _, b := range radio.sent {
		p, err := meshcore.PacketFromBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, p.RouteTypeString()+"/"+p.PayloadTypeString())
	}
	want := []string{"DIRECT/REQ", "TRANSPORT_FLOOD/REQ", "TRANSPORT_FLOOD/ADVERT", "DIRECT/TRACE"}
	if len(order) != len(want) {
		t.Fatalf("sent %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("sent %v, want %v", order, want)
		}
	}
	if p, _ := meshcore.PacketFromBytes(radio.sent[0]); string(p.Path) != "\xab\xcd" || p.PathHashSize() != 2 {
		t.Errorf("direct path = %x size %d, want abcd at 2 bytes a hop", p.Path, p.PathHashSize())
	}
}
