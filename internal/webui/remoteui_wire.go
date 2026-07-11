package webui

// ChanRemoteUI wire format (JSON frames over the peerlink data channel). One message kind per
// frame; doc/eval payloads over ruiChunkMax split into ordered chunks (the peer link is one
// ordered websocket, so reassembly is sequential per peer). Everything rides peerlink's
// Ed25519-authenticated, per-frame-MAC'd link - only paired ("connected") peers reach this code.

import (
	"encoding/json"
	"fmt"
)

const (
	// ruiChunkMax caps one frame's data payload - well under peerlink maxFrame (32 MiB) and
	// remotectl's 24 MiB precedent, keeping the link responsive for other channels.
	ruiChunkMax = 4 << 20
	// ruiReasmMax caps one reassembled doc/eval message. Policy: a larger message is dropped
	// whole (the next patch of the same fragment repaints it).
	ruiReasmMax = 24 << 20
)

// Message kinds. Controller = the instance mirroring a peer's Library; host = the controlled
// instance running the headless session.
const (
	ruiKindOpen     = "open"     // controller→host: start a session (SID minted by controller)
	ruiKindDoc      = "doc"      // host→controller: full document (initial load / resync)
	ruiKindEval     = "eval"     // host→controller: batched page evals (Go-generated JS)
	ruiKindAct      = "act"      // controller→host: page input payload (the window.rave JSON)
	ruiKindClose    = "close"    // controller→host: tear the session down
	ruiKindClosed   = "closed"   // host→controller: session ended (reason in Data; "" = clean)
	ruiKindFetch    = "fetch"    // controller→host: media byte-range request (loopback proxy)
	ruiKindFetchRes = "fetchres" // host→controller: fetch reply chunk
)

// ruiMsg is one ChanRemoteUI frame.
type ruiMsg struct {
	T    string `json:"t"`
	SID  string `json:"sid"`
	Data string `json:"data,omitempty"`

	// doc/eval chunking: I of N parts sharing MID (order guaranteed by the link).
	MID string `json:"mid,omitempty"`
	I   int    `json:"i,omitempty"`
	N   int    `json:"n,omitempty"`

	// fetch/fetchres (media proxy)
	FID    string `json:"fid,omitempty"`    // fetch correlation id
	Path   string `json:"path,omitempty"`   // host-loopback path ("/m/<tok>" | "/img/<tok>")
	Off    int64  `json:"off,omitempty"`    // requested byte offset
	Len    int    `json:"len,omitempty"`    // requested byte count (host clamps)
	Status int    `json:"status,omitempty"` // upstream HTTP status
	CT     string `json:"ct,omitempty"`     // content type
	Total  int64  `json:"total,omitempty"`  // full resource size (-1 unknown)
}

// ruiSendChunked sends m, splitting Data into ordered chunks when it exceeds max.
func ruiSendChunked(send func(ruiMsg) error, m ruiMsg, max int, mid string) error {
	if len(m.Data) <= max {
		return send(m)
	}
	n := (len(m.Data) + max - 1) / max
	for i := 0; i < n; i++ {
		lo, hi := i*max, (i+1)*max
		if hi > len(m.Data) {
			hi = len(m.Data)
		}
		part := m
		part.Data = m.Data[lo:hi]
		part.MID, part.I, part.N = mid, i, n
		if err := send(part); err != nil {
			return err
		}
	}
	return nil
}

// ruiReasm sequentially reassembles chunked doc/eval messages from ONE peer. Bounded: a single
// in-flight message, ≤ ruiReasmMax bytes; an out-of-order or oversized part drops the whole
// message (policy: next patch repaints).
type ruiReasm struct {
	mid  string
	next int
	buf  []byte
}

// feed consumes m; returns (complete message, true) once the last part lands. Unchunked
// messages pass straight through.
func (r *ruiReasm) feed(m ruiMsg) (ruiMsg, bool) {
	if m.N == 0 { // unchunked
		r.reset()
		return m, true
	}
	if m.I == 0 {
		r.reset()
		r.mid = m.MID
	}
	if m.MID != r.mid || m.I != r.next {
		r.reset() // lost/interleaved part - drop the message
		return ruiMsg{}, false
	}
	if len(r.buf)+len(m.Data) > ruiReasmMax {
		r.reset()
		return ruiMsg{}, false
	}
	r.buf = append(r.buf, m.Data...)
	r.next++
	if r.next < m.N {
		return ruiMsg{}, false
	}
	out := m
	out.Data = string(r.buf)
	out.MID, out.I, out.N = "", 0, 0
	r.reset()
	return out, true
}

func (r *ruiReasm) reset() { r.mid, r.next, r.buf = "", 0, nil }

// ruiEncode marshals a frame (error only on a programming mistake).
func ruiEncode(m ruiMsg) ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("remoteui: encode %s: %w", m.T, err)
	}
	return b, nil
}
