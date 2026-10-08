package repeater

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/OwlShack/OwlShack/internal/meshpath"
	"github.com/OwlShack/OwlShack/internal/telemetry"
)

func (rm *Client) SendStatusReq(pubkeyHex string, timeout time.Duration) (*Status, error) {
	body := make([]byte, 5)
	body[0] = meshcore.ReqTypeGetStatus
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "status")
	if err != nil {
		return nil, err
	}
	return parseRepeaterStatus(data)
}

// SendRoomKeepAlive is fire-and-forget: the firmware honours only a direct keep-alive and streams posts over the DM path.
func (rm *Client) SendRoomKeepAlive(pubkeyHex string, since uint32) error {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return fmt.Errorf("%w: hex: %w", ErrBadPubkey, err)
	}
	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadPubkey, err)
	}
	peer := rm.node.Peers().Lookup(peerIdentity.PublicKey())
	if peer == nil {
		return ErrUnknownPeer
	}
	rm.mu.Lock()
	sess := rm.sessions[pubkeyHex]
	rm.mu.Unlock()
	if sess == nil || sess.sharedSecret == nil {
		return fmt.Errorf("%w to this room", ErrNotLoggedIn)
	}
	if outPath, _ := learnedRoute(peer); outPath == nil {
		return ErrNoDirectRoute
	}

	// [tag:4][0x02][since:4] — exactly the 9 bytes the room hashes for its ACK.
	plaintext := make([]byte, 9)
	binary.LittleEndian.PutUint32(plaintext[:4], rm.UniqueTimestamp())
	plaintext[4] = meshcore.ReqTypeKeepAlive
	binary.LittleEndian.PutUint32(plaintext[5:9], since)

	encrypted, err := meshcore.EncryptThenMAC(sess.sharedSecret, plaintext)
	if err != nil {
		return fmt.Errorf("encrypting keep-alive: %w", err)
	}
	var mac [2]byte
	copy(mac[:], encrypted[:2])
	peerPub := peerIdentity.PublicKey()
	reqBytes, err := (&meshcore.Request{
		Destination:      peerPub[0],
		Source:           sess.localPubKey[0],
		MAC:              mac,
		EncryptedPayload: encrypted[2:],
	}).ToBytes()
	if err != nil {
		return fmt.Errorf("encoding keep-alive: %w", err)
	}
	pkt, _, _ := rm.routedPacket(peer, meshcore.PayloadTypeReq, reqBytes)
	return meshpath.Send(rm.node, pkt, rm.scopeFor(peerPub[:]), 0)
}

// SendRoomStatusReq is SendStatusReq for a room server, whose ServerStats trailer differs.
func (rm *Client) SendRoomStatusReq(pubkeyHex string, timeout time.Duration) (*Status, error) {
	body := make([]byte, 5)
	body[0] = meshcore.ReqTypeGetStatus
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "room status")
	if err != nil {
		return nil, err
	}
	return parseRoomStatus(data)
}

func (rm *Client) SendNeighborsReq(pubkeyHex string, count uint8, offset uint16, timeout time.Duration) (*Neighbors, error) {
	// payload: type(1) request_version(1) count(1) offset(2) order_by(1) prefix_len(1) random(4)
	body := make([]byte, 11)
	body[0] = meshcore.ReqTypeGetNeighbours
	body[1] = 0 // request version
	body[2] = count
	binary.LittleEndian.PutUint16(body[3:5], offset)
	body[5] = 2 // order_by: strongest_to_weakest
	body[6] = 6 // 6-byte pubkey prefix (matches CLI `neighbors` output)
	if _, err := rand.Read(body[7:11]); err != nil {
		return nil, fmt.Errorf("generating random: %w", err)
	}
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "neighbors")
	if err != nil {
		return nil, err
	}
	return parseRepeaterNeighbors(data, 6)
}

