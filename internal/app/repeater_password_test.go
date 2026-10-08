package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/store"
)

// A blank admin password compares equal to the blank a login sends, so it must be impossible to
// create a repeater without one. The store column defaults to ”, so omitting the field used to
// mean "no auth" rather than "not set".
func TestCreateRepeater_RejectsBlankPassword(t *testing.T) {
	t.Parallel()
	b := &backend{}
	for _, pw := range []string{"", " ", "\t", "\n  "} {
		err := b.CreateRepeater(t.Context(), api.RepeaterCreateInput{Name: "rp", AdminPassword: pw})
		if err == nil {
			t.Errorf("password %q was accepted", pw)
			continue
		}
		if !strings.Contains(err.Error(), "admin password is required") {
			t.Errorf("password %q: error should say what is wrong, got %v", pw, err)
		}
	}
}

// Omitting the field keeps the stored password; sending "" would clear it and reopen the hole.
// Guest stays clearable: a blank guest password grants PERM_ACL_GUEST (0), as the firmware does.
func TestUpdateRepeaterAdmin_RefusesToBlankThePassword(t *testing.T) {
	t.Parallel()
	b := &backend{}
	blank := ""
	spaces := "   "
	for _, pw := range []*string{&blank, &spaces} {
		err := b.UpdateRepeaterAdmin(t.Context(), api.RepeaterAdminInput{AdminPassword: pw})
		if err == nil {
			t.Errorf("clearing the admin password with %q was accepted", *pw)
			continue
		}
		if !strings.Contains(err.Error(), "cannot be blank") {
			t.Errorf("%q: got %v", *pw, err)
		}
	}
}

// "*" is the firmware's wildcard, which always exists; unscoped flood is stopped by denying it, never by removing it.
func TestRemoveRepeaterRegion_RefusesTheWildcard(t *testing.T) {
	t.Parallel()
	b := &backend{}
	err := b.RemoveRepeaterRegion(t.Context(), "*")
	if err == nil || !strings.Contains(err.Error(), "deny flood on it instead") {
		t.Errorf("removing \"*\": got %v, want a refusal that says to deny flood instead", err)
	}
}

// With no "*" entry the wildcard still exists (allowing flood), so denying it adds the entry; an unknown region is an error, not a silent no-op.
func TestSetRepeaterRegionFlood_WildcardAndUnknown(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	cfg := config.DefaultConfig()
	cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "nz"}}} // a config file with no "*"
	if err := saveConfig(ctx, b.db, &cfg); err != nil {
		t.Fatal(err)
	}

	if err := b.SetRepeaterRegionFlood(ctx, "*", true); err != nil {
		t.Fatalf("denying an absent \"*\": %v", err)
	}
	if err := b.SetRepeaterRegionFlood(ctx, "ghost", true); err == nil || !strings.Contains(err.Error(), "unknown region") {
		t.Errorf("unknown region: got %v, want an unknown-region error", err)
	}
	rep, err := b.db.Repeater.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.RepeaterRegion{{Name: "nz", Parent: "*"}, {Name: "*", DenyFlood: true}} // a region saved without a parent reads back at the top
	if !slices.Equal(rep.Regions, want) {
		t.Errorf("regions = %+v, want %+v", rep.Regions, want)
	}
}

