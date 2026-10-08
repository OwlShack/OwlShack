package app

import (
	"strings"
	"testing"
)

// Without a companion there is nothing to ask the repeaters with, and saying so beats a scan that finds them and then reports nothing.
func TestStartRegionScan_NeedsACompanion(t *testing.T) {
	b := &backend{regionScan: &regionScanner{}}
	if _, err := b.StartRegionScan(); err == nil || !strings.Contains(err.Error(), "companion") {
		t.Errorf("got %v, want an error naming the missing companion", err)
	}
	if st := b.RegionScanState(); st.Repeaters == nil || st.Phase != "" {
		t.Errorf("state before any scan = %+v, want an empty list and no phase", st)
	}
}
