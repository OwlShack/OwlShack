package mqtt

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/meshcore-go/OwlShack/internal/config"
	"github.com/meshcore-go/OwlShack/internal/logging"
	"github.com/meshcore-go/OwlShack/internal/modem"
	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/node"
)

// publishJob keeps the publish call off the RX hot path, so a stalled broker can't back-pressure the modem dispatch goroutine.
type publishJob struct {
	topic   string
	payload []byte
	qos     byte
	retain  bool
}

const (
	// publishQueueDepth absorbs a short network hiccup without touching the modem RX goroutine.
	publishQueueDepth = 256

	// publishWaitTimeout bounds the worker's wait for paho's publish token.
	publishWaitTimeout = 5 * time.Second

	// connectWaitTimeout bounds the initial connect handshake.
	connectWaitTimeout = 10 * time.Second
)

// Initial-connect backoff: paho's SetAutoReconnect only covers a client that has connected at least once. Vars so tests can shrink them.
var (
	connectRetryMin = 5 * time.Second
	connectRetryMax = 5 * time.Minute
)

type brokerClient struct {
	cfg      config.BrokerConfig
	clientMu sync.RWMutex
	client   paho.Client
	pubKeyHx string
	iata     string

	// Resolved (placeholder-expanded) publish topics for this broker.
	packetTopicStr string
	statusTopicStr string

	disallowed map[byte]bool
	dedup      *meshcore.DedupCache // rx; nil when dedup disabled for this broker
	// Separate cache: DedupCache keys on the packet hash alone, and a relayed flood carries the same hash as the rx we published.
	dedupTx *meshcore.DedupCache

	publishCh  chan publishJob
	stop       chan struct{} // closed by Observer.Stop to halt the worker + senders
	workerDone chan struct{}
	dropped    atomic.Uint64
	published  atomic.Uint64
	// retrying guards retryConnect so its two call sites never run two loops for one broker.
	retrying atomic.Bool
	// refreshMu is held while a token refresh replaces the client, so a publish waits for the new one rather than dropping.
	refreshMu sync.Mutex
}

func (b *brokerClient) currentClient() paho.Client {
	b.clientMu.RLock()
	c := b.client
	b.clientMu.RUnlock()
	return c
}

func (b *brokerClient) swapClient(c paho.Client) {
	b.clientMu.Lock()
	b.client = c
	b.clientMu.Unlock()
}

func (b *brokerClient) packetTopic() string { return b.packetTopicStr }

func (b *brokerClient) statusTopic() string { return b.statusTopicStr }

const (
	defaultPacketTopic = "meshcore/{iata}/{pubkey}/packets"
	defaultStatusTopic = "meshcore/{iata}/{pubkey}/status"
)

// resolveTopics expands a broker's topic templates, defaulting to the meshcoretomqtt-compatible layout.
func resolveTopics(bcfg config.BrokerConfig, iata, pubKeyHx, origin string) (packetTopic, statusTopic string) {
	expand := func(tmpl, fallback string) string {
		if tmpl == "" {
			tmpl = fallback
		}
		return strings.NewReplacer(
			"{iata}", iata, "{IATA}", iata,
			"{pubkey}", pubKeyHx, "{PUBKEY}", pubKeyHx,
			"{publicKey}", pubKeyHx, "{PUBLIC_KEY}", pubKeyHx,
			"{name}", origin, "{NAME}", origin, "{origin}", origin,
		).Replace(tmpl)
	}

	return expand(bcfg.PacketTopic, defaultPacketTopic),
		expand(bcfg.StatusTopic, defaultStatusTopic)
}

func (b *brokerClient) isAllowed(payloadType byte) bool {
	return !b.disallowed[payloadType]
}

