package medialink

import (
	"bytes"
	"testing"
)

// TestPlainConnRoundTrip: the both-peers-opted-out plaintext media plane frames round-trip (no
// AEAD), and the payload is visible on the wire (proves the plaintext branch, not a sealed one).
func TestPlainConnRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	wc := NewPlainConn(memRW{w: &buf})
	frames := []*Frame{
		{Stream: 1, Kind: KindAudio, Codec: CodecPCMS16, Seq: 1, Payload: []byte("hello-plain")},
		{Stream: 2, Kind: KindAudio, Codec: CodecPCMS16, Seq: 2, Payload: bytes.Repeat([]byte{0x9}, 4096)},
		{Stream: 3, Kind: KindAudio, Codec: CodecPCMS16, Seq: 3, Payload: nil}, // empty frame
	}
	for _, f := range frames {
		if err := wc.WriteFrame(f); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if !bytes.Contains(buf.Bytes(), []byte("hello-plain")) {
		t.Fatal("plaintext conn did not leave the payload in the clear")
	}
	rc := NewPlainConn(memRW{r: bytes.NewReader(buf.Bytes())})
	for i, want := range frames {
		got, err := rc.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d read: %v", i, err)
		}
		if got.Seq != want.Seq || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("frame %d mismatch: seq=%d payload=%dB", i, got.Seq, len(got.Payload))
		}
	}
}

// TestPlaneEncryptMatrix pins the media-plane decision for every pref combination and asserts it
// is symmetric (both ends call planeEncrypt on the same two prefs and must agree).
func TestPlaneEncryptMatrix(t *testing.T) {
	cases := []struct {
		a, b string
		enc  bool
	}{
		{EncOn, EncOn, true},
		{EncOn, EncOff, true},
		{EncOff, EncOn, true},
		{EncOff, EncOff, false},
		{"", EncOff, true}, // absent = ON: an older peer never downgrades this plane
		{EncOff, "", true},
		{"", "", true},
	}
	for _, c := range cases {
		if got := planeEncrypt(c.a, c.b); got != c.enc {
			t.Errorf("planeEncrypt(%q,%q)=%v want %v", c.a, c.b, got, c.enc)
		}
		if planeEncrypt(c.a, c.b) != planeEncrypt(c.b, c.a) {
			t.Errorf("planeEncrypt not symmetric for (%q,%q)", c.a, c.b)
		}
	}
}
