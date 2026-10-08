package config

import (
	"math"
	"os"
	"testing"

	"github.com/OwlShack/meshcore-go/node"
)

// CommonCLI.cpp `set dutycycle` does airtime_factor = (100/dc) - 1, so a SMALLER factor is a HIGHER duty cycle.
func TestAirtimeFactorMatchesFirmwareConversion(t *testing.T) {
	for _, tc := range []struct {
		pct        float64
		wantFactor float64
	}{
		{100, 0}, // unlimited
		{50, 1},  // the firmware default for every role
		{10, 9},  // the old legacy-prefs clamp ceiling
		{1, 99},  // EU868; factor 0.01 would be 99% — the inverted-intuition bug
		{25, 3},
		{0.1, 999}, // EU868 sub-bands; firmware reaches this only via `set af`
	} {
		pct := tc.pct
		c := &Config{DutyCycle: &pct}
		if got := c.AirtimeFactorOr(); math.Abs(got-tc.wantFactor) > 1e-9 {
			t.Errorf("%.0f%% -> factor %v, want %v", tc.pct, got, tc.wantFactor)
		}
		// And it must round-trip, since the UI shows the percentage back.
		if got := c.DutyCyclePercentOr(); math.Abs(got-tc.pct) > 1e-9 {
			t.Errorf("factor for %.0f%% reports back as %v%%", tc.pct, got)
		}
	}
}

// Unset must mean the library default, i.e. firmware parity, not "unlimited".
func TestAirtimeFactorDefaultsToFirmwareParity(t *testing.T) {
	c := &Config{}
	if got := c.AirtimeFactorOr(); got != node.DefaultAirtimeFactor {
		t.Errorf("unset factor = %v, want %v", got, node.DefaultAirtimeFactor)
	}
	if got := c.DutyCyclePercentOr(); math.Abs(got-50) > 1e-9 {
		t.Errorf("unset duty cycle = %v%%, want 50%%", got)
	}
}

func TestDutyCycleValidation(t *testing.T) {
	for _, tc := range []struct {
		pct float64
		ok  bool
	}{
		{1, true}, {50, true}, {100, true},
		{0.1, true}, // sub-1% is allowed here even though firmware's set dutycycle isn't
		{0, false}, {101, false}, {-5, false},
	} {
		pct := tc.pct
		cfg := validConfig(t)
		cfg.DutyCycle = &pct
		err := cfg.Validate()
		if tc.ok && err != nil {
			t.Errorf("%v%% rejected: %v", tc.pct, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%v%% accepted, want rejected", tc.pct)
		}
	}
}

// The airtime factor lives on the RadioMux, rebuilt only on reconnect: a change must count as a modem change, but only a change in the EFFECTIVE budget.
func TestDutyCycleChangeForcesModemReconnect(t *testing.T) {
	pct := func(v float64) *float64 { return &v }

	for _, tc := range []struct {
		name     string
		from, to *float64
		want     bool
	}{
		{"50% -> 1%", pct(50), pct(1), true},
		{"1% -> unset", pct(1), nil, true},
		{"unset -> 10%", nil, pct(10), true},
		{"1% -> 0.1%", pct(1), pct(0.1), true},
		{"unchanged", pct(50), pct(50), false},
		// Unset resolves to 50%, so these are the same budget.
		{"unset -> explicit 50%", nil, pct(50), false},
		{"explicit 50% -> unset", pct(50), nil, false},
		{"both unset", nil, nil, false},
	} {
		// If DefaultAirtimeFactor moves off 1.0 these equivalence cases turn precision-dependent — see the note in load.go.
		got := ModemSettingsChanged(&Config{DutyCycle: tc.from}, &Config{DutyCycle: tc.to})
		if got != tc.want {
			t.Errorf("%s: reconnect = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A dropped optional would silently revert the budget to 50%, so pin the round trip through every format the loader accepts.
func TestDutyCycleSurvivesRoundTrip(t *testing.T) {
	for _, ext := range []string{".json", ".yaml", ".toml"} {
		for _, want := range []*float64{nil, ptrFloat(0.1), ptrFloat(50), ptrFloat(100)} {
			cfg := validConfig(t)
			cfg.DutyCycle = want

			raw, err := Marshal("cfg"+ext, cfg)
			if err != nil {
				t.Fatalf("%s: marshal: %v", ext, err)
			}
			dir := t.TempDir()
			path := dir + "/cfg" + ext
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			got, _, err := LoadFromPath(path)
			if err != nil {
				t.Fatalf("%s: load: %v", ext, err)
			}
			switch {
			case want == nil && got.DutyCycle != nil:
				t.Errorf("%s: unset became %v", ext, *got.DutyCycle)
			case want != nil && got.DutyCycle == nil:
				t.Errorf("%s: %v%% was dropped, reverting the budget to default", ext, *want)
			case want != nil && *got.DutyCycle != *want:
				t.Errorf("%s: %v%% became %v%%", ext, *want, *got.DutyCycle)
			}
			// And the resolved factor must match, which is what actually flies.
			if got.AirtimeFactorOr() != cfg.AirtimeFactorOr() {
				t.Errorf("%s: factor %v != %v after round trip",
					ext, got.AirtimeFactorOr(), cfg.AirtimeFactorOr())
			}
		}
	}
}

func ptrFloat(v float64) *float64 { return &v }
