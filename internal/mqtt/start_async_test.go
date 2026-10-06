package mqtt

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/OwlShack/OwlShack/internal/config"
	"github.com/meshcore-go/meshcore-go/node"
)

// listenBlackhole accepts connections and never answers, which is what a firewalled or wedged broker
// looks like. A refused port fails instantly and so would not exercise the connect timeout at all.
// The close func is returned rather than registered as a cleanup: callers must tear the listener down
// BEFORE waiting for any dial to unwind, and t.Cleanup runs LIFO, which would give the wrong order.
func listenBlackhole(t *testing.T) (host string, port int, closeAll func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c) // kept open, never written to
			mu.Unlock()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	}
}

// Start must not dial on the caller's goroutine. connectBroker blocks up to connectWaitTimeout, and
// Start is called from companion.Start — so a wedged broker used to cost every companion that long on
// boot and on every SIGHUP reload, with the mesh node not yet running.
//
// The retry goroutines Start spawns MUST be gone before this returns. They sit inside connectBroker
// against a broker that never answers, reading the package backoff vars, so ANY later test that calls
// shrinkBackoff races with them across test boundaries — not calling it here is not sufficient.
func TestStart_DoesNotBlockOnUnreachableBrokers(t *testing.T) {
	host, port, closeBlackhole := listenBlackhole(t)

	o := testObserver(t)
	o.radio = (&node.RadioMux{}).NewRadio()
	var brokers []config.BrokerConfig
	for i := 1; i <= 3; i++ {
		brokers = append(brokers, config.BrokerConfig{
			Name: "wedged-" + strconv.Itoa(i), Host: host, Port: port,
			Transport: "tcp", AuthType: "none", Enabled: true,
		})
	}
	o.cfg = config.MqttConfig{Brokers: brokers}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	if err := o.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	elapsed := time.Since(start)

	// Three wedged brokers dialled serially would be 3 x connectWaitTimeout.
	if elapsed > connectWaitTimeout/4 {
		t.Errorf("Start blocked for %s with 3 unreachable brokers; it must dial off the caller goroutine", elapsed)
	}

	// Registered anyway, so an unreachable broker stays visible and keeps retrying.
	live := o.brokerList()
	if len(live) != 3 {
		t.Errorf("registered %d brokers, want 3", len(live))
	}

	// Drop the listener first so the in-flight dials fail immediately instead of burning the full
	// connect timeout, then stop the loops and wait for every one of them to actually be gone.
	closeBlackhole()
	cancel()
	o.Stop()
	for _, bc := range live {
		waitFor(t, "retry loop for "+bc.cfg.Name+" to exit", 15*time.Second,
			func() bool { return !bc.retrying.Load() })
	}
}
