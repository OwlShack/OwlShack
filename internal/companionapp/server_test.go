package companionapp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	mc "github.com/OwlShack/meshcore-go/companion"
	"github.com/OwlShack/meshcore-go/companion/client"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/modem"
	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/store"
)

type silentModem struct{}

func (silentModem) SendData([]byte) error                            { return nil }
func (silentModem) SetDataHandler(func([]byte, float32, int8, bool)) {}
func (silentModem) AddOutboundHandler(func([]byte))                  {}

type fakeStats struct{}

func (fakeStats) Transport() string { return "kiss" }
func (fakeStats) RadioConfig() modem.RadioInfo {
	return modem.RadioInfo{FreqHz: 869618000, BwHz: 62500, SF: 8, CR: 8, TxPower: 22}
}
func (fakeStats) Stats(context.Context) modem.DeviceStats { return fakeStats{}.CachedStats() }
func (fakeStats) CachedStats() modem.DeviceStats {
	return modem.DeviceStats{BatteryMV: 4100, HaveBattery: true, NoiseFloor: -98, HaveNoiseFloor: true}
}
func (fakeStats) LinkStats() modem.LinkStats       { return modem.LinkStats{} }
func (fakeStats) EstAirtimeMs(int) uint32          { return 1500 }
func (fakeStats) PacketScore(float64, int) float64 { return 0 }

type fakeTx struct{}

func (fakeTx) TxStats() node.TxStats {
	return node.TxStats{Sent: 7, SentFlood: 5, SentDirect: 2, AirtimeMs: 9000}
}

// savedSettings applies the app's changes to the store as the real saver does, without the reload.
type savedSettings struct {
	st       *store.Store
	mu       sync.Mutex
	changes  int
	channels int
}

func (s *savedSettings) UpdateCompanion(ctx context.Context, id int64, change func(*store.Companion) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.st.Companions.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := change(c); err != nil {
		return err
	}
	s.changes++
	return s.st.Companions.Update(ctx, c)
}

func (s *savedSettings) SaveChannels(context.Context, *companion.Companion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels++
	return nil
}

type rig struct {
	saved *savedSettings
	st    *store.Store
	comp  *companion.Companion
	srv   *Server
	addr  string
	id    int64
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	row := store.Companion{Name: "home", AppEnabled: true, AppPort: 5000, ShareLocation: true}
	st.WriteSync(func() { err = st.Companions.Create(ctx, &row) })
	if err != nil {
		t.Fatal(err)
	}
	lat, lon := -36.8485, 174.7633
	channels := config.ChannelList{{Name: "Public"}}
	comp, err := companion.NewCompanion(config.CompanionConfig{ID: row.ID, Name: "home", PrivateKey: strings.Repeat("11", 32), Latitude: &lat, Longitude: &lon, Channels: &channels},
		node.NewRadioMux(silentModem{}), st, nil, nil, nil, func(*meshcore.Packet) string { return "everywhere" })
	if err != nil {
		t.Fatal(err)
	}
	env := Env{Radio: fakeStats{}.RadioConfig(), AirtimeFactor: 1, Regions: []string{"nz"}, Stats: fakeStats{}, Tx: fakeTx{}, Started: time.Now()}
	saved := &savedSettings{st: st}
	srv := Start("127.0.0.1", 0, comp, env, st, saved, slog.Default())
	t.Cleanup(srv.Stop)
	if srv.ln == nil {
		t.Fatal(srv.err)
	}
	return &rig{saved: saved, st: st, comp: comp, srv: srv, addr: srv.ln.Addr().String(), id: row.ID}
}

// raw is an app speaking frames by hand, for what the library client will not send or cannot read.
type raw struct {
	t  *testing.T
	nc net.Conn
}

func (r *rig) dial(t *testing.T) *raw {
	nc, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	return &raw{t: t, nc: nc}
}

func (a *raw) send(cmd ...byte) {
	frame, err := mc.FrameEncode(mc.FrameTypeOutgoing, cmd)
	if err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.nc.Write(frame); err != nil {
		a.t.Fatal(err)
	}
}

