package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// 027: the firmware's default scope is one of its regions or none, so an unset one is everywhere and a set one is created if missing.
func TestMigration027_RepeaterDefaultRegionBecomesScope(t *testing.T) {
	t.Parallel()
	for i, c := range []struct{ before, regions, want, wantRegions string }{
		{"", `[]`, "everywhere", ""},
		{"*", `[]`, "everywhere", ""},
		{"nz", `[]`, "region:nz", "nz"}, // the firmware auto-creates the default region
		{"nz", `[{"name":"nz","parent":"*","denyFlood":true}]`, "region:nz", "nz"},
		{"#nz", `[{"name":"#nz","parent":"*"}]`, "region:nz", "#nz"}, // v1.5.0 allowed "#"; the scope validator does not, and keys it the same as nz
		{"$priv", `[]`, "everywhere", ""},                            // no key here, so it already sent unscoped
		{"#", `[]`, "everywhere", ""},                                // would leave an empty region name
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "upgrade.db")
			db := dbAt(t, path, 26)
			exec(t, db, `INSERT INTO repeater (id, name, default_region, regions) VALUES (1, 'r', ?, ?)`, c.before, c.regions)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			rep, err := st.Repeater.Get(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if rep.FloodScope != c.want {
				t.Errorf("floodScope after 027 = %q, want %q", rep.FloodScope, c.want)
			}
			var names []string
			for _, rg := range rep.Regions {
				names = append(names, rg.Name)
			}
			if got := strings.Join(names, ","); got != c.wantRegions {
				t.Errorf("regions after 027 = %q, want %q", got, c.wantRegions)
			}
		})
	}
}

// The CHECKs are the contract the API relies on: a malformed scope never reaches a row.
func TestFloodScopeColumnsRefuseMalformed(t *testing.T) {
	st := newTestStore(t)
	id := mkCompanion(t, st, "c")
	for _, bad := range []string{"", "region:", "nz", "Inherit", "REGION:nz"} {
		if _, err := st.db.Exec(`UPDATE companions SET flood_scope = ? WHERE id = ?`, bad, id); err == nil {
			t.Errorf("companions accepted flood_scope %q", bad)
		}
	}
	if _, err := st.db.Exec(`UPDATE companions SET flood_scope = 'region:nz' WHERE id = ?`, id); err != nil {
		t.Errorf("companions refused region:nz: %v", err)
	}
	if err := st.Settings.Set(t.Context(), &Settings{MapProvider: "osm", MapDarkStyle: "original", PacketRetentionDays: 7, FloodScope: "inherit"}); err == nil {
		t.Error("settings accepted inherit, but it is the top of the cascade")
	}
}
