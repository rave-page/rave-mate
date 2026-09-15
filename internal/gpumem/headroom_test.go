package gpumem

import (
	"errors"
	"testing"
	"time"
)

// errSampler always fails, for the fail-open path (the shared fakeSampler never errors).
type errSampler struct{}

func (errSampler) Sample() ([]AdapterUsage, error)             { return nil, errors.New("boom") }
func (errSampler) SampleProcesses(int) ([]AdapterProcs, error) { return nil, nil }

func TestApplyForceNoEnv(t *testing.T) {
	t.Setenv(forceFreeEnv, "")
	in := Headroom{Present: true, Name: "RTX 3060", BudgetMB: 12000, UsedMB: 8000, FreeMB: 4000}
	if got := applyForce(in); got != in {
		t.Fatalf("no env must be a no-op: got %+v want %+v", got, in)
	}
}

func TestApplyForceOverridesRealReading(t *testing.T) {
	t.Setenv(forceFreeEnv, "150")
	got := applyForce(Headroom{Present: true, Name: "RTX 3060", BudgetMB: 12000, UsedMB: 8000, FreeMB: 4000})
	if !got.Present || got.FreeMB != 150 {
		t.Fatalf("forced free not applied: %+v", got)
	}
	if got.BudgetMB != 12000 || got.UsedMB != 12000-150 {
		t.Fatalf("used must be budget-free: %+v", got)
	}
	if got.Name != "RTX 3060" {
		t.Fatalf("real name must be kept: %+v", got)
	}
}

func TestApplyForceOnEmptyReading(t *testing.T) {
	t.Setenv(forceFreeEnv, "200")
	got := applyForce(Headroom{}) // no real sample (off Windows / before first sample)
	if !got.Present || got.FreeMB != 200 || got.BudgetMB != 200 || got.UsedMB != 0 || got.Name != "forced" {
		t.Fatalf("force on empty reading wrong: %+v", got)
	}
}

func TestApplyForceInvalidIgnored(t *testing.T) {
	for _, v := range []string{"abc", "-5", ""} {
		t.Setenv(forceFreeEnv, v)
		in := Headroom{Present: true, FreeMB: 4000, BudgetMB: 12000, UsedMB: 8000}
		if got := applyForce(in); got != in {
			t.Fatalf("invalid %q must be ignored: %+v", v, got)
		}
	}
}

func TestMonitorHeadroomFromLastSample(t *testing.T) {
	t.Setenv(forceFreeEnv, "")
	m := New(Options{})
	now := time.Now()
	m.last = rankAdapters([]AdapterUsage{
		{Name: "iGPU", LUID: "0:1", BudgetMB: 512, UsedMB: 100, FreeMB: 412, Integrated: true},
		{Name: "RTX 3060", LUID: "0:2", BudgetMB: 12000, UsedMB: 11000, FreeMB: 1000},
	})
	m.lastAt = now
	h := m.Headroom()
	if !h.Present || h.Name != "RTX 3060" || h.FreeMB != 1000 || h.BudgetMB != 12000 {
		t.Fatalf("primary (discrete) adapter must win: %+v", h)
	}
	if !h.At.Equal(now) {
		t.Fatalf("At must carry lastAt: %+v", h)
	}
}

func TestMonitorHeadroomNoSampleFailsOpen(t *testing.T) {
	t.Setenv(forceFreeEnv, "")
	if h := New(Options{}).Headroom(); h.Present {
		t.Fatalf("no sample yet must be Present=false (fail open): %+v", h)
	}
}

func TestReadHeadroomFakeSampler(t *testing.T) {
	t.Setenv(forceFreeEnv, "")
	s := &fakeSampler{adapters: []AdapterUsage{{Name: "RTX 3060", LUID: "0:2", BudgetMB: 12000, UsedMB: 9000, FreeMB: 3000}}}
	h := ReadHeadroom(s)
	if !h.Present || h.FreeMB != 3000 || h.Name != "RTX 3060" {
		t.Fatalf("ReadHeadroom wrong: %+v", h)
	}
}

func TestReadHeadroomSampleErrorFailsOpen(t *testing.T) {
	t.Setenv(forceFreeEnv, "")
	if h := ReadHeadroom(errSampler{}); h.Present {
		t.Fatalf("sample error must fail open: %+v", h)
	}
	if h := ReadHeadroom(nil); h.Present {
		t.Fatalf("nil sampler must fail open: %+v", h)
	}
}

func TestReadHeadroomForceOverridesSampleError(t *testing.T) {
	t.Setenv(forceFreeEnv, "128")
	h := ReadHeadroom(errSampler{})
	if !h.Present || h.FreeMB != 128 {
		t.Fatalf("force must apply even on sample error: %+v", h)
	}
}