// next is the next reply, skipping pushes.
func (a *raw) next() []byte {
	a.t.Helper()
	for {
		f := a.frame()
		if f[0] < 0x80 {
			return f
		}
	}
}

func (a *raw) frame() []byte {
	a.t.Helper()
	a.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hdr [3]byte
	if _, err := io.ReadFull(a.nc, hdr[:]); err != nil {
		a.t.Fatal(err)
	}
	if hdr[0] != mc.FrameTypeIncoming {
		a.t.Fatalf("frame type %#x", hdr[0])
	}
	data := make([]byte, binary.LittleEndian.Uint16(hdr[1:]))
	if _, err := io.ReadFull(a.nc, data); err != nil {
		a.t.Fatal(err)
	}
	return data
}

func (a *raw) ask(want []byte, cmd ...byte) {
	a.t.Helper()
	a.send(cmd...)
	if got := a.next(); !bytes.Equal(got, want) {
		a.t.Fatalf("command %d answered % x, want % x", cmd[0], got, want)
	}
}

// connTransport carries the library client over one TCP connection.
type connTransport struct {
	addr string
	nc   net.Conn
	mu   sync.Mutex
	resp func(mc.Response)
}

func (c *connTransport) Connect(context.Context) error {
	nc, err := net.Dial("tcp", c.addr)
	if err != nil {
		return err
	}
	c.nc = nc
	go func() {
		p := mc.NewFrameParser()
		buf := make([]byte, 512)
		for {
			n, err := nc.Read(buf)
			if err != nil {
				return
			}
			for _, f := range p.Feed(buf[:n]) {
				if r, err := mc.ParseResponse(f.Data); err == nil {
					c.resp(r)
				}
			}
		}
	}()
	return nil
}
func (c *connTransport) Close() error { return c.nc.Close() }
func (c *connTransport) Send(cmd []byte) error {
	frame, err := mc.FrameEncode(mc.FrameTypeOutgoing, cmd)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.nc.Write(frame)
	return err
}
func (c *connTransport) SetResponseHandler(h func(mc.Response)) { c.resp = h }
func (c *connTransport) SetErrorHandler(func(error))            {}

