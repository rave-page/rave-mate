// RZW1 state wire (phase B): the binary replacement for the per-render state→JSON→parse round
// trip. Untagged on purpose - the encoder must exist in stub builds too, so webui compiles and
// the format is unit-testable without the Zig lib. Decoder: native/zigui/src/wire.zig.
//
// Document layout (little-endian; all offsets/lengths bounds-checked by the decoder):
//
//	"RZW1"          4 B  magic
//	msg_id          u16  which root message (an export refuses another message's document)
//	schema_hash     u32  FNV-1a of the schema; mismatch = stale lib → reject → v1 JSON path
//	arena_len       u32  length of the strings arena
//	arena           arena_len B  every string, concatenated (deduplicated); decoded zero-copy
//	body            struct body, terminated by a 0 byte
//
// Struct body: fields, then the 0 terminator. A field is a uvarint tag (num<<3 | wiretype)
// followed by its payload:
//
//	0 varint  uvarint value                      (bools: 1; absent = false)
//	1 string  uvarint arena offset, uvarint len   (absent = "")
//	2 struct  u32 body length, body               (absent = zero value)
//	3 list    uvarint count, u32 payload length, count bodies  (absent = EMPTY, never null)
//
// Zero values are encoded as ABSENT tags, and absent decodes to the zero value on both sides -
// so the JSON-era `null` vs `[]` hazard (a nil Go slice marshalled "null", the Zig parser
// rejected it, and a whole tab silently fell back to Go) is UNREPRESENTABLE here: there is no
// null, and an empty list is exactly "tag not present".
//
// Unknown field numbers are skipped (every payload is self-delimiting), so a newer encoder
// stays readable by an older lib for additive changes; anything else trips the schema hash.
//
//go:generate go run rave.page/mate/internal/zigui/wiregen -root ../..
package zigui

import (
	"encoding/binary"
	"math"
	"sync/atomic"
)

const (
	wireMagic      = "RZW1"
	wireHeaderLen  = 14
	wireWTVarint   = 0
	wireWTString   = 1
	wireWTStruct   = 2
	wireWTList     = 3
	wireMaxPayload = math.MaxUint32 // arena + any struct/list payload must fit a u32 length
)

// WireWriter builds one RZW1 document. Not safe for concurrent use; one per render (renders
// are serialized on the webui act worker). Generated encoders (internal/webui/wire_gen.go)
// are its only callers.
type WireWriter struct {
	msgID  uint16
	hash   uint32
	arena  []byte
	body   []byte
	intern map[string]uint32 // string → arena offset (dedup: log tails repeat src/level heavily)
	cap    int               // this message's prealloc hint; also sizes the intern map
	bad    bool              // over-size: Finish returns nil, caller falls back to v1
}

// NewWireWriter starts a document for root message msgID under schema hash h. Both buffers are
// preallocated from what the PREVIOUS document of the same message needed (wireSizeHints):
// re-growing two slices per render is exactly the GC pressure this format exists to remove, but
// one global constant cannot serve both a 12 kB tab document and a 60 B tick fragment - a flat
// 1 KiB made the smallest fragment (#stset-<id>) SLOWER than the JSON path it replaces, and a
// flat 256 B cost the Live cockpit 12 extra allocations per render.
func NewWireWriter(msgID uint16, h uint32) *WireWriter {
	c := wireHint(msgID)
	return &WireWriter{msgID: msgID, hash: h, cap: c, arena: make([]byte, 0, c), body: make([]byte, 0, c)}
}

// Adaptive per-message prealloc. Capacity only - it can never change a document's bytes.
const (
	wireHintSlots = 256     // root ids are small + partitioned; anything beyond uses the default
	wireHintMin   = 192     // below this the two allocations dominate a tick fragment anyway
	wireHintMax   = 1 << 18 // a 256 kB state is pathological; don't cache it
	wireHintDflt  = 1 << 10 // cold start, or an id outside the table
)

var wireSizeHints [wireHintSlots]atomic.Uint32

func wireHint(msgID uint16) int {
	if msgID >= wireHintSlots {
		return wireHintDflt
	}
	if n := int(wireSizeHints[msgID].Load()); n >= wireHintMin {
		return n
	}
	return wireHintDflt
}

// noteWireSize records what the message actually needed plus 25% headroom, so a slowly growing
// state (one more log line, one more card) does not regrow on every render.
func noteWireSize(msgID uint16, n int) {
	if msgID >= wireHintSlots {
		return
	}
	n += n / 4
	if n < wireHintMin {
		n = wireHintMin
	}
	if n > wireHintMax {
		return
	}
	wireSizeHints[msgID].Store(uint32(n))
}

func (w *WireWriter) tag(num, wt int) {
	w.uvarint(uint64(num)<<3 | uint64(wt))
}

func (w *WireWriter) uvarint(v uint64) {
	for v >= 0x80 {
		w.body = append(w.body, byte(v)|0x80)
		v >>= 7
	}
	w.body = append(w.body, byte(v))
}

// arenaOff interns s and returns its offset.
func (w *WireWriter) arenaOff(s string) uint32 {
	if off, ok := w.intern[s]; ok {
		return off
	}
	if len(w.arena)+len(s) > wireMaxPayload {
		w.bad = true
		return 0
	}
	off := uint32(len(w.arena))
	w.arena = append(w.arena, s...)
	if w.intern == nil {
		// Strings average ~20 B across the migrated states, so the byte hint also sizes the
		// map: a flat 64 wasted ~2.5 kB on a tick fragment, a flat 8 regrew five times on a
		// whole-tab document.
		w.intern = make(map[string]uint32, w.cap/24+8)
	}
	w.intern[s] = off
	return off
}

