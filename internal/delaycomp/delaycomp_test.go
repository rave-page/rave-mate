package delaycomp

import (
	"context"
	"errors"
	"testing"
	"time"

	"rave.page/mate/internal/obs"
)

func TestPlanAlignsToSlowest(t *testing.T) {
	lat := map[string]Latency{
		"fast":   {RTTms: 5, RenderMs: 20},  // total 25
		"medium": {RTTms: 10, RenderMs: 50}, // total 60
		"slow":   {RTTms: 8, RenderMs: 100}, // total 108 (reference)
	}
	plan := Plan(lat)
	if plan["slow"] != 0 {
		t.Errorf("slowest comp = %d, want 0", plan["slow"])
	}
	if plan["medium"] != 48 { // 108 - 60
		t.Errorf("medium comp = %d, want 48", plan["medium"])
	}
	if plan["fast"] != 83 { // 108 - 25
		t.Errorf("fast comp = %d, want 83", plan["fast"])
	}
}

func TestPlanEmpty(t *testing.T) {
	if got := Plan(nil); len(got) != 0 {
		t.Errorf("empty plan = %v", got)
	}
}

func TestPlanManualTrimClampsNonNegative(t *testing.T) {
	// A large negative manual trim can't produce a negative delay (only-add semantics).
	lat := map[string]Latency{
		"a": {RenderMs: 50},
		"b": {RenderMs: 50, ManualMs: -1000},
	}
	plan := Plan(lat)
	if plan["a"] < 0 || plan["b"] < 0 {
		t.Errorf("negative comp: %v", plan)
	}
}

func TestVideoDelayKind(t *testing.T) {
	if VideoDelayKind(200) != obs.FilterKindGPUDelay {
		t.Error("200ms should use gpu_delay")
	}
	if VideoDelayKind(500) != obs.FilterKindGPUDelay {
		t.Error("500ms should still use gpu_delay (boundary)")
	}
	if VideoDelayKind(501) != obs.FilterKindAsyncDelay {
		t.Error("501ms should use async_delay_filter")
	}
}

func TestProbeRTT(t *testing.T) {
	// deterministic RTTs; min = 40ms → one-way 20ms.
	rtts := []time.Duration{60 * time.Millisecond, 40 * time.Millisecond, 100 * time.Millisecond}
	i := 0
	oneWay, err := ProbeRTT(len(rtts), func() (time.Duration, error) {
		d := rtts[i]
		i++
		return d, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if oneWay < 19.9 || oneWay > 20.1 {
		t.Errorf("one-way = %vms, want ~20ms", oneWay)
	}
}

func TestProbeRTTAllFail(t *testing.T) {
	_, err := ProbeRTT(3, func() (time.Duration, error) { return 0, errors.New("down") })
	if err == nil {
		t.Fatal("want error when all probes fail")
	}
}

// fakeOBS records delaycomp's OBS calls.
type fakeOBS struct {
	filters    []obs.FilterInfo
	created    bool
	createKind string
	audioOff   int
	setDelay   int
}

func (f *fakeOBS) SetInputAudioSyncOffset(_ context.Context, _ string, off int) error {
	f.audioOff = off
	return nil
}
func (f *fakeOBS) CreateSourceFilter(_ context.Context, _, name, kind string, s map[string]any) error {
	f.created, f.createKind = true, kind
	f.filters = append(f.filters, obs.FilterInfo{Name: name, Kind: kind, Settings: s})
	if d, ok := s["delay_ms"].(int); ok {
		f.setDelay = d
	}
	return nil
}
func (f *fakeOBS) SetSourceFilterSettings(_ context.Context, _, _ string, s map[string]any, _ bool) error {
	if d, ok := s["delay_ms"].(int); ok {
		f.setDelay = d
	}
	return nil
}
func (f *fakeOBS) GetSourceFilterList(_ context.Context, _ string) ([]obs.FilterInfo, error) {
	return f.filters, nil
}

func TestApplyOBSVideoDelayCreatesThenUpdates(t *testing.T) {
	f := &fakeOBS{}
	ctx := context.Background()
	// First apply: filter absent → created (gpu_delay for 300ms).
	if err := ApplyOBSVideoDelay(ctx, f, "Cam", 300); err != nil {
		t.Fatal(err)
	}
	if !f.created || f.createKind != obs.FilterKindGPUDelay || f.setDelay != 300 {
		t.Errorf("create: created=%v kind=%s delay=%d", f.created, f.createKind, f.setDelay)
	}
	// Second apply: filter present → settings updated only (no recreate).
	f.created = false
	if err := ApplyOBSVideoDelay(ctx, f, "Cam", 420); err != nil {
		t.Fatal(err)
	}
	if f.created {
		t.Error("second apply should update, not recreate")
	}
	if f.setDelay != 420 {
		t.Errorf("updated delay = %d, want 420", f.setDelay)
	}
}

func TestApplyOBSAudioOffset(t *testing.T) {
	f := &fakeOBS{}
	if err := ApplyOBSAudioOffset(context.Background(), f, "Mic", -120); err != nil {
		t.Fatal(err)
	}
	if f.audioOff != -120 {
		t.Errorf("audio offset = %d, want -120", f.audioOff)
	}
}
