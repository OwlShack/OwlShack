package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateRegionParents(t *testing.T) {
	rg := func(name, parent string) RepeaterRegion { return RepeaterRegion{Name: name, Parent: parent} }
	for _, c := range []struct {
		name    string
		regions []RepeaterRegion
		want    string // "" = valid
	}{
		{"tree", []RepeaterRegion{rg("*", ""), rg("nz", "*"), rg("akl", "nz")}, ""},
		{"child listed first", []RepeaterRegion{rg("akl", "nz"), rg("nz", "*")}, ""},
		{"missing parent", []RepeaterRegion{rg("nz", "")}, "parent is required"},
		{"unknown parent", []RepeaterRegion{rg("akl", "nz")}, "not a configured region"},
		{"wildcard with a parent", []RepeaterRegion{rg("*", "nz"), rg("nz", "*")}, "has no parent"},
		{"self", []RepeaterRegion{rg("nz", "nz")}, "inside itself"},
		{"loop", []RepeaterRegion{rg("a", "b"), rg("b", "a")}, "inside itself"},
	} {
		err := validateRegionParents(c.regions)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

// A config file written before parents were kept loads with every region at the top.
func TestApplyDefaults_RegionParentsAtTop(t *testing.T) {
	c := &Config{Repeater: &RepeaterConfig{Regions: []RepeaterRegion{{Name: "*"}, {Name: "nz"}, {Name: "akl", Parent: "nz"}}}}
	c.ApplyDefaults()
	got := c.Repeater.Regions
	if got[0].Parent != "" || got[1].Parent != "*" || got[2].Parent != "nz" {
		t.Errorf("parents = %+v", got)
	}
}

// The Settings list is checked like a repeater's: each parent listed (or "*"), and no loops.
func TestValidate_FloodRegionParents(t *testing.T) {
	for _, c := range []struct {
		regions []FloodRegion
		want    string
	}{
		{[]FloodRegion{{"nz", "*"}, {"akl", "nz"}}, ""},
		{[]FloodRegion{{"akl", "nz"}}, "not a configured region"},
		{[]FloodRegion{{"a", "b"}, {"b", "a"}}, "inside itself"},
		{[]FloodRegion{{"nz", ""}}, "parent is required"},
	} {
		cfg := DefaultConfig()
		cfg.FloodRegions = c.regions
		err := cfg.Validate()
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: got %v, want %q", c.regions, err, c.want)
		}
	}
}

func TestValidate_FloodRegionsCapped(t *testing.T) {
	cfg := DefaultConfig()
	for i := range MaxFloodRegions + 1 {
		cfg.FloodRegions = append(cfg.FloodRegions, FloodRegion{Name: fmt.Sprintf("r%d", i), Parent: "*"})
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("%d regions: got %v, want the cap", len(cfg.FloodRegions), err)
	}
	cfg.FloodRegions = cfg.FloodRegions[:MaxFloodRegions]
	if err := cfg.Validate(); err != nil {
		t.Errorf("%d regions: %v", MaxFloodRegions, err)
	}
}
