package trigger

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/OwlShack/OwlShack/internal/logging"
	meshcore "github.com/meshcore-go/meshcore-go"
)

// DirectMessage is a decrypted plain DM the companion has already accepted and persisted.
type DirectMessage struct {
	SenderPubKey string // full hex
	SenderName   string
	Text         string
	Timestamp    uint32
}

type DMTrigger struct {
	cfg      config.TriggerConfig
	botName  string
	patterns []*regexp.Regexp
	contacts []string // lowercased pubkey prefixes or names; empty = every accepted sender
	log      *slog.Logger

	mu       sync.Mutex
	callback Callback
}

func NewDMTrigger(botName string, cfg config.TriggerConfig, log *slog.Logger) (*DMTrigger, error) {
	patterns, err := compilePatterns(cfg.Match)
	if err != nil {
		return nil, err
	}

	var contacts []string
	if cfg.Contacts != nil {
		for _, c := range *cfg.Contacts {
			if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
				contacts = append(contacts, c)
			}
		}
	}

	return &DMTrigger{
		cfg:      cfg,
		botName:  botName,
		patterns: patterns,
		contacts: contacts,
		log:      log.With("trigger", "dm"),
	}, nil
}

// Start only stores the callback: DMs arrive via the companion's persistent TxtMsg handler, because node.OnPacket cannot deregister.
func (t *DMTrigger) Start(_ context.Context, callback Callback) error {
	t.mu.Lock()
	t.callback = callback
	t.mu.Unlock()
	return nil
}

// Stop clears the callback so HandleDirectMessage is a no-op even if the dispatcher still holds this trigger.
func (t *DMTrigger) Stop() error {
	t.mu.Lock()
	t.callback = nil
	t.mu.Unlock()
	return nil
}

// HandleDirectMessage is invoked by the companion for every plain DM the policy accepted.
func (t *DMTrigger) HandleDirectMessage(dm DirectMessage, pkt *meshcore.Packet) {
	t.mu.Lock()
	cb := t.callback
	t.mu.Unlock()
	if cb == nil {
		return // stopped or not yet started
	}

	if !t.listensTo(dm) {
		t.log.Log(context.Background(), logging.LevelTrace, "sender not matched, skipping",
			"sender", dm.SenderName, "listening", t.contacts)
		return
	}

	captures := t.matchesAny(dm.Text)
	if captures == nil {
		t.log.Log(context.Background(), logging.LevelTrace, "no pattern matched",
			"sender", dm.SenderName, "text", dm.Text, "patterns", t.patternStrings())
		return
	}

	t.log.Log(context.Background(), logging.LevelTrace, "trigger matched", "captures", captures)

	data := map[string]any{
		"Sender":       dm.SenderName,
		"SenderPubKey": dm.SenderPubKey,
		"Message":      dm.Text,
		"Match":        captures,
		"Timestamp":    dm.Timestamp,
	}
	if pkt != nil {
		data["SNR"] = pkt.SNR
		data["RSSI"] = pkt.RSSI
		data["Hops"] = pkt.PathHashCount()
		data["PathHashes"] = pkt.PathHashes()
		data["PathHashSize"] = pkt.PathHashSize()
	}

	cb(Event{Type: "dm", BotName: t.botName, Data: data})
}

// listensTo matches a configured entry against either end of the identity, since an operator has the name on screen and the pubkey in the URL.
func (t *DMTrigger) listensTo(dm DirectMessage) bool {
	if len(t.contacts) == 0 {
		return true
	}
	key := strings.ToLower(dm.SenderPubKey)
	name := strings.ToLower(dm.SenderName)
	for _, want := range t.contacts {
		if want == name || strings.HasPrefix(key, want) {
			return true
		}
	}
	return false
}

func (t *DMTrigger) matchesAny(text string) map[string]string {
	return matchCaptures(t.patterns, text)
}

func (t *DMTrigger) patternStrings() []string {
	return patternStrings(t.patterns)
}

var _ Trigger = (*DMTrigger)(nil)
