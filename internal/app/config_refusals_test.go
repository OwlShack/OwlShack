package app

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/store"
)

// twoCompanions stores a valid config of two companions with one channel each, returning the companion and channel ids.
func twoCompanions(t *testing.T, b *backend) (c1, ch1, c2, ch2 int64) {
	t.Helper()
	cfg := config.DefaultConfig()
	for _, name := range []string{"one", "two"} {
		seed, err := config.GenerateSeedHex()
		if err != nil {
			t.Fatal(err)
		}
		chans := config.ChannelList{{Name: "#" + name}}
		cfg.Companions = append(cfg.Companions, config.CompanionConfig{Name: name, PrivateKey: seed, Channels: &chans})
	}
	if err := saveConfig(t.Context(), b.db, &cfg); err != nil {
		t.Fatal(err)
	}
	chans, err := b.db.Channels.List(t.Context())
	if err != nil || len(chans) != 2 {
		t.Fatalf("channels = %v, %v", chans, err)
	}
	if chans[0].Name != "#one" {
		chans[0], chans[1] = chans[1], chans[0]
	}
	return chans[0].CompanionID, chans[0].ID, chans[1].CompanionID, chans[1].ID
}

// The UI renames a channel only from its thread, so the settings save keeps the name, and a channel that isn't there is a 404.
func TestSaveChannel_KeepsTheNameAndNeedsTheChannel(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	c1, ch1, _, _ := twoCompanions(t, b)

	if _, err := b.SaveChannel(t.Context(), api.ChannelInput{ID: ch1, CompanionID: c1, Name: "#one", FloodScope: "everywhere"}); err != nil {
		t.Fatalf("saving with the same name: %v", err)
	}
	_, err := b.SaveChannel(t.Context(), api.ChannelInput{ID: ch1, CompanionID: c1, Name: "#renamed", FloodScope: "inherit"})
	if verr := (*api.ValidationError)(nil); !errors.As(err, &verr) || !strings.Contains(err.Error(), "rename") {
		t.Errorf("renaming through settings: err = %v, want a validation error about renaming", err)
	}
	_, err = b.SaveChannel(t.Context(), api.ChannelInput{ID: 9999, CompanionID: c1, Name: "x", FloodScope: "inherit"})
	if serr := (*api.StatusError)(nil); !errors.As(err, &serr) || serr.Status != http.StatusNotFound {
		t.Errorf("saving a missing channel: err = %v, want 404", err)
	}
}

// A bot posts only to its own companion's channels, and every bot but a DM bot needs one, as the form requires.
func TestSaveTrigger_ChannelRules(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	c1, ch1, _, ch2 := twoCompanions(t, b)
	sched := "0 * * * *"
	bot := func(typ string, channels []int64) api.TriggerInput {
		return api.TriggerInput{CompanionID: c1, Type: typ, Template: "hi", Schedule: &sched, ChannelIDs: channels, FloodScope: "inherit"}
	}

	if _, err := b.SaveTrigger(t.Context(), bot("cron", []int64{ch1})); err != nil {
		t.Fatalf("a cron bot on its own channel: %v", err)
	}
	var verr *api.ValidationError
	if _, err := b.SaveTrigger(t.Context(), bot("cron", nil)); !errors.As(err, &verr) || !strings.Contains(err.Error(), "channel") {
		t.Errorf("cron bot with no channel: err = %v, want a validation error about the channel", err)
	}
	blank := bot("cron", []int64{ch1})
	blank.Template = "   "
	if _, err := b.SaveTrigger(t.Context(), blank); !errors.As(err, &verr) || !strings.Contains(err.Error(), "template") {
		t.Errorf("blank template: err = %v, want a validation error about the template", err)
	}
	if _, err := b.SaveTrigger(t.Context(), bot("group", []int64{ch2})); !errors.As(err, &verr) || !strings.Contains(err.Error(), "companion") {
		t.Errorf("bot on another companion's channel: err = %v, want a validation error naming the companion", err)
	}
}

