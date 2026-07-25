//go:build zigui

package webui

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Seeded mutation fuzz over the REAL v2 exports (the P2 precedent): every mutated buffer must
// either be rejected cleanly or render valid HTML - never crash, never read out of bounds.
//
// The OOB canary: each buffer is copied into the middle of a poison-filled allocation and only
// the inner slice is handed to the ABI. Any read past the declared length drags the poison
// marker into the output, so an unbounded read cannot pass silently. Determinism (same buffer
// twice → identical result) is the second canary: a read of uninitialized/foreign memory
// almost never repeats.

const oobMark = "@@RZWOOB@@"

// poison wraps buf in a poison-filled allocation and returns the inner slice.
func poison(buf []byte) []byte {
	pad := bytes.Repeat([]byte(oobMark), 64)
	out := make([]byte, 0, len(pad)*2+len(buf))
	out = append(out, pad...)
	out = append(out, buf...)
	out = append(out, pad...)
	return out[len(pad) : len(pad)+len(buf)]
}

type wireExport struct {
	name string
	fn   func([]byte) (string, bool)
}

// mutate returns one deterministic mutation of doc. kind selects the corruption class; the
// offset is biased into the arena-length/arena/body region (byte 10+) so mutations reach the
// decoder instead of dying on the magic.
func mutate(rnd *rand.Rand, doc []byte, kind int) []byte {
	out := append([]byte(nil), doc...)
	off := func() int {
		if len(out) <= 10 {
			return rnd.Intn(len(out))
		}
		if rnd.Intn(4) == 0 {
			return rnd.Intn(len(out)) // occasionally hit the header
		}
		return 10 + rnd.Intn(len(out)-10)
	}
	switch kind % 10 {
	case 0: // bit flip
		i := off()
		out[i] ^= 1 << uint(rnd.Intn(8))
	case 1: // random byte
		out[off()] = byte(rnd.Intn(256))
	case 2: // zero byte (kills tags/terminators)
		out[off()] = 0
	case 3: // 0xFF byte (max varint continuation)
		out[off()] = 0xFF
	case 4: // truncate
		return out[:1+rnd.Intn(len(out))]
	case 5: // append garbage
		n := 1 + rnd.Intn(8)
		for i := 0; i < n; i++ {
			out = append(out, byte(rnd.Intn(256)))
		}
	case 6: // swap two bytes
		i, j := off(), off()
		out[i], out[j] = out[j], out[i]
	case 7: // huge u32 over a 4-byte window (length fields: arena, struct/list payloads)
		i := off()
		if i+4 <= len(out) {
			binary.LittleEndian.PutUint32(out[i:], 0xFFFFFFF0)
		}
	case 8: // duplicate a slice of the body (repeated/overlapping fields)
		if len(out) > 20 {
			i := 14 + rnd.Intn(len(out)-14)
			j := i + rnd.Intn(len(out)-i)
			out = append(out, out[i:j]...)
		}
	case 9: // zero a 4-byte window
		i := off()
		for j := i; j < i+4 && j < len(out); j++ {
			out[j] = 0
		}
	}
	return out
}

