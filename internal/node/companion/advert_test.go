package companion

import (
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/OwlShack/OwlShack/internal/store"
)

// The library accepts a signed advert whose app data does not parse; firmware ignores it, and it must not blank a known peer.
func TestHandleAdvert_IgnoresAdvertWithoutAName(t *testing.T) {
	c, _, _, friend := hashSizeCompanion(t)
	var err error
	c.store.WriteSync(func() {
		err = c.store.Peers.Upsert(c.runCtx, &store.Peer{PubKey: friend.PublicKeyBytes(), Name: "friend", Type: "CHAT", Lat: -41_000_000, Lon: 174_000_000, LastSeen: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, appData := range map[string][]byte{"unparseable": {0xff, 0xff, 0xff}, "nameless": {0x01}} {
		adv := meshcore.Advert{PublicKey: friend.Identity, Timestamp: uint32(time.Now().Unix()), RawAppData: appData}
		adv.SignWith(friend)
		payload, err := adv.ToBytes()
		if err != nil {
			t.Fatal(err)
		}
		c.handleAdvert(&meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeAdvert, 0), Payload: payload})
		c.store.WriteSync(func() {}) // queued behind the handler's write
		p, err := c.store.Peers.GetByPubKey(c.runCtx, friend.PublicKeyBytes())
		if err != nil {
			t.Fatal(err)
		}
		if p.Name != "friend" || p.Type != "CHAT" || p.Lat == 0 {
			t.Errorf("%s advert overwrote the peer: %+v", name, p)
		}
	}
}
