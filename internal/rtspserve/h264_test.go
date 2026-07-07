package rtspserve

import (
	"bytes"
	"testing"
)

// nal builds a fake NAL: header byte from type + payload.
func nal(t byte, payload ...byte) []byte { return append([]byte{0x60 | t}, payload...) }

func TestNALSplitterAcrossWrites(t *testing.T) {
	var got [][]byte
	s := &nalSplitter{onNAL: func(n []byte) { got = append(got, n) }}
	sps := nal(nalSPS, 0x42, 0x00, 0x1F)
	idr := nal(nalSliceIDR, 0x80, 1, 2, 3)
	stream := append([]byte{0, 0, 0, 1}, sps...)
	stream = append(stream, 0, 0, 1)
	stream = append(stream, idr...)
	stream = append(stream, 0, 0, 0, 1) // trailing start code terminates idr
	// Feed byte-by-byte to exercise split start codes.
	for _, b := range stream {
		if _, err := s.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d NALs, want 2", len(got))
	}
	if !bytes.Equal(got[0], sps) || !bytes.Equal(got[1], idr) {
		t.Fatalf("NALs mangled: %x / %x", got[0], got[1])
	}
}

func TestNALSplitterFlushAndGarbage(t *testing.T) {
	var got [][]byte
	s := &nalSplitter{onNAL: func(n []byte) { got = append(got, n) }}
	_, _ = s.Write([]byte{0xDE, 0xAD}) // pre-stream garbage
	_, _ = s.Write(append([]byte{0, 0, 1}, nal(nalPPS, 9)...))
	s.Flush()
	if len(got) != 1 || !bytes.Equal(got[0], nal(nalPPS, 9)) {
		t.Fatalf("flush: %x", got)
	}
}

func TestAUAssemblerAUDBoundaries(t *testing.T) {
	var aus []accessUnit
	var sps, pps []byte
	a := &auAssembler{
		onAU:     func(au accessUnit) { aus = append(aus, au) },
		onParams: func(s, p []byte) { sps, pps = s, p },
	}
	a.addNAL(nal(nalAUD, 0x10))
	a.addNAL(nal(nalSPS, 1))
	a.addNAL(nal(nalPPS, 2))
	a.addNAL(nal(nalSliceIDR, 0x80, 3))
	a.addNAL(nal(nalAUD, 0x10))
	a.addNAL(nal(nalSliceNonIDR, 0x80, 4))
	a.Flush()
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2", len(aus))
	}
	if !aus[0].key || len(aus[0].nals) != 3 {
		t.Fatalf("AU0: key=%v nals=%d", aus[0].key, len(aus[0].nals))
	}
	if aus[1].key || len(aus[1].nals) != 1 {
		t.Fatalf("AU1: key=%v nals=%d", aus[1].key, len(aus[1].nals))
	}
	if sps == nil || pps == nil {
		t.Fatal("params not captured")
	}
}

func TestAUAssemblerFirstMBFallback(t *testing.T) {
	var aus []accessUnit
	a := &auAssembler{onAU: func(au accessUnit) { aus = append(aus, au) }}
	// No AUDs: two frames, second one two slices (first_mb_in_slice=0 only on the first).
	a.addNAL(nal(nalSliceIDR, 0x88))    // frame 1 (first_mb=0)
	a.addNAL(nal(nalSliceNonIDR, 0x88)) // frame 2 slice 1 (first_mb=0 → boundary)
	a.addNAL(nal(nalSliceNonIDR, 0x22)) // frame 2 slice 2 (first_mb≠0 → same AU)
	a.Flush()
	if len(aus) != 2 {
		t.Fatalf("got %d AUs, want 2", len(aus))
	}
	if len(aus[1].nals) != 2 {
		t.Fatalf("AU1 has %d nals, want 2", len(aus[1].nals))
	}
}

func TestPayloadizeSingleAndFUA(t *testing.T) {
	small := nal(nalSliceNonIDR, 0x80, 1, 2)
	big := make([]byte, 3001)
	big[0] = 0x65 // NRI=3, type 5
	for i := 1; i < len(big); i++ {
		big[i] = byte(i)
	}
	out := payloadize([][]byte{small, big}, 1400)
	if len(out) != 4 { // 1 single + 3 FU-A (3000 payload bytes / 1398)
		t.Fatalf("got %d payloads, want 4", len(out))
	}
	if !bytes.Equal(out[0].data, small) || out[0].marker {
		t.Fatalf("single NAL packet wrong")
	}
	// FU-A reassembly must reproduce the original NAL.
	var re []byte
	for i, p := range out[1:] {
		if p.data[0] != (0x65&0xE0)|28 {
			t.Fatalf("FU indicator %02x", p.data[0])
		}
		s, e := p.data[1]&0x80 != 0, p.data[1]&0x40 != 0
		if (i == 0) != s || (i == 2) != e {
			t.Fatalf("FU S/E bits wrong at %d", i)
		}
		if p.data[1]&0x1F != nalSliceIDR {
			t.Fatalf("FU type %d", p.data[1]&0x1F)
		}
		re = append(re, p.data[2:]...)
	}
	if !bytes.Equal(append([]byte{0x65}, big[1:]...), append([]byte{0x65}, re...)) {
		t.Fatal("FU-A reassembly mismatch")
	}
	if !out[len(out)-1].marker {
		t.Fatal("marker missing on last packet")
	}
}
