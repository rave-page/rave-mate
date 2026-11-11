package medialink

import (
	"bytes"
	"net"
	"testing"
)

func netParse(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad test IP %q", s)
	}
	return ip
}

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

// TestResolveEncMatrix pins the media-plane AEAD decision for every (pref, pref, bothLocal) combo
// and asserts it is symmetric (both ends evaluate resolveEnc on the same inputs and must agree, or
// the AEAD keys mismatch). `on`/legacy-absent forces AEAD; auto/off go cleartext only when local.
func TestResolveEncMatrix(t *testing.T) {
	cases := []struct {
		a, b  string
		local bool
		enc   bool
	}{
		{EncOn, EncOn, true, true},      // on forces AEAD even on-subnet
		{EncOn, EncAuto, true, true},    // one on → AEAD
		{EncAuto, EncOn, true, true},    //
		{EncAuto, EncAuto, true, false}, // both auto + same subnet → cleartext (the LAN win)
		{EncAuto, EncAuto, false, true}, // both auto but routed → AEAD
		{EncOff, EncAuto, true, false},  // off+auto on-subnet → cleartext
		{EncOff, EncOff, true, false},   // off+off on-subnet → cleartext
		{EncOff, EncOff, false, true},   // off+off but routed → AEAD (safety tightening)
		{"", EncAuto, true, true},       // legacy absent = on: never downgrades
		{"", EncOff, true, true},        //
		{EncAuto, "", true, true},       //
	}
	for _, c := range cases {
		if got := resolveEnc(c.a, c.b, c.local); got != c.enc {
			t.Errorf("resolveEnc(%q,%q,local=%v)=%v want %v", c.a, c.b, c.local, got, c.enc)
		}
		if resolveEnc(c.a, c.b, c.local) != resolveEnc(c.b, c.a, c.local) {
			t.Errorf("resolveEnc not symmetric for (%q,%q,local=%v)", c.a, c.b, c.local)
		}
	}
}

// TestIsPrivateIP pins the address-class gate for enc=auto's LAN-cleartext decision.
func TestIsPrivateIP(t *testing.T) {
	priv := []string{"10.0.0.5", "172.16.3.4", "172.31.0.1", "192.168.1.50", "169.254.10.1",
		"fe80::1", "fc00::1", "fd12:3456::1"}
	pub := []string{"8.8.8.8", "1.1.1.1", "172.32.0.1", "100.64.0.1", "2606:4700::1"}
	for _, s := range priv {
		if !isPrivateIP(netParse(t, s)) {
			t.Errorf("%s should be private/LAN-trusted", s)
		}
	}
	for _, s := range pub {
		if isPrivateIP(netParse(t, s)) {
			t.Errorf("%s should NOT be LAN-trusted (routed/public/CGNAT)", s)
		}
	}
}
