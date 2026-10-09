package companion

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/meshpath"
	"github.com/OwlShack/OwlShack/internal/store"
)

// MaxDMTextBytes is the longest DM that every retry still fits, so a message that sends can also be retried.
const MaxDMTextBytes = meshcore.MaxRetryTextLen

// ErrUnknownChannel is a send to a channel this companion doesn't have.
var ErrUnknownChannel = errors.New("channel not found")

// uniqueTimestamp mirrors the firmware's getCurrentTimeUnique(): a remote node drops a second post sharing a timestamp as a retry.
func (c *Companion) uniqueTimestamp() uint32 { return c.repeaters.UniqueTimestamp() }

func (c *Companion) SendChannelMessage(channelName, text string) error {
	ch := c.findChannel(channelName)
	if ch == nil {
		return fmt.Errorf("%w: %q", ErrUnknownChannel, channelName)
	}
	return c.sendGroupReply(ch, text, c.pathHashSize(), 5*time.Second, 3, c.channelScope(ch.Name))
}

// sendGroupReply is the shared send path for the chat API and trigger replies, so bot replies are persisted, broadcast and echo-tracked like manual sends.
func (c *Companion) sendGroupReply(ch *meshcore.ChannelEntry, text string, hashSize uint8, retryTimeout time.Duration, maxRetries int, scope config.FloodScope) error {
	name := c.conf().Name
	payload := &meshcore.GroupTextPayload{
		Timestamp: c.uniqueTimestamp(),
		Sender:    name,
		Text:      text,
	}

	now := time.Now()
	msg := &store.Message{
		CompanionID: c.conf().ID,
		Channel:     ch.Name,
		ChannelHash: ch.Hash,
		Sender:      name,
		Text:        text,
		Direction:   "tx",
		Timestamp:   now,
		ReceivedAt:  now,
		FloodScope:  string(scope),
	}

	c.store.WriteSync(func() {
		if insertErr := c.store.Messages.Insert(context.Background(), msg); insertErr != nil {
			c.log.Error("failed to persist outgoing message", "error", insertErr)
		}
	})

	msgID := msg.ID

	if c.hub != nil {
		c.hub.Broadcast("messages", map[string]any{
			"companion":   name,
			"companionId": c.conf().ID,
			"channel":     ch.Name,
			"sender":      name,
			"text":        text,
			"direction":   "tx",
			"timestamp":   msg.Timestamp.UTC().Format(time.RFC3339),
			"receivedAt":  msg.ReceivedAt.UTC().Format(time.RFC3339),
			"id":          msgID,
			"floodScope":  msg.FloodScope,
		})
	}

	c.pendingOutbound.Lock()
	c.pendingOutbound.msgID = msgID
	c.pendingOutbound.channel = ch.Name
	c.pendingOutbound.Unlock()

	err := c.node.SendGroupTextScoped(
		ch, scope.MeshRegion(), payload, hashSize, retryTimeout, maxRetries,
		func(gsr node.GroupSendResult) {
			c.log.Debug("group reply result", "channel", ch.Name, "confirmed", gsr.Confirmed)
		},
	)
	if err != nil { // never sent, so the row already shown must not read as sent, nor take an earlier post's echoes
		c.pendingOutbound.Lock()
		if c.pendingOutbound.msgID == msgID {
			c.pendingOutbound.msgID, c.pendingOutbound.channel = 0, ""
		}
		c.pendingOutbound.Unlock()
		c.setMessageStatus(msgID, ch.Name, "failed")
	}
	return err
}

// setMessageStatus records an outgoing message's status and tells the open chats.
func (c *Companion) setMessageStatus(msgID int64, channel, status string) {
	c.store.WriteAsync(func() {
		if err := c.store.Messages.UpdateStatus(context.Background(), msgID, status); err != nil {
			c.log.Error("failed to update message status", "id", msgID, "error", err)
		}
	})
	if c.hub != nil {
		c.hub.Broadcast("messages", map[string]any{
			"action":      "status",
			"companion":   c.conf().Name,
			"companionId": c.conf().ID,
			"channel":     channel,
			"id":          msgID,
			"status":      status,
		})
	}
}

// SendContactMessage is the chat API's DM send: a flood or 0-hop DM goes out at the contact's bytes per hop.
func (c *Companion) SendContactMessage(pubkeyHex, text string) error {
	_, err := c.sendDM(pubkeyHex, text, c.bytesPerHopHex(pubkeyHex), 5*time.Second, c.contactScopeHex(pubkeyHex), nil)
	return err
}

