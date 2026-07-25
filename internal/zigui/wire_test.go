package zigui

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Encoder pins for the wave B-2 writer kinds (StrAlways / OptStruct / StrList). The DECODER
// side is pinned in native/zigui/src/wire.zig's tests, and the two are tied together by the
// three-way golden gate in internal/webui once a tab uses them.

func body(t *testing.T, doc []byte) []byte {
	t.Helper()
	if len(doc) < wireHeaderLen {
		t.Fatalf("document too short: %d B", len(doc))
	}
	n := int(binary.LittleEndian.Uint32(doc[10:]))
	return doc[wireHeaderLen+n:]
}

func TestStrAlwaysEmitsEmptyStrings(t *testing.T) {
	w := NewWireWriter(1, 0)
	w.Str(1, "")       // absent
	w.StrAlways(2, "") // present, zero length
	got := body(t, w.Finish())
	want := []byte{0x11, 0x00, 0x00, 0x00} // tag(2,string) off=0 len=0 · terminator
	if !bytes.Equal(got, want) {
		t.Fatalf("body = % x, want % x", got, want)
	}
	// A non-empty StrAlways is byte-identical to Str (same tag, same arena reference).
	a, b := NewWireWriter(1, 0), NewWireWriter(1, 0)
	a.Str(3, "x")
	b.StrAlways(3, "x")
	if !bytes.Equal(a.Finish(), b.Finish()) {
		t.Error("StrAlways diverges from Str on a non-empty value")
	}
}

func TestOptStructKeepsEmptyBodies(t *testing.T) {
	// Struct drops a message that emits nothing; OptStruct keeps the tag - that difference IS
	// the null-vs-present distinction for a Zig `?T` field.
	dropped := NewWireWriter(1, 0)
	dropped.Struct(5, func() {})
	if got := body(t, dropped.Finish()); !bytes.Equal(got, []byte{0}) {
		t.Errorf("Struct body = % x, want the terminator only", got)
	}
	kept := NewWireWriter(1, 0)
	kept.OptStruct(5, func() {})
	want := []byte{0x2a, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00} // tag(5,struct) len=1 body=0 · term
	if got := body(t, kept.Finish()); !bytes.Equal(got, want) {
		t.Errorf("OptStruct body = % x, want % x", got, want)
	}
}

func TestStrListLayout(t *testing.T) {
	w := NewWireWriter(1, 0)
	w.StrList(6, []string{"aa", "", "cc"})
	got := body(t, w.Finish())
	// tag(6,list)=0x33 count=3 len=u32 · ["aa"|term][term]["cc"|term] · terminator
	want := []byte{0x33, 0x03, 0x09, 0x00, 0x00, 0x00,
		0x09, 0x00, 0x02, 0x00, // "aa" at arena 0
		0x00,                   //             empty element
		0x09, 0x02, 0x02, 0x00, // "cc" at arena 2
		0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("body = % x, want % x", got, want)
	}
	// nil and empty encode identically (absent tag = empty list, never null)
	n, e := NewWireWriter(1, 0), NewWireWriter(1, 0)
	n.StrList(6, nil)
	e.StrList(6, []string{})
	if !bytes.Equal(n.Finish(), e.Finish()) {
		t.Error("nil and empty []string encode differently")
	}
	if got := body(t, n.Finish()); !bytes.Equal(got, []byte{0}) {
		t.Errorf("empty StrList body = % x, want the terminator only", got)
	}
}
