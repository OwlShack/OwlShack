package companion

import (
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/config"
)

// lastStatus waits for the newest message in channel to carry a status, as it's written asynchronously.
func lastStatus(t *testing.T, c *Companion, channel string) string {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		msgs, err := c.store.Messages.List(t.Context(), c.cfg.ID, channel, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) == 1 && msgs[0].Status != nil && *msgs[0].Status != "sending" || time.Now().After(deadline) {
			if len(msgs) != 1 || msgs[0].Status == nil {
				return ""
			}
			return *msgs[0].Status
		}
	}
}

// A send refused outright gets no result from the library, so its message must not stay "sending" or read as sent.
func TestSend_RefusedMessageIsFailed(t *testing.T) {
	c, radio, _, friend := scopeCompanion(t)
	radio.refuse = true

	friendHex := hex.EncodeToString(friend.PublicKeyBytes())
	if err := c.SendContactMessage(friendHex, "hi"); !errors.Is(err, node.ErrTxQueueFull) {
		t.Fatalf("DM err = %v, want queue full", err)
	}
	if got := lastStatus(t, c, "dm:"+friendHex); got != "failed" {
		t.Errorf("DM status = %q, want failed", got)
	}

	ch, err := channelFromRef(config.ChannelRef{Name: "#westest"})
	if err != nil {
		t.Fatal(err)
	}
	c.node.SetChannel(0, ch)
	if err := c.SendChannelMessage(ch.Name, "hi"); !errors.Is(err, node.ErrTxQueueFull) {
		t.Fatalf("channel err = %v, want queue full", err)
	}
	if got := lastStatus(t, c, ch.Name); got != "failed" {
		t.Errorf("channel status = %q, want failed", got)
	}
	c.pendingOutbound.Lock()
	echoTo := c.pendingOutbound.msgID
	c.pendingOutbound.Unlock()
	if echoTo != 0 {
		t.Errorf("echoes still go to the failed message %d", echoTo)
	}
}
