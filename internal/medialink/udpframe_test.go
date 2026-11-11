package medialink

import (
	"bytes"
	"testing"
)

// decodeOne parses + decrypts one datagram and folds it into r. Mirrors the conn recv path.
func decodeOne(t *testing.T, r *reassembler, dg []byte, token uint32, crypto *dgCrypto, nowNs int64) (*Frame, bool) {
	t.Helper()
	h, off, err := parseDatagramHeader(dg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	chunk, err := decodeBody(h, dg[off:], token, crypto, dg[:off])
	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	wire, done := r.accept(h, chunk, nowNs)
	if !done {
		return nil, false
	}
	f, err := parseFrame(wire)
	if err != nil {
		t.Fatalf("parseFrame: %v", err)
	}
	return f, true
}

func sampleFrame(seq uint32, stream uint16, payloadLen int) *Frame {
	p := make([]byte, payloadLen)
	for i := range p {
		p[i] = byte(i*7 + int(seq))
	}
	return &Frame{Stream: stream, Kind: KindVideo, Codec: CodecHEVC, Flags: FlagKeyframe,
		Seq: seq, PTS: int64(seq) * 1_000_000, Payload: p}
}

func frameEqual(a, b *Frame) bool {
	return a.Stream == b.Stream && a.Kind == b.Kind && a.Codec == b.Codec && a.Flags == b.Flags &&
		a.Seq == b.Seq && a.PTS == b.PTS && bytes.Equal(a.Payload, b.Payload)
}

func newCryptoPair(t *testing.T) (send, recv *dgCrypto) {
	t.Helper()
	master := testKey()
	s, err := newDgCrypto(master, true)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newDgCrypto(master, false)
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func TestDatagramRoundTripClear(t *testing.T) {
	const token = 0xDEADBEEF
	enc := newDgEncoder(token, nil, 1200)
	for _, plen := range []int{0, 1, 500, 1200, 1201, 5000, 40000} {
		want := sampleFrame(uint32(plen), 1, plen)
		dgs, _, count, err := enc.encodeFrame(nil, want)
		if err != nil {
			t.Fatalf("encode plen=%d: %v", plen, err)
		}
		if int(count) != numFrags(frameWireLen(want), 1200) {
			t.Fatalf("plen=%d count=%d want %d", plen, count, numFrags(frameWireLen(want), 1200))
		}
		r := newReassembler(0, 0)
		var got *Frame
		for _, dg := range dgs {
			f, done := decodeOne(t, r, dg, token, nil, 0)
			if done {
				got = f
			}
		}
		if got == nil || !frameEqual(got, want) {
			t.Fatalf("plen=%d round-trip mismatch", plen)
		}
	}
}

func TestDatagramRoundTripAEAD(t *testing.T) {
	const token = 0x12345678
	send, recv := newCryptoPair(t)
	enc := newDgEncoder(token, send, 1200)
	for _, plen := range []int{0, 800, 1200, 9000, 60000} {
		want := sampleFrame(uint32(plen+1), 2, plen)
		dgs, _, _, err := enc.encodeFrame(nil, want)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		r := newReassembler(0, 0)
		var got *Frame
		for _, dg := range dgs {
			if dg[1]&dgFlagAEAD == 0 {
				t.Fatal("AEAD flag not set")
			}
			f, done := decodeOne(t, r, dg, token, recv, 0)
			if done {
				got = f
			}
		}
		if got == nil || !frameEqual(got, want) {
			t.Fatalf("plen=%d AEAD round-trip mismatch", plen)
		}
	}
}

func TestDatagramReorderAndDuplicate(t *testing.T) {
	const token = 7
	enc := newDgEncoder(token, nil, 300)
	want := sampleFrame(5, 1, 2000) // ~7 fragments
	dgs, _, _, err := enc.encodeFrame(nil, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(dgs) < 3 {
		t.Fatalf("need multiple fragments, got %d", len(dgs))
	}
	r := newReassembler(0, 0)
	// Deliver reversed + each duplicated; only the real last-arriving fragment completes it.
	var got *Frame
	for i := len(dgs) - 1; i >= 0; i-- {
		if f, done := decodeOne(t, r, dgs[i], token, nil, 0); done {
			got = f
		}
		if f, done := decodeOne(t, r, dgs[i], token, nil, 0); done { // duplicate
			got = f
		}
	}
	if got == nil || !frameEqual(got, want) {
		t.Fatal("reorder+dup round-trip mismatch")
	}
}

func TestReassemblerMissing(t *testing.T) {
	const token = 1
	enc := newDgEncoder(token, nil, 300)
	want := sampleFrame(9, 1, 2000)
	dgs, _, _, _ := enc.encodeFrame(nil, want)
	r := newReassembler(0, 0)
	// Deliver all but index 2.
	for i, dg := range dgs {
		if i == 2 {
			continue
		}
		if _, done := decodeOne(t, r, dg, token, nil, 0); done {
			t.Fatal("completed with a missing fragment")
		}
	}
	miss := r.missing(1, 9)
	if len(miss) != 1 || miss[0] != 2 {
		t.Fatalf("missing=%v want [2]", miss)
	}
	// The delivered fragment finishes it.
	if f, done := decodeOne(t, r, dgs[2], token, nil, 0); !done || !frameEqual(f, want) {
		t.Fatal("final fragment did not complete the frame")
	}
}

func TestDatagramTokenMismatch(t *testing.T) {
	enc := newDgEncoder(100, nil, 1200)
	dgs, _, _, _ := enc.encodeFrame(nil, sampleFrame(1, 1, 50))
	h, off, err := parseDatagramHeader(dgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBody(h, dgs[0][off:], 999, nil, dgs[0][:off]); err != errDgToken {
		t.Fatalf("wrong token accepted: %v", err)
	}
}

func TestDatagramAEADTamper(t *testing.T) {
	const token = 3
	send, recv := newCryptoPair(t)
	enc := newDgEncoder(token, send, 1200)
	dgs, _, _, _ := enc.encodeFrame(nil, sampleFrame(1, 1, 400))
	dg := dgs[0]
	dg[len(dg)-1] ^= 0xFF // flip a ciphertext byte
	h, off, err := parseDatagramHeader(dg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBody(h, dg[off:], token, recv, dg[:off]); err == nil {
		t.Fatal("tampered AEAD datagram opened")
	}
	// Tampering the header (AAD) must also fail the open.
	dg2 := dgs[0]
	dg2[14] ^= 0x01 // flip a Stream bit (header/AAD)
	h2, off2, err := parseDatagramHeader(dg2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBody(h2, dg2[off2:], token, recv, dg2[:off2]); err == nil {
		t.Fatal("tampered AAD opened")
	}
}

func TestParseDatagramHeaderHostile(t *testing.T) {
	good := dgHeader{Typ: dgTypeMedia, Token: 1, Stream: 1, FrameSeq: 1, FragIdx: 0, FragCount: 2}.appendHeader(nil)
	cases := []struct {
		name string
		mut  func([]byte) []byte
	}{
		{"short", func(b []byte) []byte { return b[:dgHeaderLen-1] }},
		{"badVersion", func(b []byte) []byte { c := append([]byte(nil), b...); c[0] = 0x90 | dgTypeMedia; return c }},
		{"fragCountZero", func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[22], c[23] = 0, 0
			return c
		}},
		{"fragIdxGECount", func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[20], c[21] = 0, 5 // idx 5
			c[22], c[23] = 0, 2 // count 2
			return c
		}},
		{"fragCountHuge", func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[22], c[23] = 0xFF, 0xFF // 65535 > dgMaxFrags
			return c
		}},
	}
	for _, tc := range cases {
		if _, _, err := parseDatagramHeader(tc.mut(good)); err == nil {
			t.Errorf("%s: expected parse error", tc.name)
		}
	}
}

func TestReassemblerDropOldest(t *testing.T) {
	r := newReassembler(2, 0) // only 2 in-flight frames
	const token = 1
	enc := newDgEncoder(token, nil, 300)
	// Start 3 incomplete frames (one fragment each of a 2-fragment frame); the first is evicted.
	for fs := uint32(1); fs <= 3; fs++ {
		f := sampleFrame(fs, 1, 500)
		dgs, _, _, _ := enc.encodeFrame(nil, f)
		decodeOne(t, r, dgs[0], token, nil, 0) // first fragment only
	}
	if r.missing(1, 1) != nil {
		t.Fatal("oldest frame not evicted under maxFrames")
	}
	if r.missing(1, 3) == nil {
		t.Fatal("newest frame should still be in flight")
	}
	if r.dropped == 0 {
		t.Fatal("drop not counted")
	}
}

func TestReassemblerSweepExpired(t *testing.T) {
	r := newReassembler(0, 0)
	const token = 1
	enc := newDgEncoder(token, nil, 300)
	f := sampleFrame(1, 1, 500)
	dgs, _, _, _ := enc.encodeFrame(nil, f)
	decodeOne(t, r, dgs[0], token, nil, 1000) // addedAt = 1000ns, incomplete
	dropped := r.sweepExpired(2000)            // cutoff after addedAt
	if len(dropped) != 1 || dropped[0] != [2]uint32{1, 1} {
		t.Fatalf("sweepExpired=%v want [[1 1]]", dropped)
	}
	if r.missing(1, 1) != nil {
		t.Fatal("expired frame not removed")
	}
}

func TestReassemblerLateFragmentAfterComplete(t *testing.T) {
	r := newReassembler(0, 0)
	const token = 1
	enc := newDgEncoder(token, nil, 300)
	f := sampleFrame(4, 1, 500)
	dgs, _, _, _ := enc.encodeFrame(nil, f)
	for _, dg := range dgs {
		decodeOne(t, r, dg, token, nil, 0)
	}
	// A duplicate of the first fragment must NOT resurrect the finished frame.
	if _, done := decodeOne(t, r, dgs[0], token, nil, 0); done {
		t.Fatal("completed frame resurrected by a late fragment")
	}
}

// ── fuzz: datagram parse + reassembly must never panic on hostile input ──────────────────────────

func FuzzParseDatagramHeader(f *testing.F) {
	f.Add(dgHeader{Typ: dgTypeMedia, Token: 1, Stream: 1, FrameSeq: 1, FragCount: 1}.appendHeader(nil))
	f.Add([]byte{0x10, 0, 0, 0, 0, 0})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		h, off, err := parseDatagramHeader(data)
		if err != nil {
			return
		}
		if off != dgHeaderLen {
			t.Fatalf("off=%d", off)
		}
		if h.Typ == dgTypeMedia && (h.FragCount == 0 || h.FragIdx >= h.FragCount || h.FragCount > dgMaxFrags) {
			t.Fatalf("invalid frag metadata accepted: %+v", h)
		}
	})
}

func FuzzReassemble(f *testing.F) {
	// Seed corpus: a couple of real datagrams concatenated length-prefixed.
	enc := newDgEncoder(1, nil, 64)
	dgs, _, _, _ := enc.encodeFrame(nil, sampleFrame(1, 1, 200))
	var seed []byte
	for _, dg := range dgs {
		seed = append(seed, byte(len(dg)>>8), byte(len(dg)))
		seed = append(seed, dg...)
	}
	f.Add(seed)
	f.Add([]byte{0, 24, 0x10, 0, 0, 0, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := newReassembler(8, 1<<16) // tight caps: proves bounded memory under hostile input
		i := 0
		for i+2 <= len(data) {
			n := int(data[i])<<8 | int(data[i+1])
			i += 2
			if n == 0 || i+n > len(data) {
				break
			}
			dg := data[i : i+n]
			i += n
			h, off, err := parseDatagramHeader(dg)
			if err != nil {
				continue
			}
			chunk, err := decodeBody(h, dg[off:], 1, nil, dg[:off])
			if err != nil {
				continue
			}
			if wire, done := r.accept(h, chunk, 0); done {
				_, _ = parseFrame(wire) // must not panic
			}
			if r.bytes > r.maxBytes || len(r.entries) > r.maxFrames {
				t.Fatalf("reassembler exceeded caps: bytes=%d entries=%d", r.bytes, len(r.entries))
			}
		}
	})
}
