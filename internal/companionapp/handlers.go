package companionapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	mc "github.com/OwlShack/meshcore-go/companion"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/buildinfo"
	"github.com/OwlShack/OwlShack/internal/client/repeater"
	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/modem"
	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/store"
)

// reqTimeout is the floor on a remote request's wait; the client stretches it to the route's airtime.
const reqTimeout = 10 * time.Second

// clockSlack is how far the app's clock may be from ours and still count as the time we already have.
const clockSlack = 60 * time.Second

// Env is what the app is told about the radio and the host, refreshed on every reload.
type Env struct {
	Radio         modem.RadioInfo
	AirtimeFactor float64
	// Regions is the flood region list, which names the scope key the app sends with.
	Regions []string
	Stats   modem.StatsProvider
	// Tx is the shared radio's transmit counters, every node's sends and relays together.
	Tx      interface{ TxStats() node.TxStats }
	Started time.Time
}

// Settings saves what the app changes on its companion, through the checks and reload the UI's saves take.
type Settings interface {
	UpdateCompanion(ctx context.Context, companionID int64, change func(*store.Companion) error) error
	SaveChannels(ctx context.Context, c *companion.Companion) error
}

// session is what the firmware keeps per app: the protocol version it speaks and the scope it chose for sends.
type session struct {
	appVer byte
	// scope overrides the companion's own choice when the app set one; nil leaves it alone.
	scope *config.FloodScope
}

