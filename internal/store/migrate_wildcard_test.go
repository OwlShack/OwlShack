package store

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

// 026: a missing "*" now allows unscoped flood, so a repeater that had none gets an explicit deny and relays exactly as before.
func TestMigration026_WildcardMadeExplicit(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, before string
		want         []RepeaterRegion
	}{
		{"no regions", `[]`, []RepeaterRegion{{Name: "*", DenyFlood: true}}},
		{"named only", `[{"name":"nz"}]`, []RepeaterRegion{{Name: "nz"}, {Name: "*", DenyFlood: true}}},
		{"* allowed kept", `[{"name":"*"},{"name":"nz"}]`, []RepeaterRegion{{Name: "*"}, {Name: "nz"}}},
		{"* denied kept", `[{"name":"nz","denyFlood":true},{"name":"*","denyFlood":true}]`, []RepeaterRegion{{Name: "nz", DenyFlood: true}, {Name: "*", DenyFlood: true}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "upgrade.db")
			db := dbAt(t, path, 25)
			exec(t, db, `INSERT INTO repeater (id, name, regions) VALUES (1, 'r', ?)`, c.before)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var raw string
			if err := st.db.QueryRow(`SELECT regions FROM repeater`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var got []RepeaterRegion
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("regions %q: %v", raw, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("regions after 026 = %+v, want %+v", got, c.want)
			}
		})
	}
}
