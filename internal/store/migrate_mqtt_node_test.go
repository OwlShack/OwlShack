package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

// 028: a companion feed that named no node spoke as the first companion; the migration writes that choice down.
func TestMigration028_MqttFeedNamesItsCompanion(t *testing.T) {
	t.Parallel()
	for i, c := range []struct {
		companions []int64
		stored     *int64
		want       *int64
	}{
		{[]int64{5, 3}, nil, i64ptr(3)},
		{[]int64{5, 3}, i64ptr(5), i64ptr(5)},
		{nil, nil, nil},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "upgrade.db")
			db := dbAt(t, path, 27)
			for _, id := range c.companions {
				exec(t, db, `INSERT INTO companions (id, name, private_key) VALUES (?, ?, '')`, id, fmt.Sprint("c", id))
			}
			exec(t, db, `INSERT INTO mqtt_settings (id, node_companion_id) VALUES (1, ?)`, c.stored)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			m, err := st.Mqtt.Get(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if (m.NodeCompanionID == nil) != (c.want == nil) || m.NodeCompanionID != nil && *m.NodeCompanionID != *c.want {
				t.Errorf("node after 028 = %v, want %v", m.NodeCompanionID, c.want)
			}
			if m.NodeKind != "companion" {
				t.Errorf("kind after 028 = %q, want companion", m.NodeKind)
			}
		})
	}
}