type Observer struct {
	radio node.MuxRadio
	// mux is retained for TxStats, which is not on MuxRadio; its counters are process-wide.
	mux   *node.RadioMux
	id    meshcore.LocalIdentity
	stats modem.StatsProvider
	log   *slog.Logger

	cfg             config.MqttConfig
	originName      string
	pubKeyHx        string
	brokers         []*brokerClient
	packetsReceived atomic.Uint64
	floodRx         atomic.Uint64
	directRx        atomic.Uint64
	// Per-packet estimates, as the firmware accumulates rx_air_time / total_air_time; TX comes from NoteTx, so it covers every transmission this process makes.
	rxAirMs  atomic.Uint64
	txAirMs  atomic.Uint64
	floodTx  atomic.Uint64
	directTx atomic.Uint64
	// relaying is the repeater's `repeat` setting; false when no repeater runs here.
	relaying    atomic.Bool
	lastSNR     atomic.Int64 // quarter-dB, so the float survives an atomic
	lastRSSI    atomic.Int32
	floodDups   atomic.Uint64
	directDups  atomic.Uint64
	parseErrors *atomic.Uint64

	mu     sync.Mutex
	cancel context.CancelFunc
	// runCtx, guarded by mu, lets SetRelaying publish a status out of band.
	runCtx   context.Context
	stopOnce sync.Once

	// brokersMu guards the brokers slice: Start appends while the API server is already serving BrokerStatuses.
	brokersMu sync.RWMutex

	// Keyed by broker name, not held on brokerClient, so a broker still reports its last error after a reload drops its client.
	healthMu sync.Mutex
	health   map[string]*brokerHealth
}

// brokerHealth is the connection history we report for one broker.
type brokerHealth struct {
	lastErr   string
	lastErrAt time.Time
	// errClient is the connection the error came from; nil when no connection was made.
	errClient   paho.Client
	connectedAt time.Time
}

// BrokerStatus is a snapshot of one configured broker's connection state.
type BrokerStatus struct {
	Name        string
	Host        string
	Port        int
	Transport   string
	TLS         bool
	AuthType    string
	Enabled     bool
	Connected   bool
	LastError   string
	LastErrorAt time.Time
	ConnectedAt time.Time
	Published   uint64
	Dropped     uint64
	StatusTopic string
}

func (o *Observer) brokerList() []*brokerClient {
	o.brokersMu.RLock()
	defer o.brokersMu.RUnlock()
	return o.brokers
}

func (o *Observer) recordConnected(name string) {
	o.healthMu.Lock()
	defer o.healthMu.Unlock()
	h := o.brokerHealthLocked(name)
	h.connectedAt = time.Now()
	h.lastErr, h.lastErrAt, h.errClient = "", time.Time{}, nil
}

// recordBrokerErr keeps the latest error and the client it came from (nil when none connected), so a replaced client's error never shows as the current one's.
func (o *Observer) recordBrokerErr(name string, c paho.Client, err error) {
	if err == nil {
		return
	}
	o.healthMu.Lock()
	defer o.healthMu.Unlock()
	h := o.brokerHealthLocked(name)
	h.lastErr = err.Error()
	h.lastErrAt = time.Now()
	h.errClient = c
}

func (o *Observer) brokerHealthLocked(name string) *brokerHealth {
	if o.health == nil {
		o.health = make(map[string]*brokerHealth)
	}
	h := o.health[name]
	if h == nil {
		h = &brokerHealth{}
		o.health[name] = h
	}
	return h
}

// BrokerStatuses reports every configured broker, connected or not; liveness is paho's IsConnectionOpen, as IsConnected stays true while reconnecting.
func (o *Observer) BrokerStatuses() []BrokerStatus {
	live := make(map[string]*brokerClient)
	for _, bc := range o.brokerList() {
		live[bc.cfg.Name] = bc
	}
	out := make([]BrokerStatus, 0, len(o.cfg.Brokers))
	for _, bcfg := range o.cfg.Brokers {
		st := BrokerStatus{
			Name:      bcfg.Name,
			Host:      bcfg.Host,
			Port:      bcfg.Port,
			Transport: cmp.Or(bcfg.Transport, "tcp"),
			TLS:       bcfg.TlsEnabled,
			AuthType:  cmp.Or(bcfg.AuthType, "none"),
			Enabled:   bcfg.Enabled,
		}
		var cur paho.Client
		if bc := live[bcfg.Name]; bc != nil {
			if cur = bc.currentClient(); cur != nil {
				st.Connected = cur.IsConnectionOpen()
			}
			st.Published = bc.published.Load()
			st.Dropped = bc.dropped.Load()
			st.StatusTopic = bc.statusTopic()
		}
		o.healthMu.Lock()
		if h := o.health[bcfg.Name]; h != nil {
			st.ConnectedAt = h.connectedAt
			if h.errClient == nil || h.errClient == cur {
				st.LastError, st.LastErrorAt = h.lastErr, h.lastErrAt
			}
		}
		o.healthMu.Unlock()
		out = append(out, st)
	}
	return out
}