func (rm *Client) SendOwnerInfoReq(pubkeyHex string, timeout time.Duration) (*OwnerInfo, error) {
	body := make([]byte, 5)
	body[0] = meshcore.ReqTypeGetOwnerInfo
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "owner")
	if err != nil {
		return nil, err
	}
	return parseRepeaterOwnerInfo(data), nil
}

func (rm *Client) SendAccessListReq(pubkeyHex string, timeout time.Duration) (*AccessList, error) {
	// payload: type(1) reserved(2) reserved(2) random(4)
	body := make([]byte, 9)
	body[0] = meshcore.ReqTypeGetAccessList
	if _, err := rand.Read(body[5:9]); err != nil {
		return nil, fmt.Errorf("generating random: %w", err)
	}
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "access list")
	if err != nil {
		return nil, err
	}
	return parseRepeaterAccessList(data), nil
}

// SetAccessPerm needs an admin session; perms 0 revokes and accepts a prefix, granting needs the full key.
func (rm *Client) SetAccessPerm(pubkeyHex, targetPubkeyHex string, perms uint8, timeout time.Duration) error {
	cmd := fmt.Sprintf("setperm %s %d", targetPubkeyHex, perms)
	resp, err := rm.SendCLI(pubkeyHex, cmd, timeout)
	if err != nil {
		return err
	}
	resp = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(resp), ">"))
	resp = strings.TrimSpace(resp)
	if !strings.HasPrefix(resp, "OK") {
		return fmt.Errorf("setperm %w: %s", ErrRejected, resp)
	}
	return nil
}

// SendSeriesReq bounds are seconds before the sensor's now, start being the older edge; needs read-only or above.
func (rm *Client) SendSeriesReq(pubkeyHex string, startSecsAgo, endSecsAgo uint32, timeout time.Duration) (*telemetry.Series, error) {
	// payload: type(1) start(4) end(4) reserved(2)
	body := make([]byte, 11)
	body[0] = meshcore.ReqTypeGetAvgMinMax
	binary.LittleEndian.PutUint32(body[1:5], startSecsAgo)
	binary.LittleEndian.PutUint32(body[5:9], endSecsAgo)
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "series")
	if err != nil {
		return nil, err
	}
	return badReply(telemetry.ParseSeries(data))
}

// badReply marks a reply that arrived but would not parse, so it reads as a bad answer (502) rather than a fault here.
func badReply[T any](v T, err error) (T, error) {
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%w: %w", ErrBadReply, err)
	}
	return v, nil
}

// telemetryReqBody: type(1) mask(1) reserved(3) random(4); mask 0x00 asks for all and the firmware filters by ACL.
func telemetryReqBody() ([]byte, error) {
	body := make([]byte, 9)
	body[0] = meshcore.ReqTypeGetTelemetryData
	if _, err := rand.Read(body[5:9]); err != nil {
		return nil, fmt.Errorf("generating random: %w", err)
	}
	return body, nil
}

func (rm *Client) SendTelemetryReq(pubkeyHex string, timeout time.Duration) (*telemetry.Telemetry, error) {
	body, err := telemetryReqBody()
	if err != nil {
		return nil, err
	}
	data, err := rm.sendBinaryRequest(pubkeyHex, body, timeout, "telemetry")
	if err != nil {
		return nil, err
	}
	return badReply(telemetry.Parse(data))
}

// SendContactTelemetryReq needs no login: it encrypts with the ECDH secret shared with the contact.
func (rm *Client) SendContactTelemetryReq(pubkeyHex string, timeout time.Duration) (*telemetry.Telemetry, error) {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("%w: hex: %w", ErrBadPubkey, err)
	}
	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadPubkey, err)
	}
	peer := rm.node.Peers().Lookup(peerIdentity.PublicKey())
	if peer == nil {
		return nil, ErrUnknownPeer
	}

	self := rm.node.Identity()
	sharedSecret, err := rm.node.SharedSecret(peerIdentity)
	if err != nil {
		return nil, fmt.Errorf("deriving shared secret: %w", err)
	}

	body, err := telemetryReqBody()
	if err != nil {
		return nil, err
	}
	data, err := rm.roundtripRequest(peerIdentity.PublicKey(), peer, sharedSecret, self.PublicKey()[0], body, timeout, "contact-telemetry", true)
	if err != nil {
		return nil, err
	}
	return badReply(telemetry.Parse(data))
}