// handle answers one command. Only what is listed is served; everything else is refused as the firmware refuses a command it lacks.
func (c *conn) handle(d []byte) {
	s := c.srv
	comp := s.comp.Load()
	env := s.env.Load()
	ctx := context.Background()
	n := len(d)

	switch cmd := d[0]; {
	case cmd == mc.CmdDeviceQuery && n >= 2:
		c.session.appVer = d[1]
		c.send(deviceInfoFrame(deviceInfo{buildDate: buildinfo.Date, model: "OwlShack", version: buildinfo.Version, pathHashMode: comp.PathHashSize() - 1}))

	case cmd == mc.CmdAppStart && n >= 8:
		c.send(selfInfoFrame(c.selfInfo(comp, env)))

	case cmd == mc.CmdGetContacts:
		var since uint32
		if n >= 5 {
			since = binary.LittleEndian.Uint32(d[1:5])
		}
		contacts, err := s.st.Contacts.List(ctx, s.id)
		if err != nil {
			c.send(errFrame(mc.ErrCodeFileIoError))
			return
		}
		c.send(le32([]byte{mc.RespContactsStart}, uint32(len(contacts))))
		var newest uint32
		for i := range contacts {
			k := storedContact(&contacts[i])
			if k.lastmod > since {
				c.send(contactFrame(mc.RespContact, k))
				newest = max(newest, k.lastmod)
			}
		}
		c.send(le32([]byte{mc.RespEndOfContacts}, newest))

	case cmd == mc.CmdGetContactByKey && n >= 33:
		k, ok := s.contact(d[1:33])
		if !ok {
			c.send(errFrame(mc.ErrCodeNotFound))
			return
		}
		c.send(contactFrame(mc.RespContact, storedContact(k)))

	case cmd == mc.CmdAddUpdateContact && n >= 36:
		c.send(c.addUpdateContact(comp, d))

	case cmd == mc.CmdRemoveContact && n >= 33:
		if _, ok := s.contact(d[1:33]); !ok {
			c.send(errFrame(mc.ErrCodeNotFound))
			return
		}
		var err error
		s.st.WriteSync(func() { err = s.st.Contacts.Delete(ctx, s.id, d[1:33]) })
		c.send(okOr(err, mc.ErrCodeFileIoError))

	case cmd == mc.CmdResetPath && n >= 33:
		if _, ok := s.contact(d[1:33]); !ok {
			c.send(errFrame(mc.ErrCodeNotFound))
			return
		}
		c.send(okOr(comp.Repeaters().ResetPeerPath(hex.EncodeToString(d[1:33])), mc.ErrCodeNotFound))

	case cmd == mc.CmdGetAdvertPath && n >= 34:
		p, err := s.st.Peers.GetByPubKey(ctx, d[2:34])
		if err != nil || p == nil {
			c.send(errFrame(mc.ErrCodeNotFound))
			return
		}
		size := max(p.OutPathHashSize, 1)
		b := le32([]byte{mc.RespAdvertPath}, uint32(p.LastSeen.Unix()))
		c.send(append(append(b, meshcore.MakePathLen(size, uint8(len(p.OutPath)/int(size)))), p.OutPath...))

	case cmd == mc.CmdExportContact:
		if n < 33 {
			raw, err := comp.AppSelfAdvert()
			if err != nil {
				c.send(errFrame(mc.ErrCodeTableFull))
				return
			}
			c.send(append([]byte{mc.RespExportContact}, raw...))
			return
		}
		// Another node's advert as it was signed is not kept here, so it cannot be handed on.
		c.send(errFrame(mc.ErrCodeUnsupportedCmd))

	case cmd == mc.CmdShareContact:
		c.send(errFrame(mc.ErrCodeUnsupportedCmd))

	case cmd == mc.CmdImportContact && n > 2+32+64:
		if !comp.AppImportAdvert(d[1:]) {
			c.send(errFrame(mc.ErrCodeIllegalArg))
			return
		}
		c.send(okFrame())

	case cmd == mc.CmdSyncNextMessage:
		var frame []byte
		ok, err := s.st.AppQueue.Waiting(ctx, s.id)
		if ok && err == nil {
			s.st.WriteSync(func() { frame, ok, err = s.st.AppQueue.Pop(ctx, s.id) })
		}
		switch {
		case err != nil:
			c.send(errFrame(mc.ErrCodeFileIoError))
		case !ok:
			c.send(noMoreFrame())
		case c.session.appVer < 3 && (frame[0] == mc.RespContactMsgRecvV3 || frame[0] == mc.RespChannelMsgRecvV3):
			c.send(legacyMsgFrame(frame))
		default:
			c.send(frame)
		}

	case cmd == mc.CmdSendTxtMsg && n >= 14:
		c.sendText(comp, d)

	case cmd == mc.CmdSendChannelTxtMsg && n >= 7:
		if d[1] != meshcore.TxtTypePlain {
			c.send(errFrame(mc.ErrCodeUnsupportedCmd))
			return
		}
		err := comp.SendAppChannelMessage(int(d[2]), string(d[7:]), c.session.scope)
		switch {
		case errors.Is(err, companion.ErrUnknownChannel):
			c.send(errFrame(mc.ErrCodeNotFound))
		case err != nil:
			c.send(errFrame(mc.ErrCodeTableFull))
		default:
			c.send(okFrame())
		}

	case cmd == mc.CmdGetChannel && n >= 2:
		if int(d[1]) >= maxChannels {
			c.send(errFrame(mc.ErrCodeNotFound))
			return
		}
		// An empty slot reads blank, as in the firmware's fixed table; the app looks for one to add a channel in.
		if ch := comp.Node().Channel(int(d[1])); ch != nil {
			c.send(channelInfoFrame(d[1], ch.Name, ch.PSK))
		} else {
			c.send(channelInfoFrame(d[1], "", nil))
		}

	case cmd == mc.CmdSendSelfAdvert:
		c.send(okOr(comp.SendAdvert(n >= 2 && d[1] == 1), mc.ErrCodeTableFull))

	case cmd == mc.CmdGetDeviceTime:
		c.send(currTimeFrame(uint32(time.Now().Unix())))

	case cmd == mc.CmdGetBattAndStorage:
		var mv uint16
		if ds := env.Stats.CachedStats(); ds.HaveBattery {
			mv = ds.BatteryMV
		}
		c.send(le32(le32(le16([]byte{mc.RespBattAndStorage}, mv), 0), 0))

	case cmd == mc.CmdGetStats && n >= 2:
		c.send(c.stats(comp, env, d[1]))

	case cmd == mc.CmdGetTuningParams:
		c.send(le32(le32([]byte{mc.RespTuningParams}, 0), uint32(math.Round(env.AirtimeFactor*1000))))

	case cmd == mc.CmdGetCustomVars:
		c.send([]byte{mc.RespCustomVars})

	case cmd == mc.CmdGetAutoAddConfig:
		c.send([]byte{mc.RespAutoAddConfig, 0, 0})

	case cmd == mc.CmdGetAllowedRepeatFreq:
		c.send([]byte{mc.RespAllowedRepeatFreq})

	case cmd == mc.CmdGetDefaultFloodScope:
		name, key, ok := companionScope(comp)
		if !ok {
			c.send([]byte{mc.RespDefaultFloodScope})
			return
		}
		c.send(append(fixed([]byte{mc.RespDefaultFloodScope}, name, 32)[:32], key[:]...))

	case cmd == mc.CmdSetFloodScopeKey && n >= 2 && d[1] == 0:
		if n < 2+16 {
			c.session.scope = nil
			c.send(okFrame())
			return
		}
		if bytes.Equal(d[2:18], make([]byte, 16)) { // the firmware's null key: no override
			c.session.scope = nil
			c.send(okFrame())
			return
		}
		sc, ok := regionByKey(env.Regions, d[2:18])
		if !ok {
			c.send(errFrame(mc.ErrCodeIllegalArg))
			return
		}
		c.session.scope = &sc
		c.send(okFrame())

	case cmd == mc.CmdSetFloodScopeKey && n >= 2 && d[1] == 1:
		everywhere := config.ScopeEverywhere
		c.session.scope = &everywhere
		c.send(okFrame())

	case cmd == mc.CmdSendLogin && n >= 33:
		c.login(comp, d[1:33], string(d[33:]))

	case cmd == mc.CmdSendStatusReq && n >= 33:
		c.request(comp, d[1:33], []byte{meshcore.ReqTypeGetStatus, 0, 0, 0, 0}, func(_ uint32, pub [32]byte, data []byte) []byte {
			return replyPush(mc.PushStatusResponse, pub, data)
		})

	case cmd == mc.CmdSendTelemetryReq && n == 4:
		self := comp.Node().Identity().PublicKey()
		c.send(replyPush(mc.PushTelemetryResponse, self, comp.AppSelfTelemetry(mc.MaxFrameSize-8)))

	case cmd == mc.CmdSendTelemetryReq && n >= 36:
		body := make([]byte, 9)
		body[0] = meshcore.ReqTypeGetTelemetryData
		rand.Read(body[5:])
		c.request(comp, d[4:36], body, func(_ uint32, pub [32]byte, data []byte) []byte {
			return replyPush(mc.PushTelemetryResponse, pub, data)
		})

	case cmd == mc.CmdSendBinaryReq && n >= 34:
		c.request(comp, d[1:33], d[33:], func(tag uint32, _ [32]byte, data []byte) []byte { return binaryPush(tag, data) })

	case cmd == mc.CmdSendAnonReq && n > 33:
		c.anonRequest(comp, d[1:33], d[33:])

	case cmd == mc.CmdSendPathDiscoveryReq && n >= 34 && d[1] == 0:
		c.pathDiscovery(comp, d[2:34])

	case cmd == mc.CmdSendTracePath && n > 10:
		c.trace(comp, d)

	case cmd == mc.CmdHasConnection && n >= 33:
		if comp.Repeaters().Session(hex.EncodeToString(d[1:33])) == nil {
			c.send(errFrame(mc.ErrCodeNotFound))
			return
		}
		c.send(okFrame())

	case cmd == mc.CmdLogout && n >= 33:
		comp.Repeaters().Logout(hex.EncodeToString(d[1:33]))
		c.send(okFrame())

	case cmd == mc.CmdSendControlData && n >= 2 && d[1]&0x80 != 0:
		c.send(okOr(comp.AppSendControl(d[1:]), mc.ErrCodeTableFull))

	case cmd == mc.CmdSetPathHashMode && n >= 3 && d[1] == 0:
		switch mode := d[2]; {
		case mode >= 3:
			c.send(errFrame(mc.ErrCodeIllegalArg))
		default:
			size := int(mode) + 1
			c.send(c.update(func(r *store.Companion) error {
				// An inherited size that already matches stays inherited.
				if (r.PathHashSize == nil && size == int(comp.PathHashSize())) || (r.PathHashSize != nil && *r.PathHashSize == size) {
					return errUnchanged
				}
				r.PathHashSize = &size
				return nil
			}))
		}

	case cmd == mc.CmdSetAdvertName && n >= 2:
		name := firmwareName(d[1:])
		c.send(c.update(func(r *store.Companion) error {
			if r.Name == name {
				return errUnchanged
			}
			r.Name = name
			return nil
		}))

	case cmd == mc.CmdSetAdvertLatLon && n >= 9:
		lat, lon := int32(binary.LittleEndian.Uint32(d[1:5])), int32(binary.LittleEndian.Uint32(d[5:9]))
		if lat > 90e6 || lat < -90e6 || lon > 180e6 || lon < -180e6 {
			c.send(errFrame(mc.ErrCodeIllegalArg))
			return
		}
		c.send(c.update(func(r *store.Companion) error {
			if curLat, curLon := latLonE6(config.CompanionConfig{Latitude: r.Latitude, Longitude: r.Longitude}); lat == curLat && lon == curLon {
				return errUnchanged
			}
			r.Latitude, r.Longitude = nil, nil
			if lat != 0 || lon != 0 { // the firmware's 0,0 is no position
				la, lo := float64(lat)/1e6, float64(lon)/1e6
				r.Latitude, r.Longitude = &la, &lo
			}
			return nil
		}))

	case cmd == mc.CmdSetOtherParams && n >= 2:
		c.send(c.otherParams(d))

	case cmd == mc.CmdSetDefaultFloodScope:
		scope := config.ScopeEverywhere // a short frame clears the default: floods go out unscoped
		if n >= 1+31+16 {
			sc, ok := regionByKey(env.Regions, d[32:48])
			if !ok {
				c.send(errFrame(mc.ErrCodeIllegalArg))
				return
			}
			scope = sc
		}
		c.send(c.update(func(r *store.Companion) error {
			// An inherited scope that already resolves to this one stays inherited.
			if r.FloodScope == string(scope) || (r.FloodScope == string(config.ScopeInherit) && comp.AppConfig().FloodScope == scope) {
				return errUnchanged
			}
			r.FloodScope = string(scope)
			return nil
		}))

	case cmd == mc.CmdSetChannel && n >= 2+32+32:
		c.send(errFrame(mc.ErrCodeUnsupportedCmd)) // the firmware's 32-byte keys are not supported yet either

	case cmd == mc.CmdSetChannel && n >= 2+32+16:
		name, _, _ := strings.Cut(string(d[2:34]), "\x00")
		switch err := comp.AppSetChannel(int(d[1]), name, d[34:50]); {
		case errors.Is(err, companion.ErrNoSlot):
			c.send(errFrame(mc.ErrCodeNotFound))
		case err != nil:
			s.log.Info("app channel change refused", "slot", d[1], "name", name, "error", err)
			c.send(errFrame(mc.ErrCodeIllegalArg))
		default:
			c.send(okOr(s.settings.SaveChannels(ctx, comp), mc.ErrCodeFileIoError))
		}

	case cmd == mc.CmdExportPrivateKey, cmd == mc.CmdImportPrivateKey:
		c.send(disabledFrame())

	case isSetting(cmd):
		// These belong to the radio and the host: a write that changes nothing is answered OK, so the app's save goes through, and a change is refused.
		if c.unchanged(env, d) {
			c.send(okFrame())
			return
		}
		c.send(errFrame(mc.ErrCodeUnsupportedCmd))

	default:
		c.send(errFrame(mc.ErrCodeUnsupportedCmd))
	}
}

