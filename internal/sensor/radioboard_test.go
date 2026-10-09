package sensor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
)

func boardSensor(t *testing.T, r BoardReadings, err error) (RadioBoardProvider, *radioBoard) {
	t.Helper()
	p := RadioBoardProvider{Board: func() (BoardReadings, error) { return r, err }, MaxAge: 45 * time.Second}
	s, openErr := p.Open(Spec{})
	if openErr != nil {
		t.Fatal(openErr)
	}
	return p, s.(*radioBoard)
}

func TestRadioBoard_Read(t *testing.T) {
	t.Parallel()
	now := time.Now()
	answered := now.Add(-10 * time.Second)
	for name, tc := range map[string]struct {
		r       BoardReadings
		err     error
		want    []Metric
		notYet  bool   // the hub hides it until a first reading
		wantErr string // part of the message shown once there has been one
	}{
		"no radio": {err: ErrNoRadio, notYet: true, wantErr: "no radio"},
		// An SPI radio never will, so waiting would sit on the card for good with no reason.
		"no board":         {err: ErrNoBoard, wantErr: "no board"},
		"not answered yet": {r: BoardReadings{Transport: "kiss"}, notYet: true},
		"both":             {r: BoardReadings{BatteryV: 4.1, HaveBattery: true, MCUTempC: 31.5, HaveMCUTemp: true, At: answered}, want: []Metric{Voltage, Temperature}},
		// getBattMilliVolts is 0 on a board that cannot measure one, and 0 V is published as the firmware does.
		"battery unsupported": {r: BoardReadings{HaveBattery: true, MCUTempC: 30, HaveMCUTemp: true, At: answered}, want: []Metric{Voltage, Temperature}},
		"neither":             {r: BoardReadings{At: answered}, wantErr: "neither"},
		"starting":            {r: BoardReadings{SilentSince: now.Add(-10 * time.Second)}, notYet: true},
		// Its TCP link reconnects every 60 s, so only the kept silence start shows it never answered.
		"hung from the start": {r: BoardReadings{SilentSince: now.Add(-2 * time.Minute)}, wantErr: "since it connected"},
		"gone quiet":          {r: BoardReadings{At: now.Add(-2 * time.Minute)}, wantErr: "not answered for"},
	} {
		_, s := boardSensor(t, tc.r, tc.err)
		got, err := s.Read(context.Background())
		if errors.Is(err, errNotYet) != tc.notYet {
			t.Errorf("%s: waiting = %v, want %v (err %v)", name, errors.Is(err, errNotYet), tc.notYet, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: err %v, want it to say %q", name, err, tc.wantErr)
		}
		var metrics []Metric
		for _, r := range got {
			metrics = append(metrics, r.Metric)
		}
		if strings.Join(toStrings(metrics), ",") != strings.Join(toStrings(tc.want), ",") {
			t.Errorf("%s: read %v, want %v", name, metrics, tc.want)
		}
		if len(tc.want) > 0 && !s.SampledAt().Equal(answered) {
			t.Errorf("%s: SampledAt %v, want the board's answer at %v, since the modem only asks every 30 s", name, s.SampledAt(), answered)
		}
	}
}

func toStrings(ms []Metric) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(m)
	}
	return out
}

// SPI can never report, so the picker says why; a radio that is only down may come back and stays offered.
func TestRadioBoard_AvailableAndDiscover(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err       error
		available bool
		found     int
	}{
		"kiss up":  {available: true, found: 1},
		"no radio": {err: ErrNoRadio, available: true},
		"spi":      {err: ErrNoBoard},
	} {
		p, _ := boardSensor(t, BoardReadings{Transport: "kiss"}, tc.err)
		if ok, _ := p.Available(context.Background()); ok != tc.available {
			t.Errorf("%s: available %v, want %v", name, ok, tc.available)
		}
		if got, _ := p.Discover(context.Background()); len(got) != tc.found {
			t.Errorf("%s: found %d, want %d", name, len(got), tc.found)
		}
	}
}