func (r *rig) client(t *testing.T) *client.Client {
	t.Helper()
	cl := client.New(&connTransport{addr: r.addr})
	if err := cl.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

// The handshake reads back through the library's own decoders, which were written apart from these encoders.
func TestHandshake(t *testing.T) {
	r := newRig(t)
	cl := r.client(t)

	info, err := cl.DeviceQuery(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if info.FirmwareVersion != appVersionCode || info.BLEPin != 0 || info.Model != "OwlShack" || info.MaxChannels != maxChannels || info.PathHashMode != 0 {
		t.Errorf("device info = %+v", info)
	}
	self, err := cl.AppStart(ctx(t), 3, "test")
	if err != nil {
		t.Fatal(err)
	}
	want := r.comp.Node().Identity().PublicKey()
	if self.Name != "home" || self.PublicKey != want || self.RadioFrequency != 869618 || self.RadioBandwidth != 62500 ||
		self.RadioSpreadFactor != 8 || self.RadioCodingRate != 8 || self.TxPower != 22 || self.AdvertLatitude != -36848500 || self.ManualAddContacts != 1 {
		t.Errorf("self info = %+v", self)
	}
}

// Nothing that would hand over the identity, change the device or reach past the companion is served.
func TestRefusals(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	unsupported := []byte{mc.RespErr, mc.ErrCodeUnsupportedCmd}
	a.ask([]byte{mc.RespDisabled}, mc.CmdExportPrivateKey)
	a.ask([]byte{mc.RespDisabled}, append([]byte{mc.CmdImportPrivateKey}, make([]byte, 64)...)...)
	a.ask(unsupported, append([]byte{mc.CmdReboot}, "reboot"...)...)
	a.ask(unsupported, append([]byte{mc.CmdFactoryReset}, "reset"...)...)
	a.ask(unsupported, mc.CmdSignStart)
	a.ask(unsupported, mc.CmdSignFinish)
	a.ask(unsupported, mc.CmdSetDevicePin, 0x40, 0xe2, 0x01, 0x00)
	a.ask(unsupported, append([]byte{mc.CmdRunCLICommand}, "get wifi.pwd"...)...)
	a.ask(unsupported, append([]byte{mc.CmdSendRawPacket, 0}, make([]byte, 20)...)...)
	a.ask(unsupported, append([]byte{mc.CmdSendRawData, 0}, make([]byte, 8)...)...)
	a.ask(unsupported, append([]byte{mc.CmdSetCustomVar}, "gps:1"...)...)
	a.ask(unsupported, 0x7f)
}

// A radio or host setting the app writes unchanged goes through, so the app's save is not abandoned; any change is refused.
func TestSettingsWrites(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	ok, refused := []byte{mc.RespOk}, []byte{mc.RespErr, mc.ErrCodeUnsupportedCmd}

	radio := func(freq uint32, sf byte) []byte {
		b := le32([]byte{mc.CmdSetRadioParams}, freq)
		return append(le32(b, 62500), sf, 8)
	}
	a.ask(ok, radio(869618, 8)...)
	a.ask(refused, radio(869618, 9)...)
	a.ask(refused, append(radio(869618, 8), 1)...) // repeat on
	a.ask(ok, mc.CmdSetRadioTxPower, 22)
	a.ask(refused, mc.CmdSetRadioTxPower, 10)

	a.ask(ok, le32([]byte{mc.CmdSetDeviceTime}, uint32(time.Now().Unix())+5)...)
	a.ask(refused, le32([]byte{mc.CmdSetDeviceTime}, uint32(time.Now().Unix())+3600)...)

	nz := meshcore.NewRegion("nz").Key
	a.ask(ok, append([]byte{mc.CmdSetFloodScopeKey, 0}, nz[:]...)...)
	a.ask(ok, append([]byte{mc.CmdSetFloodScopeKey, 0}, make([]byte, 16)...)...) // the firmware's null key
	a.ask([]byte{mc.RespErr, mc.ErrCodeIllegalArg}, append([]byte{mc.CmdSetFloodScopeKey, 0}, bytes.Repeat([]byte{7}, 16)...)...)
}

func TestContacts(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	pk := bytes.Repeat([]byte{7}, 32)
	var err error
	r.st.WriteSync(func() {
		if err = r.st.Contacts.Add(bg, r.id, pk, "Owly", "REPEATER"); err == nil {
			err = r.st.Contacts.UpdateOutPath(bg, r.id, pk, []byte{0xaa, 0xbb, 0xcc, 0xdd}, 2)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	cl := r.client(t)
	contacts, newest, err := cl.GetContactsSince(ctx(t), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts", len(contacts))
	}
	c := contacts[0]
	if c.AdvertName != "Owly" || c.Type != meshcore.AdvertTypeRepeater || c.PublicKey != [32]byte(pk) ||
		c.OutPathLen != meshcore.MakePathLen(2, 2) || !bytes.Equal(c.OutPath[:4], []byte{0xaa, 0xbb, 0xcc, 0xdd}) || newest != c.LastModified {
		t.Errorf("contact = %+v, newest %d", c, newest)
	}
	again, _, err := cl.GetContactsSince(ctx(t), newest, true)
	if err != nil || len(again) != 0 {
		t.Errorf("since the newest: %d contacts, %v", len(again), err)
	}
}

// A message heard while no app is connected waits for it, and one heard while connected tickles it.
func TestOfflineQueue(t *testing.T) {
	r := newRig(t)
	from := [32]byte{1, 2, 3, 4, 5, 6}
	pkt := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeTxtMsg, 0), PathLength: meshcore.MakePathLen(1, 2), Path: []byte{1, 2}, SNR: 6.5}
	r.srv.AppMessage(companion.AppMessage{Channel: -1, From: from, TxtType: meshcore.TxtTypePlain, Timestamp: 1700000000, Text: "while away", Packet: pkt})
	r.st.WriteSync(func() {}) // behind the queued write

	cl := r.client(t)
	waiting := make(chan struct{}, 4)
	cl.OnPush(mc.PushMsgWaiting, func(mc.Response) { waiting <- struct{}{} })
	if _, err := cl.DeviceQuery(ctx(t)); err != nil {
		t.Fatal(err)
	}
	msgs, err := cl.GetWaitingMessages(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Contact == nil || msgs[0].Contact.Text != "while away" || msgs[0].Contact.PubKeyPrefix != [6]byte(from[:6]) ||
		msgs[0].Contact.SenderTimestamp != 1700000000 || msgs[0].SNR != 6.5 || msgs[0].Contact.PathLen != meshcore.MakePathLen(1, 2) {
		t.Fatalf("waiting = %+v", msgs)
	}

	r.srv.AppMessage(companion.AppMessage{Channel: 0, TxtType: meshcore.TxtTypePlain, Timestamp: 1700000001, Text: "bob: hi", Packet: pkt})
	select {
	case <-waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("no MSG_WAITING push")
	}
	msgs, err = cl.GetWaitingMessages(ctx(t))
	if err != nil || len(msgs) != 1 || msgs[0].Channel == nil || msgs[0].Channel.Text != "bob: hi" || msgs[0].Channel.ChannelIdx != 0 {
		t.Fatalf("channel message = %+v, %v", msgs, err)
	}
}

// A second app takes over, as on the firmware, and the first is closed rather than left half-open.
func TestLastConnectionWins(t *testing.T) {
	r := newRig(t)
	first := r.dial(t)
	first.send(mc.CmdGetDeviceTime)
	if first.next()[0] != mc.RespCurrTime {
		t.Fatal("first app not served")
	}
	second := r.dial(t)
	second.send(mc.CmdGetDeviceTime)
	if second.next()[0] != mc.RespCurrTime {
		t.Fatal("second app not served")
	}
	first.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := first.nc.Read(make([]byte, 1)); err == nil {
		t.Error("first app still connected")
	}
	if st := r.srv.Status(); st.Client != second.nc.LocalAddr().String() {
		t.Errorf("status client = %q, want %q", st.Client, second.nc.LocalAddr())
	}
}

// A frame longer than the firmware accepts ends the connection at its header, before its body is read.
func TestOversizedFrameDrops(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	a.nc.Write([]byte{mc.FrameTypeOutgoing, 0xff, 0xff})
	a.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := a.nc.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("read after an oversized frame: %v, want the server to close", err)
	}
}

// The app's sends go through the companion, so they are stored and shown like any other.
func TestSendsAreStored(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	peer, _ := meshcore.GenerateLocalIdentity(nil)
	pk := peer.PublicKeyBytes()
	var err error
	r.st.WriteSync(func() { err = r.st.Contacts.Add(bg, r.id, pk, "Bob", "CHAT") })
	if err != nil {
		t.Fatal(err)
	}
	cl := r.client(t)
	sent, err := cl.SendTextMessage(ctx(t), peer.Identity, "hello bob", meshcore.TxtTypePlain)
	if err != nil {
		t.Fatal(err)
	}
	if !sent.IsFlood || sent.EstTimeout == 0 {
		t.Errorf("sent = %+v, want a flood with a timeout", sent)
	}
	if _, err := cl.SendChannelTextMessage(ctx(t), 0, "hello all", meshcore.TxtTypePlain); err != nil {
		t.Fatal(err)
	}
	r.st.WriteSync(func() {})
	dm, _ := r.st.Messages.List(bg, r.id, "dm:"+hexOf(pk), 5, 0)
	pub, _ := r.st.Messages.List(bg, r.id, "Public", 5, 0)
	if len(dm) != 1 || dm[0].Text != "hello bob" || dm[0].Direction != "tx" || len(pub) != 1 || pub[0].Text != "hello all" {
		t.Errorf("stored dm %+v, channel %+v", dm, pub)
	}

	a := r.dial(t)
	unknown := append([]byte{mc.CmdSendLogin}, bytes.Repeat([]byte{9}, 32)...)
	a.ask([]byte{mc.RespErr, mc.ErrCodeNotFound}, append(unknown, "pw"...)...)
	a.ask([]byte{mc.RespErr, mc.ErrCodeNotFound}, append([]byte{mc.CmdSendTxtMsg, 0, 0, 1, 2, 3, 4, 9, 9, 9, 9, 9, 9}, "hi"...)...)
}

// A contact's advert is a short push; a node that is not a contact is offered whole, for the app to add.
func TestAdvertPushes(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	known, stranger := [32]byte{1, 1}, [32]byte{2, 2}
	r.st.WriteSync(func() {
		r.st.Peers.Upsert(bg, &store.Peer{PubKey: stranger[:], Name: "Stranger", Type: "REPEATER", Lat: -36500000, Lon: 174500000, LastAdvertTS: 1700000000, LastSeen: time.Now()})
	})
	a := r.dial(t)
	a.send(mc.CmdGetDeviceTime)
	a.next() // connected before the adverts land
	r.st.WriteSync(func() { r.st.Contacts.Add(bg, r.id, known[:], "Known", "CHAT") })
	r.srv.AppAdvert(known)
	if f := a.frame(); !bytes.Equal(f, pubkeyPush(mc.PushAdvert, known)) {
		t.Errorf("contact advert = % x", f)
	}
	r.srv.AppAdvert(stranger)
	f := a.frame()
	got, err := mc.ParseResponse(f)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := got.Data.(mc.PushNewAdvertResponse)
	if !ok || n.PublicKey != stranger || n.AdvertName != "Stranger" || n.Type != meshcore.AdvertTypeRepeater || n.OutPathLen != 0xFF || n.AdvertLatitude != -36500000 || n.LastAdvert != 1700000000 {
		t.Errorf("new advert = %+v", got.Data)
	}
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

// Every contact and the end of the list arrive, however many there are: a command's replies wait for room rather than drop.
func TestManyContacts(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	const n = 600
	r.st.WriteSync(func() {
		for i := range n {
			pk := make([]byte, 32)
			binary.BigEndian.PutUint32(pk, uint32(i+1))
			r.st.Contacts.Add(bg, r.id, pk, "c", "CHAT")
		}
	})
	contacts, err := r.client(t).GetContacts(ctx(t))
	if err != nil || len(contacts) != n {
		t.Fatalf("got %d contacts, %v; want %d", len(contacts), err, n)
	}
}

// A route the app sends is held to what the REST API accepts: whole hops of 1 to 3 bytes, at most 64 bytes.
func TestContactRouteChecked(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	peer, _ := meshcore.GenerateLocalIdentity(nil)
	frame := func(outLen byte) []byte {
		b := append([]byte{mc.CmdAddUpdateContact}, peer.PublicKeyBytes()...)
		b = append(b, meshcore.AdvertTypeChat, 0, outLen)
		b = append(b, make([]byte, 64)...)
		return append(fixed(b, "Bob", 32), 0, 0, 0, 0)
	}
	illegal := []byte{mc.RespErr, mc.ErrCodeIllegalArg}
	a.ask(illegal, frame(0x68)...) // 2 bytes x 40 hops
	a.ask(illegal, frame(0xBF)...) // 3 bytes x 63 hops
	a.ask(illegal, frame(0xC1)...) // a 4-byte hash
	a.ask([]byte{mc.RespOk}, frame(meshcore.MakePathLen(2, 3))...)
	k, ok := r.srv.contact(peer.PublicKeyBytes())
	if !ok || len(k.OutPath) != 6 || k.OutPathHashSize != 2 {
		t.Fatalf("stored route = %+v", k)
	}
}

// The path hash mode is the one setting the app may change, and it is saved as the companion's bytes per hop.
func TestPathHashModeSaves(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	a.ask([]byte{mc.RespOk}, mc.CmdSetPathHashMode, 0, 0) // unchanged: nothing saved
	if r.saved.changes != 0 {
		t.Fatal("an unchanged mode was saved")
	}
	a.ask([]byte{mc.RespOk}, mc.CmdSetPathHashMode, 0, 1)
	a.ask([]byte{mc.RespErr, mc.ErrCodeIllegalArg}, mc.CmdSetPathHashMode, 0, 3)
	if c := r.row(t); c.PathHashSize == nil || *c.PathHashSize != 2 {
		t.Errorf("path hash size = %v, want 2", c.PathHashSize)
	}
}

// Radio and packet stats read through the library's own decoder: the noise floor, the last packet's signal, airtime and counts.
func TestRadioAndPacketStats(t *testing.T) {
	r := newRig(t)
	r.srv.AppRawRX([]byte{byte(meshcore.RouteTypeFlood), 0, 1, 2}, 7.25, -61)
	r.srv.AppRawRX([]byte{byte(meshcore.RouteTypeDirect), 0, 1, 2}, 5.5, -70)
	a := r.dial(t)
	a.send(mc.CmdGetStats, 1)
	radio, err := mc.ParseStatsResponse(a.next()[1:])
	if err != nil {
		t.Fatal(err)
	}
	if rs := radio.Radio; rs == nil || rs.NoiseFloor != -98 || rs.LastRSSI != -70 || rs.LastSNR != 5.5 || rs.TxAirSecs != 9 || rs.RxAirSecs != 3 {
		t.Errorf("radio stats = %+v", radio.Radio)
	}
	a.send(mc.CmdGetStats, 2)
	packets, err := mc.ParseStatsResponse(a.next()[1:])
	if err != nil {
		t.Fatal(err)
	}
	if ps := packets.Packets; ps == nil || ps.PacketsRecv != 2 || ps.PacketsSent != 7 || ps.SentFlood != 5 || ps.SentDirect != 2 || ps.RecvFlood != 1 || ps.RecvDirect != 1 {
		t.Errorf("packet stats = %+v", packets.Packets)
	}
}

func (r *rig) row(t *testing.T) *store.Companion {
	t.Helper()
	c, err := r.st.Companions.Get(context.Background(), r.id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The companion's own settings save as the UI saves them: its name, position, telemetry, whether adverts share the position, and its scope.
func TestCompanionSettings(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	ok, illegal, unsupported := []byte{mc.RespOk}, []byte{mc.RespErr, mc.ErrCodeIllegalArg}, []byte{mc.RespErr, mc.ErrCodeUnsupportedCmd}

	a.ask(ok, append([]byte{mc.CmdSetAdvertName}, "home"...)...) // unchanged
	if r.saved.changes != 0 {
		t.Fatal("an unchanged name was saved")
	}
	a.ask(ok, append([]byte{mc.CmdSetAdvertName}, "away from home and then some more text"...)...)
	if got := r.row(t).Name; got != "away from home and then some mo" {
		t.Errorf("name = %q, want it cut to 31 bytes", got)
	}

	latlon := func(lat, lon int32) []byte {
		return le32(le32([]byte{mc.CmdSetAdvertLatLon}, uint32(lat)), uint32(lon))
	}
	a.ask(ok, latlon(-41286500, 174776200)...)
	if c := r.row(t); c.Latitude == nil || *c.Latitude != -41.2865 || *c.Longitude != 174.7762 {
		t.Errorf("position = %v, %v", c.Latitude, c.Longitude)
	}
	a.ask(illegal, latlon(91e6, 0)...)
	a.ask(ok, latlon(0, 0)...)
	if c := r.row(t); c.Latitude != nil {
		t.Error("0,0 kept as a position")
	}

	a.ask(ok, mc.CmdSetOtherParams, 1, 2<<4|1<<2|2, 0) // env everyone, position chosen contacts, base everyone; position not shared
	if c := r.row(t); c.TelemBase != "contacts" || c.TelemLoc != "selected" || c.TelemEnv != "contacts" || c.ShareLocation {
		t.Errorf("other params saved as %s/%s/%s share %v", c.TelemBase, c.TelemLoc, c.TelemEnv, c.ShareLocation)
	}
	a.ask(unsupported, mc.CmdSetOtherParams, 0)          // adding contacts from adverts
	a.ask(unsupported, mc.CmdSetOtherParams, 1, 0, 1, 1) // extra acks
	a.ask(illegal, mc.CmdSetOtherParams, 1, 3)

	nz := meshcore.NewRegion("nz").Key
	a.ask(ok, append(fixed([]byte{mc.CmdSetDefaultFloodScope}, "nz", 32)[:32], nz[:]...)...)
	if got := r.row(t).FloodScope; got != "region:nz" {
		t.Errorf("scope = %q", got)
	}
	a.ask(illegal, append(fixed([]byte{mc.CmdSetDefaultFloodScope}, "zz", 32)[:32], bytes.Repeat([]byte{7}, 16)...)...)
}

// Channels set by slot follow the Channels page's rules, and are saved.
func TestSetChannel(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	ok, illegal := []byte{mc.RespOk}, []byte{mc.RespErr, mc.ErrCodeIllegalArg}
	set := func(slot byte, name string, key []byte) []byte {
		return append(fixed([]byte{mc.CmdSetChannel, slot}, name, 32), key...)
	}
	westest := meshcore.NewChannelFromHashtag(meshcore.NormalizeHashtag("#westest")).PSK
	a.ask(ok, set(1, "#westest", westest)...)
	if ch := r.comp.Node().Channel(1); ch == nil || ch.Name != "#westest" {
		t.Fatalf("slot 1 = %+v", ch)
	}
	priv := bytes.Repeat([]byte{0x5a}, 16)
	a.ask(ok, set(3, "Club", priv)...)
	a.send(mc.CmdGetChannel, 3)
	if got := a.next(); !bytes.Equal(got, channelInfoFrame(3, "Club", priv)) {
		t.Errorf("slot 3 reads % x", got)
	}
	a.ask(illegal, set(4, "Club", priv)...)    // a second channel by that name
	a.ask(illegal, set(3, "Renamed", priv)...) // only Public is renamed in place
	a.ask([]byte{mc.RespErr, mc.ErrCodeNotFound}, set(99, "x", priv)...)
	a.ask(ok, set(3, "", make([]byte, 16))...)
	if r.comp.Node().Channel(3) != nil {
		t.Error("slot 3 not cleared")
	}
	a.ask(ok, set(0, "Public", meshcore.PublicChannel().PSK)...) // unchanged
	if r.saved.channels != 4 {
		t.Errorf("channels saved %d times, want 4", r.saved.channels)
	}
}

// The app may rename a contact and star it, as on the firmware; its type is what its adverts say.
func TestContactRenameAndFavourite(t *testing.T) {
	r := newRig(t)
	bg := context.Background()
	peer, _ := meshcore.GenerateLocalIdentity(nil)
	pk := peer.PublicKeyBytes()
	r.st.WriteSync(func() { r.st.Contacts.Add(bg, r.id, pk, "Bob", "CHAT") })
	a := r.dial(t)
	frame := func(name string, typ, flags byte) []byte {
		b := append([]byte{mc.CmdAddUpdateContact}, pk...)
		b = append(b, typ, flags, 0xFF)
		b = append(b, make([]byte, 64)...)
		return append(fixed(b, name, 32), 0, 0, 0, 0)
	}
	a.ask([]byte{mc.RespOk}, frame("Robert", meshcore.AdvertTypeChat, 1|2<<1)...)
	k, _ := r.srv.contact(pk)
	if k.Name != "Robert" || !k.Metadata.Favourite || k.Metadata.TelemPerms != 2 {
		t.Errorf("contact = %+v", k)
	}
	if c := storedContact(k); c.flags != 1|2<<1 {
		t.Errorf("flags read back %b", c.flags)
	}
	a.ask([]byte{mc.RespErr, mc.ErrCodeUnsupportedCmd}, frame("Robert", meshcore.AdvertTypeRepeater, 0)...)
}

// A rebuild that moved a channel to another slot drops the app, so it reads them again rather than file messages under the wrong one.
func TestRebuildMovingChannelsDropsApp(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	a.send(mc.CmdGetDeviceTime)
	a.next()
	channels := config.ChannelList{{Name: "Public"}, {Name: "#westest"}}
	moved, err := companion.NewCompanion(config.CompanionConfig{ID: r.id, Name: "home", PrivateKey: strings.Repeat("11", 32), Channels: &channels},
		node.NewRadioMux(silentModem{}), r.st, nil, nil, nil, func(*meshcore.Packet) string { return "everywhere" })
	if err != nil {
		t.Fatal(err)
	}
	r.srv.Use(moved)
	a.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := a.nc.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("read after the move: %v, want the app dropped", err)
	}
}

// With sharing off the companion's advert, exported or sent, carries no position.
func TestAdvertKeepsPositionWhenNotShared(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lat, lon := -36.8485, 174.7633
	for _, share := range []bool{true, false} {
		comp, err := companion.NewCompanion(config.CompanionConfig{Name: "home", PrivateKey: strings.Repeat("11", 32), Latitude: &lat, Longitude: &lon, ShareLocation: &share},
			node.NewRadioMux(silentModem{}), st, nil, nil, nil, func(*meshcore.Packet) string { return "everywhere" })
		if err != nil {
			t.Fatal(err)
		}
		raw, err := comp.AppSelfAdvert()
		if err != nil {
			t.Fatal(err)
		}
		pkt, _ := meshcore.PacketFromBytes(raw)
		adv, err := meshcore.AdvertFromBytes(pkt.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if got := adv.AppData().HasLocation; got != share {
			t.Errorf("share %v: advert has a position %v", share, got)
		}
	}
}

// A replacement the rules refuse leaves the slot as it was, not empty.
func TestSetChannelRefusedKeepsSlot(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	set := func(slot byte, name string, key []byte) []byte {
		return append(fixed([]byte{mc.CmdSetChannel, slot}, name, 32), key...)
	}
	westest := meshcore.NewChannelFromHashtag(meshcore.NormalizeHashtag("#westest")).PSK
	a.ask([]byte{mc.RespOk}, set(1, "#westest", westest)...)
	// "public" with Public's key is Public, which slot 0 already holds.
	a.ask([]byte{mc.RespErr, mc.ErrCodeIllegalArg}, set(1, "public", meshcore.PublicChannel().PSK)...)
	if ch := r.comp.Node().Channel(1); ch == nil || ch.Name != "#westest" {
		t.Errorf("slot 1 = %+v after a refused replacement", ch)
	}
}

// Contact flags keep the three telemetry classes and the star; the firmware's remote CLI bit is not a telemetry class.
func TestContactFlagsMasked(t *testing.T) {
	r := newRig(t)
	peer, _ := meshcore.GenerateLocalIdentity(nil)
	pk := peer.PublicKeyBytes()
	r.st.WriteSync(func() { r.st.Contacts.Add(context.Background(), r.id, pk, "Bob", "CHAT") })
	b := append([]byte{mc.CmdAddUpdateContact}, pk...)
	b = append(b, meshcore.AdvertTypeChat, 0x10|0x02, 0xFF)
	b = append(b, make([]byte, 64)...)
	r.dial(t).ask([]byte{mc.RespOk}, append(fixed(b, "Bob", 32), 0, 0, 0, 0)...)
	if k, _ := r.srv.contact(pk); k.Metadata.TelemPerms != 0x01 {
		t.Errorf("telemPerms = %#x, want only the base class", k.Metadata.TelemPerms)
	}
}

// A change and its undo before the reload lands both reach the saved settings: "unchanged" is judged against them, not the running companion.
func TestChangeAndUndoBeforeReload(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	a.ask([]byte{mc.RespOk}, append([]byte{mc.CmdSetAdvertName}, "away"...)...)
	a.ask([]byte{mc.RespOk}, append([]byte{mc.CmdSetAdvertName}, "home"...)...) // the running companion is still "home"
	if got := r.row(t).Name; got != "home" {
		t.Errorf("saved name = %q, want the undo saved", got)
	}
}

// An empty slot reads as a blank channel, as the firmware's table does; the app finds a free slot by it. Past the last slot is not found.
func TestGetEmptyChannelSlot(t *testing.T) {
	r := newRig(t)
	a := r.dial(t)
	a.ask(channelInfoFrame(5, "", nil), mc.CmdGetChannel, 5)
	a.ask([]byte{mc.RespErr, mc.ErrCodeNotFound}, mc.CmdGetChannel, maxChannels)
}
