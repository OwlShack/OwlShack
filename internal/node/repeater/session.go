package repeater

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/OwlShack/OwlShack/internal/store"
)

// Admin-over-mesh: the server side of internal/client/repeater, wire formats mirroring firmware MyMesh.cpp handleLoginReq / handleRequest.

// Permission levels as the ACL stores them (int); the role is the low 2 bits.
const (
	permGuest     = int(meshcore.PermACLGuest)
	permReadOnly  = int(meshcore.PermACLReadOnly)
	permReadWrite = int(meshcore.PermACLReadWrite)
	permAdmin     = int(meshcore.PermACLAdmin)
	permRoleMask  = int(meshcore.PermACLRoleMask)
)

const (
	respServerLoginOK = 0x00 // login response code
	firmwareVerLevel  = 2    // FIRMWARE_VER_LEVEL advertised in the login reply
	// Firmware SERVER_RESPONSE_DELAY: hold the reply until the half-duplex requester is back in RX.
	serverReplyDelay = 300 * time.Millisecond
)

// clientRoute is a learned return path to an admin client plus its per-hop hash width.
type clientRoute struct {
	path     []byte
	hashSize uint8
}

// handleAnonReq fires for every ANON_REQ we hear and self-filters by destination hash plus MAC.
func (r *Repeater) handleAnonReq(pkt *meshcore.Packet) {
	anon, err := meshcore.AnonReqFromBytes(pkt.Payload)
	if err != nil {
		return
	}
	me := r.node.Identity()
	if anon.Destination != me.PublicKey()[0] {
		return // not addressed to us
	}

	// The client puts its STATIC pubkey in EphemeralPubKey so the ACL recognises it across sessions.
	clientPub := anon.EphemeralPubKey
	secret, err := r.node.SharedSecret(meshcore.NewIdentity(clientPub))
	if err != nil {
		return
	}
	plain := anon.Decrypt(secret)
	if plain == nil || len(plain) < 5 {
		return // MAC failed (not for us) or too short
	}
	pkt.MarkDoNotRetransmit()

	// [timestamp:4][password:N], except that a leading control byte (0 < b < ' ') marks an unauthenticated sub-request.
	ts := binary.LittleEndian.Uint32(plain[:4])
	if plain[4] != 0 && plain[4] < ' ' {
		r.handleAnonSubReq(pkt, clientPub, secret, ts, plain[4], plain[5:])
		return
	}
	password := cString(plain[4:])

	perms, ok := r.authLogin(clientPub, password, ts)
	if !ok {
		return // bad password / replay — silent, matching the firmware
	}

	resp := make([]byte, 13)
	binary.LittleEndian.PutUint32(resp[:4], r.uniqueTimestamp())
	resp[4] = respServerLoginOK
	resp[5] = 0 // legacy keep-alive interval
	if perms&permRoleMask == permAdmin {
		resp[6] = 1
	}
	resp[7] = byte(perms)
	_, _ = rand.Read(resp[8:12]) // blob for packet-hash uniqueness
	resp[12] = firmwareVerLevel

	r.log.Info("admin login", "client", hex.EncodeToString(clientPub[:6]), "perms", perms, "flood", pkt.IsRouteFlood())
	if err := r.sendServerReply(pkt, clientPub, secret, resp); err != nil {
		r.log.Error("login reply failed", "error", err)
	}
}

// Anon sub-request types (firmware ANON_REQ_TYPE_*): unauthenticated, direct-routed only, answered ahead of any login.
const ()

// handleAnonSubReq replies [sender_ts:4][now:4][body] along the caller-supplied return path (request body [pathLenByte][path]).
func (r *Repeater) handleAnonSubReq(pkt *meshcore.Packet, clientPub [32]byte, secret []byte, ts uint32, subType byte, params []byte) {
	if pkt.IsRouteFlood() || !r.anonLimiter.allow() {
		return // firmware answers these on direct requests only
	}
	if len(params) < 1 {
		return
	}
	pathLenByte := params[0]
	if !meshcore.IsValidPathLen(pathLenByte) {
		return
	}
	hashSize := int(pathLenByte>>6)&3 + 1
	pathLen := int(pathLenByte&63) * hashSize
	if len(params) < 1+pathLen {
		return
	}

	cfg := r.cfgSnapshot()
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body[:4], ts)                        // reflected tag
	binary.LittleEndian.PutUint32(body[4:], uint32(time.Now().Unix())) // our clock
	switch subType {
	case meshcore.AnonReqTypeRegions:
		body = append(body, r.regionsExport()...)
	case meshcore.AnonReqTypeOwner:
		body = append(body, cfg.Name+"\n"+cfg.OwnerInfo...)
	case meshcore.AnonReqTypeBasic:
		var feat byte
		if cfg.IsFwdDisabled() {
			feat |= 0x80 // "is disabled" bit; we have no bridge bits to set
		}
		body = append(body, feat)
	default:
		return
	}

	me := r.node.Identity().PublicKey()
	payload, err := encPacket(secret, func(mac [2]byte, enc []byte) ([]byte, error) {
		return (&meshcore.Response{Destination: clientPub[0], Source: me[0], MAC: mac, EncryptedPayload: enc}).ToBytes()
	}, body)
	if err != nil {
		return
	}
	// pathLenByte is already the wire encoding ((hashSize-1)<<6 | hops).
	if err := r.sendPkt(&meshcore.Packet{
		Header:     meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeResponse, 0),
		PathLength: pathLenByte,
		Path:       params[1 : 1+pathLen],
		Payload:    payload,
	}, nil, serverReplyDelay); err != nil {
		r.log.Error("anon sub-request reply failed", "subType", subType, "error", err)
	}
}