func okOr(err error, code byte) []byte {
	if err != nil {
		return errFrame(code)
	}
	return okFrame()
}

func (c *conn) selfInfo(comp *companion.Companion, env *Env) selfInfo {
	cfg := comp.AppConfig()
	si := selfInfo{
		txPower: int8(env.Radio.TxPower), maxTxPower: int8(env.Radio.TxPower),
		pubKey:         comp.Node().Identity().PublicKey(),
		telemetryModes: telemMode(cfg.TelemetryEnvironment)<<4 | telemMode(cfg.TelemetryLocation)<<2 | telemMode(cfg.TelemetryBase),
		freqKHz:        env.Radio.FreqHz / 1000, bwHz: env.Radio.BwHz, sf: env.Radio.SF, cr: env.Radio.CR,
		name: cfg.Name, shareLocation: cfg.SharesLocation(),
	}
	si.lat, si.lon = latLonE6(cfg)
	return si
}

func latLonE6(cfg config.CompanionConfig) (int32, int32) {
	if !cfg.HasLatLon() {
		return 0, 0
	}
	return int32(math.Round(*cfg.Latitude * 1e6)), int32(math.Round(*cfg.Longitude * 1e6))
}

// telemMode is the firmware's TELEM_MODE_* for a telemetry policy: deny 0, the contact's own flags 1, everyone 2.
func telemMode(m *string) byte {
	switch config.TelemetryModeOrDefault(m) {
	case config.TelemetrySelected:
		return 1
	case config.TelemetryContacts:
		return 2
	}
	return 0
}

