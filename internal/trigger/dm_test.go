package trigger

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/OwlShack/OwlShack/internal/config"
)

func newDMTrigger(t *testing.T, cfg config.TriggerConfig) (*DMTrigger, *[]Event) {
	t.Helper()
	tr, err := NewDMTrigger("bot", cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewDMTrigger: %v", err)
	}
	var fired []Event
	if err := tr.Start(context.Background(), func(e Event) { fired = append(fired, e) }); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return tr, &fired
}

func TestDMTrigger_ContactFilter(t *testing.T) {
	t.Parallel()

	sender := DirectMessage{SenderPubKey: "AABBCCDDEEFF0011", SenderName: "Alice", Text: "ping"}

	tests := []struct {
		name     string
		contacts []string
		want     int
	}{
		{"no contacts listens to every accepted sender", nil, 1},
		{"a pubkey prefix matches", []string{"aabbcc"}, 1},
		{"the full pubkey matches", []string{"AABBCCDDEEFF0011"}, 1},
		{"a name matches, case-insensitively", []string{"alice"}, 1},
		{"an unlisted sender is skipped", []string{"bob"}, 0},
		{"a partial name is not a match, unlike a pubkey prefix", []string{"ali"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := config.TriggerConfig{Type: "dm", Template: "pong"}
			if tt.contacts != nil {
				cfg.Contacts = &tt.contacts
			}
			tr, fired := newDMTrigger(t, cfg)
			tr.HandleDirectMessage(sender, nil)
			if len(*fired) != tt.want {
				t.Errorf("fired %d events, want %d", len(*fired), tt.want)
			}
		})
	}
}

func TestDMTrigger_MatchCaptures(t *testing.T) {
	t.Parallel()

	patterns := []string{`^!weather (?P<place>\w+)$`}
	tr, fired := newDMTrigger(t, config.TriggerConfig{
		Type: "dm", Template: "x", Match: &patterns,
	})

	tr.HandleDirectMessage(DirectMessage{SenderPubKey: "aa", SenderName: "Alice", Text: "hello"}, nil)
	if len(*fired) != 0 {
		t.Fatalf("a non-matching DM fired %d events", len(*fired))
	}

	tr.HandleDirectMessage(DirectMessage{SenderPubKey: "aa", SenderName: "Alice", Text: "!weather nelson"}, nil)
	if len(*fired) != 1 {
		t.Fatalf("a matching DM fired %d events, want 1", len(*fired))
	}
	evt := (*fired)[0]
	if evt.Type != "dm" {
		t.Errorf("event type = %q, want dm", evt.Type)
	}
	captures, _ := evt.Data["Match"].(map[string]string)
	if captures["place"] != "nelson" {
		t.Errorf("capture place = %q, want nelson", captures["place"])
	}
	// The reply needs the full key: a DM cannot be addressed by name.
	if evt.Data["SenderPubKey"] != "aa" {
		t.Errorf("SenderPubKey = %v, want aa", evt.Data["SenderPubKey"])
	}
}

// A stopped trigger must not fire: the dispatcher still holds it until the reload swaps the set.
func TestDMTrigger_StopSilencesIt(t *testing.T) {
	t.Parallel()

	tr, fired := newDMTrigger(t, config.TriggerConfig{Type: "dm", Template: "x"})
	if err := tr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	tr.HandleDirectMessage(DirectMessage{SenderPubKey: "aa", SenderName: "Alice", Text: "ping"}, nil)
	if len(*fired) != 0 {
		t.Errorf("a stopped trigger fired %d events", len(*fired))
	}
}
