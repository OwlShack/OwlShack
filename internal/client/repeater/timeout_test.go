package repeater

import (
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
)

// fakeStats reports a fixed airtime, standing in for the modem's radio params.
type fakeStats struct{ airtime uint32 }

func (f fakeStats) EstAirtimeMs(int) uint32 { return f.airtime }

func TestReplyTimeout(t *testing.T) {
	t.Parallel()

	const floor = 10 * time.Second

	// Without a working estimator the caller's value stands: a guess would be worse than it.
	if got := (&Client{}).replyTimeout(24, nil, 0, floor); got != floor {
		t.Errorf("no stats: got %v, want the floor %v", got, floor)
	}
	if got := (&Client{stats: fakeStats{}}).replyTimeout(24, nil, 0, floor); got != floor {
		t.Errorf("zero airtime: got %v, want the floor %v", got, floor)
	}

	// 700ms is roughly a full 184-byte packet at SF7/62.5kHz, the bench preset.
	c := &Client{stats: fakeStats{airtime: 700}}

	// The bug this fixes: a multi-hop reply needs longer than the flat 10s every command had.
	twoHop := c.replyTimeout(24, []byte{0xe6, 0xc1}, 1, floor)
	if twoHop <= floor {
		t.Errorf("2-hop timeout %v did not exceed the 10s floor, which is the bug this fixes", twoHop)
	}

	// More hops, more waiting.
	oneHop := c.replyTimeout(24, []byte{0xe6}, 1, floor)
	if twoHop <= oneHop {
		t.Errorf("2-hop %v must exceed 1-hop %v", twoHop, oneHop)
	}

	// A direct neighbour is the cheapest case and must not be inflated past the floor.
	if got := c.replyTimeout(24, []byte{}, 1, floor); got != floor {
		t.Errorf("0-hop got %v, want the floor %v", got, floor)
	}

	// An unknown route floods, which the firmware times differently from a direct send.
	if flood, direct := c.replyTimeout(24, nil, 0, floor), oneHop; flood == direct {
		t.Errorf("flood and 1-hop direct both %v; the two formulas should differ", flood)
	}

	// A two-byte hash size halves the hop count for the same path bytes.
	wide := c.replyTimeout(24, []byte{0xe6, 0xc1}, 2, floor)
	if wide != oneHop {
		t.Errorf("2-byte hashes over 2 path bytes is 1 hop: got %v, want %v", wide, oneHop)
	}

	// A generous caller still wins.
	if got := c.replyTimeout(24, []byte{0xe6, 0xc1}, 1, 90*time.Second); got != 90*time.Second {
		t.Errorf("caller floor ignored: got %v", got)
	}
}

// The reply sets the pace, not the request: a 24-byte status req and a 24-byte CLI line must both
// leave room for a full-packet answer, or telemetry times out while small replies pass.
func TestReplyTimeout_SizedForTheReplyNotTheRequest(t *testing.T) {
	t.Parallel()

	var seen []int
	c := &Client{stats: sizeSpy{&seen}}
	c.replyTimeout(24, nil, 0, time.Second)

	if len(seen) != 1 || seen[0] != meshcore.MaxPacketPayload {
		t.Errorf("estimated airtime for %v, want one call at the max packet size %d", seen, meshcore.MaxPacketPayload)
	}
}

type sizeSpy struct{ seen *[]int }

func (s sizeSpy) EstAirtimeMs(n int) uint32 {
	*s.seen = append(*s.seen, n)
	return 700
}