// Adding needs an explicit parent, re-adding moves the region as the firmware's `region put` does, and a parent can't be removed before its sub-regions.
func TestRepeaterRegionParents(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	cfg := config.DefaultConfig()
	cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "nz", Parent: "*"}}}
	if err := saveConfig(ctx, b.db, &cfg); err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }

	if err := b.AddRepeaterRegion(ctx, api.RepeaterRegionInput{Name: "akl"}); err == nil || !strings.Contains(err.Error(), "parent is required") {
		t.Errorf("no parent: got %v", err)
	}
	if err := b.AddRepeaterRegion(ctx, api.RepeaterRegionInput{Name: "akl", Parent: str("ghost")}); err == nil {
		t.Error("unknown parent accepted")
	}
	if err := b.AddRepeaterRegion(ctx, api.RepeaterRegionInput{Name: "akl", Parent: str("nz")}); err != nil {
		t.Fatal(err)
	}
	if err := b.MoveRepeaterRegion(ctx, "nz", "akl"); err == nil {
		t.Error("moving nz under its own child accepted")
	}
	if err := b.RemoveRepeaterRegion(ctx, "nz"); err == nil || !strings.Contains(err.Error(), "akl") {
		t.Errorf("removing a parent: got %v, want it to name akl", err)
	}
	if err := b.MoveRepeaterRegion(ctx, "akl", "*"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetRepeaterRegionFlood(ctx, "akl", true); err != nil {
		t.Fatal(err)
	}
	if err := b.RemoveRepeaterRegion(ctx, "nz"); err != nil {
		t.Fatalf("removing an emptied parent: %v", err)
	}
	rep, err := b.db.Repeater.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.RepeaterRegion{{Name: "akl", Parent: "*", DenyFlood: true}}
	if !slices.Equal(rep.Regions, want) {
		t.Errorf("regions = %+v, want %+v", rep.Regions, want)
	}
}

// The default scope is one of the repeater's regions: picking one allows flood on it and removing it clears the default, as the firmware does.
func TestRepeaterFloodScopeIsOwnRegion(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	cfg := config.DefaultConfig()
	cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "nz", Parent: "*", DenyFlood: true}}}
	if err := saveConfig(ctx, b.db, &cfg); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"inherit", "region:au"} {
		if err := b.SetRepeaterFloodScope(ctx, api.RepeaterScopeInput{FloodScope: bad}); err == nil {
			t.Errorf("floodScope %q accepted", bad)
		}
	}
	if err := b.SetRepeaterFloodScope(ctx, api.RepeaterScopeInput{FloodScope: "region:nz"}); err != nil {
		t.Fatal(err)
	}
	rep, err := b.db.Repeater.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FloodScope != "region:nz" || rep.Regions[0].DenyFlood {
		t.Errorf("after picking nz: scope=%q regions=%+v, want region:nz with flood allowed", rep.FloodScope, rep.Regions)
	}

	// A later deny stays, as after the firmware's denyf: only picking the default again allows flood.
	if err := b.SetRepeaterRegionFlood(ctx, "nz", true); err != nil {
		t.Fatal(err)
	}
	if err := b.UpdateRepeaterRelay(ctx, api.RepeaterRelayInput{}); err != nil {
		t.Fatal(err)
	}
	if rep, err = b.db.Repeater.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if rep.FloodScope != "region:nz" || !rep.Regions[0].DenyFlood {
		t.Errorf("after a relay save: scope=%q regions=%+v, want region:nz still denied", rep.FloodScope, rep.Regions)
	}

	if err := b.RemoveRepeaterRegion(ctx, "au"); err == nil || !strings.Contains(err.Error(), "unknown region") {
		t.Errorf("removing a name not in the list: got %v, want unknown region", err)
	}
	if rep, err = b.db.Repeater.Get(ctx); err != nil || rep.FloodScope != "region:nz" {
		t.Errorf("a refused remove changed the scope to %q (%v)", rep.FloodScope, err)
	}
	if err := b.RemoveRepeaterRegion(ctx, "nz"); err != nil {
		t.Fatal(err)
	}
	if rep, err = b.db.Repeater.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if rep.FloodScope != "everywhere" {
		t.Errorf("after removing nz: scope=%q, want everywhere", rep.FloodScope)
	}
}

// The home region is a label the firmware keeps; "*" clears it and an unknown region is refused.
func TestSetRepeaterHome(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	cfg := config.DefaultConfig()
	cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "nz", Parent: "*"}}}
	if err := saveConfig(ctx, b.db, &cfg); err != nil {
		t.Fatal(err)
	}
	home := func() string {
		rep, err := b.db.Repeater.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return rep.HomeRegion
	}
	if err := b.SetRepeaterHome(ctx, api.RepeaterHomeInput{Region: "nz"}); err != nil || home() != "nz" {
		t.Errorf("home nz: err=%v home=%q", err, home())
	}
	if err := b.SetRepeaterHome(ctx, api.RepeaterHomeInput{Region: "au"}); err == nil || home() != "nz" {
		t.Errorf("unknown home au: err=%v home=%q, want refused and nz kept", err, home())
	}
	if err := b.SetRepeaterHome(ctx, api.RepeaterHomeInput{Region: "*"}); err != nil || home() != "" {
		t.Errorf("home *: err=%v home=%q, want cleared", err, home())
	}
	if err := b.SetRepeaterHome(ctx, api.RepeaterHomeInput{}); err == nil {
		t.Error("an empty region was accepted")
	}
	// The firmware ignores a leading "#" when it finds a region, and the stored name is what's kept.
	if err := b.SetRepeaterHome(ctx, api.RepeaterHomeInput{Region: "#nz"}); err != nil || home() != "nz" {
		t.Errorf("home #nz beside nz: err=%v home=%q, want nz", err, home())
	}
}