func NewObserver(cfg config.MqttConfig, name string, mux *node.RadioMux, id meshcore.LocalIdentity, stats modem.StatsProvider, parseErrors *atomic.Uint64) (*Observer, error) {
	pkHex := publicKeyHex(id)
	radio := mux.NewRadio()

	obs := &Observer{
		radio:       radio,
		mux:         mux,
		id:          id,
		cfg:         cfg,
		stats:       stats,
		parseErrors: parseErrors,
		originName:  name,
		pubKeyHx:    pkHex,
		log:         slog.Default().With("component", "mqtt", "observer", name),
	}

	return obs, nil
}

func (o *Observer) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	o.mu.Lock()
	o.cancel = cancel
	o.runCtx = ctx
	o.mu.Unlock()

	iata := "test"
	if o.cfg.IataCode != nil && *o.cfg.IataCode != "" {
		iata = *o.cfg.IataCode
	}

	for _, bcfg := range o.cfg.Brokers {
		if !bcfg.Enabled {
			continue
		}

		disallowed := parseDisallowed(bcfg.DisallowedPacketTypes)
		packetTopic, statusTopic := resolveTopics(bcfg, iata, o.pubKeyHx, o.originName)

		bc := &brokerClient{
			cfg:            bcfg,
			pubKeyHx:       o.pubKeyHx,
			iata:           iata,
			packetTopicStr: packetTopic,
			statusTopicStr: statusTopic,
			disallowed:     disallowed,
			publishCh:      make(chan publishJob, publishQueueDepth),
			stop:           make(chan struct{}),
			workerDone:     make(chan struct{}),
		}
		if bcfg.Dedup {
			bc.dedup = &meshcore.DedupCache{}
			bc.dedupTx = &meshcore.DedupCache{}
		}

		go o.publishWorker(bc)

		// Registered whether or not it connects: an unreachable broker must stay visible and keep retrying.
		o.brokersMu.Lock()
		o.brokers = append(o.brokers, bc)
		o.brokersMu.Unlock()

		// Dialled off the caller's goroutine: connectBroker blocks up to connectWaitTimeout, so N
		// unreachable brokers would cost the companion N x that on startup and on every SIGHUP reload.
		go o.retryConnect(ctx, bc)
	}

	o.radio.SetPacketFilter(func(_ *meshcore.Packet) bool { return true })
	o.radio.SetRawDataHandler(o.onData)

	go o.heartbeatLoop(ctx)
	go o.tokenRefreshLoop(ctx)

	return nil
}

func (o *Observer) Stop() {
	o.stopOnce.Do(o.stop)
}

