package netstats

import (
	"bytes"
	"io"
	"math"
	"sync/atomic"
	"testing"
	"time"
)

func TestRingPushWrap(t *testing.T) {
	r := NewRing(3)
	if _, ok := r.Latest(); ok {
		t.Fatal("empty ring reported a latest value")
	}
	r.Push(1)
	r.Push(2)
	if got := r.Values(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("Values = %v, want [1 2]", got)
	}
	r.Push(3)
	r.Push(4) // evicts 1
	r.Push(5) // evicts 2
	if got := r.Values(); len(got) != 3 || got[0] != 3 || got[1] != 4 || got[2] != 5 {
		t.Fatalf("Values = %v, want [3 4 5]", got)
	}
	if v, ok := r.Latest(); !ok || v != 5 {
		t.Fatalf("Latest = %v,%v, want 5,true", v, ok)
	}
}

func TestSamplerRates(t *testing.T) {
	s := NewSampler(4)
	t0 := time.Unix(1000, 0)
	s.Tick(t0, Totals{PeerIn: 100, PeerOut: 50, APIIn: 10, APIOut: 5}) // baseline
	if got := s.Snapshot(); len(got.PeerIn) != 0 {
		t.Fatalf("baseline tick pushed samples: %v", got.PeerIn)
	}
	s.Tick(t0.Add(time.Second), Totals{PeerIn: 1124, PeerOut: 50, APIIn: 10, APIOut: 1005})
	s.Tick(t0.Add(3*time.Second), Totals{PeerIn: 1124, PeerOut: 250, APIIn: 10, APIOut: 1005}) // 2s gap
	snap := s.Snapshot()
	if want := []float64{1024, 0}; !eq(snap.PeerIn, want) {
		t.Fatalf("PeerIn = %v, want %v", snap.PeerIn, want)
	}
	if want := []float64{0, 100}; !eq(snap.PeerOut, want) {
		t.Fatalf("PeerOut = %v, want %v", snap.PeerOut, want)
	}
	if want := []float64{1000, 0}; !eq(snap.APIOut, want) {
		t.Fatalf("APIOut = %v, want %v", snap.APIOut, want)
	}
	if snap.SessPeerIn != 1024 || snap.SessPeerOut != 200 || snap.SessAPIIn != 0 || snap.SessAPIOut != 1000 {
		t.Fatalf("session totals = %d/%d/%d/%d", snap.SessPeerIn, snap.SessPeerOut, snap.SessAPIIn, snap.SessAPIOut)
	}
}

func TestSamplerCounterReset(t *testing.T) {
	s := NewSampler(4)
	t0 := time.Unix(0, 0)
	s.Tick(t0, Totals{PeerIn: 500})
	s.Tick(t0.Add(time.Second), Totals{PeerIn: 700})
	s.Tick(t0.Add(2*time.Second), Totals{PeerIn: 30}) // reconnect: fresh counter
	snap := s.Snapshot()
	if want := []float64{200, 30}; !eq(snap.PeerIn, want) {
		t.Fatalf("PeerIn = %v, want %v", snap.PeerIn, want)
	}
	if snap.SessPeerIn != 230 {
		t.Fatalf("SessPeerIn = %d, want 230", snap.SessPeerIn)
	}
}

func TestSamplerZeroDtIgnored(t *testing.T) {
	s := NewSampler(4)
	t0 := time.Unix(0, 0)
	s.Tick(t0, Totals{})
	s.Tick(t0, Totals{PeerIn: 100}) // same instant → no sample
	if snap := s.Snapshot(); len(snap.PeerIn) != 0 {
		t.Fatalf("dt=0 pushed samples: %v", snap.PeerIn)
	}
}

func TestSamplerRTTLifecycle(t *testing.T) {
	s := NewSampler(4)
	t0 := time.Unix(0, 0)
	s.Tick(t0, Totals{RTT: map[string]RTTSample{"a": {Label: "vr", Ms: math.NaN()}}})
	s.Tick(t0.Add(time.Second), Totals{RTT: map[string]RTTSample{
		"a": {Label: "vr", Ms: 3.5},
		"b": {Label: "dj", Ms: 1.25},
	}})
	snap := s.Snapshot()
	if len(snap.RTT) != 2 {
		t.Fatalf("RTT series = %d, want 2", len(snap.RTT))
	}
	// sorted by label: dj before vr
	if snap.RTT[0].Label != "dj" || !snap.RTT[0].Has || snap.RTT[0].LatestMs != 1.25 {
		t.Fatalf("RTT[0] = %+v", snap.RTT[0])
	}
	if snap.RTT[1].NodeID != "a" || snap.RTT[1].LatestMs != 3.5 || len(snap.RTT[1].Ms) != 2 {
		t.Fatalf("RTT[1] = %+v", snap.RTT[1])
	}
	if !math.IsNaN(snap.RTT[1].Ms[0]) {
		t.Fatalf("first sample should be NaN, got %v", snap.RTT[1].Ms[0])
	}
	// peer b departs → pruned
	s.Tick(t0.Add(2*time.Second), Totals{RTT: map[string]RTTSample{"a": {Label: "vr", Ms: 4}}})
	snap = s.Snapshot()
	if len(snap.RTT) != 1 || snap.RTT[0].NodeID != "a" {
		t.Fatalf("prune failed: %+v", snap.RTT)
	}
}

func TestSamplerRTTNaNLatest(t *testing.T) {
	s := NewSampler(4)
	s.Tick(time.Unix(0, 0), Totals{RTT: map[string]RTTSample{"a": {Label: "x", Ms: math.NaN()}}})
	snap := s.Snapshot()
	if snap.RTT[0].Has {
		t.Fatalf("NaN-only series reported Has: %+v", snap.RTT[0])
	}
}

func TestCountBody(t *testing.T) {
	var ctr atomic.Uint64
	rc := CountBody(io.NopCloser(bytes.NewReader(make([]byte, 300))), &ctr)
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if ctr.Load() != 300 {
		t.Fatalf("counted %d, want 300", ctr.Load())
	}
}

func eq(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