// ErrRegionLoad: the firmware reads load-mode lines before stripping our "XX|" tag, so none parse and it swallows every later command until rebooted.
var ErrRegionLoad = errors.New("region load can't run over the mesh: the repeater would take every later command as a region line until it restarts; use region put or region def")

func (rm *Client) SendCLI(pubkeyHex, command string, timeout time.Duration) (string, error) {
	if f := strings.Fields(command); len(f) >= 2 && f[0] == "region" && f[1] == "load" {
		return "", ErrRegionLoad
	}
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return "", fmt.Errorf("%w: hex: %w", ErrBadPubkey, err)
	}

	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrBadPubkey, err)
	}

	peer := rm.node.Peers().Lookup(peerIdentity.PublicKey())
	if peer == nil {
		return "", ErrUnknownPeer
	}

	rm.mu.Lock()
	sess := rm.sessions[pubkeyHex]
	rm.mu.Unlock()
	if sess == nil || sess.sharedSecret == nil {
		return "", fmt.Errorf("%w to this repeater", ErrNotLoggedIn)
	}
	if !sess.IsAdmin {
		return "", ErrNotAdmin
	}

	resultCh := make(chan string, 1)
	var prefix string
	rm.cliMu.Lock()
	// The prefix is one hex byte, so a full map would spin the random search below forever holding cliMu.
	if len(rm.cliPending) >= 256 {
		rm.cliMu.Unlock()
		return "", ErrBusy
	}
	for {
		var b [1]byte
		rand.Read(b[:])
		prefix = fmt.Sprintf("%02X", b[0])
		if _, taken := rm.cliPending[prefix]; !taken {
			break
		}
	}
	rm.cliPending[prefix] = resultCh
	rm.cliMu.Unlock()
	framedCommand := prefix + "|" + command

	defer func() {
		rm.cliMu.Lock()
		delete(rm.cliPending, prefix)
		rm.cliMu.Unlock()
	}()

	plaintext := meshcore.BuildTextPlaintext(time.Unix(int64(rm.UniqueTimestamp()), 0), meshcore.TxtTypeCLIData<<2, []byte(framedCommand))

	encrypted, err := meshcore.EncryptThenMAC(sess.sharedSecret, plaintext)
	if err != nil {
		return "", fmt.Errorf("encrypting CLI: %w", err)
	}

	var mac [2]byte
	copy(mac[:], encrypted[:2])

	txtMsg := &meshcore.TextMessage{
		Destination:      peerIdentity.PublicKey()[0],
		Source:           sess.localPubKey[0],
		MAC:              mac,
		EncryptedPayload: encrypted[2:],
	}

	msgBytes, err := txtMsg.ToBytes()
	if err != nil {
		return "", fmt.Errorf("encoding text message: %w", err)
	}

	pkt, outPath, hashSize := rm.routedPacket(peer, meshcore.PayloadTypeTxtMsg, msgBytes)

	pub := peer.Identity.PublicKey()
	if err := meshpath.Send(rm.node, pkt, rm.scopeFor(pub[:]), 0); err != nil {
		return "", fmt.Errorf("sending CLI: %w", err)
	}

	wait := rm.replyTimeout(len(msgBytes), outPath, hashSize, timeout)
	rm.log.Debug("CLI sent", "peer", pubkeyHex[:12], "prefix", prefix, "command", command, "wait", wait)

	select {
	case response := <-resultCh:
		return response, nil
	case <-time.After(wait):
		return "", fmt.Errorf("CLI command timed out after %s: %w", wait, ErrNoReply)
	}
}
