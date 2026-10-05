package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 024 makes each host-zone time the instant it named, across daylight saving and zones named by digits (Kathmandu, Marquesas), which the driver cannot parse.
func TestMigration024_TimesBecomeUnixMS(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db := dbAt(t, path, 23)
	exec(t, db, `INSERT INTO companions (id, name, private_key, pubkey) VALUES (1, 'c', 'aa', 'k1')`)
	exec(t, db, `INSERT INTO packets (direction, raw, received_at) VALUES
		('rx', x'01', '2026-09-15 12:45:16.78 +1200 NZST m=+446.8'),
		('rx', x'02', '2026-10-05 15:30:41.751992548 +1300 NZDT m=+9824.947467963'),
		('rx', x'03', '2026-10-05 08:13:31.885773359 +0545 +0545 m=+12.5'),
		('rx', x'04', '2026-10-04 17:12:34 -0930 -0930')`)
	exec(t, db, `INSERT INTO discovered_peers (pubkey, name, last_seen) VALUES (x'aa', 'p', '2026-10-05 15:03:52.841988217 +1300 NZDT m=+8216.03')`)
	exec(t, db, `INSERT INTO companion_contacts (companion_id, peer_pubkey, name, last_seen, added_at) VALUES
		(1, x'aa', 'heard', '2026-09-20 08:00:00.5 +1200 NZST', '2026-09-11 00:20:13'),
		(1, x'bb', 'never heard', NULL, '2026-09-11 00:20:14')`)
	exec(t, db, `INSERT INTO messages (id, companion_id, channel, channel_hash, direction, timestamp) VALUES
		(1, 1, 'Public', 0, 'rx', '2026-10-05 15:00:18 +1300 NZDT')`)
	exec(t, db, `INSERT INTO message_echoes (message_id, received_at, path_hashes) VALUES
		(1, '2026-10-05 15:00:23.224428787 +1300 NZDT m=+8006.41', x'01')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	nzst, nzdt := time.FixedZone("NZST", 12*3600), time.FixedZone("NZDT", 13*3600)
	ms := func(y int, mo time.Month, d, h, mi, s, ns int, loc *time.Location) int64 {
		return time.Date(y, mo, d, h, mi, s, ns, loc).UnixMilli()
	}
	for _, tc := range []struct {
		query string
		want  sql.NullInt64
	}{
		{`SELECT received_at FROM packets WHERE raw = x'01'`, sql.NullInt64{Int64: ms(2026, 9, 15, 12, 45, 16, 780e6, nzst), Valid: true}},
		{`SELECT received_at FROM packets WHERE raw = x'02'`, sql.NullInt64{Int64: ms(2026, 10, 5, 15, 30, 41, 751992548, nzdt), Valid: true}},
		{`SELECT received_at FROM packets WHERE raw = x'03'`, sql.NullInt64{Int64: ms(2026, 10, 5, 8, 13, 31, 885773359, time.FixedZone("", 5*3600+45*60)), Valid: true}},
		{`SELECT received_at FROM packets WHERE raw = x'04'`, sql.NullInt64{Int64: ms(2026, 10, 4, 17, 12, 34, 0, time.FixedZone("", -(9*3600+30*60))), Valid: true}},
		{`SELECT last_seen FROM discovered_peers`, sql.NullInt64{Int64: ms(2026, 10, 5, 15, 3, 52, 841988217, nzdt), Valid: true}},
		{`SELECT last_seen FROM companion_contacts WHERE name = 'heard'`, sql.NullInt64{Int64: ms(2026, 9, 20, 8, 0, 0, 500e6, nzst), Valid: true}},
		{`SELECT last_seen FROM companion_contacts WHERE name = 'never heard'`, sql.NullInt64{}},
		{`SELECT added_at FROM companion_contacts WHERE name = 'heard'`, sql.NullInt64{Int64: ms(2026, 9, 11, 0, 20, 13, 0, time.UTC), Valid: true}},
		{`SELECT timestamp FROM messages`, sql.NullInt64{Int64: ms(2026, 10, 5, 15, 0, 18, 0, nzdt), Valid: true}},
		{`SELECT received_at FROM message_echoes`, sql.NullInt64{Int64: ms(2026, 10, 5, 15, 0, 23, 224428787, nzdt), Valid: true}},
	} {
		var got sql.NullInt64
		var typ string
		if err := st.db.QueryRowContext(t.Context(), tc.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.query, err)
		}
		col := strings.Fields(tc.query)[1]
		if err := st.db.QueryRowContext(t.Context(), strings.Replace(tc.query, col, "typeof("+col+")", 1)).Scan(&typ); err != nil {
			t.Fatal(err)
		}
		if got != tc.want || (tc.want.Valid && typ != "integer") {
			t.Errorf("%s = %v (%s), want %v", tc.query, got, typ, tc.want)
		}
	}
}

// A value no layout parses is refused, naming the row, rather than guessed at.
func TestMigration024_RefusesAValueThatIsNotATime(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db := dbAt(t, path, 23)
	exec(t, db, `INSERT INTO packets (direction, raw, received_at) VALUES ('rx', x'01', 'yesterday')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path)
	if err == nil {
		st.Close()
		t.Fatal("Open accepted a received_at of 'yesterday'")
	}
	if !strings.Contains(err.Error(), "packets.received_at row 1") {
		t.Errorf("error %q does not name the row", err)
	}
}

