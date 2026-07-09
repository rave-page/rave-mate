package seratoremote

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/osc"
)

// server is the TCP listener that accepts Serato's inbound connections (Serato is the TCP
// client; it opens two parallel streams - a heartbeat channel and a control channel) and
// runs one session per connection.
type server struct {
	ln       net.Listener
	topics   []string
	maxFrame int
	debug    bool
	cb       Callbacks
	route    func(osc.Message) // stateful /Status/* router (owned by the Receiver)
	log      *logbus.Bus
	tag      string

	mu       sync.Mutex
	sessions map[*session]struct{}
}

// listen binds host:port and returns the running server plus the actually-bound port.
func listen(ctx context.Context, host string, port int, topics []string, maxFrame int, debug bool, cb Callbacks, route func(osc.Message), log *logbus.Bus) (*server, int, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, 0, err
	}
	s := &server{ln: ln, topics: topics, maxFrame: maxFrame, debug: debug, cb: cb, route: route, log: log, tag: "seratoremote", sessions: map[*session]struct{}{}}
	bound := ln.Addr().(*net.TCPAddr).Port
	debuglog.Go(log, s.tag, func() { s.acceptLoop(ctx) })
	return s, bound, nil
}

func (s *server) acceptLoop(ctx context.Context) {
	go func() { <-ctx.Done(); _ = s.ln.Close() }() // unblock Accept on shutdown
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		sess := newSession(conn, s.topics, s.maxFrame, s.debug, s.cb, s.route, s.log, s.tag)
		s.mu.Lock()
		s.sessions[sess] = struct{}{}
		s.mu.Unlock()
		debuglog.Go(s.log, s.tag, func() {
			sess.run(ctx)
			s.mu.Lock()
			delete(s.sessions, sess)
			s.mu.Unlock()
		})
	}
}

// close stops accepting and tears down all live sessions.
func (s *server) close() {
	_ = s.ln.Close()
	s.mu.Lock()
	for sess := range s.sessions {
		sess.close()
	}
	s.mu.Unlock()
}

// session drives one accepted TCP connection: framing, handshake, ping, and status dispatch.
type session struct {
	conn   net.Conn
	reader *frameReader
	topics []string
	debug  bool
	cb     Callbacks
	route  func(osc.Message)
	log    *logbus.Bus
	tag    string
	addr   string

	writeMu sync.Mutex
	paired  bool
	closed  bool
}

func newSession(conn net.Conn, topics []string, maxFrame int, debug bool, cb Callbacks, route func(osc.Message), log *logbus.Bus, tag string) *session {
	return &session{
		conn:   conn,
		reader: newFrameReader(maxFrame),
		topics: topics,
		debug:  debug,
		cb:     cb,
		route:  route,
		log:    log,
		tag:    tag,
		addr:   conn.RemoteAddr().String(),
	}
}

// run reads the stream until ctx is cancelled or the connection ends. The read buffer is a
// fixed 8 KiB; the frameReader is separately bounded, so nothing here accumulates unbounded.
func (s *session) run(ctx context.Context) {
	defer s.close()
	if s.cb.OnPeerConnected != nil {
		s.cb.OnPeerConnected(s.addr)
	}
	go func() { <-ctx.Done(); s.close() }() // unblock Read on shutdown

	buf := make([]byte, 8<<10)
	for {
		n, err := s.conn.Read(buf)
		if n > 0 {
			msgs, perr := s.reader.push(buf[:n], nil)
			for _, m := range msgs {
				s.dispatch(m)
			}
			if perr != nil {
				if s.debug && s.cb.OnFrame != nil {
					s.cb.OnFrame(FrameEvent{Path: "<parse-error>", Hex: hex.EncodeToString(buf[:n])})
				}
				s.log.Warn(s.tag, "frame error - dropping connection", map[string]any{"peer": s.addr, "err": perr.Error()})
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (s *session) dispatch(m osc.Message) {
	if s.debug && s.cb.OnFrame != nil {
		s.cb.OnFrame(frameEventOf(m))
	}
	switch {
	case m.Address == pathAuthorizeRequest:
		// UNVERIFIED handshake: mirror the request shape back. Best current hypothesis
		// (serato-connect open-questions.md); the live capture may require different int
		// status codes or a transformed blob. Kept faithful to serato-connect server.ts.
		s.write(osc.Msg(pathAuthorizeResponse))
	case m.Address == pathPairingPair:
		s.write(osc.Msg(pathPairingStatus, osc.ArgInt(1))) // StatusChanged shape UNVERIFIED
		for _, t := range s.topics {
			s.write(osc.Msg(t))
		}
		s.paired = true
		if s.cb.OnPaired != nil {
			s.cb.OnPaired(s.addr)
		}
	case m.Address == pathPairingUnpair:
		s.paired = false
		s.close()
	case m.Address == pathPing:
		s.write(osc.Msg(pathPing)) // argless echo; bidirectional per spec §3.4
		if s.cb.OnPing != nil {
			s.cb.OnPing()
		}
	case strings.HasPrefix(m.Address, "/Status/"):
		if s.route != nil {
			s.route(m)
		}
	default:
		// Unknown path - keep the stream alive (may be an ACI/Error/Register echo).
	}
}

func (s *session) write(m osc.Message) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return
	}
	if _, err := s.conn.Write(frame(m)); err != nil && s.debug {
		s.log.Debug(s.tag, "write failed", map[string]any{"peer": s.addr, "path": m.Address, "err": err.Error()})
	}
}

func (s *session) close() {
	s.writeMu.Lock()
	already := s.closed
	if s.paired && !already {
		// best-effort unpair before closing
		_, _ = s.conn.Write(frame(osc.Msg(pathPairingUnpair)))
	}
	s.closed = true
	s.writeMu.Unlock()
	if already {
		return
	}
	_ = s.conn.Close()
	if s.cb.OnPeerDisconnected != nil {
		s.cb.OnPeerDisconnected(s.addr)
	}
}

// frameEventOf renders a decoded message for debug capture logging.
func frameEventOf(m osc.Message) FrameEvent {
	tag := ","
	args := make([]string, 0, len(m.Args))
	for _, a := range m.Args {
		tag += string(a.Kind)
		switch a.Kind {
		case osc.KindInt:
			args = append(args, strconv.Itoa(int(a.Int)))
		case osc.KindFloat:
			args = append(args, strconv.FormatFloat(float64(a.Float), 'g', -1, 32))
		case osc.KindString:
			args = append(args, strconv.Quote(a.Str))
		case osc.KindBlob:
			args = append(args, fmt.Sprintf("blob[%d]=%s", len(a.Blob), hex.EncodeToString(a.Blob)))
		}
	}
	return FrameEvent{Path: m.Address, TypeTag: tag, Args: args}
}
