// Package telemetry decodes MeshCore CayenneLPP payloads into named readings and firmware-independent metric keys.
package telemetry

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	meshcore "github.com/OwlShack/meshcore-go"
)

// ChannelSelf is the firmware's TELEM_CHANNEL_SELF; external sensors land on channels >= 2 on firmware 1.16+.
const ChannelSelf = 1

// Reading is one decoded LPP reading; Self marks one the node reports about itself rather than an attached sensor.
type Reading struct {
	Channel int    `json:"channel"`
	Type    int    `json:"type"`
	Name    string `json:"name"`
	Unit    string `json:"unit,omitempty"`
	Value   any    `json:"value"`
	Self    bool   `json:"self,omitempty"`
}

// Telemetry is a decoded telemetry payload: its readings plus the raw hex.
type Telemetry struct {
	Readings []Reading `json:"readings"`
	Raw      string    `json:"raw"`
}

// Parse decodes a raw CayenneLPP payload into named readings; an empty payload yields an empty (non-nil) Telemetry.
func Parse(data []byte) (*Telemetry, error) {
	out := &Telemetry{
		Raw:      hex.EncodeToString(data),
		Readings: []Reading{},
	}
	if len(data) == 0 {
		return out, nil
	}
	readings, err := meshcore.LPPDecode(data)
	if err != nil {
		return out, fmt.Errorf("decoding telemetry: %w", err)
	}
	// Firmware <=1.15 lumps external sensors onto the self channel, so only the first reading of each self-type there is the node's own.
	selfClaimed := map[byte]bool{}
	for _, r := range readings {
		self := r.Channel == ChannelSelf && isSelfType(r.Type) && !selfClaimed[r.Type]
		if self {
			selfClaimed[r.Type] = true
		}
		name, unit := lppTypeMeta(self, r.Type)
		out.Readings = append(out.Readings, Reading{
			Channel: int(r.Channel),
			Type:    int(r.Type),
			Name:    name,
			Unit:    unit,
			Value:   r.Value,
			Self:    self,
		})
	}
	// Group by channel; stable to keep firmware emit order within a channel.
	sort.SliceStable(out.Readings, func(i, j int) bool {
		return out.Readings[i].Channel < out.Readings[j].Channel
	})
	return out, nil
}

// Metric is one flattened scalar time-series value.
type Metric struct {
	Key     string
	Channel int
	Value   float64
}

// Metrics flattens readings into scalar series; an external sensor's channel is its stable handle, so it is keyed "temperature_ch2".
func (t *Telemetry) Metrics() []Metric {
	var out []Metric
	used := make(map[string]bool, len(t.Readings))
	for _, r := range t.Readings {
		base := normalizeMetric(r.Name)
		if base == "" {
			base = fmt.Sprintf("type_%d", r.Type)
		}
		for _, kv := range flattenValue(r.Value) {
			key := base + kv.suffix
			if !r.Self {
				key = fmt.Sprintf("%s_ch%d%s", base, r.Channel, kv.suffix)
			}
			key = disambiguate(used, key)
			used[key] = true
			out = append(out, Metric{Key: key, Channel: r.Channel, Value: kv.value})
		}
	}
	return out
}

// disambiguate guards the rare case of two readings sharing both type and channel.
func disambiguate(used map[string]bool, key string) string {
	if !used[key] {
		return key
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s_%d", key, i)
		if !used[cand] {
			return cand
		}
	}
}

type scalarValue struct {
	suffix string
	value  float64
}

func flattenValue(v any) []scalarValue {
	switch x := v.(type) {
	case float64:
		return []scalarValue{{"", x}}
	case float32:
		return []scalarValue{{"", float64(x)}}
	case meshcore.LPPGPSValue:
		return []scalarValue{{"_lat", x.Latitude}, {"_lon", x.Longitude}, {"_alt", x.Altitude}}
	case meshcore.LPPAccelValue:
		return []scalarValue{{"_x", x.X}, {"_y", x.Y}, {"_z", x.Z}}
	case meshcore.LPPGyroValue:
		return []scalarValue{{"_x", x.X}, {"_y", x.Y}, {"_z", x.Z}}
	default:
		return nil
	}
}

func normalizeMetric(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '_', r == '-', r == '/':
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// isSelfType reports whether an LPP type is one a node reports about itself (MCU temperature, battery voltage, GPS).
func isSelfType(typ byte) bool {
	switch typ {
	case meshcore.LPPTemperature, meshcore.LPPVoltage, meshcore.LPPGPS:
		return true
	}
	return false
}

// lppTypeMeta maps an LPP type to a display name + unit, naming self readings for the node's own hardware.
func lppTypeMeta(self bool, typ byte) (name, unit string) {
	if self {
		switch typ {
		case meshcore.LPPTemperature:
			return "MCU temperature", "°C"
		case meshcore.LPPVoltage:
			return "Battery", "V"
		case meshcore.LPPGPS:
			return "Location", ""
		}
	}
	switch typ {
	case meshcore.LPPDigitalInput:
		return "Digital input", ""
	case meshcore.LPPDigitalOutput:
		return "Digital output", ""
	case meshcore.LPPAnalogInput:
		return "Analog input", "V"
	case meshcore.LPPAnalogOutput:
		return "Analog output", "V"
	case meshcore.LPPGenericSensor:
		return "Generic sensor", ""
	case meshcore.LPPLuminosity:
		return "Luminosity", "lux"
	case meshcore.LPPPresence:
		return "Presence", ""
	case meshcore.LPPTemperature:
		return "Temperature", "°C"
	case meshcore.LPPRelativeHumidity:
		return "Humidity", "%RH"
	case meshcore.LPPAccelerometer:
		return "Accelerometer", "G"
	case meshcore.LPPBarometricPressure:
		return "Pressure", "hPa"
	case meshcore.LPPVoltage:
		return "Voltage", "V"
	case meshcore.LPPCurrent:
		return "Current", "A"
	case meshcore.LPPFrequency:
		return "Frequency", "Hz"
	case meshcore.LPPPercentage:
		return "Percentage", "%"
	case meshcore.LPPAltitude:
		return "Altitude", "m"
	case meshcore.LPPConcentration:
		return "Concentration", "ppm"
	case meshcore.LPPPower:
		return "Power", "W"
	case meshcore.LPPDistance:
		return "Distance", "m"
	case meshcore.LPPEnergy:
		return "Energy", "kWh"
	case meshcore.LPPDirection:
		return "Direction", "°"
	case meshcore.LPPUnixTime:
		return "Unix time", ""
	case meshcore.LPPGyrometer:
		return "Gyrometer", "°/s"
	case meshcore.LPPColour:
		return "Colour", "RGB"
	case meshcore.LPPGPS:
		return "GPS", ""
	case meshcore.LPPSwitch:
		return "Switch", ""
	}
	return fmt.Sprintf("Type %d", typ), ""
}