// Every writer of a time column stores an integer: a time.Time passed straight to the driver would store host-zone text again.
func TestTimeColumnsHoldUnixMS(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	ctx := t.Context()
	nz := time.FixedZone("NZDT", 13*3600)
	at := time.Now().In(nz)

	exec(t, st.db, `INSERT INTO companions (id, name, private_key, pubkey) VALUES (1, 'c', 'aa', 'k1')`)
	if err := st.Packets.Insert(ctx, &PacketRecord{ReceivedAt: at, Direction: "rx", Raw: []byte{0x00}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Peers.Upsert(ctx, &Peer{PubKey: []byte{0xaa}, Name: "p", Type: "CHAT", LastSeen: at}); err != nil {
		t.Fatal(err)
	}
	if err := st.Contacts.Add(ctx, 1, []byte{0xaa}, "p", "CHAT"); err != nil {
		t.Fatal(err)
	}
	if err := st.Contacts.RefreshFromAdvert(ctx, []byte{0xaa}, "p", "CHAT", 0, 0, 0, 0, at, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := st.Contacts.Restore(ctx, &Contact{CompanionID: 1, PeerPubKey: []byte{0xbb}, Name: "r", LastSeen: at, AddedAt: at}); err != nil {
		t.Fatal(err)
	}
	m := &Message{CompanionID: 1, Channel: "Public", Direction: "rx", Timestamp: at}
	if err := st.Messages.Insert(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := st.Echoes.Insert(ctx, &MessageEcho{MessageID: m.ID, ReceivedAt: at, PathHashes: []byte{0x01}}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ table, col string }{
		{"packets", "received_at"},
		{"discovered_peers", "last_seen"},
		{"companion_contacts", "last_seen"},
		{"companion_contacts", "added_at"},
		{"messages", "timestamp"},
		{"message_echoes", "received_at"},
	} {
		rows, err := st.db.QueryContext(ctx, "SELECT typeof("+c.col+"), "+c.col+" FROM "+c.table)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var typ string
			var v any
			if err := rows.Scan(&typ, &v); err != nil {
				t.Fatal(err)
			}
			n++
			if ms, _ := v.(int64); typ != "integer" || ms < at.UnixMilli() || ms > time.Now().UnixMilli() {
				t.Errorf("%s.%s holds %s %v, want unix ms from %d on", c.table, c.col, typ, v, at.UnixMilli())
			}
		}
		rows.Close()
		if n == 0 {
			t.Errorf("%s.%s: nothing written", c.table, c.col)
		}
	}
}
