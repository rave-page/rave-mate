package peerlink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/wirecrypto"
)

// Link is an established, authenticated peer connection. Post-handshake control frames
// (confirm/reject/ping/pong) are integrity-protected with a per-frame HMAC keyed by the
// handshake-derived bind key + a monotonic sequence number - same scheme as the studio
// channel. (The LAN transport is plaintext ws://; Phase 3 will add AEAD for the library
// payloads. Control frames need authenticity, which the MAC provides.)
type Link struct {
	conn       Conn
	bindKey    []byte
	peerNodeID string

	mu      sync.Mutex
	sendSeq int64
	recvSeq int64
	closed  bool

	// Wire byte counters (post-handshake frames). Atomic: hot-path adds only.
	bytesIn, bytesOut atomic.Uint64

	// RTT probe state: keepalive pings carry our wall clock (µs); the pong echoes it plus
	// the peer's clock, giving RTT + a rough clock offset.
	pingMu       sync.Mutex
	pingPts      int64     // wall µs sent in the last ping (pong match key)
	pingSentMono time.Time // monotonic send time of that ping; zero = none in flight
	rttMs        float64
	offsetMs     float64 // peer clock − local clock; valid with hasRTT
	hasRTT       bool

	onFrame func(t string, m map[string]any)
	onClose func(err error)
}

func newLink(conn Conn, res *Result) *Link {
	return &Link{conn: conn, bindKey: res.BindKey, peerNodeID: res.PeerNodeID, recvSeq: -1}
}

// send writes a MAC'd frame of type t with optional extra fields. The lock is held across
// conn.Send so seq allocation and wire order stay in lockstep - otherwise two concurrent
// senders (e.g. the MIDI bridge + keepalive) could emit seq N+1 before N and the peer's
// monotonic-seq check would drop the link.
func (l *Link) send(ctx context.Context, t string, extra map[string]any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return fmt.Errorf("peerlink: link closed")
	}
	seq := l.sendSeq
	l.sendSeq++

	frame := map[string]any{"t": t, "seq": seq}
	for k, v := range extra {
		frame[k] = v
	}
	frame["mac"] = l.macOf(seq, frame)
	b, err := wirecrypto.MarshalNoHTMLEscape(frame)
	if err != nil {
		return err
	}
	if err := l.conn.Send(ctx, b); err != nil {
		return err
	}
	l.bytesOut.Add(uint64(len(b)))
	return nil
}

// SendData sends an app-level data frame on the named sub-channel. payload is opaque JSON
// (the caller marshals it); it rides as a string field so the per-frame MAC canonicalization
// is lossless. Authenticity is the same HMAC as control frames; confidentiality (AEAD) lands
// with the library-sync payloads - now-playing/MIDI are ephemeral, low-sensitivity LAN data.
func (l *Link) SendData(ctx context.Context, channel string, payload []byte) error {
	return l.send(ctx, frameData, map[string]any{"ch": channel, "data": string(payload)})
}

// macOf computes b64url(HMAC(bindKey, "<seq>.<canonicalJSON(frame-without-mac)>")).
func (l *Link) macOf(seq int64, frame map[string]any) string {
	noMac := make(map[string]any, len(frame))
	for k, v := range frame {
		if k != "mac" {
			noMac[k] = v
		}
	}
	canon, _ := wirecrypto.CanonicalJSONValue(noMac)
	input := fmt.Sprintf("%d.%s", seq, canon)
	return wirecrypto.EncB64url(wirecrypto.HmacSha256(l.bindKey, []byte(input)))
}

// readLoop reads, verifies, and dispatches frames until the connection closes.
func (l *Link) readLoop(ctx context.Context) {
	var err error
	for {
		var raw []byte
		raw, err = l.conn.Recv(ctx)
		if err != nil {
			break
		}
		l.bytesIn.Add(uint64(len(raw)))
		m, perr := parseNumMap(raw)
		if perr != nil {
			continue
		}
		seq, ok := numField(m, "seq")
		if !ok || seq <= l.recvSeq {
			err = fmt.Errorf("peerlink: bad seq")
			break
		}
		claimed, _ := m["mac"].(string)
		if !wirecrypto.ConstantTimeEqualStr(claimed, l.macOf(seq, m)) {
			err = fmt.Errorf("peerlink: bad mac")
			break
		}
		l.recvSeq = seq
		t, _ := m["t"].(string)
		if l.onFrame != nil {
			l.onFrame(t, m)
		}
	}
	l.close(err)
}

func (l *Link) close(err error) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	l.mu.Unlock()
	l.conn.Close()
	if l.onClose != nil {
		l.onClose(err)
	}
}

// Close terminates the link.
func (l *Link) Close() { l.close(nil) }

// keepalive pings the peer periodically so a dead connection is noticed. Each ping carries
// our wall clock (pts, µs) - the pong echo doubles as the RTT/clock-offset probe.
func (l *Link) keepalive(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pts := time.Now().UnixMicro()
			l.pingMu.Lock()
			l.pingPts, l.pingSentMono = pts, time.Now()
			l.pingMu.Unlock()
			sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := l.send(sctx, framePing, map[string]any{"pts": pts})
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// notePong records RTT (and, when the peer echoed its clock, a rough offset) from a pong.
// Older peers send bare pongs (no pts echo) → ignored.
func (l *Link) notePong(m map[string]any) {
	pts, ok := numField(m, "pts")
	l.pingMu.Lock()
	defer l.pingMu.Unlock()
	if !ok || pts != l.pingPts || l.pingSentMono.IsZero() {
		return
	}
	rtt := time.Since(l.pingSentMono)
	l.rttMs = float64(rtt.Microseconds()) / 1000
	l.hasRTT = true
	if pt, ok := numField(m, "pt"); ok {
		// offset ≈ peerClock@pong − (localClock@ping + rtt/2)
		l.offsetMs = float64(pt-(pts+rtt.Microseconds()/2)) / 1000
	}
	l.pingSentMono = time.Time{}
}

// Stats returns cumulative wire bytes + the last RTT/clock-offset (hasRTT false until the
// first timestamped pong).
func (l *Link) Stats() (in, out uint64, rttMs, offsetMs float64, hasRTT bool) {
	l.pingMu.Lock()
	defer l.pingMu.Unlock()
	return l.bytesIn.Load(), l.bytesOut.Load(), l.rttMs, l.offsetMs, l.hasRTT
}

func parseNumMap(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	err := dec.Decode(&m)
	return m, err
}

func numField(m map[string]any, k string) (int64, bool) {
	n, ok := m[k].(json.Number)
	if !ok {
		return 0, false
	}
	v, err := n.Int64()
	return v, err == nil
}
