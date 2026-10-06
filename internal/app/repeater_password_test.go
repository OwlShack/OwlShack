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
	want := []store.RepeaterRegion{{Name: "nz"}, {Name: "*", DenyFlood: true}}
	if !slices.Equal(rep.Regions, want) {
		t.Errorf("regions = %+v, want %+v", rep.Regions, want)
	}
}
