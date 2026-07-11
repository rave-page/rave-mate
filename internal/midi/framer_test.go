package midi

import (
	"reflect"
	"testing"
)

func collect(f *framer, chunks ...[]byte) []Message {
	var out []Message
	for _, c := range chunks {
		f.feed(c, func(m Message) { out = append(out, m) })
	}
	return out
}

func TestFramerShortMessages(t *testing.T) {
	got := collect(&framer{}, []byte{0x90, 0x3C, 0x7F, 0x80, 0x3C, 0x00})
	want := []Message{{0x90, 0x3C, 0x7F}, {0x80, 0x3C, 0x00}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFramerSplitAcrossChunks(t *testing.T) {
	got := collect(&framer{}, []byte{0xB0}, []byte{0x07}, []byte{0x40})
	want := []Message{{0xB0, 0x07, 0x40}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFramerRunningStatus(t *testing.T) {
	got := collect(&framer{}, []byte{0x90, 0x3C, 0x7F, 0x3E, 0x7F, 0x40, 0x00})
	want := []Message{{0x90, 0x3C, 0x7F}, {0x90, 0x3E, 0x7F}, {0x90, 0x40, 0x00}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFramerTwoByteAndRealtime(t *testing.T) {
	// channel pressure (2-byte) with an interleaved realtime clock mid-message
	got := collect(&framer{}, []byte{0xD0, 0xF8, 0x55, 0xC1, 0x07})
	want := []Message{{Status: 0xF8}, {0xD0, 0x55, 0x00}, {0xC1, 0x07, 0x00}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFramerSysExSkipped(t *testing.T) {
	got := collect(&framer{}, []byte{0xF0, 0x7E, 0x01, 0x02, 0xF7, 0x90, 0x3C, 0x7F})
	want := []Message{{0x90, 0x3C, 0x7F}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// sysex split across chunks, aborted by an interrupting status
	got = collect(&framer{}, []byte{0xF0, 0x11}, []byte{0x22, 0x90, 0x3C, 0x7F})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interrupted sysex: got %v want %v", got, want)
	}
}

func TestFramerStrayDataDropped(t *testing.T) {
	got := collect(&framer{}, []byte{0x11, 0x22, 0x90, 0x3C, 0x7F})
	want := []Message{{0x90, 0x3C, 0x7F}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
