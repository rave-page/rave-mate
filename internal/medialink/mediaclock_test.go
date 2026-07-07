package medialink

import (
	"encoding/json"
	"testing"
)

func TestMonotonicClock(t *testing.T) {
	c := NewMonotonicClock()
	t0 := c.Now()
	if t0 < 0 {
		t.Fatalf("Now() negative: %d", t0)
	}
	if q := c.Quality(); q.Tier != TierMonotonic || !q.Locked {
		t.Fatalf("quality = %+v", q)
	}
	c.SetOffset(1_000_000) // +1 ms slew (software-sync tier hook)
	if got := c.Quality().OffsetNs; got != 1_000_000 {
		t.Fatalf("offset = %d, want 1e6", got)
	}
	if c.Now() < t0+1_000_000 {
		t.Fatal("offset not applied to Now()")
	}
	var _ ClockSource = c // interface seam holds
}

// Offer/Answer must round-trip the §2.1 wire reservations (transport/nack/fec), and omit them when
// absent (default = tcp,off,off) so a P1 peer's frames stay minimal.
func TestNegotiateReservedFields(t *testing.T) {
	off := Offer{Session: "s1", Target: "B", SourceID: "mic", SinkID: "spk", Codec: CodecPCMS24,
		Transport: TransportUDP, NACK: true, FEC: &FEC{Scheme: "rs", K: 8, N: 10}}
	raw, err := json.Marshal(off)
	if err != nil {
		t.Fatal(err)
	}
	var got Offer
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Transport != TransportUDP || !got.NACK || got.FEC == nil || got.FEC.Scheme != "rs" || got.FEC.K != 8 || got.FEC.N != 10 {
		t.Fatalf("offer reservation round-trip lost fields: %+v", got)
	}

	// Defaults omit the reserved fields entirely.
	minRaw, _ := json.Marshal(Offer{Session: "s2", SinkID: "spk"})
	var m map[string]json.RawMessage
	if err := json.Unmarshal(minRaw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"transport", "nack", "fec"} {
		if _, present := m[k]; present {
			t.Fatalf("default offer must omit %q", k)
		}
	}

	ans := Answer{Session: "s1", Accept: true, Addr: "10.0.0.2:47641", Stream: 3, Transport: TransportTCP}
	araw, _ := json.Marshal(ans)
	var ga Answer
	if err := json.Unmarshal(araw, &ga); err != nil {
		t.Fatal(err)
	}
	if ga.Stream != 3 || ga.Transport != TransportTCP || !ga.Accept {
		t.Fatalf("answer round-trip: %+v", ga)
	}
}
