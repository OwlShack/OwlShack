package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/modem"
	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/store"
)

func reloadRig(t *testing.T) (*store.Store, func(old, next *config.Config, running []*companion.Companion) []*companion.Companion) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mux := node.NewRadioMux(silentModem{})
	ms := &modem.State{}
	tel := newTelemetryPublisher(nil)
	var all []*companion.Companion
	t.Cleanup(func() { stopCompanions(all) })
	return st, func(old, next *config.Config, running []*companion.Companion) []*companion.Companion {
		t.Helper()
		out, _, err := reloadCompanions(t.Context(), old, next, running, ms, mux, st, nil, nil, tel)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, out...)
		return out
	}
}

func oneCompanion(mut func(*config.CompanionConfig)) *config.Config {
	pub, west := 0, 2
	ch := config.ChannelList{{Name: "Public", Slot: &pub}, {Name: "#westest", Slot: &west}}
	cc := config.CompanionConfig{ID: 7, Name: "home", PrivateKey: strings.Repeat("11", 32), Channels: &ch}
	if mut != nil {
		mut(&cc)
	}
	return &config.Config{Companions: []config.CompanionConfig{cc}}
}

// A rename, a new position or a new region applies to the running companion: no rebuild, so no second advert and no lost logins.
func TestReload_SettingsApplyInPlace(t *testing.T) {
	_, reload := reloadRig(t)
	first := oneCompanion(nil)
	running := reload(nil, first, nil)
	if ch := running[0].Node().Channel(2); ch == nil || ch.Name != "#westest" {
		t.Fatalf("slot 2 = %+v, want #westest kept in its slot", ch)
	}
	lat, lon := -41.3, 174.8
	renamed := oneCompanion(func(c *config.CompanionConfig) {
		c.Name, c.Latitude, c.Longitude, c.FloodScope = "away", &lat, &lon, config.ScopeEverywhere
	})
	again := reload(first, renamed, running)
	if again[0] != running[0] {
		t.Fatal("the companion was rebuilt for a settings change")
	}
	if again[0].Name() != "away" || again[0].AppConfig().Latitude == nil || *again[0].AppConfig().Latitude != lat {
		t.Errorf("applied: name %q position %v", again[0].Name(), again[0].AppConfig().Latitude)
	}

	interval := 600
	slower := oneCompanion(func(c *config.CompanionConfig) {
		c.Name, c.Latitude, c.Longitude, c.FloodScope, c.AdvertInterval = "away", &lat, &lon, config.ScopeEverywhere, &interval
	})
	if rebuilt := reload(renamed, slower, again); rebuilt[0] == again[0] {
		t.Error("an advert interval change was applied in place, but the advert loop reads it only at start")
	}
}

// A channel added on the Channels page and saved, then read back from the database, matches what the node runs, so the reload keeps the companion.
func TestReload_RunningChannelsNoRebuild(t *testing.T) {
	st, reload := reloadRig(t)
	ctx := t.Context()
	if _, err := initConfigTables(ctx, st); err != nil {
		t.Fatal(err)
	}
	seed := oneCompanion(nil)
	seed.Companions[0].ID = 0
	var err error
	st.WriteSync(func() { err = writeConfigToTables(ctx, st, seed) })
	if err != nil {
		t.Fatal(err)
	}
	before, err := readConfigFromTables(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	running := reload(nil, before, nil)
	// As the Channels page adds one: no scope named.
	if err := running[0].AddChannel(config.ChannelRef{Name: "#owl"}); err != nil {
		t.Fatal(err)
	}
	if err := persistChannels(ctx, st, running, false); err != nil {
		t.Fatal(err)
	}
	after, err := readConfigFromTables(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if again := reload(before, after, running); again[0] != running[0] {
		t.Error("rebuilt for channels the node already runs")
	}
}

// A channel slot past the node's table is skipped, not refused, so a database that loaded before still loads.
func TestSlotPastTableSkipped(t *testing.T) {
	_, reload := reloadRig(t)
	far := 60
	cfg := oneCompanion(func(c *config.CompanionConfig) {
		*c.Channels = append(*c.Channels, config.ChannelRef{Name: "#far", Slot: &far})
	})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if running := reload(nil, cfg, nil); running[0].Node().Channel(2) == nil {
		t.Error("the other channels were not joined")
	}
}

// Slots survive a save and a load, gaps included; a channel without one takes the first free.
func TestChannelSlotsSaved(t *testing.T) {
	st, _ := reloadRig(t)
	ctx := t.Context()
	cfg := oneCompanion(func(c *config.CompanionConfig) { *c.Channels = append(*c.Channels, config.ChannelRef{Name: "#owl"}) })
	cfg.Companions[0].ID = 0
	if _, err := initConfigTables(ctx, st); err != nil {
		t.Fatal(err)
	}
	var err error
	st.WriteSync(func() { err = writeConfigToTables(ctx, st, cfg) })
	if err != nil {
		t.Fatal(err)
	}
	back, err := readConfigFromTables(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	slots := map[string]int{}
	for _, ch := range *back.Companions[0].Channels {
		slots[ch.Name] = *ch.Slot
	}
	if slots["Public"] != 0 || slots["#westest"] != 2 || slots["#owl"] != 1 {
		t.Errorf("slots = %v, want Public 0, #owl 1 (first free), #westest 2", slots)
	}
}