// A hung board's TCP link reconnects every 60 s; each new link has no answer yet, which must not read as a first start.
func TestRadioBoard_ThroughAReconnect(t *testing.T) {
	t.Parallel()
	answered := time.Now().Add(-5 * time.Second)
	cur := BoardReadings{BatteryV: 4.1, HaveBattery: true, At: answered}
	var curErr error
	p := RadioBoardProvider{Board: func() (BoardReadings, error) { return cur, curErr }, MaxAge: 45 * time.Second}
	opened, _ := p.Open(Spec{})
	s := opened.(*radioBoard)
	if got, err := s.Read(context.Background()); err != nil || len(got) != 1 {
		t.Fatalf("first read: %v, %v", got, err)
	}

	curErr = ErrNoRadio // between links
	if got, err := s.Read(context.Background()); err != nil || len(got) != 1 || !s.SampledAt().Equal(answered) {
		t.Fatalf("between links: %v, %v; want the last sample with its own time", got, err)
	}

	curErr = nil
	cur = BoardReadings{At: answered.Add(-time.Minute)} // the new link has not answered; the kept answer is old
	s.at = answered.Add(-time.Minute)
	if _, err := s.Read(context.Background()); err == nil || errors.Is(err, errNotYet) || !strings.Contains(err.Error(), "not answered for") {
		t.Fatalf("hung past the limit: %v, want it to say how long the board has been silent", err)
	}
}

// A radio that never comes up must not wait on the card for good, and the wrapper must keep the reason findable.
func TestRadioBoard_NoRadioStopsWaiting(t *testing.T) {
	t.Parallel()
	_, s := boardSensor(t, BoardReadings{}, ErrNoRadio)
	_, err := s.Read(context.Background())
	if !errors.Is(err, errNotYet) || !errors.Is(err, ErrNoRadio) {
		t.Fatalf("just opened: %v, want a wait that still names ErrNoRadio", err)
	}
	s.opened = time.Now().Add(-time.Minute)
	if _, err := s.Read(context.Background()); errors.Is(err, errNotYet) || !errors.Is(err, ErrNoRadio) {
		t.Fatalf("a minute on: %v, want ErrNoRadio shown, not waited on", err)
	}
}

// A board with a BME280 on channel 2 and a light sensor and GPS on channel 3, as the KISS firmware numbers them.
func boardWithSensors() BoardReadings {
	return BoardReadings{Transport: "kiss", At: time.Now().Add(-5 * time.Second), HaveSensors: true, Sensors: []meshcore.LPPReading{
		{Channel: 2, Type: meshcore.LPPTemperature, Value: 22.5},
		{Channel: 2, Type: meshcore.LPPRelativeHumidity, Value: 55.0},
		{Channel: 2, Type: meshcore.LPPBarometricPressure, Value: 1013.2},
		{Channel: 3, Type: meshcore.LPPLuminosity, Value: 120.0},
		{Channel: 3, Type: meshcore.LPPGPS, Value: meshcore.LPPGPSValue{Latitude: -36.8}},
	}}
}

func boardChannelSpec(ch string) Spec {
	return Spec{Provider: "radio", Kind: boardSensorKind, Name: "board " + ch, Options: map[string]string{optChannel: ch}}
}

// Each board channel reads like a wired sensor, in the I2C drivers' units; a value with no single metric, such as a position, is left out.
func TestBoardSensor_Read(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		r       BoardReadings
		channel string
		want    []Reading
		wantErr string
	}{
		"a BME280": {r: boardWithSensors(), channel: "2", want: []Reading{
			{Metric: Temperature, Value: 22.5, Unit: "°C"}, {Metric: Humidity, Value: 55, Unit: "%"}, {Metric: Pressure, Value: 1013.2, Unit: "hPa"}}},
		"a light sensor beside a GPS":        {r: boardWithSensors(), channel: "3", want: []Reading{{Metric: "luminosity", Value: 120, Unit: "lux"}}},
		"a channel the board doesn't report": {r: boardWithSensors(), channel: "4", wantErr: "no sensor on channel 4"},
		// A firmware without the query answers the rest, so the card says what is missing rather than waiting.
		"a firmware without the query": {r: BoardReadings{Transport: "kiss", At: time.Now(), HaveBattery: true, BatteryV: 4.1}, channel: "2", wantErr: "no sensor on channel 2"},
	} {
		p := RadioBoardProvider{Board: func() (BoardReadings, error) { return tc.r, nil }, MaxAge: 45 * time.Second}
		s, err := p.Open(boardChannelSpec(tc.channel))
		if err != nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		got, err := s.Read(context.Background())
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err %v, want it to say %q", name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: read %+v, %v; want %+v", name, got, err, tc.want)
		}
	}
}

