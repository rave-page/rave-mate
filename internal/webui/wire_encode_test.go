package webui

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// Encoder-side format pins. UNTAGGED on purpose: the RZW1 layout must be checkable without the
// Zig lib (the three-way golden gate in zigui_wire_test.go proves the DECODER agrees).

func hdr(t *testing.T, doc []byte, msgID uint16) (arena, body []byte) {
	t.Helper()
	if len(doc) < 14 {
		t.Fatalf("document too short: %d B", len(doc))
	}
	if string(doc[:4]) != "RZW1" {
		t.Fatalf("magic = %q", doc[:4])
	}
	if got := binary.LittleEndian.Uint16(doc[4:]); got != msgID {
		t.Fatalf("msg id = %d, want %d", got, msgID)
	}
	if got := binary.LittleEndian.Uint32(doc[6:]); got != wireSchemaHash {
		t.Fatalf("schema hash = %#x, want %#x", got, wireSchemaHash)
	}
	n := int(binary.LittleEndian.Uint32(doc[10:]))
	if 14+n > len(doc) {
		t.Fatalf("arena len %d exceeds document (%d B)", n, len(doc))
	}
	return doc[14 : 14+n], doc[14+n:]
}

func TestWireHeaderAndBodyLayout(t *testing.T) {
	doc := wireAgState(agState{Title: "T", Available: true})
	arena, body := hdr(t, doc, wireMsgAgState)
	if string(arena) != "T" {
		t.Errorf("arena = %q, want %q", arena, "T")
	}
	// title: tag(1,string)=0x09 off=0 len=1 · available: tag(3,varint)=0x18 value=1 · terminator
	want := []byte{0x09, 0x00, 0x01, 0x18, 0x01, 0x00}
	if !bytes.Equal(body, want) {
		t.Errorf("body = % x, want % x", body, want)
	}
}

func TestWireZeroValuesAreAbsent(t *testing.T) {
	doc := wireAgState(agState{})
	arena, body := hdr(t, doc, wireMsgAgState)
	if len(arena) != 0 {
		t.Errorf("arena = % x, want empty", arena)
	}
	if !bytes.Equal(body, []byte{0}) { // nothing but the terminator
		t.Errorf("body = % x, want the terminator only", body)
	}
	// Nested all-zero structs are dropped too (logsState carries three of them).
	ldoc := wireLogsState(logsState{})
	if _, lbody := hdr(t, ldoc, wireMsgLogsState); !bytes.Equal(lbody, []byte{0}) {
		t.Errorf("logs body = % x, want the terminator only", lbody)
	}
}

func TestWireEmptyAndNilListsEncodeIdentically(t *testing.T) {
	nilSt := logsLines{Wired: true, NoBus: "x"}
	emptySt := logsLines{Wired: true, NoBus: "x", Entries: []logsEntry{}}
	if !bytes.Equal(wireLogsLines(nilSt), wireLogsLines(emptySt)) {
		t.Fatal("nil and empty slice must encode to the same bytes (absent tag)")
	}
	// ...and an absent list tag means the body holds only the two present fields.
	_, body := hdr(t, wireLogsLines(nilSt), wireMsgLogsLines)
	if bytes.Contains(body, []byte{0x23}) { // tag(4, list)
		t.Errorf("empty list emitted a tag: % x", body)
	}
}

func TestWireInternsRepeatedStrings(t *testing.T) {
	rep := strings.Repeat("s", 64)
	st := logsLines{Wired: true, Entries: []logsEntry{
		{Time: rep, Lvl: rep, Cls: rep, Src: rep, Msg: rep, Fields: rep},
		{Time: rep, Lvl: rep, Cls: rep, Src: rep, Msg: rep, Fields: rep},
	}}
	arena, _ := hdr(t, wireLogsLines(st), wireMsgLogsLines)
	if len(arena) != len(rep) {
		t.Errorf("arena = %d B, want %d (one copy of the repeated string)", len(arena), len(rep))
	}
}

// A 400-line tail (logTailN) must stay well under the u32 payload cap and encode smaller than
// the JSON it replaces - the whole point of the wire.
func TestWireFullTailIsCompact(t *testing.T) {
	var es []logsEntry
	for i := 0; i < logTailN; i++ {
		es = append(es, logsEntry{Time: "09:15:01.250", Lvl: "INFO", Cls: "INFO", Src: "session",
			Msg: "merge tick " + strings.Repeat("x", i%40), Fields: "map[bpm:128]"})
	}
	st := logsLines{Wired: true, NoBus: "no bus", NoEntries: "none", Entries: es}
	doc, js := wireLogsLines(st), stateJSON(st)
	if len(doc) == 0 {
		t.Fatal("encode failed")
	}
	if len(doc) >= len(js) {
		t.Errorf("wire %d B >= json %d B", len(doc), len(js))
	}
	t.Logf("400-line tail: wire %d B, json %d B (%.1f%%)", len(doc), len(js), 100*float64(len(doc))/float64(len(js)))
}