// Str writes field num, or nothing when s is empty (absent decodes to "").
func (w *WireWriter) Str(num int, s string) {
	if s == "" {
		return
	}
	off := w.arenaOff(s)
	w.tag(num, wireWTString)
	w.uvarint(uint64(off))
	w.uvarint(uint64(len(s)))
}

// Bool writes field num only when v is true (absent decodes to false).
func (w *WireWriter) Bool(num int, v bool) {
	if !v {
		return
	}
	w.tag(num, wireWTVarint)
	w.uvarint(1)
}

// Uint writes field num only when v is non-zero (absent decodes to 0).
func (w *WireWriter) Uint(num int, v uint64) {
	if v == 0 {
		return
	}
	w.tag(num, wireWTVarint)
	w.uvarint(v)
}

// Struct writes a nested message. enc emits its fields; a message that emits nothing is
// dropped entirely (absent decodes to the zero value - same bytes, smaller document).
func (w *WireWriter) Struct(num int, enc func()) {
	mark := len(w.body)
	w.tag(num, wireWTStruct)
	lp := len(w.body)
	w.body = append(w.body, 0, 0, 0, 0)
	enc()
	if len(w.body) == lp+4 {
		w.body = w.body[:mark] // no fields → absent
		return
	}
	w.body = append(w.body, 0) // body terminator
	w.patch(lp)
}

// List writes n elements of a nested message; n == 0 writes nothing (absent = empty list).
func (w *WireWriter) List(num, n int, enc func(i int)) {
	if n == 0 {
		return
	}
	w.tag(num, wireWTList)
	w.uvarint(uint64(n))
	lp := len(w.body)
	w.body = append(w.body, 0, 0, 0, 0)
	for i := 0; i < n; i++ {
		enc(i)
		w.body = append(w.body, 0) // per-element terminator
	}
	w.patch(lp)
}

// patch writes the payload length into the u32 placeholder at lp.
func (w *WireWriter) patch(lp int) {
	n := len(w.body) - lp - 4
	if n > wireMaxPayload {
		w.bad = true
		return
	}
	binary.LittleEndian.PutUint32(w.body[lp:], uint32(n))
}

// Finish assembles the document (nil = over-size; the caller uses the v1 JSON path).
func (w *WireWriter) Finish() []byte {
	if w.bad || len(w.arena) > wireMaxPayload {
		return nil
	}
	out := make([]byte, 0, wireHeaderLen+len(w.arena)+len(w.body)+1)
	out = append(out, wireMagic...)
	out = binary.LittleEndian.AppendUint16(out, w.msgID)
	out = binary.LittleEndian.AppendUint32(out, w.hash)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(w.arena)))
	out = append(out, w.arena...)
	out = append(out, w.body...)
	noteWireSize(w.msgID, max(len(w.arena), len(w.body)))
	return append(out, 0) // root body terminator
}

// --- phaseb-wire (B-2 fan-out: kinds the pilot states did not need) ---

// StrAlways writes field num even when s is empty. Required where the Zig field's DEFAULT is
// not the zero value (`fill: []const u8 = "0.00%"`, `stepS = "1"`): the JSON path always sends
// the field, so an absent tag would decode to the default instead of "" and the two paths would
// diverge. Cost is one tag + two zero varints per empty string, on a handful of fields.
func (w *WireWriter) StrAlways(num int, s string) {
	off := w.arenaOff(s)
	w.tag(num, wireWTString)
	w.uvarint(uint64(off))
	w.uvarint(uint64(len(s)))
}

// OptStruct writes a nested message whose PRESENCE is meaningful (a Zig `?T` field: null vs a
// zero-value struct render differently - motion's inactive section, the player's absent panels).
// Unlike Struct it keeps the tag even when the message emits no fields, so "present but all
// zero" cannot decode as null. Callers gate on the Go pointer being non-nil.
func (w *WireWriter) OptStruct(num int, enc func()) {
	w.tag(num, wireWTStruct)
	lp := len(w.body)
	w.body = append(w.body, 0, 0, 0, 0)
	enc()
	w.body = append(w.body, 0) // body terminator
	w.patch(lp)
}

// StrList writes a []string as a list of single-field element bodies (field 1 = the string), so
// the decoder reuses the list bounds discipline unchanged. n == 0 writes nothing (absent = empty).
func (w *WireWriter) StrList(num int, ss []string) {
	if len(ss) == 0 {
		return
	}
	w.tag(num, wireWTList)
	w.uvarint(uint64(len(ss)))
	lp := len(w.body)
	w.body = append(w.body, 0, 0, 0, 0)
	for _, s := range ss {
		w.Str(1, s)                // "" → absent → decodes to ""
		w.body = append(w.body, 0) // per-element terminator
	}
	w.patch(lp)
}

// --- end phaseb-wire ---

// NoteWireFallback records a v2→v1 downgrade that happened before the ABI was even reached
// (encoder returned nil), keyed like FallbackCounts' render keys.
func NoteWireFallback(name string) {
	fbMu.Lock()
	fbCounts[name]++
	fbMu.Unlock()
}
