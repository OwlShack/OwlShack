package mqtt

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/modem"
	meshcore "github.com/OwlShack/meshcore-go"
)

// A broker that never connected sits in o.brokers with a nil paho client, and Stop must survive a second call.
func TestObserver_StopWithUnconnectedBroker(t *testing.T) {
	o := &Observer{
		log:         slog.New(slog.DiscardHandler),
		originName:  "test",
		health:      map[string]*brokerHealth{},
		parseErrors: &atomic.Uint64{},
	}
	bc := &brokerClient{
		cfg:            config.BrokerConfig{Name: "down", Host: "127.0.0.1"},
		statusTopicStr: "meshcore/test/status",
		publishCh:      make(chan publishJob, publishQueueDepth),
		stop:           make(chan struct{}),
		workerDone:     make(chan struct{}),
	}
	o.brokers = append(o.brokers, bc)
	go o.publishWorker(bc)

	o.Stop()
	o.Stop()

	select {
	case <-bc.workerDone:
	case <-time.After(time.Second):
		t.Fatal("publish worker did not exit")
	}
	if got := bc.dropped.Load(); got == 0 {
		t.Error("offline status to a nil client should count as a drop")
	}
	if got := bc.published.Load(); got != 0 {
		t.Errorf("published = %d, want 0 with no client", got)
	}
}

// A relayed flood has the same hash as the rx we published, so the dedup caches must be per-direction.
func TestDedup_IsPerDirection(t *testing.T) {
	bc := &brokerClient{dedup: &meshcore.DedupCache{}, dedupTx: &meshcore.DedupCache{}}
	pkt := &meshcore.Packet{
		Header:  meshcore.PayloadTypeTxtMsg<<2 | meshcore.RouteTypeFlood,
		Payload: []byte{0xAB, 0xCD, 0x01},
	}

	if bc.dedupFor("rx").HasSeen(pkt) {
		t.Fatal("first rx must not be a dup")
	}
	if bc.dedupFor("tx").HasSeen(pkt) {
		t.Error("relaying a packet we already published as rx must not be a dup")
	}
	if !bc.dedupFor("tx").HasSeen(pkt) {
		t.Error("a second tx of the same packet must still be a dup")
	}
	if !bc.dedupFor("rx").HasSeen(pkt) {
		t.Error("a second rx of the same packet must still be a dup")
	}
}

// NoteTx tallies what the process transmitted; the route split comes from the packet.
func TestNoteTx_CountsAirtimeAndRouteSplit(t *testing.T) {
	o := testObserver(t)
	o.stats = fakeStats{airMs: 150}

	flood := &meshcore.Packet{Header: meshcore.PayloadTypeAdvert << 2, Payload: []byte{0x01}}
	direct := &meshcore.Packet{
		Header:  meshcore.PayloadTypeTxtMsg<<2 | meshcore.RouteTypeDirect,
		Payload: []byte{0xAB, 0xCD},
	}
	for _, pkt := range []*meshcore.Packet{flood, direct, direct} {
		b, err := pkt.ToBytes()
		if err != nil {
			t.Fatalf("ToBytes: %v", err)
		}
		o.NoteTx(b)
	}

	c := o.observerCounts()
	if c.FloodTx != 1 || c.DirectTx != 2 {
		t.Errorf("flood/direct tx = %d/%d, want 1/2", c.FloodTx, c.DirectTx)
	}
	if c.TxMs != 450 { // 3 packets x 150ms
		t.Errorf("TxMs = %d, want 450", c.TxMs)
	}
	if c.RxMs != 0 {
		t.Errorf("RxMs = %d, want 0 — NoteTx must not touch the rx accumulator", c.RxMs)
	}
}

// The first "online" status is published inside Start, so repeat must be set before Start runs.
func TestSetRelaying_IsVisibleToTheFirstStatus(t *testing.T) {
	o := testObserver(t)
	if o.observerCounts().Relaying {
		t.Fatal("a fresh observer must default to not relaying")
	}
	o.SetRelaying(true)

	raw, err := formatStatus("online", "n", "id", modem.RadioInfo{}, modem.DeviceStats{},
		PacketCounts{}, TxCounts{}, modem.LinkStats{}, o.observerCounts(), 0)
	if err != nil {
		t.Fatalf("formatStatus: %v", err)
	}
	var got struct {
		Repeat bool `json:"repeat"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Repeat {
		t.Error("repeat must reach the status payload once SetRelaying(true) is called")
	}
}

// A relay-state change must not wait for the heartbeat, nor publish when a reload re-applies the same value.
func TestSetRelaying_PublishesOnChangeOnly(t *testing.T) {
	o := testObserver(t)
	bc := testBrokerClient("b", "127.0.0.1", 1, "basic")
	o.brokers = []*brokerClient{bc}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	o.runCtx = ctx

	// The publish worker isn't running, so jobs queue up and we can count them.
	drain := func() int { return len(bc.publishCh) }

	o.SetRelaying(true)
	if got := drain(); got != 1 {
		t.Errorf("a changed value queued %d publishes, want 1", got)
	}
	o.SetRelaying(true)
	if got := drain(); got != 1 {
		t.Errorf("re-applying the same value queued %d publishes, want no extra", got)
	}
	o.SetRelaying(false)
	if got := drain(); got != 2 {
		t.Errorf("flipping back queued %d publishes, want 2", got)
	}
}

// Before Start there is no context and no broker: record the flag without publishing.
func TestSetRelaying_BeforeStartDoesNotPublish(t *testing.T) {
	o := testObserver(t)
	o.SetRelaying(true)
	if !o.observerCounts().Relaying {
		t.Error("the value must be recorded even before Start")
	}
}

// publishPacket must SELECT the cache by the direction it was called with; a hardcoded argument passes a cache-level test.
func TestPublishPacket_SelectsDedupCacheByDirection(t *testing.T) {
	o := testObserver(t)
	bc := testBrokerClient("b", "127.0.0.1", 1, "basic")
	bc.dedup = &meshcore.DedupCache{}
	bc.dedupTx = &meshcore.DedupCache{}
	o.brokers = []*brokerClient{bc}

	pkt := &meshcore.Packet{
		Header:  meshcore.PayloadTypeTxtMsg<<2 | meshcore.RouteTypeFlood,
		Payload: []byte{0xAB, 0xCD, 0x01},
	}
	raw, err := pkt.ToBytes()
	if err != nil {
		t.Fatalf("ToBytes: %v", err)
	}

	// The publish worker isn't running, so jobs stay in the channel to count.
	o.publishPacket(pkt, raw, "rx")
	if got := len(bc.publishCh); got != 1 {
		t.Fatalf("after rx: %d queued, want 1", got)
	}
	// Relaying the same flood: identical hash, other direction.
	o.publishPacket(pkt, raw, "tx")
	if got := len(bc.publishCh); got != 2 {
		t.Errorf("after relaying it as tx: %d queued, want 2", got)
	}
	// Within a direction, dedup must still apply.
	o.publishPacket(pkt, raw, "tx")
	if got := len(bc.publishCh); got != 2 {
		t.Errorf("a repeated tx queued %d, want no extra", got)
	}
	o.publishPacket(pkt, raw, "rx")
	if got := len(bc.publishCh); got != 2 {
		t.Errorf("a repeated rx queued %d, want no extra", got)
	}
}