func (o *Observer) stop() {
	o.mu.Lock()
	if o.cancel != nil {
		o.cancel()
	}
	o.mu.Unlock()

	// Detach from the mux so no new packets reach onData; an in-flight deliver is safe because publishCh is never closed.
	if o.radio != nil {
		o.radio.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, bc := range o.brokerList() {
		o.publishStatus(ctx, bc, "offline")
		close(bc.stop)
		select {
		case <-bc.workerDone:
		case <-time.After(publishWaitTimeout):
			o.log.Warn("publish worker did not exit cleanly", "broker", bc.cfg.Name)
		}
		if c := bc.currentClient(); c != nil {
			c.Disconnect(500)
		}
	}
}

// publishWorker drains a broker's publish channel serially, with a bounded token wait so a stalled broker never blocks it.
func (o *Observer) publishWorker(bc *brokerClient) {
	defer close(bc.workerDone)
	for {
		select {
		case <-bc.stop:
			// Drain what is already buffered (the offline status enqueued during Stop), then exit.
			for {
				select {
				case job := <-bc.publishCh:
					o.doPublish(bc, job)
				default:
					return
				}
			}
		case job := <-bc.publishCh:
			o.doPublish(bc, job)
		}
	}
}

func (o *Observer) doPublish(bc *brokerClient, job publishJob) {
	bc.refreshMu.Lock()
	client := bc.currentClient()
	bc.refreshMu.Unlock()
	if client == nil || !client.IsConnected() {
		// Publishing to a disconnected client would block for the full publishWaitTimeout per job and stall the worker.
		bc.dropped.Add(1)
		return
	}
	token := client.Publish(job.topic, job.qos, job.retain, job.payload)
	if !token.WaitTimeout(publishWaitTimeout) {
		o.log.Warn("publish timed out", "broker", bc.cfg.Name, "topic", job.topic)
		o.recordBrokerErr(bc.cfg.Name, client, fmt.Errorf("publish to %s timed out", job.topic))
		return
	}
	if err := token.Error(); err != nil {
		o.log.Error("publish error", "broker", bc.cfg.Name, "error", err)
		o.recordBrokerErr(bc.cfg.Name, client, err)
		return
	}
	bc.published.Add(1)
}

// enqueuePublish is called from the modem RX goroutine, so it MUST never block: a full queue drops and counts the job.
func (o *Observer) enqueuePublish(bc *brokerClient, job publishJob) {
	select {
	case bc.publishCh <- job:
	case <-bc.stop:
		// Shutting down; drop silently.
	default:
		dropped := bc.dropped.Add(1)
		if dropped == 1 || dropped%100 == 0 {
			o.log.Warn("publish queue full, dropping",
				"broker", bc.cfg.Name, "topic", job.topic, "dropped", dropped)
		}
	}
}

func (o *Observer) onData(data []byte, snr float32, rssi int8, hasSignalInfo bool) {
	o.log.Log(context.Background(), logging.LevelTrace, "raw radio data",
		"len", len(data), "hex", strings.ToUpper(hex.EncodeToString(data)),
		"snr", snr, "rssi", rssi)

	pkt, err := meshcore.PacketFromBytes(data)
	if err != nil {
		o.log.Log(context.Background(), logging.LevelTrace, "packet parse failed", "error", err)
		return
	}
	pkt.SNR = snr
	pkt.RSSI = rssi
	pkt.HasSignalInfo = hasSignalInfo

	o.packetsReceived.Add(1)
	o.lastSNR.Store(int64(snr * 4))
	o.lastRSSI.Store(int32(rssi))
	o.addAir(&o.rxAirMs, len(data))
	if pkt.IsRouteDirect() {
		o.directRx.Add(1)
	} else {
		o.floodRx.Add(1)
	}
	o.publishPacket(pkt, data, "rx")
}

// NoteTx publishes one transmitted packet and folds it into the TX counters, from the modem's outbound handler.
func (o *Observer) NoteTx(data []byte) {
	pkt, err := meshcore.PacketFromBytes(data)
	if err != nil {
		return
	}
	o.addAir(&o.txAirMs, len(data))
	if pkt.IsRouteDirect() {
		o.directTx.Add(1)
	} else {
		o.floodTx.Add(1)
	}
	o.publishPacket(pkt, data, "tx")
}

// SetRelaying records the `repeat` flag and publishes a status on change, so consumers don't wait up to StatusIntervalSeconds for it.
func (o *Observer) SetRelaying(v bool) {
	if o.relaying.Swap(v) == v {
		return
	}
	o.mu.Lock()
	ctx := o.runCtx
	o.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return // not started yet: Start publishes the first status itself
	}
	for _, bc := range o.brokerList() {
		o.publishStatus(ctx, bc, "online")
	}
}

