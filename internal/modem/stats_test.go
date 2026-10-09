package modem

import (
	"context"
	"log/slog"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/hardware"
)

// Zero radio params must give 0 rather than a fabricated number the caller would publish.
func TestKissStatsProvider_DerivedValues(t *testing.T) {
	p := &kissStatsProvider{radio: RadioInfo{FreqHz: 917_375_000, BwHz: 62_500, SF: 7, CR: 5}}

	// 16 bytes at SF7/62.5 kHz is tens of ms; the exact figure is the library's.
	if ms := p.EstAirtimeMs(16); ms < 10 || ms > 500 {
		t.Errorf("EstAirtimeMs(16) = %d, want a plausible tens-of-ms figure", ms)
	}
	if score := p.PacketScore(5, 16); score <= 0 || score > 1 {
		t.Errorf("PacketScore(5dB, 16) = %v, want a 0-1 score above 0", score)
	}
	// SF below 7 is out of the score table's range and reports 0.
	if score := p.PacketScore(5, 16); (&kissStatsProvider{}).PacketScore(5, 16) >= score {
		t.Error("an unconfigured radio must score 0, below a configured one")
	}

	if ms := (&kissStatsProvider{}).EstAirtimeMs(16); ms != 0 {
		t.Errorf("EstAirtimeMs with no radio params = %d, want 0", ms)
	}
}

// nil must mean the modem did not answer: a 0 here would read as a radio hearing everything cleanly.
func TestKissLinkStats_FirmwareCountersAbsentUntilPolled(t *testing.T) {
	p := &kissStatsProvider{modem: &hardware.KissModem{}, log: slog.Default()}

	ls := p.LinkStats()
	if ls.RecvErrors != nil || ls.PacketsRecv != nil || ls.PacketsSent != nil {
		t.Errorf("unpolled: RecvErrors=%v PacketsRecv=%v PacketsSent=%v, want all nil",
			ls.RecvErrors, ls.PacketsRecv, ls.PacketsSent)
	}

	p.mu.Lock()
	p.fwCounters = &hardware.FirmwareStats{PacketsRecv: 900, PacketsSent: 12, PacketsErrors: 4}
	p.mu.Unlock()

	ls = p.LinkStats()
	if ls.RecvErrors == nil || *ls.RecvErrors != 4 {
		t.Errorf("RecvErrors = %v, want 4", ls.RecvErrors)
	}
	if ls.PacketsRecv == nil || *ls.PacketsRecv != 900 {
		t.Errorf("PacketsRecv = %v, want 900", ls.PacketsRecv)
	}
	if ls.PacketsSent == nil || *ls.PacketsSent != 12 {
		t.Errorf("PacketsSent = %v, want 12", ls.PacketsSent)
	}
	// KISS measures this one and SPI does not; the reverse of the fields above.
	if ls.HwDecodeErrors == nil {
		t.Error("HwDecodeErrors = nil on KISS, where a SETHARDWARE frame can fail to decode")
	}
}

type frameFeed struct {
	handler func(*hardware.KissFrame)
	dead    chan struct{}
}

func (f *frameFeed) Connect(context.Context) error               { return nil }
func (f *frameFeed) Close() error                                { return nil }
func (f *frameFeed) Send([]byte) error                           { return nil }
func (f *frameFeed) SetFrameHandler(h func(*hardware.KissFrame)) { f.handler = h }
func (f *frameFeed) SetErrorHandler(func(error))                 {}
func (f *frameFeed) Dead() <-chan struct{}                       { return f.dead }

// Firmware without a query still answers it, with HW_ERR_UNKNOWN_CMD; ignoring that reply read a live board as one that never answered.
func TestKissStatsProvider_AnErrorReplyIsAnAnswer(t *testing.T) {
	feed := &frameFeed{dead: make(chan struct{})}
	km := hardware.NewKissModem(feed)
	if err := km.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer km.Close()
	p := NewKissStatsProvider(km, RadioInfo{})

	feed.handler(&hardware.KissFrame{Command: hardware.KISS_CMD_SETHARDWARE, Data: []byte{hardware.HW_RESP_ERROR, hardware.HW_ERR_UNKNOWN_CMD}})
	for deadline := time.Now().Add(2 * time.Second); p.LastReply().IsZero(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("an HW_RESP_ERROR reply did not count as the board answering")
		}
	}
}

// answeringFeed is a KISS board that answers each stats query; noise overrides its noise floor reply.
type answeringFeed struct {
	frameFeed
	noise []byte
	// sensors is the board's LPP answer; oldFirmware refuses the query as a firmware without it does.
	sensors     []byte
	oldFirmware bool
	// sensorsAnswer is how many sensor queries get an answer; zero is all of them.
	sensorsAnswer int
	sensorsAsked  int
}

func (f *answeringFeed) Send(b []byte) error {
	if len(b) < 3 || b[1] != hardware.KISS_CMD_SETHARDWARE {
		return nil
	}
	noise := f.noise
	if noise == nil {
		noise = []byte{0x9c, 0xff} // -100
	}
	if b[2] == hardware.HW_CMD_GET_SENSORS {
		f.sensorsAsked++
		if f.sensorsAnswer > 0 && f.sensorsAsked > f.sensorsAnswer {
			return nil
		}
		data := append([]byte{hardware.HwResp(b[2])}, f.sensors...)
		if f.oldFirmware {
			data = []byte{hardware.HW_RESP_ERROR, hardware.HW_ERR_UNKNOWN_CMD}
		}
		go f.handler(&hardware.KissFrame{Command: hardware.KISS_CMD_SETHARDWARE, Data: data})
		return nil
	}
	reply := map[byte][]byte{
		hardware.HW_CMD_GET_STATS:       make([]byte, 12),
		hardware.HW_CMD_GET_NOISE_FLOOR: noise,
		hardware.HW_CMD_GET_BATTERY:     {0x04, 0x10}, // 4100
		hardware.HW_CMD_GET_MCU_TEMP:    {0xd7, 0x00}, // 21.5
	}[b[2]]
	if reply != nil {
		go f.handler(&hardware.KissFrame{Command: hardware.KISS_CMD_SETHARDWARE, Data: append([]byte{hardware.HwResp(b[2])}, reply...)})
	}
	return nil
}

