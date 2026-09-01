package peerlink

import (
	"context"
	"sync"

	"github.com/coder/websocket"

	"rave.page/mate/internal/wirecrypto"
)

// maxFrame bounds a single peer-link message. Sized for screenshots + future high-bitrate media
// frames (video keyframes / audio buffers) over the MAC'd LAN link - real streaming should still
// CHUNK into many of these rather than send one giant frame, but the cap won't be the blocker.
const maxFrame = 32 << 20 // 32 MiB

// LAN transport AEAD labels. Own HKDF domain, distinct from the bridge's ("rave-bridge-*"): a
// connection is only ever ONE transport, so bridge and LAN never key from the same handshake for
// the same connection - but distinct labels make the "no shared per-direction key" property hold
// unconditionally rather than circumstantially (domain separation, the wirecrypto discipline).
const (
	lanSealI2R = "rave-lan-i2r-v1"
	lanSealR2I = "rave-lan-r2i-v1"
)

// lanCrypto is the optional AEAD layer of the LAN transport: inactive until Upgrade, after which
// every frame is AES-256-GCM (per-direction wirecrypto.Sealer, counter nonce). The frame MAC +
// monotonic seq inside the tunnel are untouched (defense in depth, zero wire-format churn there).
type lanCrypto struct {
	mu   sync.Mutex
	send *wirecrypto.Sealer
	recv *wirecrypto.Sealer
}

func (l *lanCrypto) upgrade(master []byte, initiator bool) error {
	s, r, err := wirecrypto.NewDuplexSealer(master, nil, initiator, lanSealI2R, lanSealR2I)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.send, l.recv = s, r
	l.mu.Unlock()
	return nil
}

func (l *lanCrypto) active() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.send != nil
}

func (l *lanCrypto) seal(b []byte) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.send.Seal(nil, b)
}

func (l *lanCrypto) open(b []byte) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.recv.Open(nil, b)
}

// wsConn adapts a coder/websocket connection to the Conn transport interface. It implements
// Upgrader + lanTransport: after Upgrade, frames go out as sealed binary messages instead of
// plaintext text. Send is serialized by the Link (l.mu held across conn.Send), and Recv runs on
// the single readLoop goroutine, so seal/open counters stay in lockstep with the wire order.
type wsConn struct {
	ws *websocket.Conn
	lanCrypto
}

func newWSConn(ws *websocket.Conn) *wsConn {
	ws.SetReadLimit(maxFrame)
	return &wsConn{ws: ws}
}

// lanPlane marks wsConn as the opt-out-able LAN transport (see gate.go).
func (w *wsConn) lanPlane() {}

// Upgrade switches the LAN transport to AES-256-GCM, keyed from the completed handshake. Both
// ends call it at the same point (right after the AKE, before the read loop starts), so they stay
// in lockstep; every frame after this is ciphertext.
func (w *wsConn) Upgrade(master []byte, initiator bool) error {
	return w.upgrade(master, initiator)
}

func (w *wsConn) Send(ctx context.Context, b []byte) error {
	if w.active() {
		return w.ws.Write(ctx, websocket.MessageBinary, w.seal(b))
	}
	return w.ws.Write(ctx, websocket.MessageText, b)
}

func (w *wsConn) Recv(ctx context.Context) ([]byte, error) {
	for {
		typ, b, err := w.ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if w.active() {
			if typ != websocket.MessageBinary {
				continue // stray plaintext after upgrade: ignore without touching the recv counter
			}
			return w.open(b)
		}
		if typ == websocket.MessageText {
			return b, nil
		}
	}
}

func (w *wsConn) Close() { _ = w.ws.Close(websocket.StatusNormalClosure, "") }
