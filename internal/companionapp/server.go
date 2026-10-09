// Package companionapp lets the MeshCore app use a companion over TCP, as the firmware's WiFi build does.
// The app protocol has no password, so only the commands in handlers.go are served.
package companionapp

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	mcompanion "github.com/OwlShack/meshcore-go/companion"

	"github.com/OwlShack/OwlShack/internal/node/companion"
	"github.com/OwlShack/OwlShack/internal/store"
)

// Status is what the companion's page shows about its app access.
type Status struct {
	Port      int
	Listening bool
	// Error is why the port could not be opened.
	Error  string
	Client string
	Since  time.Time
}

// Server listens on one companion's port and serves one app at a time; a new connection replaces the old, as on the firmware.
type Server struct {
	port int
	id   int64
	st   *store.Store
	log  *slog.Logger
	ln   net.Listener
	err  error

	comp atomic.Pointer[companion.Companion]
	env  atomic.Pointer[Env]

	mu   sync.Mutex
	conn *conn
	done chan struct{}
	wg   sync.WaitGroup

	tags      atomic.Uint32
	flightsMu sync.Mutex
	flights   map[string]flight

	settings Settings
	// rx is what the radio has heard while this port was open, as the firmware's radio stats count it.
	rx struct {
		flood, direct, airMs atomic.Uint64
		lastSNR              atomic.Uint32 // float32 bits
		lastRSSI             atomic.Int32
	}
}

// flight is an app DM still being tried, so the app's own resend of it is not sent twice.
type flight struct {
	tag   uint32
	flood bool
	until time.Time
}

// nextTag is the ack code the app is given for a DM; the app only matches it against the confirmation that follows.
func (s *Server) nextTag() uint32 { return s.tags.Add(1) }

func (s *Server) inFlight(key string) (flight, bool) {
	s.flightsMu.Lock()
	defer s.flightsMu.Unlock()
	f, ok := s.flights[key]
	if ok && time.Now().After(f.until) {
		delete(s.flights, key)
		return f, false
	}
	return f, ok
}

func (s *Server) startFlight(key string, f flight) {
	s.flightsMu.Lock()
	s.flights[key] = f
	s.flightsMu.Unlock()
}

func (s *Server) doneFlight(key string) {
	s.flightsMu.Lock()
	delete(s.flights, key)
	s.flightsMu.Unlock()
}

// Start opens the port and serves c; a port that cannot be opened is reported by Status, not returned.
func Start(host string, port int, c *companion.Companion, env Env, st *store.Store, settings Settings, log *slog.Logger) *Server {
	s := &Server{port: port, id: c.ID(), st: st, settings: settings, done: make(chan struct{}), flights: map[string]flight{},
		log: log.With("component", "app", "companionId", c.ID(), "companion", c.Name(), "port", port)}
	var seed [4]byte
	rand.Read(seed[:])
	s.tags.Store(binary.LittleEndian.Uint32(seed[:]))
	s.env.Store(&env)
	s.Use(c)
	s.ln, s.err = net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if s.err != nil {
		s.log.Error("app port not opened", "error", s.err)
		return s
	}
	s.log.Info("MeshCore app access on")
	s.wg.Add(1)
	go s.accept()
	return s
}

// Use rebinds the server to a rebuilt companion; if the rebuild moved a channel to another slot the app is dropped, so it reconnects and reads them again.
func (s *Server) Use(c *companion.Companion) {
	old := s.comp.Swap(c)
	c.SetAppSink(s)
	if old != nil && old != c && !slices.Equal(old.ChannelSlots(), c.ChannelSlots()) {
		s.log.Info("channels moved slots in a rebuild, dropping the app so it reads them again")
		s.Disconnect()
	}
}

// SetEnv refreshes what the handshake reports after a config reload.
func (s *Server) SetEnv(env Env) { s.env.Store(&env) }

func (s *Server) Port() int { return s.port }

// Stop closes the port and the app's connection and detaches from the companion.
func (s *Server) Stop() {
	if c := s.comp.Load(); c != nil {
		c.SetAppSink(nil)
	}
	close(s.done)
	if s.ln != nil {
		s.ln.Close()
	}
	s.Disconnect()
	s.wg.Wait()
	if s.ln != nil {
		s.log.Info("MeshCore app access off")
	}
}

func (s *Server) Status() Status {
	st := Status{Port: s.port, Listening: s.ln != nil}
	if s.err != nil {
		st.Error = s.err.Error()
	}
	s.mu.Lock()
	if s.conn != nil {
		st.Client, st.Since = s.conn.remote, s.conn.since
	}
	s.mu.Unlock()
	return st
}

