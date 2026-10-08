package repeater

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"
)

type countingRadio struct {
	silentRadio
	sent atomic.Int32
}

func (r *countingRadio) SendData([]byte) error                     { r.sent.Add(1); return nil }
func (r *countingRadio) Enqueue([]byte, uint8, time.Duration) bool { r.sent.Add(1); return true }

// The library answers a flood PATH from a peer it knows and marks it; answering again would send the peer a second path.
func TestSendReciprocalPath_SkipsWhatTheLibraryAnswered(t *testing.T) {
	peer := meshcore.NewLocalIdentityFromSeed([32]byte{0x8d})
	for _, marked := range []bool{true, false} {
		radio := &countingRadio{}
		n := node.New(meshcore.NewLocalIdentityFromSeed([32]byte{1}), radio)
		rm := NewClient(n, nil, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, func() uint8 { return 1 })
		secret, _ := n.Identity().SharedSecret(peer.Identity)
		pkt := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypePath, 0), PathLength: 1, Path: []byte{0x11}}
		if marked {
			pkt.MarkDoNotRetransmit()
		}
		rm.sendReciprocalPath(pkt, peer.PublicKeyBytes(), secret, []byte{0x11}, 1)
		time.Sleep(700 * time.Millisecond) // past the reciprocal's 500 ms delay
		n.Stop()
		if got := radio.sent.Load(); (got > 0) == marked {
			t.Errorf("marked=%v: %d sent", marked, got)
		}
	}
}
