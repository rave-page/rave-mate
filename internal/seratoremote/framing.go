package seratoremote

import (
	"bytes"
	"errors"

	"rave.page/mate/internal/osc"
)

// Sentinel is the constant 16-byte delimiter that follows every OSC packet on the Serato
// Remote wire. Captured live from Serato DJ Pro 3.3.5.29 (serato-connect, 2026-05-05):
//
//	<bare OSC packet> <16-byte Sentinel>
var Sentinel = []byte{0x4c, 0xaa, 0xc2, 0xae, 0x35, 0xb1, 0xc4, 0x76, 0xdb, 0x5a, 0x64, 0x44, 0x03, 0xbd, 0x41, 0x70}

// defaultMaxFrameBytes caps a single bare-OSC packet. Serato's messages are tiny (a track
// string or 3 floats); a low cap makes a malformed length prefix a fast connection-drop
// rather than a large allocation. Far below serato-connect's 1 MiB - we never expect a
// legitimate frame near this size.
const defaultMaxFrameBytes = 64 << 10 // 64 KiB

// errSentinelMismatch means the 16 bytes after a decoded packet were not the Sentinel -
// protocol drift or a desynced stream; the caller drops the connection.
var errSentinelMismatch = errors.New("seratoremote: frame sentinel mismatch")

// errFrameTooLarge means a packet's self-described length exceeds the cap; drop the conn.
var errFrameTooLarge = errors.New("seratoremote: frame exceeds max size")

// frame wraps one OSC message as a wire frame: bare OSC bytes + the 16-byte Sentinel.
func frame(m osc.Message) []byte {
	return append(osc.Encode(m), Sentinel...)
}

// frameReader is a stateful, BOUNDED framer that consumes incremental TCP chunks and
// yields complete OSC messages. Bound: the internal buffer never exceeds maxFrameBytes +
// len(Sentinel); a packet whose declared length exceeds maxFrameBytes returns
// errFrameTooLarge (drop policy = drop the whole connection, since the stream is desynced
// and cannot be safely resynchronised). Tolerant of TCP split/coalesce per the spec.
type frameReader struct {
	buf          []byte
	maxFrameByte int
}

func newFrameReader(maxFrameBytes int) *frameReader {
	if maxFrameBytes <= 0 {
		maxFrameBytes = defaultMaxFrameBytes
	}
	return &frameReader{maxFrameByte: maxFrameBytes}
}

// push appends chunk and returns every complete message now decodable. It returns an error
// on a sentinel mismatch, an oversized frame, or a malformed packet - all fatal for the
// connection (the stream can't be resynchronised without the length prefix Serato omits).
// rawFrames, when non-nil, receives each frame's bare-OSC bytes for debug capture logging.
func (r *frameReader) push(chunk []byte, rawFrames *[][]byte) ([]osc.Message, error) {
	// Cap the working buffer: at most one max frame + its sentinel may be pending.
	if len(r.buf)+len(chunk) > r.maxFrameByte+len(Sentinel) {
		// Could still be legitimate if the front already holds complete frames; try to
		// drain first, then re-check below. Append and let the loop consume.
	}
	if len(r.buf) == 0 {
		r.buf = append(r.buf[:0], chunk...)
	} else {
		r.buf = append(r.buf, chunk...)
	}

	var out []osc.Message
	for len(r.buf) > 0 {
		n, err := osc.PacketLen(r.buf)
		if errors.Is(err, osc.ErrIncomplete) {
			break // need more bytes
		}
		if err != nil {
			return out, err
		}
		if n > r.maxFrameByte {
			return out, errFrameTooLarge
		}
		total := n + len(Sentinel)
		if len(r.buf) < total {
			break // packet complete but sentinel not fully arrived
		}
		if !bytes.Equal(r.buf[n:total], Sentinel) {
			return out, errSentinelMismatch
		}
		msg, derr := osc.Decode(r.buf[:n])
		if derr != nil {
			return out, derr
		}
		if rawFrames != nil {
			*rawFrames = append(*rawFrames, append([]byte(nil), r.buf[:n]...))
		}
		out = append(out, msg)
		r.buf = r.buf[total:]
	}

	// Enforce the bound: if the residual (incomplete) buffer still exceeds the cap, the
	// stream is desynced (no sentinel found within a max frame) - drop it.
	if len(r.buf) > r.maxFrameByte+len(Sentinel) {
		return out, errFrameTooLarge
	}
	// Compact so a long-lived reader doesn't retain a growing backing array.
	if len(r.buf) == 0 {
		r.buf = r.buf[:0]
	} else if cap(r.buf) > 4*len(r.buf) {
		r.buf = append([]byte(nil), r.buf...)
	}
	return out, nil
}

// pending reports how many bytes are buffered awaiting a complete frame.
func (r *frameReader) pending() int { return len(r.buf) }
