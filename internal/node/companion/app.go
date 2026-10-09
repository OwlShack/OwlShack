package companion

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OwlShack/OwlShack/internal/node/advert"
	"github.com/OwlShack/OwlShack/internal/sensor"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/config"
)

// AppSink hears what the firmware would tell the MeshCore app, whether or not an app is connected.
type AppSink interface {
	AppMessage(AppMessage)
	// AppAdvert is a verified advert, stored by the time it is called.
	AppAdvert(pubkey [32]byte)
	AppPathUpdated(pubkey [32]byte)
	AppRawRX(raw []byte, snr float32, rssi int8)
	AppTrace(pkt *meshcore.Packet, tr *meshcore.Trace)
	AppControl(pkt *meshcore.Packet)
}

// AppMessage is a received message as the firmware queues it for the app.
type AppMessage struct {
	// Channel is the channel slot, or -1 for a direct message from From.
	Channel   int
	From      [32]byte
	TxtType   byte
	Timestamp uint32
	// Signer is a room post's 4-byte author prefix.
	Signer []byte
	Text   string
	Packet *meshcore.Packet
}

type appSinkBox struct{ AppSink }

// SetAppSink routes what the app needs to s; nil stops it.
func (c *Companion) SetAppSink(s AppSink) {
	if s == nil {
		c.appSink.Store(nil)
		return
	}
	c.appSink.Store(&appSinkBox{s})
}

func (c *Companion) app() AppSink {
	if b := c.appSink.Load(); b != nil {
		return b.AppSink
	}
	return nil
}

// channelSlot is the node's slot for a channel, or -1.
func (c *Companion) channelSlot(name string) int {
	for i := range node.DefaultMaxChannels {
		if ch := c.node.Channel(i); ch != nil && ch.Name == name {
			return i
		}
	}
	return -1
}

// channelText is the "name: text" plaintext the firmware hands the app.
func channelText(sender, text string) string {
	if sender == "" {
		return text
	}
	return sender + ": " + text
}

// AppDMSend is what the app is told about a DM it asked to send.
type AppDMSend struct {
	Flood   bool
	Timeout time.Duration
}

// SendAppDM sends the app's DM through the companion's own send path, so it is stored and shown like any other; done hears the outcome.
func (c *Companion) SendAppDM(pubkeyHex, text string, scope *config.FloodScope, done func(node.DMSendResult)) (AppDMSend, error) {
	s := c.contactScopeHex(pubkeyHex)
	if scope != nil {
		s = *scope
	}
	return c.sendDM(pubkeyHex, text, c.bytesPerHopHex(pubkeyHex), 5*time.Second, s, done)
}

// SendAppChannelMessage sends the app's channel post by slot, as the firmware addresses channels.
func (c *Companion) SendAppChannelMessage(slot int, text string, scope *config.FloodScope) error {
	ch := c.node.Channel(slot)
	if ch == nil {
		return ErrUnknownChannel
	}
	s := c.channelScope(ch.Name)
	if scope != nil {
		s = *scope
	}
	return c.sendGroupReply(ch, text, c.pathHashSize(), 5*time.Second, 3, s)
}

// AppSelfTelemetry is the companion's own readings and channel map with every class allowed, as the firmware answers its own app.
func (c *Companion) AppSelfTelemetry(maxBody int) []byte {
	var entries []sensor.ChannelEntry
	var statuses []sensor.Status
	if hook := c.telemetryHook(); hook != nil {
		entries, statuses = hook()
	}
	body, _ := sensor.BuildReply(sensor.PermBase|sensor.PermLocation|sensor.PermEnvironment, c.selfReadings(), entries, statuses, maxBody)
	return body
}

// AppConfig is the companion's own settings, which the app reads back and may not change.
func (c *Companion) AppConfig() config.CompanionConfig { return *c.conf() }

// PathHashSize is the bytes per hop this companion sends at.
func (c *Companion) PathHashSize() uint8 { return c.pathHashSize() }

// ChannelScope is the region a post to the named channel goes in.
func (c *Companion) ChannelScope(name string) config.FloodScope { return c.channelScope(name) }

// AppSelfAdvert is the companion's own signed advert as a flood packet, which the app exports to share.
func (c *Companion) AppSelfAdvert() ([]byte, error) {
	cfg := c.conf()
	lat, lon := advertLatLon(cfg)
	pkt, err := advert.BuildSelf(c.node, "CHAT", cfg.Name, lat, lon, true, int(hashSizeOf(cfg)))
	if err != nil {
		return nil, err
	}
	return pkt.ToBytes()
}

