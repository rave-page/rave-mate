//go:build windows

package gpumem

import (
	"os"
	"testing"
	"unsafe"
)

// TestStructSizes mirrors the SDK C_ASSERT(sizeof(D3DKMT_QUERYSTATISTICS)==0x328): a wrong
// layout makes the input segmentID land at the wrong offset and every query returns garbage.
func TestStructSizes(t *testing.T) {
	if got := unsafe.Sizeof(queryStatistics{}); got != 0x328 {
		t.Fatalf("queryStatistics size %#x != 0x328", got)
	}
	if got := unsafe.Sizeof(adapterInfo{}); got != 20 {
		t.Fatalf("adapterInfo size %d != 20", got)
	}
}

// TestLiveSample hits real hardware. Opt-in (RAVE_GPUMEM_LIVE=1); cross-check the printed
// numbers against a same-moment `nvidia-smi --query-gpu=memory.total,memory.used --format=csv`.
func TestLiveSample(t *testing.T) {
	if os.Getenv("RAVE_GPUMEM_LIVE") != "1" {
		t.Skip("set RAVE_GPUMEM_LIVE=1 to run the live D3DKMT probe")
	}
	s := newSampler()
	ads, err := s.Sample()
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	real := 0
	for _, a := range ads {
		if a.BudgetMB > 0 {
			real++
		}
		t.Logf("[gpumem] vram adapter=%q luid=%s usedMB=%d freeMB=%d budgetMB=%d",
			a.Name, a.LUID, a.UsedMB, a.FreeMB, a.BudgetMB)
	}
	if real == 0 {
		t.Fatal("no adapter with budget>0")
	}
	aps, err := s.SampleProcesses(5)
	if err != nil {
		t.Fatalf("SampleProcesses: %v", err)
	}
	for _, ap := range aps {
		t.Logf("[gpumem] vram by process adapter=%q top: %s", ap.Adapter, topString(ap.Procs))
	}
}