// companionScope is the region the companion's own floods go in, as the app's default scope.
func companionScope(comp *companion.Companion) (string, meshcore.RegionKey, bool) {
	name, ok := comp.AppConfig().FloodScope.RegionName()
	if !ok {
		return "", meshcore.RegionKey{}, false
	}
	return name, meshcore.NewRegion(name).Key, true
}

// regionByKey names a scope key the app chose; a key for a region OwlShack does not know cannot be sent with.
func regionByKey(regions []string, key []byte) (config.FloodScope, bool) {
	for _, r := range regions {
		if k := meshcore.NewRegion(r).Key; bytes.Equal(k[:], key) {
			return config.FloodScope("region:" + r), true
		}
	}
	return "", false
}

func (s *Server) contact(pub []byte) (*store.Contact, bool) {
	k, err := s.st.Contacts.Get(context.Background(), s.id, pub)
	return k, err == nil && k != nil
}

// contactByPrefix is the firmware's lookupContactByPubKey on the 6-byte prefix a text send carries.
func (s *Server) contactByPrefix(prefix []byte) (*store.Contact, bool) {
	contacts, err := s.st.Contacts.List(context.Background(), s.id)
	if err != nil {
		return nil, false
	}
	for i := range contacts {
		if bytes.HasPrefix(contacts[i].PeerPubKey, prefix) {
			return &contacts[i], true
		}
	}
	return nil, false
}