// Adding a region that exists is refused, as the form refuses it; moving one is its own request.
func TestRepeaterRegion_AddRefusesADuplicateAndMoveMoves(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	cfg := config.DefaultConfig()
	cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "*"}, {Name: "nz", Parent: "*"}, {Name: "akl", Parent: "nz"}}}
	if err := saveConfig(ctx, b.db, &cfg); err != nil {
		t.Fatal(err)
	}
	top := "*"
	err := b.AddRepeaterRegion(ctx, api.RepeaterRegionInput{Name: "#akl", Parent: &top, DenyFlood: true})
	if serr := (*api.StatusError)(nil); !errors.As(err, &serr) || serr.Status != http.StatusConflict {
		t.Errorf("adding an existing region: err = %v, want 409", err)
	}
	if err := b.MoveRepeaterRegion(ctx, "akl", "*"); err != nil {
		t.Fatalf("moving akl to the top: %v", err)
	}
	if err := b.MoveRepeaterRegion(ctx, "*", "nz"); err == nil {
		t.Error(`moving "*" was accepted`)
	}
	if err := b.MoveRepeaterRegion(ctx, "nope", "*"); err == nil {
		t.Error("moving an unknown region was accepted")
	}
	rep, err := b.db.Repeater.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, rg := range rep.Regions {
		if rg.Name == "akl" && (rg.Parent != "*" || rg.DenyFlood) {
			t.Errorf("akl = %+v, want moved to the top with flood still allowed", rg)
		}
	}
}

