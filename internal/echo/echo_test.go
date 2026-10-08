package echo

import (
	"io"
	"log/slog"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
)

// TestPrune: entries past ttl are dropped, fresh ones kept.
func TestPrune(t *testing.T) {
	tr := NewTracker(nil, nil, nil)
	tr.ttl = 10 * time.Millisecond
	var old, fresh [meshcore.PacketHashSize]byte
	old[0], fresh[0] = 1, 2

	oldKey := pendingKey{hash: old, companion: "a"}
	freshKey := pendingKey{hash: fresh, companion: "a"}
	tr.pending[oldKey] = &entry{registeredAt: time.Now().Add(-time.Second)}
	tr.pending[freshKey] = &entry{registeredAt: time.Now()}
	tr.Prune()

	if _, ok := tr.pending[oldKey]; ok {
		t.Error("expired entry survived Prune")
	}
	if _, ok := tr.pending[freshKey]; !ok {
		t.Error("fresh entry was pruned")
	}
}

// Two companions on the same channel see the same packet: one sends it, the other decodes it as a
// received message. Keyed on the hash alone the second Track replaced the first, so the sender kept
// only the echoes that arrived before the overwrite — one, in practice — and lost the rest.
func TestTrack_TwoCompanionsOnOnePacketDoNotCollide(t *testing.T) {
	tr := NewTracker(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	var hash [meshcore.PacketHashSize]byte
	hash[0] = 9

	tr.Track(hash, Sent{MessageID: 101, CompanionID: 1, Companion: "Wes", Channel: "#westest"})     // the sender
	tr.Track(hash, Sent{MessageID: 202, CompanionID: 2, Companion: "Wes Bot", Channel: "#westest"}) // the other companion receiving the same packet

	if len(tr.pending) != 2 {
		t.Fatalf("pending holds %d entries, want 2 — the second companion overwrote the first", len(tr.pending))
	}
	if e := tr.pending[pendingKey{hash: hash, companion: "Wes"}]; e == nil || e.sent.MessageID != 101 {
		t.Errorf("sender's entry = %+v, want messageID 101 still tracked", e)
	}
	if e := tr.pending[pendingKey{hash: hash, companion: "Wes Bot"}]; e == nil || e.sent.MessageID != 202 {
		t.Errorf("other companion's entry = %+v, want messageID 202", e)
	}
}
