package sensor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
)

const (
	radioBoardKind = "radio-board"
	// boardSensorKind is one sensor wired to the board, which the firmware reports on a channel of its own.
	boardSensorKind = "radio-board-sensor"
	optChannel      = "channel"
)

var (
	// ErrNoRadio is a radio that is not connected right now.
	ErrNoRadio = errors.New("no radio is connected")
	// ErrNoBoard is a radio with no board in front of the chip, as on SPI, so nothing reports a battery or temperature.
	ErrNoBoard = errors.New("this radio has no board to report a battery or temperature")
)

// BoardReadings is what the radio board last reported; At is when it last answered, zero when it has not yet, and SilentSince when its silence began.
type BoardReadings struct {
	Transport   string
	BatteryV    float64
	HaveBattery bool
	MCUTempC    float64
	HaveMCUTemp bool
	At          time.Time
	SilentSince time.Time
	// Sensors is what the board's own sensors read, one LPP channel each; HaveSensors is false until it answers.
	Sensors     []meshcore.LPPReading
	HaveSensors bool
}

// RadioBoardProvider reads the battery and MCU temperature the modem already asks its board for, so it adds no traffic on the link.
type RadioBoardProvider struct {
	// Board is the connected radio's readings, or ErrNoRadio or ErrNoBoard.
	Board func() (BoardReadings, error)
	// MaxAge is how long the modem keeps a board reading without an answer; it asks every 30 s.
	MaxAge time.Duration
}

func (RadioBoardProvider) ID() string    { return "radio" }
func (RadioBoardProvider) Label() string { return "Radio board" }

// Available is false only for a radio that can never report; one that is down now may come back.
func (p RadioBoardProvider) Available(context.Context) (bool, string) {
	if _, err := p.Board(); errors.Is(err, ErrNoBoard) {
		return false, err.Error()
	}
	return true, ""
}

func (p RadioBoardProvider) Kinds() []KindInfo {
	return []KindInfo{{
		Kind: radioBoardKind, Label: "Radio board",
		Description: "Battery voltage and MCU temperature from the radio's board",
		Category:    "Power",
		Metrics:     []Metric{Voltage, Temperature},
	}, {
		Kind: boardSensorKind, Label: "Board sensor",
		Description: "A sensor wired to a KISS modem's board, such as a BME280 beside the radio",
		Category:    "Environment",
		Metrics:     boardMetrics(),
		// What the channel reads now; while it reads nothing, as after a sensor failed to start, anything a board could report.
		ReportsUnder: func(o map[string]string) []Metric {
			ch, err := boardChannel(o)
			if err != nil {
				return nil
			}
			r, err := p.Board()
			if err != nil {
				return nil
			}
			var out []Metric
			for _, rd := range channelReadings(r, ch) {
				out = append(out, rd.Metric)
			}
			return out
		},
		Fields: []Field{{
			Key: optChannel, Label: "Board channel", Required: true, Identifies: true,
			Help: "The channel the board reports this sensor on; it counts from 2 in the order the firmware found its sensors",
		}},
	}}
}

// Validate refuses a board channel the firmware could not report a sensor on.
func (RadioBoardProvider) Validate(spec Spec) error {
	if spec.Kind != boardSensorKind {
		return nil
	}
	_, err := boardChannel(spec.Options)
	return err
}

func boardChannel(o map[string]string) (byte, error) {
	n, err := strconv.Atoi(o[optChannel])
	if err != nil || strconv.Itoa(n) != o[optChannel] || n <= int(ChannelSelf) || n > 255 {
		return 0, fmt.Errorf("board channel must be a number from %d to 255", ChannelSelf+1)
	}
	return byte(n), nil
}

func (p RadioBoardProvider) Discover(context.Context) ([]Candidate, error) {
	r, err := p.Board()
	if err != nil {
		return nil, nil
	}
	out := []Candidate{{
		Kind: radioBoardKind, Label: "Radio board", Detail: transportNames[r.Transport] + " board", Addable: true,
		Options: map[string]string{},
	}}
	if !r.HaveSensors {
		return out, nil
	}
	var channels []byte
	for _, rd := range r.Sensors {
		if !slices.Contains(channels, rd.Channel) {
			channels = append(channels, rd.Channel)
		}
	}
	slices.Sort(channels)
	for _, ch := range channels {
		var names []string
		for _, rd := range channelReadings(r, ch) {
			names = append(names, string(rd.Metric))
		}
		if len(names) == 0 {
			continue
		}
		out = append(out, Candidate{
			Kind: boardSensorKind, Label: "Board sensor", Addable: true,
			Detail:  fmt.Sprintf("channel %d: %s", ch, strings.Join(names, ", ")),
			Options: map[string]string{optChannel: strconv.Itoa(int(ch))},
		})
	}
	return out, nil
}

var transportNames = map[string]string{"kiss": "KISS", "openhop": "openHop"}