func (c *conn) sendText(comp *companion.Companion, d []byte) {
	s := c.srv
	txtType, ts, text := d[1], binary.LittleEndian.Uint32(d[3:7]), string(d[13:])
	k, ok := s.contactByPrefix(d[7:13])
	if !ok {
		c.send(errFrame(mc.ErrCodeNotFound))
		return
	}
	pubHex := hex.EncodeToString(k.PeerPubKey)
	var pub [32]byte
	copy(pub[:], k.PeerPubKey)

	switch txtType {
	case meshcore.TxtTypePlain:
		// The app resends after est_timeout with the same timestamp; while ours is still trying, that is the same message.
		key := pubHex + "|" + string(d[3:7]) + "|" + text
		if sent, ok := s.inFlight(key); ok {
			c.send(sentFrame(sent.flood, sent.tag, uint32(time.Until(sent.until).Milliseconds())))
			return
		}
		tag := s.nextTag()
		start := time.Now()
		res, err := comp.SendAppDM(pubHex, text, c.session.scope, func(r node.DMSendResult) {
			s.doneFlight(key)
			if r.Confirmed {
				s.push(sendConfirmedFrame(tag, uint32(r.RoundTrip.Milliseconds())))
			}
		})
		if errors.Is(err, node.ErrTextTooLong) {
			c.send(errFrame(mc.ErrCodeIllegalArg))
			return
		}
		if err != nil {
			c.send(errFrame(mc.ErrCodeTableFull))
			return
		}
		s.startFlight(key, flight{tag: tag, flood: res.Flood, until: start.Add(res.Timeout)})
		c.send(sentFrame(res.Flood, tag, uint32(res.Timeout.Milliseconds())))

	case meshcore.TxtTypeCLIData, meshcore.TxtTypeCLICommand:
		// A CLI command is answered with a CLI reply, not an ACK, so the firmware gives the app no ACK to wait for.
		rep := comp.Repeaters()
		if rep.Session(pubHex) == nil {
			c.send(errFrame(mc.ErrCodeBadState))
			return
		}
		c.send(sentFrame(k.OutPath == nil, 0, uint32(reqTimeout.Milliseconds())))
		go func() {
			reply, err := rep.SendCLI(pubHex, text, reqTimeout)
			if err != nil {
				s.log.Info("app CLI command got no reply", "to", pubHex[:12], "error", err)
				return
			}
			s.queue(false, contactMsgFrame(0, 0xFF, pub, meshcore.TxtTypeCLIData, ts, nil, reply))
		}()

	default:
		c.send(errFrame(mc.ErrCodeUnsupportedCmd))
	}
}

func (c *conn) login(comp *companion.Companion, pubBytes []byte, password string) {
	s := c.srv
	k, ok := s.contact(pubBytes)
	if !ok {
		c.send(errFrame(mc.ErrCodeNotFound))
		return
	}
	var pub [32]byte
	copy(pub[:], pubBytes)
	pubHex := hex.EncodeToString(pubBytes)
	var since *uint32
	if k.Type == "ROOM" {
		// The firmware keeps a room's sync_since from the posts it got; ours are the posts stored.
		v := uint32(0)
		if m, err := s.st.Messages.LatestRx(context.Background(), s.id, "dm:"+pubHex); err == nil && m != nil {
			v = uint32(m.Timestamp.Unix())
		}
		since = &v
	}
	tag := binary.LittleEndian.Uint32(pubBytes[:4])
	c.async(func(sent func(uint32, time.Duration, bool)) error {
		res, err := comp.Repeaters().AppLogin(pubHex, password, since, reqTimeout, func(wait time.Duration, flood bool) { sent(tag, wait, flood) })
		if err == nil {
			s.push(loginFrame(pub, res.Reply))
		}
		return err
	})
}