// The picker offers each channel the board reports, after the board itself, and only while the board answers the query.
func TestBoardSensor_Discover(t *testing.T) {
	t.Parallel()
	p := RadioBoardProvider{Board: func() (BoardReadings, error) { return boardWithSensors(), nil }}
	got, _ := p.Discover(context.Background())
	var details []string
	for _, c := range got {
		details = append(details, c.Kind+" "+c.Options[optChannel]+" "+c.Detail)
	}
	want := []string{
		"radio-board  KISS board",
		"radio-board-sensor 2 channel 2: temperature, humidity, pressure",
		"radio-board-sensor 3 channel 3: luminosity",
	}
	if !slices.Equal(details, want) {
		t.Errorf("candidates = %q, want %q", details, want)
	}

	quiet := RadioBoardProvider{Board: func() (BoardReadings, error) { return BoardReadings{Transport: "kiss"}, nil }}
	if got, _ := quiet.Discover(context.Background()); len(got) != 1 {
		t.Errorf("a board that hasn't reported sensors offered %d candidates, want only the board", len(got))
	}
}

// The server refuses a channel the firmware could not report a sensor on, and two sensors on one channel.
func TestBoardSensor_Prepare(t *testing.T) {
	t.Parallel()
	p := RadioBoardProvider{Board: func() (BoardReadings, error) { return boardWithSensors(), nil }}
	h := NewHub(testLog(), p)
	for _, ch := range []string{"", "1", "0", "256", "two", "2.5", " 2 ", "02", "+2"} {
		if _, err := h.Prepare(boardChannelSpec(ch)); err == nil {
			t.Errorf("channel %q was accepted", ch)
		}
	}
	spec := boardChannelSpec("2")
	spec.ID = 1
	prepared, err := h.Prepare(spec)
	if err != nil {
		t.Fatalf("channel 2: %v", err)
	}
	h.Set([]Spec{prepared})
	if _, err := h.Prepare(boardChannelSpec("2")); err == nil {
		t.Error("a second sensor on board channel 2 was accepted")
	}
	if _, err := h.Prepare(boardChannelSpec("3")); err != nil {
		t.Errorf("board channel 3 beside 2: %v", err)
	}
}

// The channel map may only pick what the channel reads now; while the board is silent it can't know, so it takes any board metric.
func TestBoardSensor_Reports(t *testing.T) {
	t.Parallel()
	answering := NewHub(testLog(), RadioBoardProvider{Board: func() (BoardReadings, error) { return boardWithSensors(), nil }})
	got := answering.Reports(boardChannelSpec("3"))
	if len(got) != 1 || !got["luminosity"] {
		t.Errorf("channel 3 reports %v, want only luminosity", got)
	}
	silent := NewHub(testLog(), RadioBoardProvider{Board: func() (BoardReadings, error) { return BoardReadings{}, ErrNoRadio }})
	if got := silent.Reports(boardChannelSpec("3")); !got[Humidity] || !got["luminosity"] {
		t.Errorf("with the radio down, channel 3 reports %v, want every board metric", got)
	}
	// A sensor that didn't start after a reboot leaves its channel empty for a while; its map rows must not block every other edit.
	if got := answering.Reports(boardChannelSpec("4")); !got[Temperature] || !got["luminosity"] {
		t.Errorf("an empty channel reports %v, want every board metric", got)
	}
}
