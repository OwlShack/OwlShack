package mqtt

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OwlShack/OwlShack/internal/modem"
)

// pollingStats mimics a board poll: Stats blocks, and a packet arrives while it does.
type pollingStats struct {
	modem.StatsProvider
	onPoll func()
}

func (p pollingStats) RadioConfig() modem.RadioInfo { return modem.RadioInfo{} }
func (p pollingStats) LinkStats() modem.LinkStats   { return modem.LinkStats{} }

func (p pollingStats) Stats(context.Context) modem.DeviceStats {
	time.Sleep(20 * time.Millisecond)
	p.onPoll()
	return modem.DeviceStats{}
}

// Every counter in a status message must be read at ONE instant. Stats blocks for ~500ms polling the
// board, so a packet landing inside that window makes a message that samples either side of it
// self-contradictory: packets received with no signal on any of them, which reads as a deaf radio.
func TestPublishStatus_SamplesEveryCounterAtOneInstant(t *testing.T) {
	o := testObserver(t)
	bc := testBrokerClient("b", "127.0.0.1", 1, "basic")

	o.stats = pollingStats{onPoll: func() {
		o.packetsReceived.Add(1)
		o.lastSNR.Store(44) // quarter-dB, so 11 dB
		o.lastRSSI.Store(-12)
	}}

	o.publishStatus(context.Background(), bc, "online")

	var job publishJob
	select {
	case job = <-bc.publishCh:
	default:
		t.Fatal("publishStatus enqueued nothing")
	}

	var got struct {
		Stats struct {
			Recv     uint64  `json:"recv"`
			LastSNR  float64 `json:"last_snr"`
			LastRSSI int16   `json:"last_rssi"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(job.payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Stats.Recv == 0 {
		t.Fatal("the packet that arrived during the poll was not counted at all")
	}
	if got.Stats.LastSNR == 0 || got.Stats.LastRSSI == 0 {
		t.Errorf("recv=%d but last_snr=%v last_rssi=%d: counters sampled at different instants",
			got.Stats.Recv, got.Stats.LastSNR, got.Stats.LastRSSI)
	}
}