// request sends a contact request for the app and pushes what answers it; nothing is pushed for a request that goes unanswered, as on the firmware.
func (c *conn) request(comp *companion.Companion, pubBytes, body []byte, push func(tag uint32, pub [32]byte, data []byte) []byte) {
	s := c.srv
	if _, ok := s.contact(pubBytes); !ok {
		c.send(errFrame(mc.ErrCodeNotFound))
		return
	}
	var pub [32]byte
	copy(pub[:], pubBytes)
	c.async(func(sent func(uint32, time.Duration, bool)) error {
		var tag uint32
		data, err := comp.Repeaters().AppRequest(hex.EncodeToString(pubBytes), body, reqTimeout, func(t uint32, w time.Duration, f bool) {
			tag = t
			sent(t, w, f)
		})
		if err == nil {
			s.push(push(tag, pub, data))
		}
		return err
	})
}

func (c *conn) anonRequest(comp *companion.Companion, pubBytes, body []byte) {
	s := c.srv
	c.async(func(sent func(uint32, time.Duration, bool)) error {
		var tag uint32
		data, err := comp.Repeaters().AppAnonRequest(hex.EncodeToString(pubBytes), body, reqTimeout, func(t uint32, w time.Duration, f bool) {
			tag = t
			sent(t, w, f)
		})
		if err == nil {
			s.push(binaryPush(tag, data))
		}
		return err
	})
}

func (c *conn) pathDiscovery(comp *companion.Companion, pubBytes []byte) {
	s := c.srv
	if _, ok := s.contact(pubBytes); !ok {
		c.send(errFrame(mc.ErrCodeNotFound))
		return
	}
	var pub [32]byte
	copy(pub[:], pubBytes)
	c.async(func(sent func(uint32, time.Duration, bool)) error {
		out, in, err := comp.Repeaters().AppPathDiscovery(hex.EncodeToString(pubBytes), reqTimeout, sent)
		if err == nil {
			s.push(pathDiscoveryPush(pub, out.Len, out.Hops, in.Len, in.Hops))
		}
		return err
	})
}

// async runs a request whose RESP_CODE_SENT comes once it is on its way and whose answer is pushed later; one that never went out is refused.
func (c *conn) async(run func(sent func(tag uint32, wait time.Duration, flood bool)) error) {
	sent := make(chan struct{})
	go func() {
		err := run(func(tag uint32, wait time.Duration, flood bool) {
			c.send(sentFrame(flood, tag, uint32(wait.Milliseconds())))
			close(sent)
		})
		select {
		case <-sent:
		default:
			c.srv.log.Info("app request not sent", "error", err)
			c.send(errFrame(sendErrCode(err)))
			close(sent)
		}
	}()
	<-sent
}

func sendErrCode(err error) byte {
	switch {
	case errors.Is(err, repeater.ErrUnknownPeer), errors.Is(err, repeater.ErrBadPubkey):
		return mc.ErrCodeNotFound
	}
	return mc.ErrCodeTableFull
}

func (c *conn) trace(comp *companion.Companion, d []byte) {
	tag, auth, flags, path := binary.LittleEndian.Uint32(d[1:5]), binary.LittleEndian.Uint32(d[5:9]), d[9], d[10:]
	sz := flags & 0x03
	if sz > 2 || len(path)%(1<<sz) != 0 || len(path)>>sz > 64 {
		c.send(errFrame(mc.ErrCodeIllegalArg))
		return
	}
	if err := comp.AppSendTrace(tag, auth, path, 1<<sz); err != nil {
		c.send(errFrame(mc.ErrCodeTableFull))
		return
	}
	env := c.srv.env.Load()
	airtime := env.Stats.EstAirtimeMs(len(path) + 11 + 2)
	wait := node.CalcDirectTimeout(airtime, uint8(len(path)>>sz))
	c.send(sentFrame(false, tag, uint32(max(wait, reqTimeout).Milliseconds())))
}

