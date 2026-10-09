package companionapp

import (
	"context"
	"math"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	mc "github.com/OwlShack/meshcore-go/companion"

	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/store"
)

// The Server is the companion's AppSink: what it hears goes to the app's queue or straight to a connected app.

func (s *Server) AppMessage(m companion.AppMessage) {
	if m.Channel >= 0 {
		s.queue(true, channelMsgFrame(snrByte(m.Packet), byte(m.Channel), pathLenByte(m.Packet), m.Timestamp, m.Text))
		return
	}
	s.queue(false, contactMsgFrame(snrByte(m.Packet), pathLenByte(m.Packet), m.From, m.TxtType, m.Timestamp, m.Signer, m.Text))
}

// queue holds a message frame for the app's next sync and tickles a connected app once it is stored.
func (s *Server) queue(channel bool, frame []byte) {
	s.st.WriteAsync(func() {
		if err := s.st.AppQueue.Push(context.Background(), s.id, channel, frame); err != nil {
			s.log.Error("queueing a message for the app", "error", err)
			return
		}
		s.push(msgWaitingFrame())
	})
}

// AppAdvert tells the app a contact re-advertised, or offers it a node it does not have, as the firmware does with manual adding.
func (s *Server) AppAdvert(pub [32]byte) {
	if !s.connected() {
		return
	}
	if _, ok := s.contact(pub[:]); ok {
		s.push(pubkeyPush(mc.PushAdvert, pub))
		return
	}
	p, err := s.st.Peers.GetByPubKey(context.Background(), pub[:])
	if err != nil || p == nil {
		return
	}
	s.push(contactFrame(mc.PushNewAdvert, peerContact(p)))
}

func (s *Server) AppPathUpdated(pub [32]byte) { s.push(pubkeyPush(mc.PushPathUpdated, pub)) }

func (s *Server) AppRawRX(raw []byte, snr float32, rssi int8) {
	if len(raw) > 0 {
		// Route types 0 and 1 are the transport and plain floods, 2 and 3 direct.
		if raw[0]&0x03 <= 1 {
			s.rx.flood.Add(1)
		} else {
			s.rx.direct.Add(1)
		}
		s.rx.airMs.Add(uint64(s.env.Load().Stats.EstAirtimeMs(len(raw))))
		s.rx.lastSNR.Store(math.Float32bits(snr))
		s.rx.lastRSSI.Store(int32(rssi))
	}
	if len(raw)+3 <= mc.MaxFrameSize {
		s.push(logRxFrame(raw, snr, rssi))
	}
}

func (s *Server) AppTrace(pkt *meshcore.Packet, tr *meshcore.Trace) { s.push(traceFrame(pkt, tr)) }

func (s *Server) AppControl(pkt *meshcore.Packet) {
	if len(pkt.Payload)+4 <= mc.MaxFrameSize {
		s.push(controlFrame(pkt))
	}
}

// advTypes is the firmware's ADV_TYPE_* by the names peers are stored under.
var advTypes = map[string]byte{"CHAT": meshcore.AdvertTypeChat, "REPEATER": meshcore.AdvertTypeRepeater, "ROOM": meshcore.AdvertTypeRoom, "SENSOR": meshcore.AdvertTypeSensor}

var advNames = map[byte]string{meshcore.AdvertTypeChat: "CHAT", meshcore.AdvertTypeRepeater: "REPEATER", meshcore.AdvertTypeRoom: "ROOM", meshcore.AdvertTypeSensor: "SENSOR"}

// peerContact is a heard node as the firmware offers it before it is a contact: no route yet.
func peerContact(p *store.Peer) contactInfo {
	c := contactInfo{advType: advTypes[p.Type], outPathLen: 0xFF, name: p.Name, lastAdvert: p.LastAdvertTS, lastmod: uint32(time.Now().Unix())}
	copy(c.pubKey[:], p.PubKey)
	c.lat, c.lon = p.Lat, p.Lon
	return c
}

// storedContact is a contact as RESP_CODE_CONTACT carries it; lastmod is when it was added or last heard.
func storedContact(k *store.Contact) contactInfo {
	flags := k.Metadata.TelemPerms << 1
	if k.Metadata.Favourite {
		flags |= 1
	}
	c := contactInfo{advType: advTypes[k.Type], flags: flags, outPathLen: 0xFF, outPath: k.OutPath,
		name: k.Name, lastAdvert: k.LastAdvertTS, lat: k.Lat, lon: k.Lon, lastmod: contactLastmod(k)}
	copy(c.pubKey[:], k.PeerPubKey)
	if k.OutPath != nil {
		size := max(k.OutPathHashSize, 1)
		c.outPathLen = meshcore.MakePathLen(size, uint8(len(k.OutPath)/int(size)))
	}
	return c
}

func contactLastmod(k *store.Contact) uint32 {
	t := k.AddedAt
	if k.LastSeen.After(t) {
		t = k.LastSeen
	}
	return uint32(t.Unix())
}
