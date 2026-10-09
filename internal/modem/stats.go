package modem

import (
	"context"
	"encoding/binary"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OwlShack/OwlShack/internal/logging"
	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/hardware"
)

type RadioInfo struct {
	FreqHz  uint32
	BwHz    uint32
	SF      uint8
	CR      uint8
	TxPower uint8
}

type DeviceStats struct {
	// NoiseFloor is dBm; HaveNoiseFloor is false until the radio has measured one, so an unmeasured 0 never reads as a reading.
	NoiseFloor     int16
	HaveNoiseFloor bool
	// HaveBattery is false when there is no cell at all; a KISS board answering 0 still sets it.
	BatteryMV   uint16
	HaveBattery bool
	UptimeSecs  uint32
	// MCUTempC is the modem board's MCU temperature. HaveMCUTemp is false when
	// the board can't measure one (the modem answers HW_ERR_NO_CALLBACK), so
	// 0 °C is distinguishable from unknown.
	MCUTempC    float64
	HaveMCUTemp bool
	// Sensors is what the board's own sensors read, one LPP channel each; HaveSensors is false until it answers the query.
	Sensors     []meshcore.LPPReading
	HaveSensors bool
}

// LinkStats mirrors hardware.ModemStats. Throughout, nil means the transport cannot measure that
// counter and 0 means it measured none: a KISS framing fault has no analogue on the SPI path, and a
// chip-level CRC count has none on the KISS path.
type LinkStats struct {
	// InboundDroppedNew is a frame discarded because our inbound queue was full; the SPI driver's own drop count lands here.
	InboundDroppedNew uint64
	// HandlerSlow counts dispatches over the watchdog; DATA dispatch is serial, so one slow handler stalls RX for every consumer.
	HandlerSlow uint64

	// KISS framing concepts: the SPI chip hands us a decoded packet with its metadata attached, so none of these can occur there.
	// HwDecodeErrors is a malformed SETHARDWARE frame, i.e. the battery/temp/noise-floor channel, not a mesh packet.
	HwDecodeErrors       *uint64
	InboundDroppedOldest *uint64
	// Both count signal reports lost on the link, whose packet goes out with no SNR/RSSI: a timeout waited 1 s, a misattribution was caught by the next frame.
	RxMetaTimeouts      *uint64
	RxMetaMisattributed *uint64
	HwErrors            *uint64 // HW_RESP_ERROR frames received
	TxOutcomeLost       *uint64 // TX_DONE waits abandoned by a reconnect

	// The SPI path's own counters.
	// PacketsRecv and PacketsSent are the chip's own totals; with CRCErrors they separate a deaf radio from one hearing only garbage.
	PacketsRecv *uint64
	PacketsSent *uint64
	CRCErrors   *uint64 // chip-level CRC and header errors: a noisy channel
	// RecvErrors is the radio driver failing to read a packet it knew had arrived: the firmware's recv_errors, measurable on both transports.
	RecvErrors *uint64
	// DriverErrors is SPI transaction failures, busy timeouts and failed IRQ reads.
	DriverErrors *uint64
	// RecvRecoveries is the watchdog re-arming a stuck receiver.
	RecvRecoveries *uint64
}

type StatsProvider interface {
	// Transport names the link ("kiss" or "spi"), which decides which counters can move at all.
	Transport() string
	RadioConfig() RadioInfo
	Stats(ctx context.Context) DeviceStats
	// CachedStats is the last readings the board volunteered: no wire traffic, no 500ms wait, up to StaleReadingAfter old.
	CachedStats() DeviceStats
	// LinkStats takes no ctx: atomic loads, unlike Stats which polls the board over the wire.
	LinkStats() LinkStats
	// EstAirtimeMs and PacketScore mirror the firmware's getEstAirtimeFor and packetScore; both return 0 when radio params are unknown.
	EstAirtimeMs(packetLen int) uint32
	PacketScore(snrDB float64, packetLen int) float64
}

type kissStatsProvider struct {
	modem     *hardware.KissModem
	radio     RadioInfo
	startTime time.Time
	log       *slog.Logger

	// lastReply is UnixNano of the modem's last answer to a hardware query, an error reply included
	// (the firmware answers a command it lacks with HW_ERR_UNKNOWN_CMD); 0 means it has never answered.
	lastReply atomic.Int64

	mu          sync.Mutex
	fwCounters  *hardware.FirmwareStats // nil until the modem answers HW_CMD_GET_STATS
	noiseFloor  int16
	haveNoise   bool
	batteryMV   uint16
	haveBattery bool
	mcuTempC    float64
	haveMCUTemp bool
	sensors     []meshcore.LPPReading
	haveSensors bool
	// sensorsAt is the sensor query's own last answer, as the board's other answers don't vouch for it.
	sensorsAt time.Time
}