func (o *Observer) publishPacket(pkt *meshcore.Packet, rawBytes []byte, direction string) {
	o.log.Log(context.Background(), logging.LevelTrace, "new packet accepted",
		"direction", direction, "type", pkt.PayloadType(),
		"payload_len", len(pkt.Payload))

	payload, err := formatPacket(pkt, rawBytes, o.originName, o.pubKeyHx, direction, o.stats)
	if err != nil {
		o.log.Error("format error", "error", err)
		return
	}

	for _, bc := range o.brokerList() {
		if !bc.isAllowed(pkt.PayloadType()) {
			o.log.Log(context.Background(), logging.LevelTrace, "packet type filtered",
				"broker", bc.cfg.Name, "type", pkt.PayloadType())
			continue
		}
		if d := bc.dedupFor(direction); d != nil && d.HasSeen(pkt) {
			o.log.Log(context.Background(), logging.LevelTrace, "dedup hit, skipping",
				"broker", bc.cfg.Name, "type", pkt.PayloadType())
			if pkt.IsRouteDirect() {
				o.directDups.Add(1)
			} else {
				o.floodDups.Add(1)
			}
			continue
		}
		o.log.Log(context.Background(), logging.LevelTrace, "publishing packet",
			"broker", bc.cfg.Name, "topic", bc.packetTopic(), "direction", direction)
		o.enqueuePublish(bc, publishJob{
			topic:   bc.packetTopic(),
			payload: payload,
			qos:     0,
			retain:  false,
		})
	}
}

func (o *Observer) heartbeatLoop(ctx context.Context) {
	interval := time.Duration(o.cfg.StatusIntervalSeconds()) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, bc := range o.brokerList() {
				o.publishStatus(ctx, bc, "online")
			}
		}
	}
}

// retryConnect covers the two cases paho's auto-reconnect does not: a broker unreachable at startup, and a failed token-refresh reconnect.
func (o *Observer) retryConnect(ctx context.Context, bc *brokerClient) {
	if !bc.retrying.CompareAndSwap(false, true) {
		return // a loop is already running for this broker
	}
	defer bc.retrying.Store(false)

	// The first attempt is immediate; backoff applies only once one has failed.
	var delay time.Duration
	firstFailure := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-bc.stop:
			return
		case <-time.After(delay):
		}

		if c := bc.currentClient(); c != nil && c.IsConnected() {
			return // paho's auto-reconnect got there first
		}

		client, err := o.connectBroker(bc.cfg, bc.iata)
		if err != nil {
			o.recordBrokerErr(bc.cfg.Name, nil, err)
			delay = min(max(delay*2, connectRetryMin), connectRetryMax)
			// The first failure is the one an operator needs to see; the rest are a loop.
			if firstFailure {
				firstFailure = false
				o.log.Error("broker connect failed, retrying in background",
					"broker", bc.cfg.Name, "error", err, "retry_in", delay)
			} else {
				o.log.Debug("broker connect retry failed",
					"broker", bc.cfg.Name, "error", err, "retry_in", delay)
			}
			continue
		}
		// Stop may have run while we were dialling; adopting the client now would leak a live connection.
		select {
		case <-bc.stop:
			client.Disconnect(0)
			return
		default:
		}
		if old := bc.currentClient(); old != nil {
			old.Disconnect(0)
		}
		bc.swapClient(client)
		o.recordConnected(bc.cfg.Name)
		o.publishStatus(ctx, bc, "online")
		o.log.Info("connected", "broker", bc.cfg.Name)
		return
	}
}

func (o *Observer) tokenRefreshLoop(ctx context.Context) {
	refreshAt := time.Duration(float64(tokenLifetime) * 0.8)
	ticker := time.NewTicker(refreshAt)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, bc := range o.brokerList() {
				o.refreshToken(ctx, bc)
			}
		}
	}
}

