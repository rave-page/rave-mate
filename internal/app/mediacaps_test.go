package app

import (
	"slices"
	"sync"
	"testing"

	"rave.page/mate/internal/encoderscan"
)

const (
	testLUIDNV = "0x00000000_0x0000c1a1"
	testLUIDIN = "0x00000000_0x0000b0b0"
)

type capsSink struct {
	mu    sync.Mutex
	calls [][]string
}

func (s *capsSink) push(enc, _ []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slices.Clone(enc))
}

func (s *capsSink) last() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return nil
	}
	return s.calls[len(s.calls)-1]
}

func (s *capsSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// report with two adapters; obsOnNV marks a live OBS stream holding the NVIDIA encoder.
func fakeReport(obsOnNV bool) encoderscan.Report {
	r := encoderscan.Report{
		ProtectedAdapter: map[string]bool{},
		ProtectedFamily:  map[encoderscan.EncoderFamily]bool{},
		AdapterEncPct:    map[string]float64{testLUIDNV: 45, testLUIDIN: 3},
		AdapterNames:     map[string]string{testLUIDNV: "NVIDIA GeForce RTX 4090", testLUIDIN: "Intel(R) Arc(TM) A770"},
		AdapterVRAMFree:  map[string]float64{testLUIDNV: 9000, testLUIDIN: 6000},
	}
	if obsOnNV {
		r.ProtectedFamily[encoderscan.FamilyNVENC] = true
		r.ProtectedAdapter[testLUIDNV] = true
	}
	return r
}

func newTestCaps(sink *capsSink, live *bool, rep func() encoderscan.Report) *mediaCaps {
	m := newMediaCaps(nil, sink.push)
	m.scan = rep
	m.streaming = func() bool { return *live }
	m.cpu = func() float64 { return 25 }
	return m
}

// A live stream on NVENC must steer the advertisement onto the free adapter, and give NVENC back
// when the stream ends - without a re-probe.
func TestMediaCapsReplansOnStreamingEdge(t *testing.T) {
	sink := &capsSink{}
	live := false
	m := newTestCaps(sink, &live, func() encoderscan.Report { return fakeReport(live) })

	m.setProbed([]string{"hevc_nvenc", "h264_nvenc", "h264_qsv", "libx264"}, []string{"h264", "hevc"}, true)
	if got := sink.last(); !slices.Contains(got, "hevc_nvenc") {
		t.Fatalf("idle machine must advertise nvenc, got %v", got)
	}

	live = true
	m.onGovernorChange()
	got := sink.last()
	if slices.Contains(got, "hevc_nvenc") || slices.Contains(got, "h264_nvenc") {
		t.Errorf("stream live: nvenc must be withheld, got %v", got)
	}
	if !slices.Contains(got, "h264_qsv") {
		t.Errorf("the free hardware encoder must stay advertised, got %v", got)
	}
	if _, _, plan := m.snapshot(); len(plan.Withheld) != 2 {
		t.Errorf("want 2 withheld encoders recorded, got %v", plan.Withheld)
	}

	live = false
	m.onGovernorChange()
	if got := sink.last(); !slices.Contains(got, "hevc_nvenc") {
		t.Errorf("stream ended: nvenc must come back, got %v", got)
	}
}

// Non-streaming governor signals (focus/minimize/drag) must not trigger a re-plan: each one costs a
// process + PDH scan and they flap with normal window use.
func TestMediaCapsIgnoresNonStreamingEdges(t *testing.T) {
	sink := &capsSink{}
	live := false
	scans := 0
	m := newTestCaps(sink, &live, func() encoderscan.Report { scans++; return fakeReport(live) })
	m.setProbed([]string{"h264_qsv"}, []string{"h264"}, true)
	before, scansBefore := sink.count(), scans
	for range 5 {
		m.onGovernorChange() // same streaming state each time
	}
	if sink.count() != before || scans != scansBefore {
		t.Errorf("no streaming edge → no re-plan/scan: pushes %d→%d scans %d→%d",
			before, sink.count(), scansBefore, scans)
	}
}

// SetCodecCaps re-advertises to every peer, so an unchanged list must not be pushed again.
func TestMediaCapsSkipsUnchangedPush(t *testing.T) {
	sink := &capsSink{}
	live := false
	m := newTestCaps(sink, &live, func() encoderscan.Report { return fakeReport(false) })
	m.setProbed([]string{"h264_qsv"}, []string{"h264"}, true)
	if sink.count() != 1 {
		t.Fatalf("want 1 push, got %d", sink.count())
	}
	m.replan("test")
	if sink.count() != 1 {
		t.Errorf("identical plan must not re-push, got %d pushes", sink.count())
	}
}

// Nothing probed yet = nothing to advertise: a re-plan must not push an empty list (that would make
// peers refuse routes / fall back to raw).
func TestMediaCapsNoProbeNoPush(t *testing.T) {
	sink := &capsSink{}
	live := true
	m := newTestCaps(sink, &live, func() encoderscan.Report { return fakeReport(true) })
	m.onGovernorChange()
	m.replan("test")
	if sink.count() != 0 {
		t.Errorf("must not advertise before a probe result, got %v", sink.calls)
	}
}

// The validated flag rides through to the diagnostic snapshot (listing-only probe must not claim
// its encoders were proven).
func TestMediaCapsValidatedFlagSnapshot(t *testing.T) {
	sink := &capsSink{}
	live := false
	m := newTestCaps(sink, &live, func() encoderscan.Report { return fakeReport(false) })
	m.setProbed([]string{"h264_qsv"}, nil, false)
	if _, validated, _ := m.snapshot(); validated {
		t.Error("listing-only probe must report validated=false")
	}
	m.setProbed([]string{"h264_qsv"}, nil, true)
	if _, validated, _ := m.snapshot(); !validated {
		t.Error("test-encoded probe must report validated=true")
	}
}
