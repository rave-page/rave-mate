package osc

import (
	"math"
	"testing"
)

// Ported from serato-connect tests/remote/osc.test.ts - parity vectors for the OSC 1.1
// encode/decode used by the Serato Remote protocol.

func TestEncodeDecodeRoundTripEmpty(t *testing.T) {
	buf := Encode(Msg("/Ping"))
	if len(buf)%4 != 0 {
		t.Fatalf("not 4-byte aligned: %d", len(buf))
	}
	m, err := Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if m.Address != "/Ping" || len(m.Args) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestEncodeDecodeIS(t *testing.T) {
	buf := Encode(Msg("/Status/Deck/Song/Title", ArgInt(0), ArgString("Track Of The Night")))
	if len(buf)%4 != 0 {
		t.Fatalf("not aligned: %d", len(buf))
	}
	m, err := Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if m.Address != "/Status/Deck/Song/Title" {
		t.Fatalf("addr %q", m.Address)
	}
	if len(m.Args) != 2 || m.Args[0].Kind != KindInt || m.Args[0].Int != 0 ||
		m.Args[1].Kind != KindString || m.Args[1].Str != "Track Of The Night" {
		t.Fatalf("args %+v", m.Args)
	}
}

func TestEncodeDecodeIF(t *testing.T) {
	m, err := Decode(Encode(Msg("/Status/Deck/Loop/AutoLoopOn", ArgInt(2), ArgFloat(1.0))))
	if err != nil {
		t.Fatal(err)
	}
	if m.Args[0].Int != 2 || m.Args[1].Kind != KindFloat || math.Abs(float64(m.Args[1].Float)-1.0) > 1e-5 {
		t.Fatalf("args %+v", m.Args)
	}
}

func TestEncodeDecodeIFFF(t *testing.T) {
	m, err := Decode(Encode(Msg("/Status/Deck/Playhead", ArgInt(1), ArgFloat(42.5), ArgFloat(180.25), ArgFloat(124.5))))
	if err != nil {
		t.Fatal(err)
	}
	if m.Args[0].Int != 1 {
		t.Fatalf("deck %d", m.Args[0].Int)
	}
	want := []float32{42.5, 180.25, 124.5}
	for i, w := range want {
		if math.Abs(float64(m.Args[i+1].Float)-float64(w)) > 1e-5 {
			t.Fatalf("float %d = %v want %v", i, m.Args[i+1].Float, w)
		}
	}
}

func TestEncodeDecodeSingleF(t *testing.T) {
	m, err := Decode(Encode(Msg("/Status/Video/Mixer/Crossfader", ArgFloat(-0.25))))
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(m.Args[0].Float)-(-0.25)) > 1e-5 {
		t.Fatalf("cf %v", m.Args[0].Float)
	}
}

func TestStringPadding(t *testing.T) {
	// "abc" + null = 4 bytes; address(4) + ",\0\0\0"(4) = 8.
	if got := len(Encode(Msg("abc"))); got != 8 {
		t.Fatalf("abc len %d want 8", got)
	}
	// "abcd" + null = 5 bytes -> pads to 8; total 8+4=12.
	if got := len(Encode(Msg("abcd"))); got != 12 {
		t.Fatalf("abcd len %d want 12", got)
	}
}

func TestDecodeUTF8(t *testing.T) {
	title := "Café - Étude n°3"
	m, err := Decode(Encode(Msg("/Status/Deck/Song/Title", ArgInt(0), ArgString(title))))
	if err != nil {
		t.Fatal(err)
	}
	if m.Args[1].Str != title {
		t.Fatalf("got %q", m.Args[1].Str)
	}
}

func TestDecodeMissingComma(t *testing.T) {
	// address "/X\0\0" + tag "iX\0\0" (no leading comma).
	b := []byte{'/', 'X', 0, 0, 'i', 'X', 0, 0}
	if _, err := Decode(b); err == nil {
		t.Fatal("expected error for missing comma")
	}
}

func TestDecodeUnsupportedTag(t *testing.T) {
	buf := Encode(Msg("/X", ArgInt(1)))
	buf[5] = 'q' // patch type tag ",i" -> ",q" (address "/X\0\0" occupies 4 bytes, tag at 4)
	if _, err := Decode(buf); err == nil {
		t.Fatal("expected error for unsupported tag")
	}
}

func TestEncodeDecodeBlob(t *testing.T) {
	blob := []byte{0x3f, 0x47, 0x50, 0x0e, 0x7d, 0x40, 0x14, 0x1e, 0x77, 0x4a, 0x9b, 0x90, 0x8e, 0xf6, 0xbe, 0xc0}
	m, err := Decode(Encode(Msg("/StreamMgmt/Authorize/Request", ArgBlob(blob), ArgInt(1), ArgInt(1))))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Args) != 3 || m.Args[0].Kind != KindBlob || len(m.Args[0].Blob) != 16 {
		t.Fatalf("blob args %+v", m.Args)
	}
	for i := range blob {
		if m.Args[0].Blob[i] != blob[i] {
			t.Fatalf("blob byte %d", i)
		}
	}
}

func TestPacketLen(t *testing.T) {
	buf := Encode(Msg("/Status/Deck/Playhead", ArgInt(1), ArgFloat(1), ArgFloat(2), ArgFloat(3)))
	n, err := PacketLen(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(buf) {
		t.Fatalf("PacketLen %d want %d", n, len(buf))
	}
	// Truncated -> incomplete.
	if _, err := PacketLen(buf[:len(buf)-2]); err != ErrIncomplete {
		t.Fatalf("expected ErrIncomplete, got %v", err)
	}
}
