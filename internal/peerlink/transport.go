package peerlink

import (
	"context"

	"github.com/coder/websocket"
)

// maxFrame bounds a single peer-link message. Sized for screenshots + future high-bitrate media
// frames (video keyframes / audio buffers) over the trusted, MAC'd LAN link - real streaming should
// still CHUNK into many of these rather than send one giant frame, but the cap won't be the blocker.
const maxFrame = 32 << 20 // 32 MiB

// wsConn adapts a coder/websocket connection to the Conn transport interface.
type wsConn struct {
	ws *websocket.Conn
}

func newWSConn(ws *websocket.Conn) *wsConn {
	ws.SetReadLimit(maxFrame)
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

func (w *wsConn) Close() { _ = w.ws.Close(websocket.StatusNormalClosure, "") }
