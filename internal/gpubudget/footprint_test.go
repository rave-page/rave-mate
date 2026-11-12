package gpubudget

import (
	"testing"
	"time"

	"rave.page/mate/internal/gpumem"
)

// fakeProcSampler returns a fixed per-adapter process list. topN truncation is honoured so the test
// can prove the self sweep asks for ALL processes (our small children must not be truncated).
type fakeProcSampler struct {
	procs []gpumem.ProcUsage
	calls int
	lastN int
}

func (f *fakeProcSampler) Sample() ([]gpumem.AdapterUsage, error) { return nil, nil }
func (f *fakeProcSampler) SampleProcesses(topN int) ([]gpumem.AdapterProcs, error) {
	f.calls++
	f.lastN = topN
	list := f.procs
	if topN > 0 && len(list) > topN {
		list = list[:topN]
	}
	return []gpumem.AdapterProcs{{Adapter: "RTX 3060", Procs: list}}, nil
}

func TestSelfFootprintSampleSumsOurProcesses(t *testing.T) {
	// Heavy non-ours (Resolume) ranks first; our small children rank last. A top-N sweep would drop
	// them - the self sweep must ask for ALL and still sum ours.
	fs := &fakeProcSampler{procs: []gpumem.ProcUsage{
		{Name: "Arena.exe", PID: 1, MB: 8000},
		{Name: "rave-mate.exe", PID: 2, MB: 40},
		{Name: "rave-mate-feature-media.exe", PID: 3, MB: 120},
		{Name: "rave-mate-enc.exe", PID: 4, MB: 300},
		{Name: "obs64.exe", PID: 5, MB: 500},
	}}
	f := NewSelfFootprint(fs, nil, 30*time.Second)
	mb, known := f.Sample()
	if !known {
		t.Fatal("synchronous sample must be known")
	}
	if mb != 40+120+300 {
		t.Fatalf("self footprint = %d, want %d (daemon+media+enc, excluding Arena/OBS)", mb, 460)
	}
	if fs.lastN != 0 {
		t.Fatalf("self sweep must request ALL processes (topN=0), got %d", fs.lastN)
	}
}

func TestSelfFootprintGetAsyncThenKnown(t *testing.T) {
	fs := &fakeProcSampler{procs: []gpumem.ProcUsage{{Name: "rave-mate.exe", PID: 2, MB: 55}}}
	f := NewSelfFootprint(fs, nil, 30*time.Second)
	// First Get kicks the async refresh and returns unknown.
	if _, known := f.Get(); known {
		t.Fatal("first Get must be unknown (async)")
	}
	// Give the background sweep a moment, then it is known.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mb, known := f.Get(); known {
			if mb != 55 {
				t.Fatalf("async footprint = %d, want 55", mb)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("async refresh never landed")
}

func TestSelfFootprintNilSampler(t *testing.T) {
	f := NewSelfFootprint(nil, nil, 0)
	if _, known := f.Sample(); known {
		t.Fatal("nil sampler => unknown")
	}
	if _, known := f.Get(); known {
		t.Fatal("nil sampler => unknown")
	}
}
