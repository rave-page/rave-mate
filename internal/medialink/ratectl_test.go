package medialink

import "testing"

func TestRateHintRoundTrip(t *testing.T) {
	in := RateHint{Type: MetaRate, Stream: 0, MaxBitrateKbps: 8000, MaxFPS: 30, MaxHeight: 1080}
	f, err := MetaFrame(in, 12345)
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != KindMeta || f.Stream != metaStream {
		t.Fatalf("not a stream-0 meta frame: kind=%d stream=%d", f.Kind, f.Stream)
	}
	got, err := DecodeRate(f)
	if err != nil {
		t.Fatal(err)
	}
	if got != in {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, in)
	}
}

func TestDecodeRateRejectsOtherMeta(t *testing.T) {
	nf, err := MetaFrame(NACK{Type: MetaNACK, FrameLevel: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRate(nf); err == nil {
		t.Fatal("DecodeRate must reject a non-rate meta frame")
	}
}

// fakeRateSrc is a minimal RateControlSource for the dispatch assertion.
type fakeRateSrc struct {
	Source
	got RateHint
}

func (f *fakeRateSrc) SetRateHint(h RateHint) { f.got = h }

func TestRateLadder(t *testing.T) {
	const floor = 1024
	base, fps := 20000, 60
	if h := rateLadder(floor*2, floor, base, fps, 7); h.MaxBitrateKbps != base || h.MaxFPS != fps || h.MaxHeight != 0 {
		t.Fatalf("healthy must restore full quality: %+v", h)
	}
	h1 := rateLadder(floor+10, floor, base, fps, 7)   // tier 1: bitrate only
	h2 := rateLadder(floor/2+10, floor, base, fps, 7) // tier 2: bitrate + fps
	h3 := rateLadder(10, floor, base, fps, 7)         // tier 3: hard trim
	if h1.MaxBitrateKbps >= base || h1.MaxFPS != fps {
		t.Fatalf("tier1 must trim bitrate only: %+v", h1)
	}
	if h2.MaxFPS >= fps || h3.MaxFPS >= h2.MaxFPS {
		t.Fatalf("fps must step down at tier2/3: %+v %+v", h2, h3)
	}
	// bitrate monotonic non-increasing as headroom worsens
	if !(h3.MaxBitrateKbps <= h2.MaxBitrateKbps && h2.MaxBitrateKbps <= h1.MaxBitrateKbps && h1.MaxBitrateKbps <= base) {
		t.Fatalf("bitrate not monotonic: base=%d t1=%d t2=%d t3=%d", base, h1.MaxBitrateKbps, h2.MaxBitrateKbps, h3.MaxBitrateKbps)
	}
	// OWNER RULE: resolution is the last resort and is NEVER cut by the ladder.
	for i, h := range []RateHint{h1, h2, h3} {
		if h.MaxHeight != 0 {
			t.Fatalf("tier%d cut resolution (forbidden): %+v", i+1, h)
		}
		if h.MaxBitrateKbps <= 0 || h.MaxFPS <= 0 {
			t.Fatalf("tier%d zeroed a stream: %+v", i+1, h)
		}
	}
}

func TestRateLadderNeverZerosASmallStream(t *testing.T) {
	h := rateLadder(10, 1024, 900, 8, 0) // base already below the sane floors
	if h.MaxBitrateKbps <= 0 || h.MaxFPS <= 0 {
		t.Fatalf("must never zero a stream: %+v", h)
	}
}

func TestRateControlSourceReceivesHint(t *testing.T) {
	var rc RateControlSource = &fakeRateSrc{}
	h := RateHint{Type: MetaRate, MaxBitrateKbps: 6000, MaxFPS: 24}
	rc.SetRateHint(h)
	if rc.(*fakeRateSrc).got != h {
		t.Fatalf("hint not delivered: %+v", rc.(*fakeRateSrc).got)
	}
}