// refreshToken re-mints a token broker's credentials by reconnecting it, and reports whether it did.
func (o *Observer) refreshToken(ctx context.Context, bc *brokerClient) bool {
	if !strings.EqualFold(bc.cfg.AuthType, "token") {
		return false
	}
	// retryConnect owns the client while it runs, and a never-connected broker has none.
	if bc.retrying.Load() {
		return false
	}
	c := bc.currentClient()
	if c == nil {
		return false
	}
	o.log.Debug("refreshing token", "broker", bc.cfg.Name)

	// Close the old session before dialling: a second session on the same ClientID is a takeover, which makes
	// the broker publish our offline will, and paho redials a kicked client at once, so the two fight.
	bc.refreshMu.Lock()
	c.Disconnect(250)
	newClient, err := o.connectBroker(bc.cfg, bc.iata)
	if err == nil {
		bc.swapClient(newClient)
	}
	bc.refreshMu.Unlock()
	if err != nil {
		o.log.Error("token refresh reconnect failed, retrying in background",
			"broker", bc.cfg.Name, "error", err)
		o.recordBrokerErr(bc.cfg.Name, nil, err)
		// Without this the broker sits disconnected until the next refresh tick.
		go o.retryConnect(ctx, bc)
		return false
	}
	o.publishStatus(ctx, bc, "online")
	o.log.Info("token refreshed", "broker", bc.cfg.Name)
	return true
}

// txCounts reads the shared mux's transmit counters; zero when no mux is wired (tests).
func (o *Observer) txCounts() TxCounts {
	if o.mux == nil {
		return TxCounts{}
	}
	s := o.mux.TxStats()
	out := TxCounts{
		Sent:          s.Sent,
		BusyRequeued:  s.BusyRequeued,
		BusyDropped:   s.BusyDropped,
		QueueRejected: s.QueueRejected,
		Failed:        s.Failed,
	}
	if o.radio != nil {
		out.QueueLen = o.radio.TxQueueLen()
	}
	return out
}

// addAir accumulates one packet's estimated airtime.
func (o *Observer) addAir(dst *atomic.Uint64, packetLen int) {
	if o.stats == nil {
		return
	}
	if ms := o.stats.EstAirtimeMs(packetLen); ms > 0 {
		dst.Add(uint64(ms))
	}
}

func (bc *brokerClient) dedupFor(direction string) *meshcore.DedupCache {
	if direction == "tx" {
		return bc.dedupTx
	}
	return bc.dedup
}

func (o *Observer) observerCounts() ObserverCounts {
	return ObserverCounts{
		RxMs:     o.rxAirMs.Load(),
		TxMs:     o.txAirMs.Load(),
		FloodTx:  o.floodTx.Load(),
		DirectTx: o.directTx.Load(),
		Relaying: o.relaying.Load(),
		LastSNR:  float64(o.lastSNR.Load()) / 4,
		LastRSSI: int16(o.lastRSSI.Load()),
	}
}

func (o *Observer) linkStats() modem.LinkStats {
	if o.stats == nil {
		return modem.LinkStats{}
	}
	return o.stats.LinkStats()
}

func (o *Observer) publishStatus(ctx context.Context, bc *brokerClient, status string) {
	var radio modem.RadioInfo
	var ds modem.DeviceStats
	if o.stats != nil {
		radio = o.stats.RadioConfig()
		ds = o.stats.Stats(ctx)
	}

	packets := PacketCounts{
		Received:   o.packetsReceived.Load(),
		FloodRx:    o.floodRx.Load(),
		DirectRx:   o.directRx.Load(),
		FloodDups:  o.floodDups.Load(),
		DirectDups: o.directDups.Load(),
	}

	payload, err := formatStatus(status, o.originName, o.pubKeyHx, radio, ds, packets,
		o.txCounts(), o.linkStats(), o.observerCounts(), o.parseErrors.Load())
	if err != nil {
		o.log.Error("status format error", "error", err)
		return
	}

	o.log.Log(ctx, logging.LevelTrace, "publishing status",
		"broker", bc.cfg.Name, "topic", bc.statusTopic(),
		"json", string(payload))
	o.enqueuePublish(bc, publishJob{
		topic:   bc.statusTopic(),
		payload: payload,
		qos:     1,
		retain:  bc.cfg.RetainStatus,
	})
}

