package store

import (
	"path/filepath"
	"testing"
	"time"
)

// A sender's clock can be years out, so every message also carries our own receive time, and a message without one is refused rather than stored as 1 AD.
func TestMessages_ReceivedAtIsRequired(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")
	sent := time.Date(2024, 5, 15, 10, 56, 25, 0, time.UTC)

	if err := st.Messages.Insert(t.Context(), &Message{CompanionID: cid, Channel: "public", Direction: "rx", Timestamp: sent}); err == nil {
		t.Fatal("Insert accepted a message with no receive time")
	}
	got := time.Now().Truncate(time.Millisecond)
	m := &Message{CompanionID: cid, Channel: "public", Direction: "rx", Timestamp: sent, ReceivedAt: got}
	if err := st.Messages.Insert(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	back, err := st.Messages.GetByID(t.Context(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Timestamp.Equal(sent) || !back.ReceivedAt.Equal(got) {
		t.Errorf("read back sent %v received %v, want %v and %v", back.Timestamp, back.ReceivedAt, sent, got)
	}
}

// A thread was last active when we last received in it, not when a sender's clock says it wrote.
func TestConversations_LastActiveIsReceiveTime(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")
	got := time.Now().Truncate(time.Millisecond)
	m := &Message{CompanionID: cid, Channel: "Public", Direction: "rx", Sender: "far", Text: "hi",
		Timestamp: time.Date(2024, 5, 15, 10, 56, 25, 0, time.UTC), ReceivedAt: got}
	if err := st.Messages.Insert(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	convos, err := st.Conversations.List(t.Context(), cid, []string{"Public"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(convos) != 1 || !convos[0].LastActive.Equal(got) {
		t.Fatalf("conversations %+v, want Public last active at %v", convos, got)
	}
}

// Backup day windows keep what we received in them, whatever the sender's clock said.
func TestBackup_MessageWindowIsReceiveTime(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	cid := mkCompanion(t, st, "alpha")
	now := time.Now()
	for _, m := range []*Message{
		{Text: "sender two years behind, received yesterday", Timestamp: now.AddDate(-2, 0, 0), ReceivedAt: now.AddDate(0, 0, -1)},
		{Text: "sender says now, received 40 days ago", Timestamp: now, ReceivedAt: now.AddDate(0, 0, -40)},
	} {
		m.CompanionID, m.Channel, m.Direction = cid, "Public", "rx"
		if err := st.Messages.Insert(t.Context(), m); err != nil {
			t.Fatal(err)
		}
	}
	opts := PruneOptions{CompanionIDs: []int64{cid}, MessageDays: 7}
	counts, err := st.CountForBackup(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Messages != 1 {
		t.Errorf("a 7-day backup counts %d messages, want the one received yesterday", counts.Messages)
	}

	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := st.BackupTo(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	if err := PruneBackup(t.Context(), backup, opts); err != nil {
		t.Fatal(err)
	}
	db, err := openWritableDB(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var kept string
	if err := db.QueryRow(`SELECT group_concat(text) FROM messages`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != "sender two years behind, received yesterday" {
		t.Errorf("the 7-day backup kept %q", kept)
	}
}

// 025 gives every existing message a receive time, the sent time being the best record of when it came in.
func TestMigration025_ReceivedAtFromTimestamp(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db := dbAt(t, path, 24)
	exec(t, db, `INSERT INTO companions (id, name, private_key, pubkey) VALUES (1, 'c', 'aa', 'k1')`)
	exec(t, db, `INSERT INTO messages (companion_id, channel, channel_hash, direction, timestamp) VALUES (1, 'Public', 0, 'rx', 1715770585000)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var received int64
	if err := st.db.QueryRow(`SELECT received_at FROM messages`).Scan(&received); err != nil {
		t.Fatal(err)
	}
	if received != 1715770585000 {
		t.Errorf("received_at %d, want the timestamp 1715770585000", received)
	}
}
