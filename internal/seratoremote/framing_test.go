package seratoremote

import (
	"bytes"
	"testing"

	"rave.page/mate/internal/osc"
)

// Ported from serato-connect tests/remote/framing.test.ts - parity vectors for the bare-OSC
// + 16-byte-sentinel TCP framing.

func addrs(msgs []osc.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Address
	}
	return out
}

func TestFrameOneWholeFrame(t *testing.T) {
	r := newFrameReader(0)
	out, err := r.push(frame(osc.Msg(pathPing)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Address != pathPing {
		t.Fatalf("got %v", addrs(out))
	}
	if r.pending() != 0 {
		t.Fatalf("pending %d", r.pending())
	}
}

func TestFrameSplitAcrossChunks(t *testing.T) {
	r := newFrameReader(0)
	full := frame(osc.Msg(pathSongTitle, osc.ArgInt(0), osc.ArgString("Hello")))
	var got []osc.Message
	for i := 0; i < len(full); i++ { // 1-byte chunks
		msgs, err := r.push(full[i:i+1], nil)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, msgs...)
	}
	if len(got) != 1 || got[0].Address != pathSongTitle {
		t.Fatalf("got %v", addrs(got))
	}
	if r.pending() != 0 {
		t.Fatalf("pending %d", r.pending())
	}
}

func TestFrameMultipleInOneChunk(t *testing.T) {
	r := newFrameReader(0)
	var chunk []byte
	chunk = append(chunk, frame(osc.Msg(pathPing))...)
	chunk = append(chunk, frame(osc.Msg(pathCrossfader, osc.ArgFloat(0.5)))...)
	chunk = append(chunk, frame(osc.Msg(pathSongValid, osc.ArgInt(1), osc.ArgFloat(1)))...)
	out, err := r.push(chunk, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{pathPing, pathCrossfader, pathSongValid}
	if got := addrs(out); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFramePartialBufferedForNextPush(t *testing.T) {
	r := newFrameReader(0)
	a := frame(osc.Msg(pathPing))
	b := frame(osc.Msg(pathSongTitle, osc.ArgInt(2), osc.ArgString("next")))
	combined := append(append([]byte(nil), a...), b...)
	split := len(a) + 4 // mid-way through the second frame
	first, err := r.push(combined[:split], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Address != pathPing {
		t.Fatalf("first %v", addrs(first))
	}
	if r.pending() == 0 {
		t.Fatal("expected buffered bytes")
	}
	second, err := r.push(combined[split:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Address != pathSongTitle {
		t.Fatalf("second %v", addrs(second))
	}
}

func TestFrameAppendsSentinel(t *testing.T) {
	f := frame(osc.Msg(pathPing))
	bare := osc.Encode(osc.Msg(pathPing))
	if len(f) != len(bare)+len(Sentinel) {
		t.Fatalf("frame len %d", len(f))
	}
	if !bytes.Equal(f[len(bare):], Sentinel) {
		t.Fatal("sentinel not appended")
	}
}

func TestFrameSentinelMismatch(t *testing.T) {
	r := newFrameReader(0)
	bare := osc.Encode(osc.Msg(pathPing))
	bad := append(append([]byte(nil), bare...), make([]byte, len(Sentinel))...) // zero trailer
	if _, err := r.push(bad, nil); err != errSentinelMismatch {
		t.Fatalf("expected sentinel mismatch, got %v", err)
	}
}

func TestFrameCapturedAuthorizeBlob(t *testing.T) {
	// Captured 2026-05-05: ,bii with 16-byte blob and two int32=1.
	r := newFrameReader(0)
	blob := []byte{0x3f, 0x47, 0x50, 0x0e, 0x7d, 0x40, 0x14, 0x1e, 0x77, 0x4a, 0x9b, 0x90, 0x8e, 0xf6, 0xbe, 0xc0}
	out, err := r.push(frame(osc.Msg(pathAuthorizeRequest, osc.ArgBlob(blob), osc.ArgInt(1), osc.ArgInt(1))), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Address != pathAuthorizeRequest {
		t.Fatalf("addr %v", addrs(out))
	}
	if len(out[0].Args) != 3 || out[0].Args[0].Kind != osc.KindBlob || len(out[0].Args[0].Blob) != 16 {
		t.Fatalf("args %+v", out[0].Args)
	}
}

func TestFrameRawCapture(t *testing.T) {
	r := newFrameReader(0)
	var raw [][]byte
	if _, err := r.push(frame(osc.Msg(pathPing)), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || !bytes.Equal(raw[0], osc.Encode(osc.Msg(pathPing))) {
		t.Fatalf("raw capture mismatch: %d frames", len(raw))
	}
}

func TestFrameOversizedDrops(t *testing.T) {
	r := newFrameReader(64) // tiny cap
	// A string arg long enough to exceed the cap.
	big := make([]byte, 200)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := r.push(frame(osc.Msg(pathSongTitle, osc.ArgInt(0), osc.ArgString(string(big)))), nil); err != errFrameTooLarge {
		t.Fatalf("expected frame-too-large, got %v", err)
	}
}
