package api

import (
	"bytes"
	"encoding/hex"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/meshcore-go/OwlShack/internal/store"
	meshcore "github.com/meshcore-go/meshcore-go"
)

// The connection web folds the packet log's flood paths into routes toward us.

const webSelfID = "self"

type webNodeJSON struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	// Degrees x 1e6, as /api/peers; 0,0 = no position.
	Lat  int32  `json:"lat"`
	Lon  int32  `json:"lon"`
	Hash string `json:"hash,omitempty"`
	// Candidates: every repeater whose key starts with Hash. Pinned: the operator chose, not the distance pick.
	Candidates   []webCandidateJSON `json:"candidates"`
	Pinned       bool               `json:"pinned"`
	Observations int                `json:"observations"`
	Packets      int                `json:"packets"`
}

type webCandidateJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Lat      int32  `json:"lat"`
	Lon      int32  `json:"lon"`
	LastSeen string `json:"lastSeen"`
}

// webChainJSON is one distinct route, source side first and ending at "self"; SNR and RSSI belong to its last link.
type webChainJSON struct {
	Nodes    []string `json:"nodes"`
	Count    int      `json:"count"`
	First    int      `json:"first"`
	LastSeen string   `json:"lastSeen"`
	SNRSum   float64  `json:"snrSum"`
	SNRN     int      `json:"snrN"`
	SNRMin   *float64 `json:"snrMin"`
	SNRMax   *float64 `json:"snrMax"`
	RSSISum  int      `json:"rssiSum"`
	RSSIN    int      `json:"rssiN"`
}

type webLatLon struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type connectionWebJSON struct {
	Self          *webLatLon     `json:"self"`
	Hours         int            `json:"hours"`
	RetentionDays int            `json:"retentionDays"`
	Nodes         []webNodeJSON  `json:"nodes"`
	Chains        []webChainJSON `json:"chains"`
}

type webNode struct {
	webNodeJSON
	packets map[string]bool
}

type webChain struct {
	webChainJSON
	lastSeen time.Time
}

type webBuilder struct {
	self      *webLatLon
	relay     []byte // our repeater's pubkey; nil when none is configured
	byPubkey  map[string]*store.Peer
	repByHash map[string][]*store.Peer // hex prefix of every width 1-3 -> repeaters
	pins      map[string][]byte        // hash -> the operator's choice; a nil pubkey = none of the known peers
	nodes     map[string]*webNode
	chains    map[string]*webChain
	firstCopy map[string]webFirstCopy // per packet hash
}

// webFirstCopy is the lowest-id copy of a packet and the chain it came over.
type webFirstCopy struct {
	id  int64
	key string
}

func newWebBuilder(peers []store.Peer, self *webLatLon, relay []byte, pins map[string][]byte) *webBuilder {
	b := &webBuilder{
		self: self, relay: relay, pins: pins,
		byPubkey:  make(map[string]*store.Peer, len(peers)),
		repByHash: map[string][]*store.Peer{},
		nodes:     map[string]*webNode{},
		chains:    map[string]*webChain{},
		firstCopy: map[string]webFirstCopy{},
	}
	for i := range peers {
		p := &peers[i]
		b.byPubkey[hex.EncodeToString(p.PubKey)] = p
		// Only a repeater forwards, so only a repeater can be the owner of a hop hash.
		if p.Type != "REPEATER" {
			continue
		}
		for size := 1; size <= 3 && size <= len(p.PubKey); size++ {
			k := hex.EncodeToString(p.PubKey[:size])
			b.repByHash[k] = append(b.repByHash[k], p)
		}
	}
	return b
}