// Claim makes the board, and each of its channels, addable once: there is only one radio.
func (RadioBoardProvider) Claim(spec Spec) string {
	if spec.Kind == boardSensorKind {
		return "board channel " + spec.Options[optChannel]
	}
	return "the radio board"
}

func (p RadioBoardProvider) StaleAfter(Spec) (time.Duration, bool) { return p.MaxAge, true }

func (p RadioBoardProvider) Open(spec Spec) (Sensor, error) {
	s := &radioBoard{board: p.Board, maxAge: p.MaxAge, opened: time.Now(), pick: boardReadings,
		none: "the radio board reports neither a battery nor an MCU temperature"}
	if spec.Kind == boardSensorKind {
		ch, err := boardChannel(spec.Options)
		if err != nil {
			return nil, err
		}
		s.pick = func(r BoardReadings) []Reading { return channelReadings(r, ch) }
		s.none = fmt.Sprintf("the radio board reports no sensor on channel %d", ch)
	}
	return s, nil
}

// boardReadings is the board's own battery and MCU temperature.
func boardReadings(r BoardReadings) []Reading {
	var out []Reading
	if r.HaveBattery {
		out = append(out, Reading{Metric: Voltage, Label: "battery", Value: r.BatteryV, Unit: "V"})
	}
	if r.HaveMCUTemp {
		out = append(out, Reading{Metric: Temperature, Label: "MCU", Value: r.MCUTempC, Unit: "°C"})
	}
	return out
}

// channelReadings is what one board channel reads; a type that carries more than one value has no metric of its own and is left out.
func channelReadings(r BoardReadings, ch byte) []Reading {
	if !r.HaveSensors {
		return nil
	}
	var out []Reading
	for _, rd := range r.Sensors {
		v, ok := rd.Value.(float64)
		if rd.Channel != ch || !ok {
			continue
		}
		if m, unit, ok := lppMetric(rd.Type); ok {
			out = append(out, Reading{Metric: m, Value: v, Unit: unit})
		}
	}
	return out
}

// lppMetric names an LPP type as a metric, in the units the I2C drivers report, so a board BME280 reads like a wired one.
func lppMetric(typ byte) (Metric, string, bool) {
	switch typ {
	case meshcore.LPPTemperature:
		return Temperature, "°C", true
	case meshcore.LPPRelativeHumidity:
		return Humidity, "%", true
	case meshcore.LPPBarometricPressure:
		return Pressure, "hPa", true
	case meshcore.LPPVoltage:
		return Voltage, "V", true
	case meshcore.LPPCurrent:
		return Current, "A", true
	case meshcore.LPPPercentage:
		return Percentage, "%", true
	}
	t, ok := lookupLPPType(typ)
	if !ok {
		return "", "", false
	}
	return Metric(strings.ReplaceAll(strings.ToLower(t.Name), " ", "_")), t.Unit, true
}

// boardMetrics is every metric a board channel can read.
func boardMetrics() []Metric {
	var out []Metric
	for _, t := range lppTypes {
		if m, _, ok := lppMetric(t.Code); ok && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

type radioBoard struct {
	board  func() (BoardReadings, error)
	pick   func(BoardReadings) []Reading
	none   string
	maxAge time.Duration
	// opened bounds how long a start may wait on the radio before the wait is itself the fault.
	opened time.Time
	// last and at are the last good sample, which stands through a reconnect until it is as old as the modem keeps one.
	last []Reading
	at   time.Time
}

// waiting is an error the hub shows only once a reading has been had, so a start before the radio is up does not flash as a failure.
type waiting struct{ error }

func (waiting) Is(target error) bool { return target == errNotYet }

func (w waiting) Unwrap() error { return w.error }

func (s *radioBoard) Read(context.Context) ([]Reading, error) {
	r, err := s.board()
	var out []Reading
	if err == nil {
		out = s.pick(r)
	}
	if len(out) > 0 {
		s.last, s.at = out, r.At
		return out, nil
	}
	if s.last != nil && time.Since(s.at) <= s.maxAge {
		return s.last, nil
	}
	switch {
	case errors.Is(err, ErrNoBoard):
		return nil, err // for good, so the card must say so rather than wait
	case err != nil:
		if time.Since(s.opened) <= s.maxAge {
			return nil, waiting{err}
		}
		return nil, err
	case r.At.IsZero():
		from := r.SilentSince
		if from.IsZero() {
			from = s.opened
		}
		if time.Since(from) <= s.maxAge {
			return nil, errNotYet
		}
		return nil, fmt.Errorf("the radio board has not answered in the %s since it connected", time.Since(from).Round(time.Second))
	case time.Since(r.At) > s.maxAge:
		return nil, fmt.Errorf("the radio board has not answered for %s", time.Since(r.At).Round(time.Second))
	}
	return nil, errors.New(s.none)
}

// SampledAt is the board's last answer, which the modem asks for every 30 s, not this read.
func (s *radioBoard) SampledAt() time.Time { return s.at }
