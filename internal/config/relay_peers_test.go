package config

import (
	"strings"
	"testing"
)

// A key that doesn't decode is skipped on the packet path, so saving one would relay what the operator asked us not to.
func TestValidate_RelayPeers(t *testing.T) {
	key, other := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	hold := func(v float64) *float64 { return &v }
	for _, tc := range []struct {
		name   string
		peers  []RelayPeer
		hold   *float64
		wantOK bool
	}{
		{"none", nil, nil, true},
		{"dedupe and ignore", []RelayPeer{{key, RelayPeerDedupe}, {other, RelayPeerIgnore}}, hold(4), true},
		{"upper-case key", []RelayPeer{{strings.ToUpper(key), RelayPeerDedupe}}, nil, true},
		{"short key", []RelayPeer{{"abcd", RelayPeerDedupe}}, nil, false},
		{"not hex", []RelayPeer{{strings.Repeat("zz", 32), RelayPeerDedupe}}, nil, false},
		{"unknown mode", []RelayPeer{{key, "backup"}}, nil, false},
		{"listed twice", []RelayPeer{{key, RelayPeerDedupe}, {strings.ToUpper(key), RelayPeerIgnore}}, nil, false},
		{"hold below 4", nil, hold(3), false},
		{"hold at min", nil, hold(MinDedupeHold), true},
		{"hold above max", nil, hold(MaxDedupeHold + 1), false},
		{"hold at max", nil, hold(MaxDedupeHold), true},
	} {
		c := validConfig(t)
		c.Repeater = &RepeaterConfig{Name: "rp", AdminPassword: "pw", RelayPeers: tc.peers, DedupeHold: tc.hold}
		if err := c.Validate(); (err == nil) != tc.wantOK {
			t.Errorf("%s: err = %v, want ok %v", tc.name, err, tc.wantOK)
		}
	}
}
