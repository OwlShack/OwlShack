package repeater

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/store"
	"github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/hardware"
	"github.com/OwlShack/meshcore-go/node"
)

func peerKey(prefix ...byte) string {
	k := make([]byte, 32)
	copy(k, prefix)
	return hex.EncodeToString(k)
}

func floodVia(hashSize uint8, path ...byte) *meshcore.Packet {
	return &meshcore.Packet{
		Header:     meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, 0),
		PathLength: meshcore.MakePathLen(hashSize, uint8(len(path))/hashSize),
		Path:       path,
		Payload:    []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}
}

// Only the last hop is the relayer we heard; a peer earlier in the path relayed it somewhere we may not reach.
func TestLastHopPeer(t *testing.T) {
	r := &Repeater{}
	r.relayPeers, r.hasDedupe = parseRelayPeers([]config.RelayPeer{
		{PubKey: peerKey(0xAB, 0xCD, 0xEF), Mode: config.RelayPeerDedupe},
		{PubKey: peerKey(0x11, 0x22, 0x33), Mode: config.RelayPeerIgnore},
		{PubKey: peerKey(0xAB, 0x99), Mode: config.RelayPeerIgnore}, // shares the dedupe peer's first byte
	})
	direct := floodVia(1, 0xAB)
	direct.Header = meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeGrpTxt, 0)

	for _, tc := range []struct {
		name          string
		pkt           *meshcore.Packet
		dedupe, found bool
	}{
		{"no path", floodVia(1), false, false},
		{"1-byte, dedupe peer last", floodVia(1, 0x01, 0xAB), true, true},
		{"2-byte, dedupe peer last", floodVia(2, 0x01, 0x02, 0xAB, 0xCD), true, true},
		{"3-byte, dedupe peer last", floodVia(3, 0xAB, 0xCD, 0xEF), true, true},
		{"2-byte, second byte differs", floodVia(2, 0xAB, 0xCE), false, false},
		{"dedupe peer mid-path", floodVia(1, 0xAB, 0x01), false, false},
		{"ignore peer last", floodVia(1, 0x11), false, true},
		{"2-byte, ignore peer sharing a first byte", floodVia(2, 0xAB, 0x99), false, true},
		{"direct packet", direct, false, false},
	} {
		if dedupe, found := r.lastHopPeer(tc.pkt); dedupe != tc.dedupe || found != tc.found {
			t.Errorf("%s: dedupe, found = %v, %v; want %v, %v", tc.name, dedupe, found, tc.dedupe, tc.found)
		}
	}
}

// Refusing a peer's copy is half of standing by; the other half is still relaying what it missed, and counting those.
func TestAllowForward_RelayPeers(t *testing.T) {
	newRepeater := func(peers ...config.RelayPeer) *Repeater {
		r := telemetryRepeater(t, nil)
		r.node = node.New(meshcore.NewLocalIdentityFromSeed([32]byte{1}), &recordingRadio{})
		t.Cleanup(r.node.Stop)
		r.relayPeers, r.hasDedupe = parseRelayPeers(peers)
		return r
	}
	main := config.RelayPeer{PubKey: peerKey(0xAB), Mode: config.RelayPeerDedupe}
	other := config.RelayPeer{PubKey: peerKey(0x11), Mode: config.RelayPeerIgnore}

	r := newRepeater(main, other)
	if r.allowForward(floodVia(1, 0xAB)) {
		t.Error("relayed the dedupe peer's own copy")
	}
	if r.allowForward(floodVia(1, 0x11)) {
		t.Error("relayed an ignore peer's copy")
	}
	if !r.allowForward(floodVia(1, 0x55)) {
		t.Error("refused a copy no peer relayed")
	}
	if got := r.Stats().BackupRelays; got == nil || *got != 1 {
		t.Errorf("backup relays = %v, want 1", got)
	}

	r = newRepeater(other)
	if !r.allowForward(floodVia(1, 0x55)) {
		t.Error("refused a copy no peer relayed")
	}
	if got := r.Stats().BackupRelays; got != nil {
		t.Errorf("backup relays = %d with no dedupe peer, want it left out", *got)
	}
}

