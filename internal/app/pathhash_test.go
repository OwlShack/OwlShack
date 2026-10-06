package app

import (
	"testing"

	"github.com/OwlShack/OwlShack/internal/config"
)

func ptr(v int) *int { return &v }

// The Settings default must reach the effective block, or the reload diff never sees a global change.
func TestPathHashSizeInheritance(t *testing.T) {
	cfg := &config.Config{
		PathHashSize: ptr(2),
		Companions: []config.CompanionConfig{
			{Name: "inherits"},
			{Name: "overrides", PathHashSize: ptr(1)},
		},
		Repeater: &config.RepeaterConfig{Name: "rp"},
	}

	blocks := effectiveCompanionConfigs(cfg)
	if got := *blocks[0].PathHashSize; got != 2 {
		t.Errorf("inheriting companion = %d, want 2", got)
	}
	if got := *blocks[1].PathHashSize; got != 1 {
		t.Errorf("overriding companion = %d, want 1", got)
	}
	if got := *effectiveRepeaterConfig(cfg).PathHashSize; got != 2 {
		t.Errorf("inheriting repeater = %d, want 2", got)
	}

	cfg.Repeater.PathHashSize = ptr(3)
	if got := *effectiveRepeaterConfig(cfg).PathHashSize; got != 3 {
		t.Errorf("overriding repeater = %d, want 3", got)
	}

	// No global set: everything falls back to 1 byte.
	bare := &config.Config{Companions: []config.CompanionConfig{{Name: "a"}}, Repeater: &config.RepeaterConfig{Name: "rp"}}
	if got := *effectiveCompanionConfigs(bare)[0].PathHashSize; got != config.DefaultPathHashSize {
		t.Errorf("default companion = %d, want %d", got, config.DefaultPathHashSize)
	}
	if got := *effectiveRepeaterConfig(bare).PathHashSize; got != config.DefaultPathHashSize {
		t.Errorf("default repeater = %d, want %d", got, config.DefaultPathHashSize)
	}

	// Resolving must not mutate the source: a nil stored value is what keeps the node following the global.
	if cfg.Companions[0].PathHashSize != nil {
		t.Error("effectiveCompanionConfigs mutated the source companion")
	}
	if bare.Repeater.PathHashSize != nil {
		t.Error("effectiveRepeaterConfig mutated the source repeater")
	}
}