// A region must be in the Settings list to be picked anywhere, and stays there while anything uses it.
func TestRegions_OnlyListedAndKeptWhileUsed(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	c1, ch1, _, _ := twoCompanions(t, b)
	nz := []store.FloodRegion{{Name: "nz", Parent: "*"}}
	var verr *api.ValidationError

	if err := b.SaveFloodRegions(ctx, api.FloodRegionsInput{Regions: &nz, FloodScope: "region:au"}); !errors.As(err, &verr) {
		t.Errorf("default outside the list: err = %v, want a validation error", err)
	}
	if err := b.SaveFloodRegions(ctx, api.FloodRegionsInput{Regions: &nz, FloodScope: "everywhere"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveChannel(ctx, api.ChannelInput{ID: ch1, Name: "#one", FloodScope: "region:au"}); !errors.As(err, &verr) {
		t.Errorf("channel in an unlisted region: err = %v, want a validation error", err)
	}
	if _, err := b.SaveChannel(ctx, api.ChannelInput{ID: ch1, Name: "#one", FloodScope: "region:nz"}); err != nil {
		t.Errorf("channel in a listed region: %v", err)
	}
	sched := "0 * * * *"
	if _, err := b.SaveTrigger(ctx, api.TriggerInput{CompanionID: c1, Type: "cron", Template: "hi", Schedule: &sched, ChannelIDs: []int64{ch1}, FloodScope: "region:au"}); !errors.As(err, &verr) {
		t.Errorf("bot in an unlisted region: err = %v, want a validation error", err)
	}
	if err := b.SetContactFloodScope(ctx, c1, make([]byte, 32), "region:au"); !errors.As(err, &verr) {
		t.Errorf("contact in an unlisted region: err = %v, want a validation error", err)
	}

	none := []store.FloodRegion{}
	err := b.SaveFloodRegions(ctx, api.FloodRegionsInput{Regions: &none, FloodScope: "everywhere"})
	if !errors.As(err, &verr) || !strings.Contains(err.Error(), "#one") {
		t.Errorf("removing nz while #one uses it: err = %v, want a validation error naming #one", err)
	}
}

// A region dropped before the list was enforced still loads; saves that don't touch it, and edits to the list itself, go through.
func TestRegions_UnlistedFromBeforeDoesNotBlockOtherSaves(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	c1, ch1, _, _ := twoCompanions(t, b)
	var werr error
	b.db.WriteSync(func() {
		ch, err := b.db.Channels.Get(ctx, ch1)
		if err != nil {
			werr = err
			return
		}
		ch.FloodScope = "region:gone"
		werr = b.db.Channels.Update(ctx, ch)
	})
	if werr != nil {
		t.Fatal(werr)
	}
	nz := []store.FloodRegion{{Name: "nz", Parent: "*"}}
	if err := b.SaveFloodRegions(ctx, api.FloodRegionsInput{Regions: &nz, FloodScope: "everywhere"}); err != nil {
		t.Errorf("adding nz with #one still on a region from before: %v", err)
	}
	if _, err := b.SaveChannel(ctx, api.ChannelInput{ID: ch1, Name: "#one", FloodScope: "region:gone"}); err != nil {
		t.Errorf("saving #one with its region unchanged: %v", err)
	}
	sched := "0 * * * *"
	if _, err := b.SaveTrigger(ctx, api.TriggerInput{CompanionID: c1, Type: "cron", Template: "hi", Schedule: &sched, ChannelIDs: []int64{ch1}, FloodScope: "region:gone"}); err == nil {
		t.Error("a new bot in an unlisted region was accepted")
	}
}

// A contact sending in a region keeps it listed, like a channel does.
func TestRegions_ContactKeepsItsRegionListed(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	c1, _, _, _ := twoCompanions(t, b)
	nz := []store.FloodRegion{{Name: "nz", Parent: "*"}}
	if err := b.SaveFloodRegions(ctx, api.FloodRegionsInput{Regions: &nz, FloodScope: "everywhere"}); err != nil {
		t.Fatal(err)
	}
	peer := make([]byte, 32)
	peer[0] = 0xab
	var werr error
	b.db.WriteSync(func() { werr = b.db.Contacts.Add(ctx, c1, peer, "Wes Tag", "CHAT") })
	if werr != nil {
		t.Fatal(werr)
	}
	if err := b.SetContactFloodScope(ctx, c1, peer, "region:nz"); err != nil {
		t.Fatal(err)
	}
	none := []store.FloodRegion{}
	err := b.SaveFloodRegions(ctx, api.FloodRegionsInput{Regions: &none, FloodScope: "everywhere"})
	if verr := (*api.ValidationError)(nil); !errors.As(err, &verr) || !strings.Contains(err.Error(), "contact Wes Tag") {
		t.Errorf("removing nz while a contact uses it: err = %v, want a validation error naming the contact", err)
	}
}

// An update naming an id that isn't stored is a 404, not a quiet success that wrote nothing.
func TestSaves_MissingIDIsNotFound(t *testing.T) {
	t.Parallel()
	b := newHealthBackend(t)
	ctx := t.Context()
	c1, ch1, _, _ := twoCompanions(t, b)
	sched := "0 * * * *"
	deny := config.TelemetryDeny
	for name, save := range map[string]func() error{
		"companion": func() error {
			_, err := b.SaveCompanion(ctx, api.CompanionInput{ID: 9999, Name: "x", FloodScope: "inherit"})
			return err
		},
		"bot": func() error {
			_, err := b.SaveTrigger(ctx, api.TriggerInput{ID: 9999, CompanionID: c1, Type: "cron", Template: "hi", Schedule: &sched, ChannelIDs: []int64{ch1}, FloodScope: "inherit"})
			return err
		},
		"broker": func() error {
			_, err := b.SaveBroker(ctx, api.BrokerInput{ID: 9999, Name: "x"})
			return err
		},
		"telemetry": func() error {
			return b.SetCompanionTelemetry(ctx, 9999, api.CompanionTelemetryInput{Base: deny, Location: deny, Environment: deny})
		},
	} {
		if serr := (*api.StatusError)(nil); !errors.As(save(), &serr) || serr.Status != http.StatusNotFound {
			t.Errorf("%s with a missing id: want 404", name)
		}
	}

	rb := newHealthBackend(t)
	cfg := config.DefaultConfig()
	cfg.Repeater = &config.RepeaterConfig{Name: "rp", AdminPassword: "pw", Regions: []config.RepeaterRegion{{Name: "*"}}}
	if err := saveConfig(ctx, rb.db, &cfg); err != nil {
		t.Fatal(err)
	}
	if serr := (*api.StatusError)(nil); !errors.As(rb.RemoveRepeaterRegion(ctx, "nope"), &serr) || serr.Status != http.StatusNotFound {
		t.Error("removing an unknown repeater region: want 404")
	}
}
