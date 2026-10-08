package modem

import (
	"testing"

	"github.com/OwlShack/meshcore-go/hardware"
	"github.com/OwlShack/meshcore-go/hardware/sx12xx"
)

var _ StatsProvider = (*sx12xxStatsProvider)(nil)

// A Pi has no battery and no MCU sensor, and no firmware to ask, so those readings must stay absent rather than arrive as zeroes.
func TestSx12xxStats_ReportsNoBoardReadingsItCannotMeasure(t *testing.T) {
	p := NewSx12xxStatsProvider(RadioInfo{FreqHz: 917_375_000, BwHz: 62_500, SF: 7, CR: 5, TxPower: 22})

	ds := p.Stats(t.Context())
	if ds.BatteryMV != 0 || ds.HaveBattery {
		t.Errorf("battery = %d mV (have=%v), want absent: there is no battery to read",
			ds.BatteryMV, ds.HaveBattery)
	}
	if ds.HaveMCUTemp {
		t.Error("HaveMCUTemp = true, but there is no MCU sensor on this path")
	}
	if got := p.RadioConfig(); got.FreqHz != 917_375_000 || got.SF != 7 || got.TxPower != 22 {
		t.Errorf("RadioConfig() = %+v, want the modulation it was built with", got)
	}
}

// A CRC error is a noisy channel and a driver error is our own fault, so the two must never be folded together.
func TestSx12xxStats_KeepsCRCAndDriverErrorsSeparate(t *testing.T) {
	// Distinct values per field, so a cross-wired mapping shows up as a wrong number rather than a coincidental match.
	s := sx12xx.RadioStats{
		PacketsRecv: 100, PacketsSent: 20,
		PacketsRecvErrors: 3, PacketsCRCErrors: 7, PacketsDropped: 5,
	}
	ls := radioLinkStats(s)
	if ls.CRCErrors == nil || *ls.CRCErrors != 7 {
		t.Errorf("CRCErrors = %v, want 7 (the driver's CRC count, not another counter)", ls.CRCErrors)
	}
	if ls.InboundDroppedNew != 5 {
		t.Errorf("InboundDroppedNew = %d, want 5 (PacketsDropped)", ls.InboundDroppedNew)
	}
	// PacketsRecvErrors is the firmware's recv_errors, not a SETHARDWARE decode failure.
	if ls.RecvErrors == nil || *ls.RecvErrors != 3 {
		t.Errorf("RecvErrors = %v, want 3 (PacketsRecvErrors)", ls.RecvErrors)
	}
	if ls.HwDecodeErrors != nil {
		t.Errorf("HwDecodeErrors = %v, want nil: SETHARDWARE frames do not exist on SPI", *ls.HwDecodeErrors)
	}
	// Without PacketsRecv a 0 CRC count reads the same on a healthy quiet channel and a deaf radio.
	if ls.PacketsRecv == nil || *ls.PacketsRecv != 100 {
		t.Errorf("PacketsRecv = %v, want 100", ls.PacketsRecv)
	}
	if ls.PacketsSent == nil || *ls.PacketsSent != 20 {
		t.Errorf("PacketsSent = %v, want 20", ls.PacketsSent)
	}
	// Nil, not zero: a 0 here would tell a consumer the SPI path measured these and saw none.
	if ls.RxMetaTimeouts != nil || ls.RxMetaMisattributed != nil || ls.HwErrors != nil ||
		ls.TxOutcomeLost != nil || ls.InboundDroppedOldest != nil || ls.HwDecodeErrors != nil {
		t.Errorf("a KISS-only field was populated on the SPI path: %+v", ls)
	}
}

