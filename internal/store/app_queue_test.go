package store

import (
	"context"
	"testing"
)

func appQueueStore(t *testing.T, enabled bool) (*Store, int64) {
	t.Helper()
	st := newTestStore(t)
	c := Companion{Name: "home", AppEnabled: enabled, AppPort: 5000}
	if err := st.Companions.Create(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	return st, c.ID
}

func pop(t *testing.T, st *Store, id int64) []byte {
	t.Helper()
	f, ok, err := st.AppQueue.Pop(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return nil
	}
	return f
}

// Nothing is held for an app that has no access, and turning access off forgets what was waiting.
func TestAppQueue_OnlyWhileAccessIsOn(t *testing.T) {
	ctx := context.Background()
	st, id := appQueueStore(t, false)
	if err := st.AppQueue.Push(ctx, id, false, []byte{16, 1}); err != nil {
		t.Fatal(err)
	}
	if f := pop(t, st, id); f != nil {
		t.Fatalf("queued with access off: % x", f)
	}

	c, _ := st.Companions.Get(ctx, id)
	c.AppEnabled = true
	if err := st.Companions.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	st.AppQueue.Push(ctx, id, false, []byte{16, 2})
	st.AppQueue.Push(ctx, id, true, []byte{17, 3})
	if f := pop(t, st, id); f[1] != 2 {
		t.Fatalf("first out = % x, want the first in", f)
	}
	c.AppEnabled = false
	if err := st.Companions.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.AppEnabled = true
	st.Companions.Update(ctx, c)
	if f := pop(t, st, id); f != nil {
		t.Fatalf("kept across access off: % x", f)
	}
}

// Full, the queue drops its oldest channel message for a new one, and drops the new one when it holds only direct messages (firmware addToOfflineQueue).
func TestAppQueue_Full(t *testing.T) {
	ctx := context.Background()
	st, id := appQueueStore(t, true)
	st.AppQueue.Push(ctx, id, true, []byte{17, 0})
	for i := 1; i < AppQueueMax; i++ {
		st.AppQueue.Push(ctx, id, false, []byte{16, byte(i)})
	}
	st.AppQueue.Push(ctx, id, false, []byte{16, 0xee})
	if f := pop(t, st, id); f[0] != 16 || f[1] != 1 {
		t.Fatalf("head = % x, want the channel message gone", f)
	}
	st.AppQueue.Push(ctx, id, false, []byte{16, 0xef}) // one free slot again
	st.AppQueue.Push(ctx, id, false, []byte{16, 0xff}) // full of direct messages: dropped
	var last []byte
	for f := pop(t, st, id); f != nil; f = pop(t, st, id) {
		last = f
	}
	if last[1] != 0xef {
		t.Fatalf("last = % x, want the message that fitted, not the one past full", last)
	}
}
