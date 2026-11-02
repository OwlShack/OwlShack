package store

import (
	"slices"
	"strings"
	"testing"
)

// The list rides in one JSON column beside regions; a field missed in Get or Set reads back as no peers, and the node relays everything again.
func TestRepeater_RelayPeersRoundTrip(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)

	hold := 2.5
	want := []RepeaterRelayPeer{
		{PubKey: strings.Repeat("ab", 32), Mode: "dedupe"},
		{PubKey: strings.Repeat("cd", 32), Mode: "ignore"},
	}
	if err := st.Repeater.Set(t.Context(), &Repeater{Name: "rp", RelayPeers: want, DedupeHold: &hold}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Repeater.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.RelayPeers, want) {
		t.Errorf("relay peers = %v, want %v", got.RelayPeers, want)
	}
	if got.DedupeHold == nil || *got.DedupeHold != hold {
		t.Errorf("dedupe hold = %v, want %v", got.DedupeHold, hold)
	}

	if err := st.Repeater.Set(t.Context(), &Repeater{Name: "rp"}); err != nil {
		t.Fatal(err)
	}
	got, err = st.Repeater.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RelayPeers) != 0 || got.DedupeHold != nil {
		t.Errorf("cleared row read back peers %v, hold %v; want none", got.RelayPeers, got.DedupeHold)
	}
}