// Three modem counters land in three similar-looking fields, so a cross-wire reads as a plausible number.
func TestModemLinkStats_MapsEachCounterToItsOwnField(t *testing.T) {
	ls := modemLinkStats(LinkStats{InboundDroppedNew: 5}, 11, 2, 3)
	if ls.DriverErrors == nil || *ls.DriverErrors != 11 {
		t.Errorf("DriverErrors = %v, want 11", ls.DriverErrors)
	}
	if ls.RecvRecoveries == nil || *ls.RecvRecoveries != 2 {
		t.Errorf("RecvRecoveries = %v, want 2", ls.RecvRecoveries)
	}
	if ls.HandlerSlow != 3 {
		t.Errorf("HandlerSlow = %d, want 3; the driver counts it once setupSPI sets a threshold", ls.HandlerSlow)
	}
	if ls.InboundDroppedNew != 5 {
		t.Errorf("InboundDroppedNew = %d, want the radio counters left intact", ls.InboundDroppedNew)
	}
}

// A nil counter means the backend cannot measure it; 0 would read as "measured, none happened" on every KISS node.
func TestKissLinkStats_LeavesTheSPICountersUnmeasured(t *testing.T) {
	ls := (&kissStatsProvider{modem: &hardware.KissModem{}}).LinkStats()
	if ls.CRCErrors != nil || ls.DriverErrors != nil || ls.RecvRecoveries != nil {
		t.Errorf("a KISS modem reported an SPI counter: crc=%v driver=%v recoveries=%v",
			ls.CRCErrors, ls.DriverErrors, ls.RecvRecoveries)
	}
	if ls.PacketsRecv != nil || ls.PacketsSent != nil {
		t.Errorf("a KISS modem reported a chip packet counter: recv=%v sent=%v", ls.PacketsRecv, ls.PacketsSent)
	}
	// The other direction: these five are real measurements here, so nil would hide a genuine zero.
	for name, got := range map[string]*uint64{
		"InboundDroppedOldest": ls.InboundDroppedOldest,
		"RxMetaTimeouts":       ls.RxMetaTimeouts,
		"RxMetaMisattributed":  ls.RxMetaMisattributed,
		"HwErrors":             ls.HwErrors,
		"TxOutcomeLost":        ls.TxOutcomeLost,
	} {
		if got == nil {
			t.Errorf("%s is nil on a KISS modem, which measures it", name)
		}
	}
}

// The error handler predates Attach, so a radio that never came up still says why.
func TestSx12xxStats_ReportsDriverErrorsBeforeAttach(t *testing.T) {
	p := NewSx12xxStatsProvider(RadioInfo{})
	p.NoteError(errFake{})

	ls := p.LinkStats()
	if ls.DriverErrors == nil || *ls.DriverErrors != 1 {
		t.Errorf("DriverErrors = %v with no modem attached, want 1", ls.DriverErrors)
	}
	if ls.CRCErrors != nil || ls.RecvRecoveries != nil {
		t.Error("a counter that only the modem can supply was reported without one")
	}
}

// Before Attach, and after a driver teardown, there is no modem to dereference.
func TestSx12xxStats_SurvivesWithNoModemAttached(t *testing.T) {
	p := NewSx12xxStatsProvider(RadioInfo{})
	if got := p.EstAirtimeMs(64); got != 0 {
		t.Errorf("EstAirtimeMs = %d, want 0 with no modem attached", got)
	}
	if got := p.PacketScore(-4.75, 64); got != 0 {
		t.Errorf("PacketScore = %v, want 0 with no modem attached", got)
	}
	if ds := p.Stats(t.Context()); ds.HaveNoiseFloor {
		t.Errorf("noise floor %d reported with no modem attached, want none", ds.NoiseFloor)
	}
}

// Driver faults are the only fault signal on this path, so they must be counted, not only logged.
func TestSx12xxStats_CountsDriverErrors(t *testing.T) {
	p := NewSx12xxStatsProvider(RadioInfo{})
	if got := p.DriverErrors(); got != 0 {
		t.Fatalf("DriverErrors = %d on a fresh provider, want 0", got)
	}
	p.NoteError(errFake{})
	p.NoteError(errFake{})
	if got := p.DriverErrors(); got != 2 {
		t.Errorf("DriverErrors = %d after two faults, want 2", got)
	}
}

type errFake struct{}

func (errFake) Error() string { return "spi transaction failed" }
