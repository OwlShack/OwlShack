package mqtt

import (
	"encoding/json"
	"testing"

	"github.com/OwlShack/OwlShack/internal/modem"
	meshcore "github.com/meshcore-go/meshcore-go"
)

// Busy and queue drops mean opposite things to an operator, so a cross-wired mapping inverts the diagnosis silently.
func TestFormatStatus_CountersMapDistinctly(t *testing.T) {
	tx := TxCounts{
		Sent: 1, QueueLen: 2, BusyRequeued: 3,
		BusyDropped: 4, QueueRejected: 5, Failed: 6,
	}
	u := func(v uint64) *uint64 { return &v }
	link := modem.LinkStats{
		InboundDroppedNew: 8, HandlerSlow: 11, HwDecodeErrors: u(12),
		InboundDroppedOldest: u(7), RxMetaTimeouts: u(9), RxMetaMisattributed: u(10),
		HwErrors: u(13), TxOutcomeLost: u(14), RecvErrors: u(15),
	}

	raw, err := formatStatus("online", "n", "id", modem.RadioInfo{}, modem.DeviceStats{},
		PacketCounts{}, tx, link, ObserverCounts{}, 99)
	if err != nil {
		t.Fatalf("formatStatus: %v", err)
	}
	var got struct {
		Stats map[string]any `json:"stats"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// packets_received matched no consumer: CoreScope's status ingest wants packets_recv.
	if _, ok := got.Stats["packets_received"]; ok {
		t.Error(`"packets_received" matched no consumer; the key CoreScope reads is packets_recv`)
	}
	// Both vocabularies ship: recv/sent for the firmware, packets_recv/packets_sent for CoreScope's status ingest.
	for _, want := range []string{"recv", "packets_recv", "sent", "packets_sent"} {
		if _, ok := got.Stats[want]; !ok {
			t.Errorf("%q missing from published stats", want)
		}
	}
	if got.Stats["recv"] != got.Stats["packets_recv"] || got.Stats["sent"] != got.Stats["packets_sent"] {
		t.Error("the aliased counters disagree; they must carry the same value")
	}

	for field, want := range map[string]float64{
		"sent":                  1,
		"queue_len":             2,
		"tx_requeued":           3,
		"tx_dropped_busy":       4,
		"tx_dropped_queue":      5,
		"tx_failed":             6,
		"rx_dropped":            15, // 7 + 8
		"rx_meta_timeouts":      9,
		"rx_meta_misattributed": 10,
		"handler_slow":          11,
		"hw_decode_errors":      12,
		"hw_errors":             13,
		"tx_outcome_lost":       14,
		// Distinct values, so a re-crossed mapping shows up as the wrong number rather than passing.
		"packet_parse_errors": 99,
		"recv_errors":         15,
	} {
		v, ok := got.Stats[field]
		if !ok {
			t.Errorf("%s missing from published stats", field)
			continue
		}
		if v != want {
			t.Errorf("%s = %v, want %v", field, v, want)
		}
	}
}

// meshcoretomqtt scrapes the firmware log line with SNR=(-?\d+) and republishes the capture, and C's (int) cast truncates toward zero.
func TestFormatPacket_SNRIsIntegerDBTruncated(t *testing.T) {
	for _, tc := range []struct {
		wire int8
		want string
	}{
		{19, "4"},   // 4.75 dB
		{-19, "-4"}, // -4.75 dB
		{0, "0"},
		{-128, "-32"},
	} {
		pkt := &meshcore.Packet{SNR: meshcore.SNRFromWire(tc.wire), RSSI: -93, HasSignalInfo: true}
		b, err := formatPacket(pkt, []byte{0x00, 0x01}, "n", "AA", "rx", nil)
		if err != nil {
			t.Fatalf("formatPacket: %v", err)
		}
		var got packetMessage
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.SNR != tc.want {
			t.Errorf("wire %d: SNR = %q, want %q", tc.wire, got.SNR, tc.want)
		}
	}
}

// Board readings live inside "stats" under the firmware's key names; a missing MCU sensor omits the key rather than publishing a reading it never took.
func TestFormatStatus_BoardReadings(t *testing.T) {
	read := func(ds modem.DeviceStats) map[string]any {
		raw, err := formatStatus("online", "n", "id", modem.RadioInfo{}, ds,
			PacketCounts{}, TxCounts{}, modem.LinkStats{}, ObserverCounts{LastSNR: -4.75, LastRSSI: -93}, 0)
		if err != nil {
			t.Fatalf("formatStatus: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return got
	}

	msg := read(modem.DeviceStats{BatteryMV: 3900, HaveBattery: true, NoiseFloor: -120})
	if _, ok := msg["battery_percent"]; ok {
		t.Error("battery_percent is not in the schema; battery_mv inside stats is")
	}
	if _, ok := msg["noise_floor"]; ok {
		t.Error("noise_floor belongs inside stats, not at the top level")
	}
	stats, _ := msg["stats"].(map[string]any)
	for field, want := range map[string]float64{
		"battery_mv": 3900, "noise_floor": -120, "last_snr": -4.75, "last_rssi": -93,
	} {
		if got, ok := stats[field]; !ok || got != want {
			t.Errorf("stats[%q] = %v (present %v), want %v", field, got, ok, want)
		}
	}
	if _, ok := stats["mcu_temp_c"]; ok {
		t.Error("mcu_temp_c must be omitted when the board reports no sensor")
	}

	stats, _ = read(modem.DeviceStats{MCUTempC: 31.5, HaveMCUTemp: true})["stats"].(map[string]any)
	if got := stats["mcu_temp_c"]; got != 31.5 {
		t.Errorf("mcu_temp_c = %v, want 31.5", got)
	}

	// 0 mV is inside the measurement's own range, so it plots as a flat cell.
	stats, _ = read(modem.DeviceStats{NoiseFloor: -120})["stats"].(map[string]any)
	if _, ok := stats["battery_mv"]; ok {
		t.Errorf("battery_mv = %v, must be omitted when there is no battery to measure", stats["battery_mv"])
	}
}

// SPI counters must stay absent on a KISS modem rather than publishing a 0 it never measured.
func TestFormatStatus_SPICountersAreOmittedUnlessMeasured(t *testing.T) {
	read := func(link modem.LinkStats) map[string]any {
		raw, err := formatStatus("online", "n", "id", modem.RadioInfo{}, modem.DeviceStats{},
			PacketCounts{}, TxCounts{}, link, ObserverCounts{}, 0)
		if err != nil {
			t.Fatalf("formatStatus: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		stats, _ := got["stats"].(map[string]any)
		return stats
	}

	stats := read(modem.LinkStats{})
	for _, field := range []string{"crc_errors", "driver_errors", "recv_recoveries"} {
		if _, ok := stats[field]; ok {
			t.Errorf("%q = %v on a KISS modem, which counts none of them", field, stats[field])
		}
	}

	// Distinct values, so a cross-wired field shows up as the wrong number.
	crc, driver, recoveries := uint64(7), uint64(11), uint64(2)
	stats = read(modem.LinkStats{CRCErrors: &crc, DriverErrors: &driver, RecvRecoveries: &recoveries})
	for field, want := range map[string]float64{
		"crc_errors": 7, "driver_errors": 11, "recv_recoveries": 2,
	} {
		if got, ok := stats[field]; !ok || got != want {
			t.Errorf("stats[%q] = %v (present %v), want %v", field, got, ok, want)
		}
	}

	// A measured zero still publishes: the point is not hiding zeroes.
	zero := uint64(0)
	stats = read(modem.LinkStats{DriverErrors: &zero})
	if got, ok := stats["driver_errors"]; !ok || got != float64(0) {
		t.Errorf("driver_errors = %v (present %v), want a published 0", got, ok)
	}
}

// The KISS-only counters went to pointers internally; this schema is shared with meshcore-bot and
// CoreScope, so a nil must still publish 0 there rather than silently dropping the key.
func TestFormatStatus_KISSCountersStayOnTheWireWhenUnmeasured(t *testing.T) {
	raw, err := formatStatus("online", "n", "id", modem.RadioInfo{}, modem.DeviceStats{},
		PacketCounts{}, TxCounts{}, modem.LinkStats{}, ObserverCounts{}, 0)
	if err != nil {
		t.Fatalf("formatStatus: %v", err)
	}
	var got struct {
		Stats map[string]any `json:"stats"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{
		"rx_dropped", "rx_meta_timeouts", "rx_meta_misattributed", "hw_errors", "tx_outcome_lost",
	} {
		v, ok := got.Stats[field]
		if !ok {
			t.Errorf("%q dropped off the published schema", field)
			continue
		}
		if v != float64(0) {
			t.Errorf("%q = %v, want 0", field, v)
		}
	}
}

// On a TX row these would publish a measured 0 dB for our own transmission.
func TestFormatPacket_TxOmitsRxMeasurements(t *testing.T) {
	pkt := &meshcore.Packet{SNR: 0, RSSI: 0}
	raw, err := formatPacket(pkt, []byte{0x00, 0x01}, "n", "AA", "tx", nil)
	if err != nil {
		t.Fatalf("formatPacket: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"SNR", "RSSI", "score", "duration", "path"} {
		if _, ok := got[field]; ok {
			t.Errorf("%q must be absent on a tx row, got %v", field, got[field])
		}
	}
	if _, ok := got["hash"]; !ok {
		t.Error("hash is computed, not measured, so it stays on a tx row")
	}
}

type fakeStats struct {
	modem.StatsProvider // only the two derived-value methods are called here
	airMs               uint32
	score               float64
}

func (f fakeStats) EstAirtimeMs(int) uint32          { return f.airMs }
func (f fakeStats) PacketScore(float64, int) float64 { return f.score }

// Mirrors the firmware's log line: score scaled by 1000 and truncated, duration the airtime estimate in ms, path its "[src -> dst]" trailer.
func TestFormatPacket_RxDerivedFields(t *testing.T) {
	// A direct-routed TXT_MSG: dest hash 0xAB, source hash 0xCD.
	pkt := &meshcore.Packet{
		Header:        meshcore.PayloadTypeTxtMsg<<2 | meshcore.RouteTypeDirect,
		Payload:       []byte{0xAB, 0xCD, 0x01},
		SNR:           5,
		HasSignalInfo: true,
	}
	raw, err := formatPacket(pkt, make([]byte, 16), "n", "AA", "rx",
		fakeStats{airMs: 123, score: 0.4567})
	if err != nil {
		t.Fatalf("formatPacket: %v", err)
	}
	var got packetMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Score != "456" {
		t.Errorf("score = %q, want %q (0.4567 * 1000, truncated)", got.Score, "456")
	}
	if got.Duration != "123" {
		t.Errorf("duration = %q, want %q", got.Duration, "123")
	}
	if got.Path != "CD -> AB" {
		t.Errorf("path = %q, want %q", got.Path, "CD -> AB")
	}
}

// flood_tx / direct_tx and tx_air_secs derive from the transmitted packet itself, so they cover the whole process.
func TestFormatStatus_TxCountersAndRepeat(t *testing.T) {
	raw, err := formatStatus("online", "n", "id", modem.RadioInfo{}, modem.DeviceStats{},
		PacketCounts{}, TxCounts{}, modem.LinkStats{},
		ObserverCounts{TxMs: 9_500, RxMs: 2_000, FloodTx: 7, DirectTx: 3, Relaying: true}, 0)
	if err != nil {
		t.Fatalf("formatStatus: %v", err)
	}
	var got struct {
		Repeat bool           `json:"repeat"`
		Stats  map[string]any `json:"stats"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// repeat is read at the TOP level by CoreScope, not inside stats.
	if !got.Repeat {
		t.Error("repeat must be published at the top level")
	}
	if _, ok := got.Stats["repeat"]; ok {
		t.Error("repeat belongs at the top level, not in stats")
	}
	for field, want := range map[string]float64{
		"tx_air_secs": 9, // 9500ms truncates to 9s, as the firmware's /1000 does
		"rx_air_secs": 2,
		"flood_tx":    7,
		"direct_tx":   3,
	} {
		if got.Stats[field] != want {
			t.Errorf("stats[%q] = %v, want %v", field, got.Stats[field], want)
		}
	}
}

// Signal metadata that never arrived must publish no measurement rather than 0 dB / 0 dBm; the frame-derived fields stay.
func TestFormatPacket_RxWithoutSignalInfoOmitsMeasurements(t *testing.T) {
	pkt := &meshcore.Packet{
		Header:  meshcore.PayloadTypeTxtMsg<<2 | meshcore.RouteTypeDirect,
		Payload: []byte{0xAB, 0xCD, 0x01},
		// HasSignalInfo deliberately false: the metadata frame was never paired.
	}
	raw, err := formatPacket(pkt, make([]byte, 16), "n", "AA", "rx",
		fakeStats{airMs: 123, score: 0.9})
	if err != nil {
		t.Fatalf("formatPacket: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"SNR", "RSSI", "score"} {
		if v, ok := got[field]; ok {
			t.Errorf("%q must be absent without signal metadata, got %v", field, v)
		}
	}
	for _, field := range []string{"duration", "path", "hash"} {
		if _, ok := got[field]; !ok {
			t.Errorf("%q is derived from the frame and must survive", field)
		}
	}
}

// recv_errors is the firmware's field, published to the same brokers by meshcoretomqtt, so a parse failure must never land in it.
func TestFormatStatus_RecvErrorsIsTheRadioCounterNotTheParseCount(t *testing.T) {
	u := func(v uint64) *uint64 { return &v }
	read := func(link modem.LinkStats, parseErrors uint64) map[string]any {
		raw, err := formatStatus("online", "n", "id", modem.RadioInfo{}, modem.DeviceStats{},
			PacketCounts{}, TxCounts{}, link, ObserverCounts{}, parseErrors)
		if err != nil {
			t.Fatalf("formatStatus: %v", err)
		}
		var got struct {
			Stats map[string]any `json:"stats"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return got.Stats
	}

	// SPI: the radio driver measures it, the parse count is its own number.
	stats := read(modem.LinkStats{RecvErrors: u(4)}, 40)
	if stats["recv_errors"] != float64(4) {
		t.Errorf("recv_errors = %v, want the radio counter 4", stats["recv_errors"])
	}
	if stats["packet_parse_errors"] != float64(40) {
		t.Errorf("packet_parse_errors = %v, want 40", stats["packet_parse_errors"])
	}

	// A modem that does not answer: 0 on the wire, never the parse count leaking across.
	stats = read(modem.LinkStats{}, 40)
	if stats["recv_errors"] != float64(0) {
		t.Errorf("recv_errors = %v unmeasured, want 0 and never the parse count", stats["recv_errors"])
	}
	if stats["packet_parse_errors"] != float64(40) {
		t.Errorf("packet_parse_errors = %v, want 40", stats["packet_parse_errors"])
	}

	// Both keys always ship: they are on a shared schema, so a consumer never sees one vanish.
	for _, k := range []string{"recv_errors", "packet_parse_errors", "hw_decode_errors"} {
		if _, ok := stats[k]; !ok {
			t.Errorf("%q dropped off the published schema", k)
		}
	}
}