// add folds one received flood packet in; anything without a usable relay path is ignored.
func (b *webBuilder) add(rec *store.PacketRecord) {
	if rec.Direction != "rx" {
		return
	}
	pkt, err := meshcore.PacketFromBytes(rec.Raw)
	if err != nil || !pkt.IsRouteFlood() || pkt.PayloadType() == meshcore.PayloadTypeTrace {
		return
	}
	size := int(pkt.PathHashSize())
	hops := make([][]byte, 0, pkt.PathHashCount())
	for i := 0; size > 0 && i+size <= len(pkt.Path); i += size {
		hop := pkt.Path[i : i+size]
		// ponytail: our repeater's hash in the path is our own relay heard back; a foreign repeater sharing it is dropped too.
		if len(b.relay) >= size && bytes.Equal(hop, b.relay[:size]) {
			return
		}
		hops = append(hops, hop)
	}

	ids := make([]string, len(hops)+2)
	ids[len(ids)-1] = webSelfID
	next := b.self
	for i := len(hops) - 1; i >= 0; i-- {
		var at *webLatLon
		ids[i+1], at = b.resolveHop(hops[i], next)
		if at != nil {
			next = at
		}
	}
	if pkt.PayloadType() == meshcore.PayloadTypeAdvert && len(pkt.Payload) >= meshcore.PubKeySize {
		// Stored peers came from adverts verified at ingest; an unknown key is left unnamed rather than trusted.
		if p := b.byPubkey[hex.EncodeToString(pkt.Payload[:meshcore.PubKeySize])]; p != nil {
			ids[0] = b.peerNode(p).ID
		}
	}

	chainIDs := ids[:0]
	for _, id := range ids {
		if id != "" && (len(chainIDs) == 0 || chainIDs[len(chainIDs)-1] != id) {
			chainIDs = append(chainIDs, id)
		}
	}
	if len(chainIDs) < 2 {
		return
	}

	key := strings.Join(chainIDs, ">")
	c := b.chains[key]
	if c == nil {
		c = &webChain{webChainJSON: webChainJSON{Nodes: append([]string(nil), chainIDs...)}}
		b.chains[key] = c
	}
	c.Count++
	if rec.ReceivedAt.After(c.lastSeen) {
		c.lastSeen = rec.ReceivedAt
	}
	if rec.SNR != nil {
		v := *rec.SNR
		c.SNRSum += v
		c.SNRN++
		if c.SNRMin == nil || v < *c.SNRMin {
			c.SNRMin = &v
		}
		if c.SNRMax == nil || v > *c.SNRMax {
			c.SNRMax = &v
		}
	}
	if rec.RSSI != nil {
		c.RSSISum += int(*rec.RSSI)
		c.RSSIN++
	}

	for _, id := range chainIDs[:len(chainIDs)-1] {
		n := b.nodes[id]
		n.Observations++
		n.packets[rec.PacketHash] = true
	}
	if fc, ok := b.firstCopy[rec.PacketHash]; !ok || rec.ID < fc.id {
		b.firstCopy[rec.PacketHash] = webFirstCopy{rec.ID, key}
	}
}

// resolveHop picks a hash's owner: a pin, else the candidate nearest `next`, else the most recently heard.
func (b *webBuilder) resolveHop(hash []byte, next *webLatLon) (string, *webLatLon) {
	h := hex.EncodeToString(hash)
	cands := b.repByHash[h]
	if pin, pinned := b.pins[h]; pinned {
		if pin == nil {
			return b.unresolvedNode(h), nil
		}
		// A pinned peer since deleted falls back to the automatic pick.
		if p := b.byPubkey[hex.EncodeToString(pin)]; p != nil {
			return b.hopNode(p, h)
		}
	}
	if len(cands) == 0 {
		return b.unresolvedNode(h), nil
	}
	var best *store.Peer
	if next != nil {
		bestKm := math.Inf(1)
		for _, c := range cands {
			if !c.HasLocation() {
				continue
			}
			if d := haversineKm(next.Lat, next.Lon, float64(c.Lat)/1e6, float64(c.Lon)/1e6); d < bestKm {
				best, bestKm = c, d
			}
		}
	}
	if best == nil {
		best = cands[0]
		for _, c := range cands[1:] {
			if c.LastSeen.After(best.LastSeen) {
				best = c
			}
		}
	}
	return b.hopNode(best, h)
}

func (b *webBuilder) hopNode(p *store.Peer, hash string) (string, *webLatLon) {
	n := b.peerNode(p)
	n.Hash = hash
	if !p.HasLocation() {
		return n.ID, nil
	}
	return n.ID, &webLatLon{Lat: float64(p.Lat) / 1e6, Lon: float64(p.Lon) / 1e6}
}

func (b *webBuilder) unresolvedNode(hash string) string {
	id := "h:" + hash
	if b.nodes[id] == nil {
		b.nodes[id] = &webNode{webNodeJSON: webNodeJSON{ID: id, Hash: hash}, packets: map[string]bool{}}
	}
	return id
}

func (b *webBuilder) peerNode(p *store.Peer) *webNode {
	id := hex.EncodeToString(p.PubKey)
	n := b.nodes[id]
	if n == nil {
		n = &webNode{
			webNodeJSON: webNodeJSON{ID: id, Name: p.Name, Type: p.Type, Lat: p.Lat, Lon: p.Lon},
			packets:     map[string]bool{},
		}
		b.nodes[id] = n
	}
	return n
}