// A new repeater starts sending everywhere, its own default; it never inherits.
func TestCreateRepeater_Succeeds(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	cfg := config.DefaultConfig()
	if err := saveConfig(ctx, b.db, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := b.CreateRepeater(ctx, api.RepeaterCreateInput{Name: "rp", AdminPassword: "pw"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	rep, err := b.db.Repeater.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FloodScope != "everywhere" {
		t.Errorf("new repeater scope = %q, want everywhere", rep.FloodScope)
	}
}

// "#nz" is "nz" to the firmware's lookup: adding it is refused as a duplicate, and moving it moves nz.
func TestAddRepeaterRegion_HashIsTheSameRegion(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	cfg := config.DefaultConfig()
	cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "au", Parent: "*"}, {Name: "nz", Parent: "*"}}}
	if err := saveConfig(ctx, b.db, &cfg); err != nil {
		t.Fatal(err)
	}
	au := "au"
	if err := b.AddRepeaterRegion(ctx, api.RepeaterRegionInput{Name: "#nz", Parent: &au}); err == nil {
		t.Error("adding #nz beside nz was accepted")
	}
	if err := b.MoveRepeaterRegion(ctx, "#nz", au); err != nil {
		t.Fatal(err)
	}
	rep, err := b.db.Repeater.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.RepeaterRegion{{Name: "au", Parent: "*"}, {Name: "nz", Parent: "au"}}
	if !slices.Equal(rep.Regions, want) {
		t.Errorf("regions = %+v, want %+v", rep.Regions, want)
	}
	if err := b.SetRepeaterRegionFlood(ctx, "#nz", true); err != nil {
		t.Errorf("deny flood on #nz: %v", err)
	}
	if err := b.RemoveRepeaterRegion(ctx, "#au"); err == nil || !strings.Contains(err.Error(), "sub-regions") {
		t.Errorf("remove #au with nz under it: %v, want refused for its sub-regions", err)
	}
	if err := b.RemoveRepeaterRegion(ctx, "#nz"); err != nil {
		t.Errorf("remove #nz: %v", err)
	}
	if rep, err = b.db.Repeater.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []store.RepeaterRegion{{Name: "au", Parent: "*"}}; !slices.Equal(rep.Regions, want) {
		t.Errorf("after removing #nz: %+v, want %+v", rep.Regions, want)
	}
}

// "*" is found only by its exact name, as the firmware's findByName checks it before stripping the "#", so "#*" is no region.
func TestHashStarIsNotTheWildcard(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	cfg := config.DefaultConfig()
	cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "*", DenyFlood: true}}}
	if err := saveConfig(ctx, b.db, &cfg); err != nil {
		t.Fatal(err)
	}
	// "*" has no sub-regions here, so only the name rule can refuse this.
	if err := b.RemoveRepeaterRegion(ctx, "#*"); err == nil {
		t.Error(`removing "#*" succeeded`)
	}
	if err := b.SetRepeaterRegionFlood(ctx, "#*", false); err == nil {
		t.Error(`deny flood on "#*" succeeded`)
	}
	if err := b.SetRepeaterHome(ctx, api.RepeaterHomeInput{Region: "#*"}); err == nil {
		t.Error(`home "#*" succeeded`)
	}
	rep, err := b.db.Repeater.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.Regions, store.RepeaterRegion{Name: "*", DenyFlood: true}) || rep.HomeRegion != "" {
		t.Errorf(`"*" changed: regions=%+v home=%q`, rep.Regions, rep.HomeRegion)
	}
}
