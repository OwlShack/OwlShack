package meshpath

import (
	"bytes"
	"testing"

	meshcore "github.com/OwlShack/meshcore-go"
)

func secret() []byte {
	s := make([]byte, 32)
	for i := range s {
		s[i] = byte(i + 1)
	}
	return s
}

// Two returns over the same route must differ: AES-ECB has no IV, so identical plaintext gives
// identical bytes and a relay's seen-table drops the second (Mesh.cpp:472-477 salts for this).
func TestBuildReturn_NoExtraIsSaltedUnique(t *testing.T) {
	t.Parallel()

	var self [meshcore.PubKeySize]byte
	dest := bytes.Repeat([]byte{0xAB}, 32)
	inPath := []byte{0x11, 0x22}

	a, err := BuildReturn(self, dest, secret(), inPath, 0x02, 0, nil)
	if err != nil {
		t.Fatalf("BuildReturn: %v", err)
	}
	b, err := BuildReturn(self, dest, secret(), inPath, 0x02, 0, nil)
	if err != nil {
		t.Fatalf("BuildReturn: %v", err)
	}
	if bytes.Equal(a.Payload, b.Payload) {
		t.Error("two extra-less path returns encoded identically, so relays would drop the second")
	}
}

// With a real extra payload the bytes are determined by it, and no salt is added.
func TestBuildReturn_WithExtraIsDeterministic(t *testing.T) {
	t.Parallel()

	var self [meshcore.PubKeySize]byte
	dest := bytes.Repeat([]byte{0xAB}, 32)
	extra := []byte{1, 2, 3, 4}

	a, _ := BuildReturn(self, dest, secret(), []byte{0x11}, 0x01, meshcore.PayloadTypeAck, extra)
	b, _ := BuildReturn(self, dest, secret(), []byte{0x11}, 0x01, meshcore.PayloadTypeAck, extra)
	if !bytes.Equal(a.Payload, b.Payload) {
		t.Error("an ack-carrying path return must not be salted: the ack is what makes it unique")
	}
}

func TestDirect_FramesTheRoute(t *testing.T) {
	t.Parallel()

	pkt := &meshcore.Packet{}
	Direct(pkt, []byte{0xaa, 0xbb, 0xcc, 0xdd}, 2)

	if !pkt.IsRouteDirect() {
		t.Error("Direct must set the direct route type")
	}
	if got := pkt.PathHashCount(); got != 2 {
		t.Errorf("hop count = %d, want 2 (4 bytes at 2 bytes per hash)", got)
	}
	if got := pkt.PathHashSize(); got != 2 {
		t.Errorf("hash size = %d, want 2", got)
	}
}