func (o *Observer) connectBroker(bcfg config.BrokerConfig, iata string) (paho.Client, error) {
	var scheme string
	switch strings.ToLower(bcfg.Transport) {
	case "websockets", "ws", "wss":
		if bcfg.TlsEnabled {
			scheme = "wss"
		} else {
			scheme = "ws"
		}
	default:
		if bcfg.TlsEnabled {
			scheme = "tls"
		} else {
			scheme = "tcp"
		}
	}

	brokerURL := fmt.Sprintf("%s://%s:%d%s", scheme, bcfg.Host, bcfg.Port, bcfg.Path)
	clientID := fmt.Sprintf("meshcore_%s_%s", o.pubKeyHx[:16], bcfg.Host)

	opts := paho.NewClientOptions()
	opts.AddBroker(brokerURL)
	opts.SetClientID(clientID)
	opts.SetKeepAlive(60 * time.Second)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(5 * time.Minute)

	if bcfg.TlsEnabled {
		opts.SetTLSConfig(&tls.Config{
			InsecureSkipVerify: bcfg.TlsInsecure,
			MinVersion:         tls.VersionTLS12,
		})
	}

	switch strings.ToLower(bcfg.AuthType) {
	case "token":
		audience := cmp.Or(bcfg.Audience, bcfg.Host)
		username := tokenUsername(o.id)
		email, owner := derefStr(o.cfg.Email), derefStr(o.cfg.Owner)
		// Minted per connect attempt so paho's auto-reconnect never presents an expired token.
		opts.SetCredentialsProvider(func() (string, string) {
			token, _, err := generateToken(o.id, audience, email, owner)
			if err != nil {
				// The provider cannot fail, so surface the error here.
				o.log.Error("generating auth token", "broker", bcfg.Name, "error", err)
				o.recordBrokerErr(bcfg.Name, nil, fmt.Errorf("generating auth token: %w", err))
			}
			return username, token
		})
	case "basic":
		opts.SetUsername(bcfg.Username)
		opts.SetPassword(bcfg.Password)
	}

	_, statusTopic := resolveTopics(bcfg, iata, o.pubKeyHx, o.originName)

	// LWT uses minimal status (no live stats — we're about to disconnect).
	offlinePayload, _ := formatStatus("offline", o.originName, o.pubKeyHx,
		modem.RadioInfo{}, modem.DeviceStats{}, PacketCounts{}, TxCounts{}, modem.LinkStats{}, ObserverCounts{}, 0)
	opts.SetWill(statusTopic, string(offlinePayload), 1, bcfg.RetainStatus)

	opts.SetOnConnectHandler(func(paho.Client) { o.recordConnected(bcfg.Name) })
	opts.SetConnectionLostHandler(func(c paho.Client, err error) {
		o.log.Warn("broker connection lost", "broker", bcfg.Name, "error", err)
		o.recordBrokerErr(bcfg.Name, c, err)
	})

	client := paho.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(connectWaitTimeout) {
		client.Disconnect(0)
		return nil, fmt.Errorf("connecting to %s: timeout after %s", brokerURL, connectWaitTimeout)
	}
	if err := token.Error(); err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", brokerURL, err)
	}

	return client, nil
}

var payloadTypeNames = map[string]byte{
	"req":        meshcore.PayloadTypeReq,
	"response":   meshcore.PayloadTypeResponse,
	"txt_msg":    meshcore.PayloadTypeTxtMsg,
	"ack":        meshcore.PayloadTypeAck,
	"advert":     meshcore.PayloadTypeAdvert,
	"grp_txt":    meshcore.PayloadTypeGrpTxt,
	"grp_data":   meshcore.PayloadTypeGrpData,
	"anon_req":   meshcore.PayloadTypeAnonReq,
	"path":       meshcore.PayloadTypePath,
	"trace":      meshcore.PayloadTypeTrace,
	"multi_part": meshcore.PayloadTypeMultiPart,
	"control":    meshcore.PayloadTypeControl,
	"raw_custom": meshcore.PayloadTypeRawCustom,
}

func parseDisallowed(names []string) map[byte]bool {
	if len(names) == 0 {
		return nil
	}
	m := make(map[byte]bool, len(names))
	for _, name := range names {
		if v, ok := payloadTypeNames[strings.ToLower(name)]; ok {
			m[v] = true
		}
	}
	return m
}