// AppImportAdvert takes a shared advert as if heard, as the firmware's importContact loops it back.
func (c *Companion) AppImportAdvert(raw []byte) bool {
	pkt, err := meshcore.PacketFromBytes(raw)
	if err != nil || pkt.PayloadType() != meshcore.PayloadTypeAdvert {
		return false
	}
	c.handleAdvert(pkt)
	return true
}

// AppSendTrace sends the app's trace with its own tag and auth, which the app matches the reply by.
func (c *Companion) AppSendTrace(tag, auth uint32, path []byte, hashSize uint8) error {
	return c.sendTracePacket(tag, auth, path, hashSize)
}

// AppSendControl sends the app's control data zero-hop, as the firmware does.
func (c *Companion) AppSendControl(payload []byte) error {
	pkt := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeDirect, meshcore.PayloadTypeControl, 0), Payload: payload}
	return c.node.SendZeroHop(pkt, nil, 0)
}

// ErrNoSlot is a channel slot past the node's last.
var ErrNoSlot = errors.New("no such channel slot")

// AppSetChannel puts a channel in a slot as the firmware's setChannel does, an empty name clearing it, under the same rules as the Channels page.
func (c *Companion) AppSetChannel(slot int, name string, secret []byte) error {
	if slot < 0 || slot >= node.DefaultMaxChannels {
		return ErrNoSlot
	}
	c.chanEditMu.Lock()
	defer c.chanEditMu.Unlock()
	cur := c.node.Channel(slot)
	if name == "" {
		if cur == nil {
			return nil
		}
		return c.removeChannel(cur.Name)
	}
	if cur != nil && cur.Name == name && bytes.Equal(cur.PSK, secret) {
		return nil
	}
	if cur != nil && bytes.Equal(cur.PSK, secret) {
		return c.renameChannel(cur.Name, name)
	}
	ref := channelRefFor(name, secret)
	if err := config.CheckChannelName(ref.Name); err != nil {
		return api.Invalid(err)
	}
	if err := ref.Validate(); err != nil {
		return api.Invalid(err)
	}
	if _, err := channelFromRef(ref); err != nil {
		return api.Invalid(err)
	}
	for i := range node.DefaultMaxChannels {
		if ch := c.node.Channel(i); i != slot && ch != nil && ch.Name == ref.Name {
			return api.Invalid(fmt.Errorf("channel %q already exists", ref.Name))
		}
	}
	if cur == nil {
		return c.addChannel(ref, slot)
	}
	old := channelRefFor(cur.Name, cur.PSK)
	old.FloodScope = c.ChannelScopeOwn(cur.Name)
	if err := c.removeChannel(cur.Name); err != nil {
		return err
	}
	if err := c.addChannel(ref, slot); err != nil {
		// Put the channel it replaced back rather than leave the slot empty.
		if rerr := c.addChannel(old, slot); rerr != nil {
			c.log.Error("channel lost while replacing it", "channel", old.Name, "slot", slot, "error", rerr)
		}
		return err
	}
	return nil
}

// ChannelScopeOwn is the channel's own region choice, which may be inherit.
func (c *Companion) ChannelScopeOwn(name string) config.FloodScope {
	c.chanScopesMu.Lock()
	defer c.chanScopesMu.Unlock()
	return c.chanScopes[name]
}

// channelRefFor is how the channel is stored here: Public and a hashtag channel by name alone, as the Channels page adds them, anything else by its key.
func channelRefFor(name string, secret []byte) config.ChannelRef {
	ref := config.ChannelRef{Name: name, FloodScope: config.ScopeInherit}
	switch {
	case strings.EqualFold(name, "Public") && bytes.Equal(secret, meshcore.PublicChannel().PSK):
		ref.Name = "Public"
	case strings.HasPrefix(name, "#") && bytes.Equal(secret, meshcore.NewChannelFromHashtag(meshcore.NormalizeHashtag(name)).PSK):
	default:
		ref.PrivateKey = hex.EncodeToString(secret)
	}
	return ref
}

// ChannelSlots is each slot's channel name, "" where empty, so a rebuild that moved one can be told apart.
func (c *Companion) ChannelSlots() []string {
	out := make([]string, node.DefaultMaxChannels)
	for i := range out {
		if ch := c.node.Channel(i); ch != nil {
			out[i] = ch.Name
		}
	}
	return out
}
