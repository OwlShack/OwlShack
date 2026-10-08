package app

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	repeaterclient "github.com/OwlShack/OwlShack/internal/client/repeater"
	"github.com/OwlShack/OwlShack/internal/monitor"
	"github.com/OwlShack/OwlShack/internal/telemetry"
)

// offAirNode is a repeater we're logged into that has stopped answering, as one out of range or powered off does.
type offAirNode struct {
	session   *repeaterclient.Session
	loginsTry int
	statusTry int
	back      bool // in range again
}

func (n *offAirNode) Session(string) *repeaterclient.Session { return n.session }
func (n *offAirNode) SendLogin(string, string, time.Duration) (*repeaterclient.LoginResult, error) {
	n.loginsTry++
	if n.back {
		return &repeaterclient.LoginResult{Success: true}, nil
	}
	return nil, repeaterclient.ErrNoReply
}
func (n *offAirNode) SendStatusReq(string, time.Duration) (*repeaterclient.Status, error) {
	n.statusTry++
	if n.back {
		return &repeaterclient.Status{}, nil
	}
	return nil, repeaterclient.ErrNoReply
}
func (n *offAirNode) SendTelemetryReq(string, time.Duration) (*telemetry.Telemetry, error) {
	return nil, repeaterclient.ErrNoReply
}
func (n *offAirNode) SendNeighborsReq(string, uint8, uint16, time.Duration) (*repeaterclient.Neighbors, error) {
	return nil, repeaterclient.ErrNoReply
}

