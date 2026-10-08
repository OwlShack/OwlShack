package config

import (
	"strings"
	"testing"

	meshcore "github.com/OwlShack/meshcore-go"
)

func TestResolveScope_MostSpecificNonInheritWins(t *testing.T) {
	for _, c := range []struct {
		levels []FloodScope
		want   FloodScope
	}{
		{[]FloodScope{ScopeInherit, ScopeInherit, "region:nz"}, "region:nz"},
		{[]FloodScope{"region:wlg", ScopeInherit, "region:nz"}, "region:wlg"},
		{[]FloodScope{ScopeInherit, ScopeEverywhere, "region:nz"}, ScopeEverywhere},
		{[]FloodScope{"", ScopeInherit, "region:nz"}, "region:nz"}, // unset inherits
		{[]FloodScope{ScopeInherit}, ScopeEverywhere},
	} {
		if got := ResolveScope(c.levels...); got != c.want {
			t.Errorf("ResolveScope(%v) = %q, want %q", c.levels, got, c.want)
		}
	}
}

func TestFloodScopeValidate(t *testing.T) {
	for _, c := range []struct {
		s            FloodScope
		allowInherit bool
		ok           bool
	}{
		{"everywhere", false, true},
		{"region:nz", false, true},
		{"region:nz-wlg", false, true},
		{"inherit", true, true},
		{"inherit", false, false},
		{"region:", true, false},
		{"region:$private", true, false}, // no key to derive
		{"region:#nz", true, false},
		{"nz", true, false},
	} {
		if err := c.s.Validate(c.allowInherit); (err == nil) != c.ok {
			t.Errorf("%q.Validate(%v) = %v, want ok=%v", c.s, c.allowInherit, err, c.ok)
		}
	}
	if RequireScope("", true) == nil {
		t.Error("RequireScope accepted a missing scope")
	}
}

// A packet we scope must be labelled with the same region on the way in, and anything else must not be.
func TestPacketScope(t *testing.T) {
	nz, wlg := meshcore.NewRegion("nz"), meshcore.NewRegion("wlg")
	flood := func(scope *meshcore.Region) *meshcore.Packet {
		p := &meshcore.Packet{Header: meshcore.MakeHeader(meshcore.RouteTypeFlood, meshcore.PayloadTypeGrpTxt, 0), Payload: []byte("hello there")}
		p.SetScope(scope)
		raw, err := p.ToBytes()
		if err != nil {
			t.Fatal(err)
		}
		back, err := meshcore.PacketFromBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		return back
	}
	known := []*meshcore.Region{wlg, nz}
	if got := PacketScope(flood(nz), known); got != "region:nz" {
		t.Errorf("nz-scoped = %q", got)
	}
	if got := PacketScope(flood(nil), known); got != "everywhere" {
		t.Errorf("unscoped = %q", got)
	}
	if got := PacketScope(flood(meshcore.NewRegion("au")), known); got != ScopeUnknown {
		t.Errorf("au-scoped = %q, want unknown", got)
	}
}

func TestLabelRegions_ListThenRepeaterWithoutWildcardOrDuplicates(t *testing.T) {
	c := &Config{FloodRegions: []FloodRegion{{Name: "nz", Parent: "*"}, {Name: "wlg", Parent: "nz"}}, Repeater: &RepeaterConfig{Regions: []RepeaterRegion{{Name: "*"}, {Name: "nz"}, {Name: "akl"}, {Name: "#bne"}, {Name: "$priv"}}}}
	var got []string
	for _, r := range c.LabelRegions() {
		got = append(got, r.Name)
	}
	if want := "nz,wlg,akl,bne"; strings.Join(got, ",") != want { // "#bne" keys as bne; "$priv" has no key here
		t.Errorf("LabelRegions = %v, want %s", got, want)
	}
}

// A config file from before scopes names the repeater's advert region defaultRegion; an import keeps it, creating the region as the firmware does.
func TestApplyDefaults_FoldsDefaultRegion(t *testing.T) {
	for before, want := range map[string]FloodScope{"": ScopeEverywhere, "*": ScopeEverywhere, "nz": "region:nz", "#nz": "region:nz", "$priv": ScopeEverywhere} {
		c := &Config{Repeater: &RepeaterConfig{Name: "r", AdminPassword: "pw", DefaultRegion: before}}
		c.ApplyDefaults()
		if c.Repeater.FloodScope != want || c.Repeater.DefaultRegion != "" {
			t.Errorf("defaultRegion %q imported as %q (left %q), want %q", before, c.Repeater.FloodScope, c.Repeater.DefaultRegion, want)
		}
		if err := c.Validate(); err != nil {
			t.Errorf("defaultRegion %q: imported config fails validation: %v", before, err)
		}
	}
}

// The repeater's default scope is one of its own regions, as the firmware's default_id is.
func TestValidate_RepeaterScopeIsOwnRegion(t *testing.T) {
	for scope, ok := range map[FloodScope]bool{"region:nz": true, "region:akl": true, "region:au": false, ScopeInherit: false, ScopeEverywhere: true} {
		c := DefaultConfig()
		c.Repeater = &RepeaterConfig{Name: "r", AdminPassword: "pw", FloodScope: scope, Regions: []RepeaterRegion{{Name: "nz", Parent: "*"}, {Name: "#akl", Parent: "nz"}}}
		if err := c.Validate(); (err == nil) != ok {
			t.Errorf("scope %q: err = %v, want ok=%v", scope, err, ok)
		}
	}
}

// The firmware finds a region with any leading "#" ignored, so a home named either way is the stored region.
func TestApplyDefaults_HomeRegionIgnoresHash(t *testing.T) {
	for _, c := range []struct{ stored, home string }{{"#nz", "nz"}, {"nz", "#nz"}} {
		cfg := DefaultConfig()
		cfg.Repeater = &RepeaterConfig{Name: "r", AdminPassword: "pw", HomeRegion: c.home, Regions: []RepeaterRegion{{Name: c.stored, Parent: "*"}}}
		cfg.ApplyDefaults()
		if cfg.Repeater.HomeRegion != c.stored {
			t.Errorf("home %q beside %q = %q, want the stored name", c.home, c.stored, cfg.Repeater.HomeRegion)
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("home %q beside %q: %v", c.home, c.stored, err)
		}
	}
}

// "nz" and "#nz" are one region to the firmware, so a list holding both is a duplicate.
func TestValidate_RepeaterRegionsDuplicateIgnoresHash(t *testing.T) {
	c := DefaultConfig()
	c.Repeater = &RepeaterConfig{Name: "r", AdminPassword: "pw", Regions: []RepeaterRegion{{Name: "nz", Parent: "*"}, {Name: "#nz", Parent: "*"}}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("nz beside #nz: err = %v, want a duplicate", err)
	}
}

// A parent named with or without its "#" is the stored region too.
func TestApplyDefaults_ParentIgnoresHash(t *testing.T) {
	c := DefaultConfig()
	c.Repeater = &RepeaterConfig{Name: "r", AdminPassword: "pw", Regions: []RepeaterRegion{{Name: "#nz", Parent: "*"}, {Name: "akl", Parent: "nz"}}}
	c.ApplyDefaults()
	if p := c.Repeater.Regions[1].Parent; p != "#nz" {
		t.Errorf("akl's parent = %q, want #nz", p)
	}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
}
