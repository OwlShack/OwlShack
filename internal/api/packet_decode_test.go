package api

import (
	"testing"

	meshcore "github.com/meshcore-go/meshcore-go"
)

// The firmware puts the control type in the flags byte's top nibble and the node type in the low one (simple_repeater/MyMesh.cpp:786-809).
func TestPacketSummary_DiscoverControl(t *testing.T) {
	for flags, want := range map[byte]string{
		0x80: "Control: Discover Request",
		0x92: "Control: Discover Response", // from a repeater
		0x91: "Control: Discover Response", // from a chat node
		0x30: "Control (flags:30)",
	} {
		pkt := &meshcore.Packet{
			Header:  meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, 0),
			Payload: []byte{flags, 0x02, 1, 2, 3, 4},
		}
		if got := PacketSummary(pkt, nil); got != want {
			t.Errorf("flags %02x: %q, want %q", flags, got, want)
		}
	}
}