// A poll that gets no answer tries one fresh login; pollClient can't log out, and TestSendLogin_UnansweredKeepsTheSession pins that a failed login keeps the one held.
func TestCollect_UnansweredPollKeepsTheLogin(t *testing.T) {
	t.Parallel()
	rc := &repeaterCollector{db: newHealthBackend(t).db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	node := &offAirNode{session: &repeaterclient.Session{IsAdmin: true}}
	_, err := rc.collect(t.Context(), node, monitor.Target{Pubkey: make([]byte, 32), Probes: []string{"status"}}, 1, "rpt")
	if !errors.Is(err, repeaterclient.ErrNoReply) {
		t.Errorf("err = %v, want no reply", err)
	}
	if node.loginsTry != 1 {
		t.Errorf("tried %d fresh logins, want one", node.loginsTry)
	}
}

// Once a fresh login has gone unanswered, each later poll sends one request, alternating status and a login; the operator's login is still kept.
func TestCollect_OffAirNodeCostsOneRequestPerPoll(t *testing.T) {
	t.Parallel()
	rc := &repeaterCollector{db: newHealthBackend(t).db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	node := &offAirNode{session: &repeaterclient.Session{IsAdmin: true}}
	target := monitor.Target{Pubkey: make([]byte, 32), Probes: []string{"status"}}
	poll := func() error {
		_, err := rc.collect(t.Context(), node, target, 1, "rpt")
		return err
	}

	_ = poll()
	if node.statusTry != 1 || node.loginsTry != 1 {
		t.Fatalf("first poll: %d status, %d logins, want 1 and 1", node.statusTry, node.loginsTry)
	}
	_ = poll()
	if node.statusTry != 2 || node.loginsTry != 1 {
		t.Errorf("second poll: %d status, %d logins in all, want only a status (2 and 1)", node.statusTry, node.loginsTry)
	}
	_ = poll()
	if node.statusTry != 2 || node.loginsTry != 2 {
		t.Errorf("third poll: %d status, %d logins in all, want only a login (2 and 2)", node.statusTry, node.loginsTry)
	}

	node.back = true
	if err := poll(); err != nil {
		t.Fatalf("back in range: %v", err)
	}
	node.loginsTry, node.statusTry = 0, 0
	if err := poll(); err != nil || node.loginsTry != 0 || node.statusTry != 1 {
		t.Errorf("after recovering: err=%v %d logins %d status, want a plain status poll", err, node.loginsTry, node.statusTry)
	}
}

// scriptedNode answers a login only when loginWorks, and a status when statusWorks says so.
type scriptedNode struct {
	offAirNode
	loginWorks  bool
	statusWorks func(try int) bool
}

func (n *scriptedNode) SendLogin(string, string, time.Duration) (*repeaterclient.LoginResult, error) {
	n.loginsTry++
	if n.loginWorks {
		return &repeaterclient.LoginResult{Success: true}, nil
	}
	return nil, repeaterclient.ErrNoReply
}
func (n *scriptedNode) SendStatusReq(string, time.Duration) (*repeaterclient.Status, error) {
	n.statusTry++
	if n.statusWorks(n.statusTry) {
		return &repeaterclient.Status{}, nil
	}
	return nil, repeaterclient.ErrNoReply
}

// A stored password the node drops never answers, so polling must go back to the operator's working login rather than retry the password for ever.
func TestCollect_WrongStoredPasswordKeepsUsingTheSession(t *testing.T) {
	t.Parallel()
	rc := &repeaterCollector{db: newHealthBackend(t).db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	node := &scriptedNode{offAirNode: offAirNode{session: &repeaterclient.Session{IsAdmin: true}}, statusWorks: func(try int) bool { return try > 1 }}
	target := monitor.Target{Pubkey: make([]byte, 32), Probes: []string{"status"}}
	if _, err := rc.collect(t.Context(), node, target, 1, "rpt"); err == nil {
		t.Fatal("first poll: want the lost status and unanswered login to fail it")
	}
	for i := 2; i <= 3; i++ {
		if _, err := rc.collect(t.Context(), node, target, 1, "rpt"); err != nil {
			t.Fatalf("poll %d: %v, want the held session to answer", i, err)
		}
	}
	if node.loginsTry != 1 {
		t.Errorf("%d logins, want only the first poll's", node.loginsTry)
	}
}

// A node that forgot us while out of reach answers only a fresh login, so quiet polls must still send one.
func TestCollect_ForgottenSessionGetsALogin(t *testing.T) {
	t.Parallel()
	rc := &repeaterCollector{db: newHealthBackend(t).db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var node *scriptedNode
	node = &scriptedNode{offAirNode: offAirNode{session: &repeaterclient.Session{IsAdmin: true}}, statusWorks: func(int) bool { return node.loginsTry > 1 }}
	target := monitor.Target{Pubkey: make([]byte, 32), Probes: []string{"status"}}
	_, _ = rc.collect(t.Context(), node, target, 1, "rpt") // away: status and login both lost
	node.loginWorks = true
	var err error
	for range 2 {
		if _, err = rc.collect(t.Context(), node, target, 1, "rpt"); err == nil {
			break
		}
	}
	if err != nil {
		t.Errorf("still failing two polls after the node came back: %v", err)
	}
}

// Each companion's client holds its own session, so one companion's unanswered login must not change how another polls the same node.
func TestCollect_QuietIsPerCompanion(t *testing.T) {
	t.Parallel()
	rc := &repeaterCollector{db: newHealthBackend(t).db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	target := monitor.Target{Pubkey: make([]byte, 32), Probes: []string{"status"}}
	_, _ = rc.collect(t.Context(), &offAirNode{session: &repeaterclient.Session{IsAdmin: true}}, target, 1, "rpt")
	healthy := &offAirNode{session: &repeaterclient.Session{IsAdmin: true}, back: true}
	if _, err := rc.collect(t.Context(), healthy, target, 2, "rpt"); err != nil || healthy.loginsTry != 0 {
		t.Errorf("companion 2: err=%v, %d logins, want a plain status poll", err, healthy.loginsTry)
	}
}

// telemetryNode answers telemetry on a held session and drops every login.
type telemetryNode struct{ scriptedNode }

func (n *telemetryNode) SendTelemetryReq(string, time.Duration) (*telemetry.Telemetry, error) {
	if n.session == nil {
		return nil, repeaterclient.ErrNoReply
	}
	return &telemetry.Telemetry{}, nil
}

// With status polling off, any probe the held session answers ends the quiet spell, or odd polls would keep failing on a dropped login.
func TestCollect_AnsweredProbeEndsQuiet(t *testing.T) {
	t.Parallel()
	rc := &repeaterCollector{db: newHealthBackend(t).db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	node := &telemetryNode{scriptedNode{statusWorks: func(int) bool { return false }}}
	target := monitor.Target{Pubkey: make([]byte, 32), Probes: []string{"telemetry"}}
	if _, err := rc.collect(t.Context(), node, target, 1, "rpt"); err == nil {
		t.Fatal("first poll: want the unanswered login to fail it")
	}
	node.session = &repeaterclient.Session{IsAdmin: true} // the operator logs in from the UI
	for i := 2; i <= 4; i++ {
		if _, err := rc.collect(t.Context(), node, target, 1, "rpt"); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	if node.loginsTry != 1 {
		t.Errorf("%d logins, want only the first poll's", node.loginsTry)
	}
}

// With the status probe off, a poll nothing answers is a failure, and the node then gets a fresh login as with status.
func TestCollect_NoProbeAnsweredFails(t *testing.T) {
	t.Parallel()
	rc := &repeaterCollector{db: newHealthBackend(t).db, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	node := &offAirNode{session: &repeaterclient.Session{IsAdmin: true}}
	target := monitor.Target{Pubkey: make([]byte, 32), Probes: []string{"telemetry", "neighbors"}}
	for poll := 1; poll <= 3; poll++ {
		if _, err := rc.collect(t.Context(), node, target, 1, "rpt"); !errors.Is(err, repeaterclient.ErrNoReply) {
			t.Fatalf("poll %d: err = %v, want no reply", poll, err)
		}
	}
	if node.loginsTry == 0 {
		t.Error("a node that stopped answering never got a fresh login")
	}
}
