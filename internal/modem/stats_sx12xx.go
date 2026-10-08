package modem

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/OwlShack/meshcore-go/hardware/sx12xx"
)

// sx12xxStatsProvider reports what a directly-attached SPI radio can measure, with no MeshCore firmware in front of the chip.
type sx12xxStatsProvider struct {
	// modem is attached after NewModem, which needs this provider's error handler first.
	modem     atomic.Pointer[sx12xx.Modem]
	radio     RadioInfo
	startTime time.Time
	log       *slog.Logger

	// driverErrors counts driver faults: SPI transaction failures, busy-line timeouts, failed IRQ reads.
	driverErrors atomic.Uint64
}

// NewSx12xxStatsProvider wraps a directly-attached radio: pass NoteError as the modem's error handler, then Attach the modem.
func NewSx12xxStatsProvider(radio RadioInfo) *sx12xxStatsProvider {
	return &sx12xxStatsProvider{
		radio:     radio,
		startTime: time.Now(),
		log:       slog.Default().With("component", "stats", "type", "sx12xx"),
	}
}

func (p *sx12xxStatsProvider) Attach(m *sx12xx.Modem) { p.modem.Store(m) }

func (p *sx12xxStatsProvider) NoteError(err error) {
	p.driverErrors.Add(1)
	p.log.Warn("radio driver error", "error", err)
}

func (p *sx12xxStatsProvider) DriverErrors() uint64 { return p.driverErrors.Load() }

func (p *sx12xxStatsProvider) Transport() string { return "spi" }

func (p *sx12xxStatsProvider) RadioConfig() RadioInfo { return p.radio }

// Stats reports the modem's noise floor and our uptime; battery and MCU temperature stay absent because a Pi has neither sensor.
// CachedStats is Stats here: SPI reads chip registers, so there is nothing to wait for or cache.
func (p *sx12xxStatsProvider) CachedStats() DeviceStats { return p.Stats(context.Background()) }

func (p *sx12xxStatsProvider) Stats(context.Context) DeviceStats {
	ds := DeviceStats{UptimeSecs: uint32(time.Since(p.startTime).Seconds())}
	if m := p.modem.Load(); m != nil {
		if nf, ok := m.NoiseFloor(); ok {
			ds.NoiseFloor = int16(nf)
		}
	}
	return ds
}

// LinkStats leaves the KISS-only fields nil: those events cannot occur on the SPI path, since the chip hands us a decoded packet with its RSSI and SNR attached.
func (p *sx12xxStatsProvider) LinkStats() LinkStats {
	driver := p.driverErrors.Load()
	m := p.modem.Load()
	if m == nil {
		// The error handler is wired before Attach, so a radio that never came up still reports why.
		return LinkStats{DriverErrors: &driver}
	}
	return modemLinkStats(radioLinkStats(m.Stats()), driver, m.RecvRecoveries(), m.HandlerSlow())
}

// radioLinkStats maps the driver's counters onto the KISS-shaped fields; CRC errors keep their own field because a noisy channel is not a driver fault.
// modemLinkStats folds in the counters only the Modem holds; HandlerSlow counts only because setupSPI sets a threshold.
func modemLinkStats(ls LinkStats, driverErrors, recoveries, handlerSlow uint64) LinkStats {
	ls.DriverErrors, ls.RecvRecoveries = &driverErrors, &recoveries
	ls.HandlerSlow = handlerSlow
	return ls
}

func radioLinkStats(s sx12xx.RadioStats) LinkStats {
	crc, recv, sent := s.PacketsCRCErrors, s.PacketsRecv, s.PacketsSent
	recvErrs := s.PacketsRecvErrors
	return LinkStats{
		InboundDroppedNew: s.PacketsDropped,
		CRCErrors:         &crc,
		PacketsRecv:       &recv,
		PacketsSent:       &sent,
		RecvErrors:        &recvErrs,
	}
}

// EstAirtimeMs and PacketScore mirror the firmware's getEstAirtimeFor and packetScore, and fill the packet log's time= and score= fields.
func (p *sx12xxStatsProvider) EstAirtimeMs(packetLen int) uint32 {
	m := p.modem.Load()
	if m == nil {
		return 0
	}
	return m.AirtimeEstimator()(packetLen)
}

func (p *sx12xxStatsProvider) PacketScore(snrDB float64, packetLen int) float64 {
	m := p.modem.Load()
	if m == nil {
		return 0
	}
	return m.PacketScore(snrDB, packetLen)
}
