package repeater

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"

	"github.com/OwlShack/OwlShack/internal/meshpath"
)

type LoginResult struct {
	Success     bool   `json:"success"`
	IsAdmin     bool   `json:"isAdmin"`
	Permissions int    `json:"permissions"`
	Role        string `json:"role,omitempty"`
}

func (rm *Client) SendLogin(pubkeyHex, password string, timeout time.Duration) (*LoginResult, error) {
	return rm.sendLogin(pubkeyHex, password, nil, timeout)
}

// SendRoomLogin is SendLogin plus a sync_since cursor; the server pushes posts newer than it.
func (rm *Client) SendRoomLogin(pubkeyHex, password string, syncSince uint32, timeout time.Duration) (*LoginResult, error) {
	return rm.sendLogin(pubkeyHex, password, &syncSince, timeout)
}

func (rm *Client) sendLogin(pubkeyHex, password string, roomSyncSince *uint32, timeout time.Duration) (*LoginResult, error) {
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

	// The static identity (not an ephemeral key) puts us in the repeater's ACL, so its getClient() lookup accepts blank-password reauth.
	selfIdentity := rm.node.Identity()
	sharedSecret, err := rm.node.SharedSecret(peerIdentity)
	if err != nil {
		return nil, fmt.Errorf("deriving shared secret: %w", err)
	}

	var plaintext []byte
	if roomSyncSince != nil {
		// Room login: [timestamp:4][sync_since:4][password:N]
		plaintext = make([]byte, 8+len(password))
		binary.LittleEndian.PutUint32(plaintext[:4], rm.UniqueTimestamp())
		binary.LittleEndian.PutUint32(plaintext[4:8], *roomSyncSince)
		copy(plaintext[8:], password)
	} else {
		// Repeater login: [timestamp:4][password:N]
		plaintext = make([]byte, 4+len(password))
		binary.LittleEndian.PutUint32(plaintext[:4], rm.UniqueTimestamp())
		copy(plaintext[4:], password)
	}

	encrypted, err := meshcore.EncryptThenMAC(sharedSecret, plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypting login: %w", err)
	}

	var mac [2]byte
	copy(mac[:], encrypted[:2])

	anonReq := &meshcore.AnonReq{
		Destination:      peerIdentity.PublicKey()[0],
		EphemeralPubKey:  selfIdentity.PublicKey(),
		MAC:              mac,
		EncryptedPayload: encrypted[2:],
	}

	payload, err := anonReq.ToBytes()
	if err != nil {
		return nil, fmt.Errorf("encoding anon req: %w", err)
	}

	resultCh := make(chan []byte, 1)
	pl := &pendingLogin{
		ch:             resultCh,
		created:        time.Now(),
		sharedSecret:   sharedSecret,
		peerPubKeyByte: peerIdentity.PublicKey()[0],
		peerPubKey:     peerIdentity.PublicKey(),
	}
	rm.loginMu.Lock()
	rm.pendingLogins = append(rm.pendingLogins, pl)
	rm.loginMu.Unlock()

	defer func() {
		rm.loginMu.Lock()
		for i, p := range rm.pendingLogins {
			if p == pl {
				rm.pendingLogins = append(rm.pendingLogins[:i], rm.pendingLogins[i+1:]...)
				break
			}
		}
		rm.loginMu.Unlock()
	}()

	pkt, outPath, hashSize := rm.routedPacket(peer, meshcore.PayloadTypeAnonReq, payload)

	pub := peer.Identity.PublicKey()
	if err := meshpath.Send(rm.node, pkt, rm.scopeFor(pub[:]), 0); err != nil {
		return nil, fmt.Errorf("sending login: %w", err)
	}

	wait := rm.replyTimeout(len(payload), outPath, hashSize, timeout)
	rm.log.Debug("login sent", "peer", pubkeyHex[:12], "wait", wait)

	select {
	case data := <-resultCh:
		reply, _ := meshcore.ParseLoginReply(data[4:]) // isLoginReply already parsed it
		isAdmin := reply.Admin == 1
		perms := int(reply.Permissions)
		role := ""
		if roomSyncSince != nil {
			// Room login reply admin byte: 1=admin, 2=read-only (guest), 0=read-write
			switch reply.Admin {
			case 1:
				role = "admin"
			case 2:
				role = "read-only"
			default:
				role = "read-write"
			}
		}
		rm.mu.Lock()
		rm.sessions[pubkeyHex] = &Session{
			PubKeyHex:    pubkeyHex,
			IsAdmin:      isAdmin,
			Permissions:  perms,
			Role:         role,
			IsRoom:       roomSyncSince != nil,
			LoggedInAt:   time.Now(),
			sharedSecret: sharedSecret,
			localPubKey:  selfIdentity.PublicKey(),
		}
		rm.mu.Unlock()
		return &LoginResult{Success: true, IsAdmin: isAdmin, Permissions: perms, Role: role}, nil
	case <-time.After(wait):
		// A login that times out on a route drops it so the retry floods, as the firmware's path discovery does (companion MyMesh.cpp:1613-1616); a lost mid-session reply does not.
		if outPath != nil {
			rm.log.Debug("login timed out on a learned route, clearing it so the retry floods",
				"peer", pubkeyHex[:12], "path", hex.EncodeToString(outPath))
			rm.node.Peers().ResetOutPath(peerIdentity.PublicKey())
			rm.persistOutPath(pubkeyBytes, nil, 0)
		}
		return nil, fmt.Errorf("login timed out after %s: %w", wait, ErrNoReply)
	}
}