func (b *webBuilder) result() connectionWebJSON {
	for _, fc := range b.firstCopy {
		b.chains[fc.key].First++
	}
	out := connectionWebJSON{Self: b.self, Nodes: []webNodeJSON{}, Chains: []webChainJSON{}}
	for _, n := range b.nodes {
		n.Packets = len(n.packets)
		n.Candidates = []webCandidateJSON{}
		for _, c := range b.repByHash[n.Hash] {
			n.Candidates = append(n.Candidates, webCandidateJSON{
				ID: hex.EncodeToString(c.PubKey), Name: c.Name, Lat: c.Lat, Lon: c.Lon,
				LastSeen: c.LastSeen.UTC().Format(TimestampLayout),
			})
		}
		_, n.Pinned = b.pins[n.Hash]
		out.Nodes = append(out.Nodes, n.webNodeJSON)
	}
	for _, c := range b.chains {
		c.LastSeen = c.lastSeen.UTC().Format(TimestampLayout)
		out.Chains = append(out.Chains, c.webChainJSON)
	}
	return out
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

func (s *Server) handleConnectionWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	retention := s.store.Settings.PacketRetentionDays(ctx)
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	hours = min(hours, retention*24)

	peers, err := s.store.Peers.LoadAll(ctx)
	if err != nil {
		s.serverError(w, "failed to load peers", err)
		return
	}

	// Our position follows useOwnPosition.ts: the repeater's, else the first companion's.
	at := func(lat, lon *float64) *webLatLon {
		if lat == nil || lon == nil || (*lat == 0 && *lon == 0) {
			return nil
		}
		return &webLatLon{Lat: *lat, Lon: *lon}
	}
	var self *webLatLon
	var relay []byte
	if rep, err := s.store.Repeater.Get(ctx); err == nil {
		self = at(rep.Latitude, rep.Longitude)
		relay, _ = hex.DecodeString(rep.PubKey)
	}
	if self == nil {
		comps, err := s.store.Companions.List(ctx)
		if err != nil {
			s.serverError(w, "failed to load companions", err)
			return
		}
		for _, c := range comps {
			if self = at(c.Latitude, c.Longitude); self != nil {
				break
			}
		}
	}

	pins, err := s.store.HopPins.List(ctx)
	if err != nil {
		s.serverError(w, "failed to load hop pins", err)
		return
	}
	b := newWebBuilder(peers, self, relay, pins)
	if err := s.store.Packets.ScanFloodRxSince(ctx, time.Now().Add(-time.Duration(hours)*time.Hour), b.add); err != nil {
		s.serverError(w, "failed to read packets", err)
		return
	}
	out := b.result()
	out.Hours, out.RetentionDays = hours, retention
	writeJSON(w, http.StatusOK, out)
}

// handlePinHop records which peer owns a path hash; a null pubkey says none of the known ones does.
func (s *Server) handlePinHop(w http.ResponseWriter, r *http.Request) {
	hash, ok := hopHashParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Pubkey *string `json:"pubkey"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var pubkey []byte
	if body.Pubkey != nil {
		pk, err := hex.DecodeString(*body.Pubkey)
		if err != nil || len(pk) != meshcore.PubKeySize || !strings.HasPrefix(hex.EncodeToString(pk), hash) {
			writeError(w, http.StatusBadRequest, "pubkey must be a full key that starts with the hash")
			return
		}
		pubkey = pk
	}
	var err error
	s.store.WriteSync(func() { err = s.store.HopPins.Set(r.Context(), hash, pubkey) })
	if err != nil {
		s.serverError(w, "failed to pin hop", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUnpinHop hands a path hash back to the automatic pick.
func (s *Server) handleUnpinHop(w http.ResponseWriter, r *http.Request) {
	hash, ok := hopHashParam(w, r)
	if !ok {
		return
	}
	var err error
	s.store.WriteSync(func() { err = s.store.HopPins.Delete(r.Context(), hash) })
	if err != nil {
		s.serverError(w, "failed to unpin hop", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func hopHashParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	hash := strings.ToLower(r.PathValue("hash"))
	if b, err := hex.DecodeString(hash); err != nil || len(b) < 1 || len(b) > 3 {
		writeError(w, http.StatusBadRequest, "hash must be 1-3 bytes of hex")
		return "", false
	}
	return hash, true
}