// sendDMReply is a DM trigger's answer: the trigger's pathHashSize frames it only when no route is stored, since a stored path already fixes its own hash width.
func (c *Companion) sendDMReply(pubkeyHex, text string, hashSize uint8, ackTimeout time.Duration, scope config.FloodScope) error {
	_, err := c.sendDM(pubkeyHex, text, hashSize, ackTimeout, scope, nil)
	return err
}

// dmAckTimeout mirrors the firmware's calcFloodTimeoutMillisFor / calcDirectTimeoutMillisFor
// (MyMesh.cpp:851-858): the wait has to scale with airtime and hop count or a slow preset gives up
// before the ack can physically arrive. floor keeps the caller's configured value as a minimum,
// and is used outright when the radio params are unknown and airtime reads 0.
// Library limitation: SendTextMessage applies one timeout to every attempt, while the firmware
// recomputes per attempt, so a 0-hop neighbour that goes out of range mid-conversation runs its
// flood fallback attempts on the shorter direct timeout.
func (c *Companion) dmAckTimeout(textLen int, outPath []byte, hashSize uint8, floor time.Duration) time.Duration {
	if c.stats == nil {
		return floor
	}
	// [4 ts][1 flags][text] padded to an AES block, plus dest+src+MAC, header and path-length byte.
	cipherLen := 5 + textLen
	if rem := cipherLen % 16; rem != 0 {
		cipherLen += 16 - rem
	}
	airtime := c.stats.EstAirtimeMs(2 + len(outPath) + 4 + cipherLen)
	if airtime == 0 {
		return floor
	}

	timeout := node.CalcFloodTimeout(airtime)
	if outPath != nil { // non-nil, even empty, routes direct
		hops := 0
		if hashSize > 0 {
			hops = len(outPath) / int(hashSize)
		}
		timeout = node.CalcDirectTimeout(airtime, uint8(min(hops, 255)))
	}
	return max(timeout, floor)
}

// sendDM stores, shows and sends a DM; done, when set, also hears the outcome.
func (c *Companion) sendDM(pubkeyHex, text string, fallbackHashSize uint8, ackTimeout time.Duration, scope config.FloodScope, done func(node.DMSendResult)) (AppDMSend, error) {
	pubkeyBytes, err := hex.DecodeString(pubkeyHex)
	if err != nil {
		return AppDMSend{}, fmt.Errorf("invalid pubkey hex: %w", err)
	}

	peerIdentity, err := meshcore.NewIdentityFromBytes(pubkeyBytes)
	if err != nil {
		return AppDMSend{}, fmt.Errorf("invalid pubkey: %w", err)
	}

	// The UI counts characters; the wire counts bytes, and a retry past attempt 3 appends 2 more.
	if len(text) > MaxDMTextBytes {
		return AppDMSend{}, fmt.Errorf("%w: message is %d bytes, over the %d-byte limit (multibyte characters cost more than one)", node.ErrTextTooLong, len(text), MaxDMTextBytes)
	}

	// SendTextMessage treats a nil path as a flood, so an unrouted contact still sends.
	outPath, hashSize, haveRoute := c.learnedRoute(pubkeyBytes)
	if !haveRoute {
		outPath = nil // flood
	}
	// Nothing to frame on a flood or a 0-hop neighbour, so the caller's width decides the header.
	if len(outPath) == 0 {
		hashSize = fallbackHashSize
	}

	ackTimeout = c.dmAckTimeout(len(text), outPath, hashSize, ackTimeout)

	channelKey := "dm:" + pubkeyHex
	statusSending := "sending"

	now := time.Now()
	msg := &store.Message{
		CompanionID: c.conf().ID,
		Channel:     channelKey,
		ChannelHash: 0,
		Sender:      c.conf().Name,
		Text:        text,
		Direction:   "tx",
		Timestamp:   now,
		ReceivedAt:  now,
		Status:      &statusSending,
		FloodScope:  string(scope),
	}

	c.store.WriteSync(func() {
		if insertErr := c.store.Messages.Insert(context.Background(), msg); insertErr != nil {
			c.log.Error("failed to persist outgoing DM", "error", insertErr)
		}
	})

	if c.hub != nil {
		c.hub.Broadcast("messages", map[string]any{
			"companion":   c.conf().Name,
			"companionId": c.conf().ID,
			"channel":     channelKey,
			"sender":      c.conf().Name,
			"text":        text,
			"direction":   "tx",
			"timestamp":   msg.Timestamp.UTC().Format(time.RFC3339),
			"receivedAt":  msg.ReceivedAt.UTC().Format(time.RFC3339),
			"id":          msg.ID,
			"status":      "sending",
			"floodScope":  msg.FloodScope,
		})
	}

	msgID := msg.ID
	err = c.node.SendTextMessageScoped(
		peerIdentity,
		scope.MeshRegion(),
		[]byte(text),
		0,
		time.Unix(int64(c.uniqueTimestamp()), 0),
		outPath,
		hashSize,
		ackTimeout,
		func(result node.DMSendResult) {
			var status string
			if result.Confirmed {
				status = "delivered"
				c.log.Debug("DM delivered", "peer", pubkeyHex[:12], "roundTrip", result.RoundTrip)
			} else {
				status = "failed"
				c.log.Warn("DM delivery failed", "peer", pubkeyHex[:12])
				// Every attempt failed on a route we believed in, so stop believing in it: the
				// official app issues CMD_RESET_PATH here. The next path return re-learns it.
				if haveRoute {
					c.node.Peers().ResetOutPath(peerIdentity.PublicKey())
					c.store.WriteAsync(func() {
						if err := c.store.Contacts.UpdateOutPath(context.Background(), c.conf().ID, pubkeyBytes, nil, 0); err != nil {
							c.log.Error("failed to clear stale route", "peer", pubkeyHex[:12], "error", err)
						}
					})
					c.log.Info("cleared stale route after failed delivery", "peer", pubkeyHex[:12])
				}
			}

			c.setMessageStatus(msgID, channelKey, status)
			if done != nil {
				done(result)
			}
		},
	)
	if err != nil { // the library reports no result for a send it refused outright
		c.setMessageStatus(msgID, channelKey, "failed")
		return AppDMSend{}, err
	}
	// The library makes 4 flood attempts, or 6 starting direct, each waiting ackTimeout.
	attempts := 4
	if outPath != nil {
		attempts = 6
	}
	return AppDMSend{Flood: outPath == nil, Timeout: ackTimeout * time.Duration(attempts)}, nil
}

