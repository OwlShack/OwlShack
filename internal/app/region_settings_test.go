package app

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OwlShack/OwlShack/internal/api"
	"github.com/OwlShack/OwlShack/internal/store"
)

func regionBackend(t *testing.T) (*backend, *store.Store) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := initConfigTables(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	return &backend{db: st}, st
}

// The Settings form saves the radio fields and the regions through two endpoints, so the first must carry the second's values through.
func TestSaveSettings_KeepsRegionsAndDefault(t *testing.T) {
	b, st := regionBackend(t)
	regions := []store.FloodRegion{{Name: "nz", Parent: "*"}, {Name: "akl", Parent: "nz"}}
	if err := b.SaveFloodRegions(t.Context(), api.FloodRegionsInput{Regions: &regions, FloodScope: "region:akl"}); err != nil {
		t.Fatal(err)
	}
	days := 30
	if err := b.SaveSettings(t.Context(), api.SettingsInput{PacketRetentionDays: &days}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Settings.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.FloodRegions, regions) || got.FloodScope != "region:akl" {
		t.Errorf("after a Settings save: regions %+v scope %q, want them kept", got.FloodRegions, got.FloodScope)
	}
}

func TestSaveFloodRegions_RefusesBadInput(t *testing.T) {
	b, _ := regionBackend(t)
	list := func(r ...store.FloodRegion) *[]store.FloodRegion { return &r }
	for name, in := range map[string]api.FloodRegionsInput{
		"no regions":      {FloodScope: "everywhere"},
		"no default":      {Regions: list()},
		"inherit default": {Regions: list(), FloodScope: "inherit"},
		"wildcard name":   {Regions: list(store.FloodRegion{Name: "*", Parent: "*"}), FloodScope: "everywhere"},
		"hash name":       {Regions: list(store.FloodRegion{Name: "#nz", Parent: "*"}), FloodScope: "everywhere"},
		"no parent":       {Regions: list(store.FloodRegion{Name: "nz"}), FloodScope: "everywhere"},
		"unlisted parent": {Regions: list(store.FloodRegion{Name: "akl", Parent: "nz"}), FloodScope: "everywhere"},
		"listed twice":    {Regions: list(store.FloodRegion{Name: "nz", Parent: "*"}, store.FloodRegion{Name: "nz", Parent: "*"}), FloodScope: "everywhere"},
		"loop":            {Regions: list(store.FloodRegion{Name: "a", Parent: "b"}, store.FloodRegion{Name: "b", Parent: "a"}), FloodScope: "everywhere"},
		"malformed scope": {Regions: list(), FloodScope: "nz"},
	} {
		if err := b.SaveFloodRegions(t.Context(), in); err == nil {
			t.Errorf("%s: saved, want refused", name)
		}
	}
	if err := b.SaveFloodRegions(t.Context(), api.FloodRegionsInput{Regions: list(), FloodScope: "everywhere"}); err != nil {
		t.Errorf("empty list: %v", err)
	}
}

func TestSetContactFloodScope_RefusesBadInput(t *testing.T) {
	b, _ := regionBackend(t)
	if err := b.SetContactFloodScope(t.Context(), 1, make([]byte, 32), "nz"); err == nil {
		t.Error("malformed scope accepted")
	}
	if err := b.SetContactFloodScope(t.Context(), 1, make([]byte, 32), "everywhere"); !errors.Is(err, store.ErrNotContact) {
		t.Errorf("unknown contact: got %v, want ErrNotContact", err)
	}
}