// regionsExport mirrors RegionMap::exportNamesTo(mask=DENY_FLOOD): flood-allowed names, "*" first when unscoped flood is allowed.
func (r *Repeater) regionsExport() string {
	return regionNames(r.cfgRegions(), false)
}

// handlePath learns a client's return route from a PATH packet body [pathLenByte][path][extraType][extra...]; no reciprocal path is sent, matching the firmware.
func (r *Repeater) handlePath(pkt *meshcore.Packet) {
	p, err := meshcore.PathFromBytes(pkt.Payload)
	if err != nil || p.Destination != r.node.Identity().PublicKey()[0] {
		return
	}
	client, secret, ok := r.aclClient(p.Source, p.VerifyMAC)
	if !ok {
		return // not one of our logged-in clients
	}
	pp, err := meshcore.ParsePathPayload(p.Decrypt(secret))
	if err != nil {
		return
	}
	pkt.MarkDoNotRetransmit()
	pub, err := hex.DecodeString(client.PubKey)
	if err != nil || len(pub) != 32 {
		return
	}
	var clientPub [32]byte
	copy(clientPub[:], pub)
	r.learnRoute(clientPub, pp.Path, pp.PathHashSize())
}

// authLogin grants a permission from the login password; a blank password reauths an existing client, keeping its role.
func (r *Repeater) authLogin(clientPub [32]byte, password string, ts uint32) (int, bool) {
	pubHex := hex.EncodeToString(clientPub[:])
	existing := r.aclGet(pubHex)

	if password == "" && existing != nil {
		return existing.Permissions, true
	}

	cfg := r.cfgSnapshot()
	var perms int
	switch password {
	case cfg.AdminPassword:
		perms = permAdmin
	case cfg.GuestPassword:
		perms = permGuest
	default:
		// The firmware answers a bad password with silence, so the client can only time out.
		// Log it, or the two silent rejections here are indistinguishable from a lost reply.
		r.log.Debug("login rejected: password did not match", "client", hex.EncodeToString(clientPub[:6]))
		return 0, false
	}

	// Replay guard: the timestamp must strictly advance (a new client starts at 0).
	if existing == nil {
		existing = &store.RepeaterACLEntry{PubKey: pubHex}
	}
	if ts <= existing.LastTimestamp {
		r.log.Debug("login rejected: replay guard",
			"client", hex.EncodeToString(clientPub[:6]), "ts", ts, "lastTs", existing.LastTimestamp)
		return 0, false
	}

	perms |= existing.Permissions &^ permRoleMask // firmware keeps the upper (alert) bits
	r.aclPut(&store.RepeaterACLEntry{PubKey: pubHex, Permissions: perms, LastTimestamp: ts, LastSeen: time.Now()})
	return perms, true
}

// aclClient ties a REQ/TXT to a logged-in client by prefix byte plus MAC; ECDH runs outside the lock because it is slow.
func (r *Repeater) aclClient(src byte, verify func(secret []byte) bool) (*store.RepeaterACLEntry, []byte, bool) {
	r.acl.RLock()
	candidates := make([]store.RepeaterACLEntry, 0, len(r.acl.m))
	for _, e := range r.acl.m {
		candidates = append(candidates, *e)
	}
	r.acl.RUnlock()

	for i := range candidates {
		pub, err := hex.DecodeString(candidates[i].PubKey)
		if err != nil || len(pub) != 32 || pub[0] != src {
			continue
		}
		var pubArr [32]byte
		copy(pubArr[:], pub)
		secret, err := r.node.SharedSecret(meshcore.NewIdentity(pubArr))
		if err != nil {
			continue
		}
		if verify(secret) {
			e := candidates[i]
			return &e, secret, true
		}
	}
	return nil, nil, false
}