func (c *Companion) SendTrace(path []byte, pathHashSize uint8) (uint32, error) {
	if len(path) == 0 {
		return 0, fmt.Errorf("path is required")
	}
	if pathHashSize != 1 && pathHashSize != 2 && pathHashSize != 4 {
		return 0, fmt.Errorf("pathHashSize must be 1, 2, or 4")
	}
	if len(path)%int(pathHashSize) != 0 {
		return 0, fmt.Errorf("path length %d is not divisible by pathHashSize %d", len(path), pathHashSize)
	}

	var tagBytes [4]byte
	if _, err := rand.Read(tagBytes[:]); err != nil {
		return 0, fmt.Errorf("generating trace tag: %w", err)
	}
	tag := binary.LittleEndian.Uint32(tagBytes[:])

	var authBytes [4]byte
	if _, err := rand.Read(authBytes[:]); err != nil {
		return 0, fmt.Errorf("generating trace auth: %w", err)
	}
	auth := binary.LittleEndian.Uint32(authBytes[:])

	if err := c.sendTracePacket(tag, auth, path, pathHashSize); err != nil {
		return 0, err
	}
	return tag, nil
}

// sendTracePacket is split out of SendTrace so RunTrace can register its waiter under the tag before the packet hits the radio.
func (c *Companion) sendTracePacket(tag, auth uint32, path []byte, pathHashSize uint8) error {
	var flags byte
	switch pathHashSize {
	case 1:
		flags = 0
	case 2:
		flags = 1
	case 4:
		flags = 2
	}

	trace := &meshcore.Trace{
		Tag:        tag,
		AuthCode:   auth,
		Flags:      flags,
		PathHashes: path,
	}

	payload, err := trace.ToBytes()
	if err != nil {
		return fmt.Errorf("encoding trace: %w", err)
	}

	pkt := meshcore.Packet{
		Header:     meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeTrace, 0),
		PathLength: 0,
		Payload:    payload,
	}

	if err := meshpath.Send(c.node, &pkt, nil, 0); err != nil {
		return fmt.Errorf("sending trace: %w", err)
	}

	c.log.Debug("trace sent", "tag", fmt.Sprintf("%08x", tag), "hops", len(path)/int(pathHashSize), "pathHashSize", pathHashSize)
	return nil
}

func (c *Companion) findChannel(name string) *meshcore.ChannelEntry {
	for _, ch := range c.node.Channels() {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}
