package repeater

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	meshcore "github.com/OwlShack/meshcore-go"
	"github.com/OwlShack/meshcore-go/node"

	"github.com/OwlShack/OwlShack/internal/store"
	"github.com/OwlShack/OwlShack/internal/telemetry"
)

// Each way a request can fail before it reaches the node carries its kind, which the API answers with a status.
func TestSendCLI_FailuresCarryTheirKind(t *testing.T) {
	t.Parallel()
	self := meshcore.NewLocalIdentityFromSeed([32]byte{0x11})
	rpt := meshcore.NewLocalIdentityFromSeed([32]byte{0x22})
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := node.New(self, captureRadio{sent: make(chan []byte, 4)})
	t.Cleanup(n.Stop)
	rm := NewClient(n, st, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, func() uint8 { return 1 }, func([]byte) *meshcore.Region { return nil })
	pk := hex.EncodeToString(rpt.PublicKeyBytes())

	check := func(stage string, pubkey string, want error) {
		t.Helper()
		if _, err := rm.SendCLI(pubkey, "ver", 0); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", stage, err, want)
		}
	}
	check("not hex", "zz", ErrBadPubkey)
	check("not in the peer table", pk, ErrUnknownPeer)
	n.Peers().Insert(&node.Peer{Identity: rpt.Identity, Name: "rpt"})
	check("no login", pk, ErrNotLoggedIn)
	rm.mu.Lock()
	rm.sessions[pk] = &Session{sharedSecret: []byte{1}}
	rm.mu.Unlock()
	check("guest login", pk, ErrNotAdmin)
}

// A login that gets no answer leaves the login already held, so the monitor's retry can't log an operator out.
func TestSendLogin_UnansweredKeepsTheSession(t *testing.T) {
	t.Parallel()
	self := meshcore.NewLocalIdentityFromSeed([32]byte{0x11})
	rpt := meshcore.NewLocalIdentityFromSeed([32]byte{0x22})
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	n := node.New(self, captureRadio{sent: make(chan []byte, 4)})
	t.Cleanup(n.Stop)
	rm := NewClient(n, st, 0, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, func() uint8 { return 1 }, func([]byte) *meshcore.Region { return nil })
	pk := hex.EncodeToString(rpt.PublicKeyBytes())
	n.Peers().Insert(&node.Peer{Identity: rpt.Identity, Name: "rpt"})
	held := &Session{PubKeyHex: pk, IsAdmin: true, sharedSecret: []byte{1}}
	rm.mu.Lock()
	rm.sessions[pk] = held
	rm.mu.Unlock()

	if _, err := rm.SendLogin(pk, "pw", 50*time.Millisecond); !errors.Is(err, ErrNoReply) {
		t.Fatalf("err = %v, want no reply", err)
	}
	if rm.Session(pk) != held {
		t.Error("an unanswered login dropped the session already held")
	}
}

// A telemetry reply that arrives but will not parse is the node's bad answer, not a fault here.
func TestBadReply_TelemetryThatWillNotParse(t *testing.T) {
	junk := []byte{0x01, 0xEE, 0x00}
	if _, err := badReply(telemetry.Parse(junk)); !errors.Is(err, ErrBadReply) {
		t.Errorf("Parse: err = %v, want ErrBadReply", err)
	}
	if _, err := badReply(telemetry.ParseSeries(junk)); !errors.Is(err, ErrBadReply) {
		t.Errorf("ParseSeries: err = %v, want ErrBadReply", err)
	}
}