// touchClient mutates the cached entry in place; writing back a copy from aclClient/aclGet would revert a concurrent setperm.
func (r *Repeater) touchClient(e *store.RepeaterACLEntry, ts uint32) {
	r.acl.Lock()
	cur := r.acl.m[e.PubKey]
	if cur == nil {
		r.acl.Unlock()
		return // revoked between the lookup and here
	}
	cur.LastTimestamp = ts
	cur.LastSeen = time.Now()
	cp := *cur
	r.acl.Unlock()

	if r.store == nil {
		return
	}
	r.store.WriteAsync(func() {
		if err := r.store.RepeaterACL.Upsert(context.Background(), &cp); err != nil {
			r.log.Error("acl upsert failed", "error", err)
		}
	})
}

// aclLoad seeds the cache from the DB at construction, before handlers register.
func (r *Repeater) aclLoad() {
	entries, err := r.store.RepeaterACL.List(context.Background())
	if err != nil {
		r.log.Error("acl load failed", "error", err)
		return
	}
	r.acl.Lock()
	for i := range entries {
		e := entries[i]
		r.acl.m[e.PubKey] = &e
	}
	r.acl.Unlock()
}

// aclGet returns a copy of the cached entry for pubHex, or nil.
func (r *Repeater) aclGet(pubHex string) *store.RepeaterACLEntry {
	r.acl.RLock()
	defer r.acl.RUnlock()
	if e := r.acl.m[pubHex]; e != nil {
		cp := *e
		return &cp
	}
	return nil
}

// aclPut caches an entry (a copy) and persists it asynchronously.
func (r *Repeater) aclPut(e *store.RepeaterACLEntry) {
	cp := *e
	r.acl.Lock()
	r.acl.m[cp.PubKey] = &cp
	r.acl.Unlock()
	if r.store == nil {
		return
	}
	r.store.WriteAsync(func() {
		if err := r.store.RepeaterACL.Upsert(context.Background(), &cp); err != nil {
			r.log.Error("acl upsert failed", "error", err)
		}
	})
}

// aclDelete removes an entry from the cache and the DB.
func (r *Repeater) aclDelete(pubHex string) {
	r.acl.Lock()
	delete(r.acl.m, pubHex)
	r.acl.Unlock()
	if r.store == nil {
		return
	}
	r.store.WriteAsync(func() {
		if err := r.store.RepeaterACL.Delete(context.Background(), pubHex); err != nil {
			r.log.Error("acl delete failed", "error", err)
		}
	})
}

// aclMatchPrefix mirrors the firmware ClientACL::getClient byte-prefix compare.
func (r *Repeater) aclMatchPrefix(prefix string) (string, bool) {
	r.acl.RLock()
	defer r.acl.RUnlock()
	if _, ok := r.acl.m[prefix]; ok {
		return prefix, true
	}
	for k := range r.acl.m {
		if strings.HasPrefix(k, prefix) {
			return k, true
		}
	}
	return "", false
}

// ACLEntry is an admin-client ACL row for the API.
type ACLEntry struct {
	PubKey     string `json:"pubkey"`
	Name       string `json:"name"`       // resolved from peers/companions; "" if unknown
	Permission int    `json:"permission"` // 0=guest 1=read-only 2=read-write 3=admin
	LastSeen   int64  `json:"lastSeen"`   // unix seconds
}

// ACLList returns the current admin clients, most-recently-seen first.
func (r *Repeater) ACLList() []ACLEntry {
	r.acl.RLock()
	rows := make([]store.RepeaterACLEntry, 0, len(r.acl.m))
	for _, e := range r.acl.m {
		rows = append(rows, *e)
	}
	r.acl.RUnlock()

	out := make([]ACLEntry, len(rows))
	for i, e := range rows {
		out[i] = ACLEntry{
			PubKey:     e.PubKey,
			Name:       r.resolveName(e.PubKey),
			Permission: e.Permissions,
			LastSeen:   e.LastSeen.Unix(),
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen > out[j].LastSeen })
	return out
}

// RevokeACL mirrors an admin `setperm <pubkey> 0`.
func (r *Repeater) RevokeACL(pubHex string) error {
	return r.SetACL(pubHex, 0)
}

// resolveName falls back to the companions table because a companion's own pubkey isn't in its peers list; "" when unknown.
func (r *Repeater) resolveName(pubHex string) string {
	pub, err := hex.DecodeString(pubHex)
	if err != nil {
		return ""
	}
	if p, err := r.store.Peers.GetByPubKey(context.Background(), pub); err == nil && p != nil && p.Name != "" {
		return p.Name
	}
	if comps, err := r.store.Companions.List(context.Background()); err == nil {
		for _, c := range comps {
			if strings.EqualFold(c.PubKey, pubHex) {
				return c.Name
			}
		}
	}
	return ""
}

