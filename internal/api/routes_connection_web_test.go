package api

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-go/OwlShack/internal/store"
	meshcore "github.com/meshcore-go/meshcore-go"
)

func webKey(b byte) []byte { return bytes.Repeat([]byte{b}, meshcore.PubKeySize) }

func webPacket(id int64, hash string, route, payloadType byte, dir string, payload []byte, hops ...byte) *store.PacketRecord {
	raw := append([]byte{meshcore.MakeHeader(route, payloadType, 0), byte(len(hops))}, hops...)
	snr := -float64(id)
	return &store.PacketRecord{
		ID: id, ReceivedAt: time.Unix(1_700_000_000+id, 0), Direction: dir,
		Raw: append(raw, payload...), PacketHash: hash, SNR: &snr,
	}
}

func TestConnectionWeb_Builder(t *testing.T) {
	t.Parallel()
	rep := func(key []byte, name string, lat, lon int32) store.Peer {
		return store.Peer{PubKey: key, Name: name, Type: "REPEATER", Lat: lat, Lon: lon}
	}
	near := append([]byte{0x55, 0x01}, webKey(0)[2:]...)
	far := append([]byte{0x55, 0x02}, webKey(0)[2:]...)
	peers := []store.Peer{
		rep(webKey(0x22), "R2", -41_100_000, 174_100_000),
		rep(webKey(0x33), "R3", -41_200_000, 174_000_000),
		rep(webKey(0x44), "R4", -41_300_000, 174_200_000),
		// far is listed first and was heard last, so only the distance check picks near.
		{PubKey: far, Name: "far", Type: "REPEATER", Lat: -45_000_000, Lon: 170_000_000, LastSeen: time.Now()},
		rep(near, "near", -41_010_000, 174_010_000),
		{PubKey: webKey(0x66), Name: "phone", Type: "CHAT"},
	}
	self := &webLatLon{Lat: -41, Lon: 174}
	relay := webKey(0x99)

	// An advert payload is the sender's pubkey, a timestamp and a signature.
	advertFromR4 := append(webKey(0x44), make([]byte, 4+64)...)
	text := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	flood, direct := meshcore.RouteTypeFlood, meshcore.RouteTypeDirect
	advert, grp, trace := meshcore.PayloadTypeAdvert, meshcore.PayloadTypeGrpTxt, meshcore.PayloadTypeTrace

	b := newWebBuilder(peers, self, relay, nil)
	for _, rec := range []*store.PacketRecord{
		// p1 heard twice: first over R2, then over R3. p2 and p3 only over R2.
		webPacket(1, "p1", flood, advert, "rx", advertFromR4, 0x22),
		webPacket(2, "p1", flood, advert, "rx", advertFromR4, 0x33),
		webPacket(3, "p2", flood, advert, "rx", advertFromR4, 0x22),
		webPacket(4, "p3", flood, advert, "rx", advertFromR4, 0x22),
		// A colliding hash resolves to the repeater nearest us; a CHAT-only hash stays unresolved.
		webPacket(5, "p4", flood, grp, "rx", text, 0x66, 0x55),
		// No relay path to learn from.
		webPacket(6, "p5", direct, grp, "rx", text, 0x22),
		webPacket(7, "p6", flood, trace, "rx", text, 0x22),
		webPacket(8, "p7", flood, grp, "tx", text, 0x22),
		// Our own relay heard back.
		webPacket(9, "p8", flood, grp, "rx", text, 0x22, 0x99, 0x33),
		// Zero hops and no identifiable source: nothing to draw.
		webPacket(10, "p9", flood, grp, "rx", text),
	} {
		b.add(rec)
	}
	out := b.result()

	r2, r3, r4 := hex.EncodeToString(webKey(0x22)), hex.EncodeToString(webKey(0x33)), hex.EncodeToString(webKey(0x44))
	nearID := hex.EncodeToString(near)
	chains := map[string]webChainJSON{}
	for _, c := range out.Chains {
		chains[strings.Join(c.Nodes, ">")] = c
	}
	want := map[string]struct{ count, first int }{
		r4 + ">" + r2 + ">self":    {3, 3},
		r4 + ">" + r3 + ">self":    {1, 0},
		"h:66>" + nearID + ">self": {1, 1},
	}
	if len(chains) != len(want) {
		t.Errorf("got %d chains %v, want %d", len(chains), keys(chains), len(want))
	}
	for k, w := range want {
		c, ok := chains[k]
		if !ok {
			t.Errorf("missing chain %s", k)
			continue
		}
		if c.Count != w.count || c.First != w.first {
			t.Errorf("chain %s: count %d first %d, want %d %d", k, c.Count, c.First, w.count, w.first)
		}
	}

	viaR2 := chains[r4+">"+r2+">self"]
	if viaR2.SNRN != 3 || *viaR2.SNRMin != -4 || *viaR2.SNRMax != -1 || viaR2.SNRSum != -8 {
		t.Errorf("R2 last-link SNR: n %d min %v max %v sum %v, want 3 -4 -1 -8", viaR2.SNRN, *viaR2.SNRMin, *viaR2.SNRMax, viaR2.SNRSum)
	}

	nodes := map[string]webNodeJSON{}
	for _, n := range out.Nodes {
		nodes[n.ID] = n
	}
	if n := nodes[r4]; n.Observations != 4 || n.Packets != 3 {
		t.Errorf("R4: observations %d packets %d, want 4 3", n.Observations, n.Packets)
	}
	if n := nodes[nearID]; len(n.Candidates) != 2 || n.Pinned {
		t.Errorf("near: %d candidates, pinned %v; want both 55 repeaters, unpinned", len(n.Candidates), n.Pinned)
	}
	if _, ok := nodes["h:66"]; !ok {
		t.Error("a hash matching only a CHAT node should stay an unresolved h:66 node")
	}

	// The operator overrides the distance pick with far, and says no known repeater is 33.
	farID := hex.EncodeToString(far)
	pinned := newWebBuilder(peers, self, relay, map[string][]byte{"55": far, "33": nil})
	pinned.add(webPacket(2, "p1", flood, advert, "rx", advertFromR4, 0x33))
	pinned.add(webPacket(5, "p4", flood, grp, "rx", text, 0x66, 0x55))
	out = pinned.result()
	chains = map[string]webChainJSON{}
	for _, c := range out.Chains {
		chains[strings.Join(c.Nodes, ">")] = c
	}
	for _, k := range []string{r4 + ">h:33>self", "h:66>" + farID + ">self"} {
		if _, ok := chains[k]; !ok {
			t.Errorf("pinned: missing chain %s, got %v", k, keys(chains))
		}
	}
	nodes = map[string]webNodeJSON{}
	for _, n := range out.Nodes {
		nodes[n.ID] = n
	}
	if n := nodes[farID]; !n.Pinned || len(n.Candidates) != 2 {
		t.Errorf("far: pinned %v with %d candidates, want pinned with 2", n.Pinned, len(n.Candidates))
	}
	if n := nodes["h:33"]; !n.Pinned || len(n.Candidates) != 1 {
		t.Errorf("h:33: pinned %v with %d candidates, want pinned with R3 still offered", n.Pinned, len(n.Candidates))
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
