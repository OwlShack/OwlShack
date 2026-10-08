package repeater

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/OwlShack/OwlShack/internal/config"
)

func newRegionRepeater(t *testing.T, regions ...config.RepeaterRegion) *Repeater {
	t.Helper()
	r := &Repeater{cfg: config.RepeaterConfig{Regions: regions}}
	r.reconfigure = testReconfigure(r)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.runCtx = ctx
	return r
}

// waitRegions waits out reconfigureAfterReply and returns the applied list.
func waitRegions(t *testing.T, r *Repeater, done func([]config.RepeaterRegion) bool) []config.RepeaterRegion {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if regions := r.cfgSnapshot().Regions; done(regions) {
			return regions
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("regions never settled: %+v", r.cfgSnapshot().Regions)
	return nil
}

func TestRegionPutParentMoveAndRemove(t *testing.T) {
	r := newRegionRepeater(t, config.RepeaterRegion{Name: "*"}, config.RepeaterRegion{Name: "nz", Parent: "*"})

	if got := r.runCLI("region put akl nz"); got != "OK - (flood allowed)" {
		t.Fatalf("put akl nz = %q", got)
	}
	regions := waitRegions(t, r, func(rs []config.RepeaterRegion) bool { return regionParent(rs, "akl") == "nz" })
	if regionDenies(regions, "akl") {
		t.Error("put should allow flood, as the firmware sets flags = 0")
	}
	if got := r.runCLI("region get akl"); got != " akl (nz) F" {
		t.Errorf("get akl = %q, want the firmware's \" %%s (%%s) %%s\"", got)
	}
	if got := r.runCLI("region"); got != "*^ F\n nz F\n  akl F\n" { // no home set: the wildcard is home
		t.Errorf("tree = %q", got)
	}
	if got := r.runCLI("region remove nz"); got != "Err - not empty" {
		t.Errorf("remove a parent = %q, want Err - not empty", got)
	}
	if got := r.runCLI("region put nz akl"); got != "Err - unable to put" {
		t.Errorf("moving nz under its own child = %q, want refused", got)
	}
	if got := r.runCLI("region put nz nz"); got != "Err - unable to put" {
		t.Errorf("nz under itself = %q, want refused", got)
	}

	if got := r.runCLI("region put akl *"); got != "OK - (flood allowed)" {
		t.Fatalf("move akl to the top = %q", got)
	}
	waitRegions(t, r, func(rs []config.RepeaterRegion) bool { return regionParent(rs, "akl") == "*" })
	if got := r.runCLI("region remove nz"); got != "OK" {
		t.Errorf("remove an emptied parent = %q", got)
	}
	waitRegions(t, r, func(rs []config.RepeaterRegion) bool { return regionParent(rs, "nz") == "" })
}

func TestRegionDef(t *testing.T) {
	r := newRegionRepeater(t, config.RepeaterRegion{Name: "*"})

	want := "*^ F\n nz F\n  akl F\n  wlg F\n au F\n"
	if got := r.runCLI("region def nz akl|nz wlg|* au"); got != want { // name|jump: put name, then carry on from jump
		t.Errorf("def reply = %q, want %q", got, want)
	}
	regions := waitRegions(t, r, func(rs []config.RepeaterRegion) bool { return regionParent(rs, "au") == "*" })
	for name, parent := range map[string]string{"nz": "*", "akl": "nz", "wlg": "nz", "au": "*"} {
		if got := regionParent(regions, name); got != parent {
			t.Errorf("%s parent = %q, want %q", name, got, parent)
		}
	}

	for cmd, want := range map[string]string{
		"region def":           "Err - empty def",
		"region def a|":        "Err - empty jump",
		"region def |nz":       "Err - empty name",
		"region def a|ghost":   "Err - unknown jump: ghost",
		"region def bad.name ": "Err - put failed: bad.name",
	} {
		if got := r.runCLI(cmd); got != want {
			t.Errorf("%q = %q, want %q", cmd, got, want)
		}
	}
	// The firmware keeps the names put before the failing token.
	waitRegions(t, r, func(rs []config.RepeaterRegion) bool { return regionParent(rs, "a") == "*" })
}

func TestRegionLoad(t *testing.T) {
	r := newRegionRepeater(t,
		config.RepeaterRegion{Name: "*", DenyFlood: true},
		config.RepeaterRegion{Name: "nz", Parent: "*", DenyFlood: true},
		config.RepeaterRegion{Name: "gone", Parent: "*"},
	)
	r.cfg.HomeRegion = "gone"
	r.cfg.FloodScope = "region:gone"

	if got := r.runCLI("region load"); got != "" {
		t.Fatalf("load = %q, want no reply", got)
	}
	for _, line := range []string{
		"* F",      // indent 0 is the root line of `region` output; ignored
		" nz F",    // existing: keeps its own deny, not this F
		"  akl F",  // new under nz, flood allowed
		"  wlg",    // new under nz, denied
		" au F",    // back to the top
		"      x",  // indent 6 with nothing at 5: no parent, skipped
		"ver",      // indent 0: not a command while loading
		"ab| ch F", // the CLI prefix isn't stripped in load mode
	} {
		if got := r.runCLI(line); got != "" {
			t.Errorf("line %q replied %q, want nothing", line, got)
		}
	}
	if got := r.runCLI("  "); got != "OK - loaded 4 regions" {
		t.Fatalf("blank line = %q", got)
	}
	regions := waitRegions(t, r, func(rs []config.RepeaterRegion) bool { return regionParent(rs, "au") == "*" })

	want := []config.RepeaterRegion{
		{Name: "*", DenyFlood: true},
		{Name: "nz", Parent: "*", DenyFlood: true},
		{Name: "akl", Parent: "nz"},
		{Name: "wlg", Parent: "nz", DenyFlood: true},
		{Name: "au", Parent: "*"},
	}
	if !slices.Equal(regions, want) {
		t.Errorf("loaded = %+v\nwant     %+v", regions, want)
	}
	if home := r.cfgSnapshot().HomeRegion; home != "" {
		t.Errorf("home = %q, want cleared since gone wasn't loaded", home)
	}
	if scope := r.cfgSnapshot().FloodScope; scope != config.ScopeEverywhere {
		t.Errorf("default scope = %q, want everywhere since gone wasn't loaded", scope)
	}
	if got := r.runCLI("ver"); got == "" {
		t.Error("load mode didn't end on the blank line")
	}
}

// A "#nz" default keys as nz does, so the scope is stored bare; a "$" region can't be one here, as we can't load its keys.
func TestRegionDefault_HashAndPrivate(t *testing.T) {
	r := newRegionRepeater(t, config.RepeaterRegion{Name: "#nz", Parent: "*"}, config.RepeaterRegion{Name: "$priv", Parent: "*"})
	r.cfg.Name = "rp"

	if got := r.runCLI("region default #nz"); got != " default scope is now #nz" {
		t.Fatalf("default #nz = %q", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for r.cfgSnapshot().FloodScope != "region:nz" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.cfgSnapshot().FloodScope; got != "region:nz" {
		t.Errorf("scope = %q, want region:nz", got)
	}
	if got := r.runCLI("region default $priv"); got != "Err - a private region can't be the default here" {
		t.Errorf("default $priv = %q", got)
	}
}

// A load that keeps the default region keeps the default; only one that drops it resets it.
func TestRegionLoad_KeepsScopeItLoads(t *testing.T) {
	r := newRegionRepeater(t, config.RepeaterRegion{Name: "*"}, config.RepeaterRegion{Name: "nz", Parent: "*"})
	r.cfg.FloodScope = "region:nz"
	r.runCLI("region load")
	r.runCLI(" nz F")
	r.runCLI(" au F")
	if got := r.runCLI(""); got != "OK - loaded 2 regions" {
		t.Fatalf("blank line = %q", got)
	}
	waitRegions(t, r, func(rs []config.RepeaterRegion) bool { return regionParent(rs, "au") == "*" })
	if scope := r.cfgSnapshot().FloodScope; scope != "region:nz" {
		t.Errorf("scope = %q, want region:nz kept", scope)
	}
}
