package companion

import (
	"testing"

	"github.com/OwlShack/OwlShack/internal/meshpath"
)

// The library answers a flood path return from a peer it knows and marks the packet; we must not answer it a second time, but still persist the route.
func TestPathReturn_MarkedIsOnlyPersisted(t *testing.T) {
	for _, marked := range []bool{true, false} {
		c, radio, self, friend := hashSizeCompanion(t)
		secret, _ := friend.SharedSecret(self.Identity)
		learned := []byte{0xAA, 0xBB}
		pkt, err := meshpath.BuildReturn(friend.PublicKey(), self.PublicKeyBytes(), secret, learned, byte(len(learned)), 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		pkt.Path, pkt.PathLength = []byte{0x11}, 1 // arrived by flood through one repeater
		if marked {
			pkt.MarkDoNotRetransmit()
		}
		c.handleDMPathReturn(pkt)
		c.store.WriteSync(func() {})

		sent := radio.take()
		if marked && len(sent) != 0 {
			t.Errorf("answered a path return the library already answered (%d sent)", len(sent))
		}
		if !marked && len(sent) != 1 {
			t.Errorf("an unknown peer's flood path return got %d answers, want 1", len(sent))
		}
		ct, err := c.store.Contacts.Get(c.runCtx, c.cfg.ID, friend.PublicKeyBytes())
		if err != nil {
			t.Fatal(err)
		}
		if string(ct.OutPath) != string(learned) {
			t.Errorf("marked=%v: stored route %x, want %x", marked, ct.OutPath, learned)
		}
	}
}