// sendServerReply answers a flood request with a flooded PATH-return and a direct one with a RESPONSE datagram, flooding when no route is cached.
func (r *Repeater) sendServerReply(reqPkt *meshcore.Packet, clientPub [32]byte, secret, respPlaintext []byte) error {
	me := r.node.Identity().PublicKey()

	if reqPkt.IsRouteFlood() {
		r.learnFloodRoute(clientPub, reqPkt)

		// PATH-return payload: [pathLenByte][path][extraType=RESPONSE][resp].
		inner := make([]byte, 0, 1+len(reqPkt.Path)+1+len(respPlaintext))
		inner = append(inner, reqPkt.PathLength)
		inner = append(inner, reqPkt.Path...)
		inner = append(inner, meshcore.PayloadTypeResponse)
		inner = append(inner, respPlaintext...)

		payload, err := encPacket(secret, func(mac [2]byte, enc []byte) ([]byte, error) {
			return (&meshcore.Path{Destination: clientPub[0], Source: me[0], MAC: mac, EncryptedPayload: enc}).ToBytes()
		}, inner)
		if err != nil {
			return err
		}
		out := &meshcore.Packet{
			Header:  meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypePath, 0),
			Payload: payload,
		}
		return r.sendFloodScoped(out, reqPkt, serverReplyDelay)
	}

	// Direct request → RESPONSE datagram.
	payload, err := encPacket(secret, func(mac [2]byte, enc []byte) ([]byte, error) {
		return (&meshcore.Response{Destination: clientPub[0], Source: me[0], MAC: mac, EncryptedPayload: enc}).ToBytes()
	}, respPlaintext)
	if err != nil {
		return err
	}
	routeType, pathLen, path := r.replyRoute(clientPub)
	out := &meshcore.Packet{
		Header:     meshcore.MakeHeader(routeType, meshcore.PayloadTypeResponse, 0),
		PathLength: pathLen,
		Path:       path,
		Payload:    payload,
	}
	if routeType == meshcore.RouteTypeFlood { // no learned route — flooded fallback gets the request's scope too
		return r.sendFloodScoped(out, reqPkt, serverReplyDelay)
	}
	return r.sendPkt(out, nil, serverReplyDelay)
}

// sendFloodScoped ports MyMesh::sendFloodReply + chooseReplyScope: reuse the request's scope, else the default, else unscoped.
func (r *Repeater) sendFloodScoped(out, reqPkt *meshcore.Packet, delay time.Duration) error {
	out.PathLength = meshcore.MakePathLen(reqPkt.PathHashSize(), 0)
	return r.sendPkt(out, r.replyScope(reqPkt), delay)
}

// learnFloodRoute caches a flood request's accumulated path, reversed into send order.
func (r *Repeater) learnFloodRoute(clientPub [32]byte, pkt *meshcore.Packet) {
	r.learnRoute(clientPub, reverseHops(pkt.Path, pkt.PathHashSize()), pkt.PathHashSize())
}

// learnRoute caches a client's return path, already in send order.
func (r *Repeater) learnRoute(clientPub [32]byte, path []byte, hashSize uint8) {
	cp := append([]byte(nil), path...)
	r.routes.Lock()
	r.routes.m[clientPub] = clientRoute{path: cp, hashSize: hashSize}
	r.routes.Unlock()
}

// replyRoute goes direct along a cached path, otherwise floods so the reply still reaches the client.
func (r *Repeater) replyRoute(clientPub [32]byte) (routeType byte, pathLen uint8, path []byte) {
	r.routes.Lock()
	route, ok := r.routes.m[clientPub]
	r.routes.Unlock()
	if !ok {
		return meshcore.RouteTypeFlood, 0, nil
	}
	if len(route.path) == 0 {
		return meshcore.RouteTypeDirect, 0, nil // direct neighbour, 0 hops
	}
	hashSize := int(route.hashSize)
	if hashSize == 0 {
		hashSize = int(meshcore.PathHashSize)
	}
	return meshcore.RouteTypeDirect, meshcore.MakePathLen(uint8(hashSize), uint8(len(route.path)/hashSize)), route.path
}

// encPacket splits the 2-byte MAC prefix off the ciphertext and hands both to build.
func encPacket(secret []byte, build func(mac [2]byte, enc []byte) ([]byte, error), plaintext []byte) ([]byte, error) {
	encrypted, err := meshcore.EncryptThenMAC(secret, plaintext)
	if err != nil {
		return nil, err
	}
	var mac [2]byte
	copy(mac[:], encrypted[:2])
	return build(mac, encrypted[2:])
}

// cString reads a NUL-terminated string from a (block-padded) plaintext buffer.
func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