func (c *conn) stats(comp *companion.Companion, env *Env, kind byte) []byte {
	s := c.srv
	switch kind {
	case 0:
		var mv uint16
		if ds := env.Stats.CachedStats(); ds.HaveBattery {
			mv = ds.BatteryMV
		}
		b := le16([]byte{mc.RespStats, 0}, mv)
		b = le32(b, uint32(time.Since(env.Started).Seconds()))
		return append(le16(b, 0), byte(min(comp.Node().TxQueueLen(), 255)))
	case 1:
		var floor int16
		if ds := env.Stats.CachedStats(); ds.HaveNoiseFloor {
			floor = ds.NoiseFloor
		}
		b := le16([]byte{mc.RespStats, 1}, uint16(floor))
		b = append(b, byte(s.rx.lastRSSI.Load()), byte(meshcore.SNRToWire(math.Float32frombits(s.rx.lastSNR.Load()))))
		b = le32(b, uint32(env.Tx.TxStats().AirtimeMs/1000))
		return le32(b, uint32(s.rx.airMs.Load()/1000))
	case 2:
		tx := env.Tx.TxStats()
		var errs uint64
		if e := env.Stats.LinkStats().RecvErrors; e != nil {
			errs = *e
		}
		b := le32([]byte{mc.RespStats, 2}, uint32(s.rx.flood.Load()+s.rx.direct.Load()))
		b = le32(le32(le32(b, uint32(tx.Sent)), uint32(tx.SentFlood)), uint32(tx.SentDirect))
		b = le32(le32(b, uint32(s.rx.flood.Load())), uint32(s.rx.direct.Load()))
		return le32(b, uint32(errs))
	}
	return errFrame(mc.ErrCodeIllegalArg)
}

func (c *conn) addUpdateContact(comp *companion.Companion, d []byte) []byte {
	s := c.srv
	ctx := context.Background()
	pub := d[1:33]
	advType, flags, outLen := d[33], d[34], d[35]
	if n := len(d); n < 36+64+32+4 {
		return errFrame(mc.ErrCodeIllegalArg)
	}
	outPath := d[36:100]
	if size, hops := meshcore.PathLenFields(outLen); outLen != 0xFF && (size < config.MinPathHashSize || size > config.MaxPathHashSize || int(size)*int(hops) > len(outPath)) {
		return errFrame(mc.ErrCodeIllegalArg)
	}
	name := strings.TrimRight(string(d[100:132]), "\x00")
	var lat, lon *int32
	if len(d) >= 144 {
		la, lo := int32(binary.LittleEndian.Uint32(d[136:140])), int32(binary.LittleEndian.Uint32(d[140:144]))
		lat, lon = &la, &lo
	}
	self := comp.Node().Identity().PublicKey()
	if bytes.Equal(pub, self[:]) {
		return errFrame(mc.ErrCodeIllegalArg)
	}
	id, err := meshcore.NewIdentityFromBytes(pub)
	if err != nil {
		return errFrame(mc.ErrCodeIllegalArg)
	}
	typeName := advNames[advType]

	existing, isContact := s.contact(pub)
	if isContact && existing.Type != typeName {
		// A contact's type is what its adverts say it is.
		return errFrame(mc.ErrCodeUnsupportedCmd)
	}
	if name == "" {
		return errFrame(mc.ErrCodeIllegalArg)
	}
	var werr error
	s.st.WriteSync(func() {
		if !isContact {
			if p, _ := s.st.Peers.GetByPubKey(ctx, pub); p == nil {
				if werr = s.st.Peers.Upsert(ctx, &store.Peer{PubKey: pub, Name: name, Type: typeName, LastSeen: time.Now()}); werr != nil {
					return
				}
			}
			if werr = s.st.Contacts.Add(ctx, s.id, pub, name, typeName); werr != nil {
				return
			}
		}
		if isContact && existing.Name != name {
			if werr = s.st.Contacts.SetName(ctx, s.id, pub, name); werr != nil {
				return
			}
		}
		if lat != nil && (!isContact || existing.Lat != *lat || existing.Lon != *lon) {
			if werr = s.st.Contacts.SetLocation(ctx, s.id, pub, *lat, *lon); werr != nil {
				return
			}
		}
		k, err := s.st.Contacts.Get(ctx, s.id, pub)
		if err != nil {
			werr = err
			return
		}
		// Bits above the three telemetry classes, such as the firmware's remote CLI flag, are not kept.
		if perms, fav := flags>>1&0x07, flags&1 == 1; k.Metadata.TelemPerms != perms || k.Metadata.Favourite != fav {
			k.Metadata.TelemPerms, k.Metadata.Favourite = perms, fav
			werr = s.st.Contacts.UpdateMetadata(ctx, s.id, pub, k.Metadata)
		}
	})
	if werr != nil {
		return errFrame(mc.ErrCodeFileIoError)
	}
	if comp.Node().Peers().Lookup(id.PublicKey()) == nil {
		comp.Node().Peers().Insert(&node.Peer{Identity: id, Name: name, Type: typeName, LastSeen: time.Now()})
	}
	pubHex := hex.EncodeToString(pub)
	if outLen == 0xFF {
		if isContact && existing.OutPath != nil {
			comp.Repeaters().ResetPeerPath(pubHex)
		}
		return okFrame()
	}
	size, hops := meshcore.PathLenFields(outLen)
	path := outPath[:int(size)*int(hops)]
	if isContact && existing.OutPath != nil && bytes.Equal(existing.OutPath, path) && existing.OutPathHashSize == size {
		return okFrame()
	}
	return okOr(comp.Repeaters().SetPeerPath(pubHex, path, size), mc.ErrCodeIllegalArg)
}