// The hold works only if the dedupe peer's copy is processed at once and every other copy waits; a negative firmware delay must not eat into it.
func TestRxDelay_DedupeHold(t *testing.T) {
	const air = 100 // ms
	base3, hold6, off := 3.0, 6.0, true
	main := config.RelayPeer{PubKey: peerKey(0xAB), Mode: config.RelayPeerDedupe}
	other := config.RelayPeer{PubKey: peerKey(0x11), Mode: config.RelayPeerIgnore}
	mk := func(cfg config.RepeaterConfig, peers ...config.RelayPeer) *Repeater {
		r := &Repeater{cfg: cfg, sf: 7}
		r.relayPeers, r.hasDedupe = parseRelayPeers(peers)
		return r
	}
	snr := func(p *meshcore.Packet, v float32) *meshcore.Packet { p.SNR = v; return p }

	firmware := mk(config.RepeaterConfig{RxDelayBase: &base3}).rxDelay(snr(floodVia(1, 0x55), -7), 40, air)
	if firmware <= 0 {
		t.Fatalf("firmware rx delay for a weak packet = %v, want > 0", firmware)
	}

	for _, tc := range []struct {
		name string
		r    *Repeater
		pkt  *meshcore.Packet
		want time.Duration
	}{
		{"no peers, base 0", mk(config.RepeaterConfig{}), floodVia(1, 0x55), 0},
		{"dedupe peer's copy skips even a weak rxdelay", mk(config.RepeaterConfig{RxDelayBase: &base3}, main), snr(floodVia(1, 0xAB), -7), 0},
		{"other copy waits the default hold", mk(config.RepeaterConfig{}, main), floodVia(1, 0x55), 400 * time.Millisecond},
		{"the original, with no path, waits too", mk(config.RepeaterConfig{}, main), floodVia(1), 400 * time.Millisecond},
		{"hold adds to rxdelay", mk(config.RepeaterConfig{RxDelayBase: &base3}, main), snr(floodVia(1, 0x55), -7), firmware + 400*time.Millisecond},
		{"strong copy gets the whole hold", mk(config.RepeaterConfig{RxDelayBase: &base3}, main), snr(floodVia(1, 0x55), 10), 400 * time.Millisecond},
		{"custom hold", mk(config.RepeaterConfig{DedupeHold: &hold6}, main), floodVia(1, 0x55), 600 * time.Millisecond},
		{"ignore peer's copy keeps plain rxdelay", mk(config.RepeaterConfig{RxDelayBase: &base3}, main, other), snr(floodVia(1, 0x11), -7), firmware},
		{"ignore peers alone add no hold", mk(config.RepeaterConfig{}, other), floodVia(1, 0x55), 0},
		{"no hold when forwarding is off", mk(config.RepeaterConfig{DisableFwd: &off}, main), floodVia(1, 0x55), 0},
	} {
		if got := tc.r.rxDelay(tc.pkt, 40, air); got != tc.want {
			t.Errorf("%s: rxDelay = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// End to end on a real node: the dedupe peer's copy inside the hold stands ours down, and a packet it never relays goes out after the hold.
func TestRelayPeers_StandsDownForDedupePeer(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "peers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	freq, bw, sf, cr := 917.375, 62.5, 7, 5
	if err := st.Settings.Set(ctx, &store.Settings{ConnectionType: "kiss", PacketRetentionDays: 7, Freq: &freq, BW: &bw, SF: &sf, CR: &cr}); err != nil {
		t.Fatal(err)
	}
	est := hardware.LoRaAirtimeEstimator(&hardware.RadioConfig{FreqHz: 917_375_000, BwHz: 62_500, SF: 7, CR: 5})
	m := &relayModem{AirtimeSource: estAirtime(est)}
	mux := node.NewRadioMux(m)
	t.Cleanup(mux.Stop)
	zero := 0.0
	r, err := NewRepeater(config.RepeaterConfig{
		Name: "rp", PrivateKey: strings.Repeat("22", 32), AdminPassword: "pw", TxDelayFactor: &zero,
		RelayPeers: []config.RelayPeer{{PubKey: peerKey(0xAB, 0xCD), Mode: config.RelayPeerDedupe}},
	}, mux, st, nil, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop() })

	hear := func(payload []byte, snr float32, path ...byte) {
		raw, err := (&meshcore.Packet{
			Header:     meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, 0),
			PathLength: meshcore.MakePathLen(1, uint8(len(path))),
			Path:       path,
			Payload:    payload,
		}).ToBytes()
		if err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		h := m.handler
		m.mu.Unlock()
		h(raw, snr, -90, true)
	}
	sent := func() int {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.sent)
	}
	payload := func() []byte {
		p := make([]byte, 40)
		rand.Read(p)
		return p
	}
	// The original is header + path length + 40 bytes of payload.
	hold := time.Duration(config.DefaultDedupeHold*float64(est(42))) * time.Millisecond

	p := payload()
	hear(p, -5)
	time.Sleep(100 * time.Millisecond)
	hear(p, 10, 0xAB) // the dedupe peer relays it
	time.Sleep(hold + 300*time.Millisecond)
	if n := sent(); n != 0 {
		t.Fatalf("relayed %d copies of a packet the dedupe peer relayed, want 0", n)
	}

	start := time.Now()
	hear(payload(), -5) // one the dedupe peer misses
	for sent() == 0 && time.Since(start) < hold+2*time.Second {
		time.Sleep(5 * time.Millisecond)
	}
	waited := time.Since(start)
	if n := sent(); n != 1 {
		t.Fatalf("relayed %d copies of a packet the dedupe peer missed, want 1", n)
	}
	if waited < hold {
		t.Errorf("relayed after %v, before the %v hold ended", waited, hold)
	}
	if got := r.backupRelays.Load(); got != 1 {
		t.Errorf("backup relays = %d, want 1", got)
	}
}