func TestZigWireMutationFuzz(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	exports := []wireExport{
		{"appgroups_v2", zigui.RenderAppGroupsV2},
		{"appgroups_body_v2", zigui.RenderAppGroupsBodyV2},
		{"logs_v2", zigui.RenderLogsV2},
		{"logs_lines_v2", zigui.RenderLogsLinesV2},
	}

	// Bases: every fixture from both golden suites, plus their fragment states.
	type base struct {
		name string
		doc  []byte
	}
	var bases []base
	for n, st := range agFixtures() {
		bases = append(bases, base{"ag/" + n, wireAgState(st)})
	}
	for n, st := range logsFixtures() {
		bases = append(bases, base{"logs/" + n, wireLogsState(st)})
		bases = append(bases, base{"lines/" + n, wireLogsLines(st.Lines)})
	}
	for _, b := range bases {
		if len(b.doc) == 0 {
			t.Fatalf("%s: encode failed", b.name)
		}
	}

	rnd := rand.New(rand.NewSource(0xB1B1_5EED)) // fixed seed: reproducible failures
	var cases, accepted, rejected int
	check := func(label string, ex wireExport, buf []byte) {
		cases++
		h1, ok1 := ex.fn(poison(buf))
		h2, ok2 := ex.fn(poison(buf)) // determinism canary
		if ok1 != ok2 || h1 != h2 {
			t.Fatalf("%s %s: non-deterministic result (ok %v/%v, len %d/%d)", ex.name, label, ok1, ok2, len(h1), len(h2))
		}
		if !ok1 {
			rejected++
			if h1 != "" {
				t.Fatalf("%s %s: rejected but returned %d bytes", ex.name, label, len(h1))
			}
			return
		}
		accepted++
		if strings.Contains(h1, oobMark) {
			t.Fatalf("%s %s: output contains the OOB poison marker (read past the buffer)", ex.name, label)
		}
		// Output is bounded by the document: every element body costs >= 1 byte and every
		// string comes out of the arena, so a wild multiple means the decoder invented data.
		if max := 4096 + 512*len(buf); len(h1) > max {
			t.Fatalf("%s %s: output %d B from a %d B document (cap %d)", ex.name, label, len(h1), len(buf), max)
		}
	}

	// 1) mutations of valid documents, every corruption class, every export (cross-fed too:
	//    an appgroups document must be refused by the logs export and vice versa).
	for _, b := range bases {
		for kind := 0; kind < 10; kind++ {
			for rep := 0; rep < 2; rep++ {
				m := mutate(rnd, b.doc, kind)
				for _, ex := range exports {
					check(fmt.Sprintf("%s/kind%d/%d", b.name, kind, rep), ex, m)
				}
			}
		}
	}

	// 2) pure random buffers (no valid header) - must all be refused.
	for i := 0; i < 120; i++ {
		buf := make([]byte, rnd.Intn(64))
		for j := range buf {
			buf[j] = byte(rnd.Intn(256))
		}
		ex := exports[i%len(exports)]
		cases++
		if h, ok := ex.fn(poison(buf)); ok {
			t.Fatalf("%s: random %d-byte buffer accepted (%d bytes out)", ex.name, len(buf), len(h))
		} else if h != "" {
			t.Fatalf("%s: rejected but returned data", ex.name)
		}
		rejected++
	}

	// 3) hand-built adversarial documents: each must be REFUSED (not merely survived).
	base0 := wireAgState(agFixtures()["populated"])
	hd := hdr14(wireMsgAgState, 0) // valid header, empty arena - so the BODY checks are what reject
	doc := func(parts ...[]byte) []byte {
		out := append([]byte(nil), hd...)
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	adversarial := map[string][]byte{
		"empty":           {},
		"headerOnly":      hd,
		"noTerminator":    base0[:len(base0)-1],
		"trailingGarbage": append(append([]byte(nil), base0...), 0x42),
		"arenaEatsBody":   withU32(base0, 10, uint32(len(base0)-14)),
		"arenaOverflow":   withU32(base0, 10, 0xFFFFFFFF),
		// field number 0 is the body terminator, never a tag
		"zeroTagField": doc([]byte{0x01}, uvar(0), uvar(0), []byte{0}),
		// wiretype 7 is not skippable and field 1 is a string
		"unknownWiretype": doc(tagB(1, 7), []byte{0}),
		"varintNoEnd":     doc(tagB(3, 0), bytes.Repeat([]byte{0x98}, 12), []byte{0}),
		// string offset/length outside the (empty) arena
		"stringPastArena": doc(tagB(1, 1), uvar(16383), uvar(3), []byte{0}),
		"stringLenPast":   doc(tagB(1, 1), uvar(0), uvar(1), []byte{0}),
		// list count far larger than its payload can hold (allocation bomb)
		"listCountHuge": doc(tagB(8, 3), uvar(0xFFFFFFFF), u32b(1), []byte{0}, []byte{0}),
		// struct/list payload length beyond the parent body
		"structLenPastEnd": doc(tagB(8, 2), u32b(0xFFFF), []byte{0}),
		"listLenPastEnd":   doc(tagB(8, 3), uvar(1), u32b(0xFFFF), []byte{0}),
		// nested body that does not end on its terminator
		"listShortCount": doc(tagB(8, 3), uvar(1), u32b(2), []byte{0, 0}, []byte{0}),
	}
	for name, buf := range adversarial {
		cases++
		if h, ok := zigui.RenderAppGroupsV2(poison(buf)); ok {
			t.Errorf("adversarial/%s accepted (%d bytes out)", name, len(h))
		} else if h != "" {
			t.Errorf("adversarial/%s rejected but returned data", name)
		} else {
			rejected++
		}
	}

	if cases < 400 {
		t.Fatalf("only %d fuzz cases - the gate requires >= 400", cases)
	}
	t.Logf("%d cases: %d rejected cleanly, %d rendered (no crash, no OOB marker, deterministic)",
		cases, rejected, accepted)
}

// withU32 returns a copy of buf with a u32 written at off.
func withU32(buf []byte, off int, v uint32) []byte {
	out := append([]byte(nil), buf...)
	binary.LittleEndian.PutUint32(out[off:], v)
	return out
}

// hdr14 builds a valid RZW1 header for msgID with an arena of arenaLen bytes.
func hdr14(msgID uint16, arenaLen uint32) []byte {
	h := make([]byte, 14)
	copy(h, "RZW1")
	binary.LittleEndian.PutUint16(h[4:], msgID)
	binary.LittleEndian.PutUint32(h[6:], wireSchemaHash)
	binary.LittleEndian.PutUint32(h[10:], arenaLen)
	return h
}

func uvar(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func tagB(num, wt int) []byte { return uvar(uint64(num)<<3 | uint64(wt)) }

func u32b(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// TestZigWireSkipsUnknownFields pins the forward-compatibility rule that replaces JSON's
// ignore_unknown_fields: a document from a NEWER encoder (extra field numbers, every wiretype)
// renders exactly like the same document without them.
func TestZigWireSkipsUnknownFields(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := agFixtures()["populated"]
	base := wireAgState(st)
	want, ok := zigui.RenderAppGroupsV2(base)
	if !ok {
		t.Fatal("base render failed")
	}
	extra := append([]byte(nil), base[:len(base)-1]...) // drop the root terminator
	extra = append(extra, tagB(99, 0)...)               // unknown varint
	extra = append(extra, uvar(123456)...)
	extra = append(extra, tagB(100, 1)...) // unknown string (empty slice of the arena)
	extra = append(extra, uvar(0)...)
	extra = append(extra, uvar(0)...)
	extra = append(extra, tagB(101, 2)...) // unknown struct
	extra = append(extra, u32b(3)...)
	extra = append(extra, tagB(1, 0)...)
	extra = append(extra, uvar(1)...)
	extra = append(extra, 0)
	extra = append(extra, tagB(102, 3)...) // unknown list of 2 empty bodies
	extra = append(extra, uvar(2)...)
	extra = append(extra, u32b(2)...)
	extra = append(extra, 0, 0)
	extra = append(extra, 0) // root terminator

	got, ok := zigui.RenderAppGroupsV2(poison(extra))
	if !ok {
		t.Fatal("document with unknown fields rejected")
	}
	assertBytesEqual(t, "unknown-field skip", want, got)
}
