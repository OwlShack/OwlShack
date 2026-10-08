package repeater

import (
	"crypto/rand"
	"encoding/binary"
	"testing"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"
)

type stubRadio struct{}

func (stubRadio) SendData([]byte) error                               { return nil }
func (stubRadio) SetDataHandler(func(*meshcore.Packet))               {}
func (stubRadio) SetRawDataHandler(func([]byte, float32, int8, bool)) {}
func (stubRadio) AddOutboundHandler(func([]byte))                     {}
func (stubRadio) Close() error                                        { return nil }

// An unmeasured noise floor is absent from our stats, while the STATUS reply carries 0 as firmware does.
func TestNoiseFloor_UnmeasuredIsAbsent(t *testing.T) {
	id, err := meshcore.GenerateLocalIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n := node.New(id, stubRadio{})
	t.Cleanup(n.Stop)
	r := &Repeater{node: n}

	r.cacheDeviceStats(DeviceStats{NoiseFloor: -104, BatteryMV: 4100, HaveBattery: true})
	if nf := r.Stats().NoiseFloor; nf != nil {
		t.Errorf("stats noise floor = %d before a measurement, want none", *nf)
	}
	if got := int16(binary.LittleEndian.Uint16(r.statusBody()[4:6])); got != 0 {
		t.Errorf("STATUS noise floor = %d before a measurement, want 0", got)
	}

	r.cacheDeviceStats(DeviceStats{NoiseFloor: -104, HaveNoiseFloor: true})
	if nf := r.Stats().NoiseFloor; nf == nil || *nf != -104 {
		t.Errorf("stats noise floor = %v, want -104", nf)
	}
	if got := int16(binary.LittleEndian.Uint16(r.statusBody()[4:6])); got != -104 {
		t.Errorf("STATUS noise floor = %d, want -104", got)
	}

}

// A reading the provider has stopped vouching for is withdrawn, not kept on the air.
func TestDeviceStats_StaleReadingsAreWithdrawn(t *testing.T) {
	id, err := meshcore.GenerateLocalIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	n := node.New(id, stubRadio{})
	t.Cleanup(n.Stop)
	r := &Repeater{node: n}

	r.cacheDeviceStats(DeviceStats{BatteryMV: 4100, HaveBattery: true, MCUTempC: 31.5, HaveMCUTemp: true})
	r.cacheDeviceStats(DeviceStats{BatteryMV: 4100, MCUTempC: 31.5})
	if got := binary.LittleEndian.Uint16(r.statusBody()[0:2]); got != 0 {
		t.Errorf("STATUS battery = %d mV after it went stale, want 0", got)
	}
	if got := r.selfReadings().TempC; got != nil {
		t.Errorf("MCU temperature = %v after it went stale, want none", *got)
	}
}