// A query returns as soon as its answer is read, before the reply handlers run, so the first poll must report what the queries returned.
func TestKissStatsProvider_FirstPollHasItsAnswers(t *testing.T) {
	feed := &answeringFeed{frameFeed: frameFeed{dead: make(chan struct{})}}
	km := hardware.NewKissModem(feed)
	if err := km.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	km.OnHwResponse(hardware.HwResp(hardware.HW_CMD_GET_MCU_TEMP), func(byte, []byte) { <-release })
	defer km.Close()
	defer close(release)
	p := NewKissStatsProvider(km, RadioInfo{})

	ds := p.Stats(t.Context())
	if ds.NoiseFloor != -100 || !ds.HaveNoiseFloor || !ds.HaveBattery || ds.BatteryMV != 4100 || !ds.HaveMCUTemp || ds.MCUTempC != 21.5 {
		t.Errorf("first poll = %+v, want noise -100, battery 4100, MCU 21.5", ds)
	}
}

// The firmware answers a noise floor of 0 until it has sampled, so 0 is no reading yet.
func TestKissStatsProvider_NoiseFloorZeroIsUnmeasured(t *testing.T) {
	feed := &answeringFeed{frameFeed: frameFeed{dead: make(chan struct{})}, noise: []byte{0, 0}}
	km := hardware.NewKissModem(feed)
	if err := km.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer km.Close()
	p := NewKissStatsProvider(km, RadioInfo{})
	if ds := p.Stats(t.Context()); ds.HaveNoiseFloor {
		t.Errorf("noise floor %d reported before the firmware sampled, want none", ds.NoiseFloor)
	}
}

// A late reply goes through the handler, not the poll, and a 0 there is unmeasured too.
func TestKissStatsProvider_LateZeroReplyIsUnmeasured(t *testing.T) {
	p := &kissStatsProvider{}
	p.onNoiseFloor(0, []byte{0x98, 0xff})
	if !p.haveNoise || p.noiseFloor != -104 {
		t.Fatalf("after -104: have %v, floor %d", p.haveNoise, p.noiseFloor)
	}
	p.onNoiseFloor(0, []byte{0, 0})
	if p.haveNoise {
		t.Error("a 0 reply still counts as measured")
	}
}

// The board's own sensors come back decoded, one LPP channel each; a firmware without the query reports none rather than failing the poll.
func TestKissStatsProvider_BoardSensors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		feed        *answeringFeed
		wantSensors bool
		wantCount   int
	}{
		{"a temperature and humidity on channel 2", &answeringFeed{sensors: []byte{2, meshcore.LPPTemperature, 0x00, 0xe1, 2, meshcore.LPPRelativeHumidity, 0x6e}}, true, 2},
		{"no sensors", &answeringFeed{}, true, 0},
		{"a firmware without the query", &answeringFeed{oldFirmware: true}, false, 0},
	} {
		tc.feed.frameFeed = frameFeed{dead: make(chan struct{})}
		km := hardware.NewKissModem(tc.feed)
		if err := km.Connect(t.Context()); err != nil {
			t.Fatal(err)
		}
		ds := NewKissStatsProvider(km, RadioInfo{}).Stats(t.Context())
		km.Close()
		if ds.HaveSensors != tc.wantSensors || len(ds.Sensors) != tc.wantCount {
			t.Errorf("%s: have %v with %d readings, want %v with %d", tc.name, ds.HaveSensors, len(ds.Sensors), tc.wantSensors, tc.wantCount)
		}
		if !ds.HaveBattery {
			t.Errorf("%s: the battery went missing from the poll", tc.name)
		}
		if tc.wantCount == 2 && (ds.Sensors[0].Channel != 2 || ds.Sensors[0].Value != 22.5 || ds.Sensors[1].Value != 55.0) {
			t.Errorf("%s: readings = %+v, want channel 2 at 22.5 °C and 55 %%", tc.name, ds.Sensors)
		}
	}
}

// A board whose sensor query stops answering while the rest of the poll does must not keep its last sensor readings as current.
func TestKissStatsProvider_BoardSensorsGoStaleOnTheirOwn(t *testing.T) {
	feed := &answeringFeed{frameFeed: frameFeed{dead: make(chan struct{})}, sensors: []byte{2, meshcore.LPPTemperature, 0x00, 0xe1}, sensorsAnswer: 1}
	km := hardware.NewKissModem(feed)
	if err := km.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer km.Close()
	p := NewKissStatsProvider(km, RadioInfo{})
	if ds := p.Stats(t.Context()); !ds.HaveSensors {
		t.Fatal("the first poll has no sensors")
	}
	p.mu.Lock()
	p.sensorsAt = p.sensorsAt.Add(-time.Minute)
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	ds := p.Stats(ctx)
	if !ds.HaveBattery {
		t.Fatal("the battery stopped answering too, so this doesn't test the sensors alone")
	}
	if ds.HaveSensors {
		t.Errorf("sensor readings from a minute ago still read as current: %+v", ds.Sensors)
	}
}