// StaleReadingAfter is how long a board reading survives without the modem answering. Longer than
// one probe interval so a single dropped reply does not flap the value in and out of the payload.
const StaleReadingAfter = 45 * time.Second

// kissQueryTimeout bounds one stats poll's board queries together.
const kissQueryTimeout = 3 * time.Second

// ConnectedAt is when this link was set up, the start of a silence for a board that has never answered.
func (p *kissStatsProvider) ConnectedAt() time.Time { return p.startTime }

// LastReply reports when the modem last answered a hardware query; the zero time means never.
func (p *kissStatsProvider) LastReply() time.Time {
	ns := p.lastReply.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func NewKissStatsProvider(modem *hardware.KissModem, radio RadioInfo) *kissStatsProvider {
	p := &kissStatsProvider{
		modem:     modem,
		radio:     radio,
		startTime: time.Now(),
		log:       slog.Default().With("component", "stats", "type", "kiss"),
	}

	answered := func(byte, []byte) { p.lastReply.Store(time.Now().UnixNano()) }
	modem.OnHwResponse(hardware.HW_RESP_ERROR, answered)
	modem.OnHwResponse(hardware.HwResp(hardware.HW_CMD_GET_STATS), answered)
	modem.OnHwResponse(hardware.HwResp(hardware.HW_CMD_GET_NOISE_FLOOR), p.onNoiseFloor)
	modem.OnHwResponse(hardware.HwResp(hardware.HW_CMD_GET_BATTERY), p.onBattery)
	modem.OnHwResponse(hardware.HwResp(hardware.HW_CMD_GET_MCU_TEMP), p.onMCUTemp)

	return p
}

func (p *kissStatsProvider) LinkStats() LinkStats {
	s := p.modem.Stats()
	p.mu.Lock()
	fw := p.fwCounters
	p.mu.Unlock()

	ls := LinkStats{
		InboundDroppedNew:    s.InboundDroppedNew,
		HandlerSlow:          s.HandlerSlow,
		HwDecodeErrors:       &s.HwDecodeErrors,
		InboundDroppedOldest: &s.InboundDroppedOldest,
		RxMetaTimeouts:       &s.RxMetaTimeouts,
		RxMetaMisattributed:  &s.RxMetaMisattributed,
		HwErrors:             &s.HwErrors,
		TxOutcomeLost:        &s.TxOutcomeLost,
	}
	if fw != nil {
		recv, sent, errs := uint64(fw.PacketsRecv), uint64(fw.PacketsSent), uint64(fw.PacketsErrors)
		ls.PacketsRecv, ls.PacketsSent, ls.RecvErrors = &recv, &sent, &errs
	}
	return ls
}

// CachedStats skips the queries and the wait; snapshot already drops a reading once the board goes quiet, so stale becomes absent.
func (p *kissStatsProvider) CachedStats() DeviceStats { return p.snapshot() }

func (p *kissStatsProvider) Transport() string { return "kiss" }

func (p *kissStatsProvider) RadioConfig() RadioInfo {
	return p.radio
}

func (p *kissStatsProvider) EstAirtimeMs(packetLen int) uint32 {
	if p.radio.BwHz == 0 || p.radio.SF == 0 {
		return 0
	}
	return hardware.LoRaAirtimeEstimator(&hardware.RadioConfig{
		FreqHz: p.radio.FreqHz, BwHz: p.radio.BwHz, SF: p.radio.SF, CR: p.radio.CR,
	})(packetLen)
}

func (p *kissStatsProvider) PacketScore(snrDB float64, packetLen int) float64 {
	return hardware.PacketScore(snrDB, p.radio.SF, packetLen)
}

func (p *kissStatsProvider) Stats(ctx context.Context) DeviceStats {
	// Each query returns before the reply handlers run, so its answer is recorded here; the deadline keeps a lost reply from stalling the poll.
	ctx, cancel := context.WithTimeout(ctx, kissQueryTimeout)
	defer cancel()
	record := func(set func()) {
		p.lastReply.Store(time.Now().UnixNano())
		p.mu.Lock()
		set()
		p.mu.Unlock()
	}
	if fw, err := p.modem.FirmwareCounters(ctx); err != nil {
		p.log.Debug("firmware counters unavailable", "error", err)
	} else {
		record(func() { p.fwCounters = &fw })
	}
	if nf, err := p.modem.NoiseFloor(ctx); err != nil {
		p.log.Debug("noise floor unavailable", "error", err)
	} else {
		record(func() { p.noiseFloor, p.haveNoise = nf, nf != 0 }) // the firmware answers 0 until it has sampled
	}
	if mv, err := p.modem.Battery(ctx); err != nil {
		p.log.Debug("battery unavailable", "error", err)
	} else {
		record(func() { p.batteryMV, p.haveBattery = mv, true })
	}
	if c, err := p.modem.MCUTemp(ctx); err != nil {
		p.log.Debug("mcu temperature unavailable", "error", err)
	} else {
		record(func() { p.mcuTempC, p.haveMCUTemp = math.Round(float64(c)*10)/10, true })
	}
	// Last, as the firmware reads every sensor before it answers, so a slow one can't cost the poll the rest.
	if lpp, err := p.modem.Sensors(ctx, meshcore.TelemPermEnvironment); err != nil {
		p.log.Debug("board sensors unavailable", "error", err)
	} else if rs, err := meshcore.LPPDecode(lpp); err != nil {
		p.log.Debug("board sensors undecodable", "error", err)
		record(func() { p.sensors, p.haveSensors = nil, false })
	} else {
		record(func() { p.sensors, p.haveSensors, p.sensorsAt = rs, true, time.Now() })
	}

	ds := p.snapshot()
	p.log.Log(ctx, logging.LevelTrace, "stats polled",
		"noise_floor", ds.NoiseFloor, "battery_mv", ds.BatteryMV,
		"mcu_temp_c", ds.MCUTempC, "board_sensors", len(ds.Sensors), "uptime_secs", ds.UptimeSecs,
		"readings_current", ds.HaveBattery || ds.HaveMCUTemp)
	return ds
}

// snapshot builds the reading set without touching the modem, so a board reading is only reported
// while the modem is still answering. The reply flags are sticky: a modem whose serial port had gone
// away kept publishing its last battery voltage and temperature, so a consumer saw a healthy 4.1 V
// board at the moment the port was closed.
func (p *kissStatsProvider) snapshot() DeviceStats {
	last := p.LastReply()
	fresh := !last.IsZero() && time.Since(last) <= StaleReadingAfter

	p.mu.Lock()
	defer p.mu.Unlock()
	return DeviceStats{
		NoiseFloor:     p.noiseFloor,
		HaveNoiseFloor: p.haveNoise && fresh,
		BatteryMV:      p.batteryMV,
		HaveBattery:    p.haveBattery && fresh,
		UptimeSecs:     uint32(time.Since(p.startTime).Seconds()),
		MCUTempC:       p.mcuTempC,
		HaveMCUTemp:    p.haveMCUTemp && fresh,
		Sensors:        p.sensors,
		HaveSensors:    p.haveSensors && fresh && time.Since(p.sensorsAt) <= StaleReadingAfter,
	}
}

func (p *kissStatsProvider) onNoiseFloor(_ byte, data []byte) {
	if len(data) < 2 {
		return
	}
	p.lastReply.Store(time.Now().UnixNano())
	p.mu.Lock()
	p.noiseFloor = int16(binary.LittleEndian.Uint16(data[:2]))
	p.haveNoise = p.noiseFloor != 0
	p.mu.Unlock()
}

// onMCUTemp decodes the int16 tenths-of-a-degree reply (KissModem::handleGetMCUTemp).
func (p *kissStatsProvider) onMCUTemp(_ byte, data []byte) {
	if len(data) < 2 {
		return
	}
	p.lastReply.Store(time.Now().UnixNano())
	p.mu.Lock()
	p.mcuTempC = float64(int16(binary.LittleEndian.Uint16(data[:2]))) / 10
	p.haveMCUTemp = true
	p.mu.Unlock()
}

func (p *kissStatsProvider) onBattery(_ byte, data []byte) {
	if len(data) < 2 {
		return
	}
	p.lastReply.Store(time.Now().UnixNano())
	p.mu.Lock()
	p.batteryMV = binary.LittleEndian.Uint16(data[:2])
	p.haveBattery = true
	p.mu.Unlock()
}