// isSetting is a command that writes the radio's or the host's settings, which the app may not change.
func isSetting(cmd byte) bool {
	switch cmd {
	case mc.CmdSetRadioParams, mc.CmdSetRadioTxPower, mc.CmdSetTuningParams, mc.CmdSetAutoAddConfig, mc.CmdSetDeviceTime:
		return true
	}
	return false
}

// unchanged reports whether a settings write leaves everything as it is, read as the firmware reads that command.
func (c *conn) unchanged(env *Env, d []byte) bool {
	n := len(d)
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(d[i : i+4]) }
	switch d[0] {
	case mc.CmdSetRadioParams:
		return n >= 11 && u32(1) == env.Radio.FreqHz/1000 && u32(5) == env.Radio.BwHz && d[9] == env.Radio.SF && d[10] == env.Radio.CR && (n < 12 || d[11] == 0)
	case mc.CmdSetRadioTxPower:
		return n >= 2 && int8(d[1]) == int8(env.Radio.TxPower)
	case mc.CmdSetTuningParams:
		return n >= 9 && u32(1) == 0 && u32(5) == uint32(math.Round(env.AirtimeFactor*1000))
	case mc.CmdSetAutoAddConfig:
		return n >= 2 && d[1] == 0 && (n < 3 || d[2] == 0)
	case mc.CmdSetDeviceTime:
		return n >= 5 && time.Since(time.Unix(int64(u32(1)), 0)).Abs() <= clockSlack
	}
	return false
}

// errUnchanged ends a change that would write what is already saved: no write, no reload.
var errUnchanged = errors.New("unchanged")

// update saves a change to the companion's own settings, judged against what is saved rather than the running companion, which a reload may not have caught up with; one the checks refuse is the app's illegal argument.
func (c *conn) update(change func(*store.Companion) error) []byte {
	err := c.srv.settings.UpdateCompanion(context.Background(), c.srv.id, change)
	if errors.Is(err, errUnchanged) {
		return okFrame()
	}
	if err != nil {
		c.srv.log.Info("app settings change refused", "error", err)
		return errFrame(mc.ErrCodeIllegalArg)
	}
	return okFrame()
}

// firmwareName is the name as the firmware keeps it: 31 bytes at most, here never cutting a character in two.
func firmwareName(b []byte) string {
	if len(b) > 31 {
		b = b[:31]
	}
	name, _, _ := strings.Cut(strings.ToValidUTF8(string(b), ""), "\x00")
	return name
}

// telemModes are the firmware's TELEM_MODE_* by value: deny, the contact's own flags, everyone.
var telemModes = []string{config.TelemetryDeny, config.TelemetrySelected, config.TelemetryContacts}

// otherParams is SET_OTHER_PARAMS: the telemetry modes and sharing the position are the companion's; adding contacts from adverts and extra acks are not done here.
func (c *conn) otherParams(d []byte) []byte {
	n := len(d)
	if d[1] != 1 || (n >= 5 && d[4] != 0) {
		return errFrame(mc.ErrCodeUnsupportedCmd)
	}
	var modes []string
	if n >= 3 {
		for _, v := range []byte{d[2] & 3, d[2] >> 2 & 3, d[2] >> 4 & 3} {
			if int(v) >= len(telemModes) {
				return errFrame(mc.ErrCodeIllegalArg)
			}
			modes = append(modes, telemModes[v])
		}
	}
	share := n >= 4 && d[3] != 0 // the firmware shares for any policy but none
	return c.update(func(r *store.Companion) error {
		same := n < 4 || share == r.ShareLocation
		if modes != nil {
			same = same && modes[0] == r.TelemBase && modes[1] == r.TelemLoc && modes[2] == r.TelemEnv
		}
		if same {
			return errUnchanged
		}
		if modes != nil {
			r.TelemBase, r.TelemLoc, r.TelemEnv = modes[0], modes[1], modes[2]
		}
		if n >= 4 {
			r.ShareLocation = share
		}
		return nil
	})
}
