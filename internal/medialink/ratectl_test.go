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

func TestRateControlSourceReceivesHint(t *testing.T) {
	var rc RateControlSource = &fakeRateSrc{}
	h := RateHint{Type: MetaRate, MaxBitrateKbps: 6000, MaxFPS: 24}
	rc.SetRateHint(h)
	if rc.(*fakeRateSrc).got != h {
		t.Fatalf("hint not delivered: %+v", rc.(*fakeRateSrc).got)
	}
}