// Disconnect drops the connected app, if any.
func (s *Server) Disconnect() {
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		c.close()
	}
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			s.log.Warn("app accept failed", "error", err)
			time.Sleep(time.Second)
			continue
		}
		if tc, ok := nc.(*net.TCPConn); ok {
			tc.SetNoDelay(true)
			tc.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3})
		}
		c := &conn{nc: nc, remote: nc.RemoteAddr().String(), since: time.Now(), srv: s, out: make(chan []byte, outQueue), closed: make(chan struct{})}
		s.mu.Lock()
		// Stop closes done before it takes mu, so a connection accepted as it stops is closed here rather than left for it to wait on.
		select {
		case <-s.done:
			s.mu.Unlock()
			nc.Close()
			return
		default:
		}
		old := s.conn
		s.conn = c
		s.mu.Unlock()
		if old != nil {
			s.log.Info("MeshCore app replaced by a new connection", "old", old.remote, "new", c.remote)
			old.close()
		}
		s.log.Info("MeshCore app connected", "from", c.remote)
		s.wg.Add(2)
		go func() {
			defer s.wg.Done()
			c.write()
		}()
		go func() {
			defer s.wg.Done()
			err := c.serve()
			s.mu.Lock()
			if s.conn == c {
				s.conn = nil
			}
			s.mu.Unlock()
			c.close()
			s.log.Info("MeshCore app disconnected", "from", c.remote, "reason", err)
		}()
	}
}

// push offers an unsolicited frame to whichever app is connected now; an app not keeping up misses it, as on the firmware.
func (s *Server) push(frame []byte) {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	if c != nil {
		c.offer(frame)
	}
}

func (s *Server) connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

// conn is one app's session; what the firmware keeps per app (its protocol version, a send scope) lives here.
type conn struct {
	nc     net.Conn
	remote string
	since  time.Time
	srv    *Server

	out     chan []byte
	closed  chan struct{}
	closeMu sync.Once

	session session
}

// outQueue is how many frames may wait for a slow app before more are dropped, as the firmware drops them.
const outQueue = 256

var errFrameTooBig = errors.New("frame larger than the firmware accepts")

// serve reads '<' frames and answers each in turn; bytes before a frame start are skipped as the firmware skips them.
func (c *conn) serve() error {
	r := bufio.NewReader(c.nc)
	var hdr [mcompanion.FrameHeaderSize]byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return err
		}
		if b != mcompanion.FrameTypeOutgoing {
			continue
		}
		hdr[0] = b
		if _, err := io.ReadFull(r, hdr[1:]); err != nil {
			return err
		}
		n := int(binary.LittleEndian.Uint16(hdr[1:]))
		if n > mcompanion.MaxFrameSize {
			return fmt.Errorf("%w: %d bytes", errFrameTooBig, n)
		}
		if n == 0 {
			continue
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		if err := c.safeHandle(data); err != nil {
			return err
		}
	}
}

// safeHandle keeps a frame that trips a handler from taking the whole process down with it; the app is dropped instead.
func (c *conn) safeHandle(data []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			c.srv.log.Error("app command failed", "code", data[0], "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("command 0x%02x failed", data[0])
		}
	}()
	c.handle(data)
	return nil
}

// send queues a reply to the app's command, waiting for room: the app waits on every reply, so none may be dropped.
func (c *conn) send(data []byte) {
	if frame := c.frame(data); frame != nil {
		select {
		case c.out <- frame:
		case <-c.closed:
		}
	}
}

// offer queues a push without waiting; over a full queue it is dropped.
func (c *conn) offer(data []byte) {
	if frame := c.frame(data); frame != nil {
		select {
		case c.out <- frame:
		case <-c.closed:
		default:
			c.srv.log.Warn("app is not keeping up, push dropped", "code", data[0])
		}
	}
}

// frame is a '>' frame, or nil for one the firmware could not send either.
func (c *conn) frame(data []byte) []byte {
	frame, err := mcompanion.FrameEncode(mcompanion.FrameTypeIncoming, data)
	if err != nil {
		c.srv.log.Warn("app frame not sent", "code", data[0], "error", err)
		return nil
	}
	return frame
}

func (c *conn) write() {
	for {
		select {
		case <-c.closed:
			return
		case frame := <-c.out:
			c.nc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := c.nc.Write(frame); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *conn) close() {
	c.closeMu.Do(func() {
		close(c.closed)
		c.nc.Close()
	})
}
