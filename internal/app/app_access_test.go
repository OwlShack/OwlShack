package app

import (
	"fmt"
	"testing"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/store"
)

// App access is only ever set by its own endpoint, with both fields named, on a port in the range no other companion holds.
func TestSetCompanionApp(t *testing.T) {
	b, st := regionBackend(t)
	ctx := t.Context()
	one, err := b.SaveCompanion(ctx, api.CompanionInput{Name: "one", FloodScope: "inherit", ShareLocation: &share})
	if err != nil {
		t.Fatal(err)
	}
	two, err := b.SaveCompanion(ctx, api.CompanionInput{Name: "two", FloodScope: "inherit", ShareLocation: &share})
	if err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	port := func(p int) *int { return &p }

	for name, in := range map[string]api.CompanionAppInput{
		"no enabled":      {Port: port(5000)},
		"no port":         {Enabled: &on},
		"below the range": {Enabled: &on, Port: port(4999)},
		"above the range": {Enabled: &on, Port: port(5016)},
		"off, port 80":    {Enabled: &off, Port: port(80)},
	} {
		if err := b.SetCompanionApp(ctx, one, in); err == nil {
			t.Errorf("%s: saved", name)
		}
	}

	if err := b.SetCompanionApp(ctx, one, api.CompanionAppInput{Enabled: &on, Port: port(5000)}); err != nil {
		t.Fatal(err)
	}
	if err := b.SetCompanionApp(ctx, two, api.CompanionAppInput{Enabled: &on, Port: port(5000)}); err == nil {
		t.Error("a second companion was let in on 5000")
	}
	if err := b.SetCompanionApp(ctx, two, api.CompanionAppInput{Enabled: &off, Port: port(5000)}); err != nil {
		t.Errorf("a port kept while access is off may match another's: %v", err)
	}

	// Saving the companion's other settings leaves its access as it was.
	if _, err := b.SaveCompanion(ctx, api.CompanionInput{ID: one, Name: "one", FloodScope: "inherit", ShareLocation: &share}); err != nil {
		t.Fatal(err)
	}
	c, err := st.Companions.Get(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	if !c.AppEnabled || c.AppPort != 5000 {
		t.Errorf("after a companion save: enabled %v port %d, want on at 5000", c.AppEnabled, c.AppPort)
	}
}

// share is the shareLocation every test companion is saved with.
var share = true

// The app's changes go through the same checks as a save from the UI: a rename onto another companion's name is refused.
func TestUpdateCompanionChecks(t *testing.T) {
	b, st := regionBackend(t)
	ctx := t.Context()
	one, err := b.SaveCompanion(ctx, api.CompanionInput{Name: "one", FloodScope: "inherit", ShareLocation: &share})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveCompanion(ctx, api.CompanionInput{Name: "two", FloodScope: "inherit", ShareLocation: &share}); err != nil {
		t.Fatal(err)
	}
	if err := b.UpdateCompanion(ctx, one, func(c *store.Companion) error { c.Name = "two"; return nil }); err == nil {
		t.Error("renamed onto another companion's name")
	}
	if err := b.UpdateCompanion(ctx, one, func(c *store.Companion) error { c.Name = "uno"; c.ShareLocation = false; return nil }); err != nil {
		t.Fatal(err)
	}
	c, _ := st.Companions.Get(ctx, one)
	if c.Name != "uno" || c.ShareLocation {
		t.Errorf("saved %q share %v", c.Name, c.ShareLocation)
	}
}

// The Channels page cannot add a channel past the node's table.
func TestSaveChannelRefusesPastTable(t *testing.T) {
	b, _ := regionBackend(t)
	ctx := t.Context()
	id, err := b.SaveCompanion(ctx, api.CompanionInput{Name: "one", FloodScope: "inherit", ShareLocation: &share})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 50; i++ { // Public is the first
		if _, err := b.SaveChannel(ctx, api.ChannelInput{CompanionID: id, Name: fmt.Sprintf("#c%d", i), FloodScope: "inherit"}); err != nil {
			t.Fatalf("channel %d: %v", i, err)
		}
	}
	if _, err := b.SaveChannel(ctx, api.ChannelInput{CompanionID: id, Name: "#one-too-many", FloodScope: "inherit"}); err == nil {
		t.Error("a 51st channel was saved")
	}
}
