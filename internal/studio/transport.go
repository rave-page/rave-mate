package studio

import (
	"context"

	"github.com/coder/websocket"
)

// Conn is the framed message transport a studio session runs over. The protocol above it is
// pure []byte-in / map-out and was designed relay-ready from the start ("authTag proves ECDH
// possession (anti-MITM on a relay)", session.go) - this interface is what finally lets it run
// somewhere other than the loopback socket:
//
//	loopback WS   the web app on this machine (wsConn, below)
//	account bridge  the web app on ANY machine, via the rave.page relay (internal/bridge.Conn)
//	in-memory pipe  tests
//
// The protocol itself is NOT forked for the relay: the same handshake, the same per-frame HMAC,
// the same mutual /auth/me identity match, byte for byte.
type Conn interface {
	Send(ctx context.Context, b []byte) error
	Recv(ctx context.Context) ([]byte, error)
	// Close ends the session with a studio close code (protocol.go, 4001-4007).
	Close(code int, reason string)
}

// Transport names how a session reached us. Sent verbatim in handshake-ok, so the web client
// can tell a local channel from one crossing the internet.
const (
	TransportLoopback = "loopback"
	TransportBridge   = "bridge"
)

// wsConn adapts the loopback websocket to Conn - the original transport, unchanged.
type wsConn struct {
	ws *websocket.Conn
}

func newWSConn(ws *websocket.Conn) *wsConn {
	ws.SetReadLimit(maxPayload)
	return &wsConn{ws: ws}
}

func (w *wsConn) Send(ctx context.Context, b []byte) error {
	return w.ws.Write(ctx, websocket.MessageText, b)
}

func (w *wsConn) Recv(ctx context.Context) ([]byte, error) {
	for {
		typ, b, err := w.ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ == websocket.MessageText {
			return b, nil
		}
	}
}

func (w *wsConn) Close(code int, reason string) {
	_ = w.ws.Close(websocket.StatusCode(code), reason)
}
