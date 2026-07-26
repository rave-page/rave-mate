//go:build windows

package gpuwatch

import (
	"syscall"
	"testing"
	"time"
)

// scanTDR must complete promptly against the real System log - the query is time-bounded (1h) and
// returns at the first parsed record, where the unbounded variant walked the entire log per poll.
func TestScanTDRBounded(t *testing.T) {
	if err := procEvtQuery.Find(); err != nil {
		t.Skip("wevtapi unavailable")
	}
	chanPtr, _ := syscall.UTF16PtrFromString("System")
	qPtr, _ := syscall.UTF16PtrFromString(tdrQuery)
	start := time.Now()
	newest, _, ok := scanTDR(chanPtr, qPtr)
	if !ok {
		t.Fatal("scanTDR query failed")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("scan took %v - not bounded", d)
	}
	// Two scans in a row agree on the tip (or both see an empty window) - deterministic input for
	// the tracker unless a real TDR lands mid-test.
	newest2, _, ok2 := scanTDR(chanPtr, qPtr)
	if !ok2 || newest2 < newest {
		t.Fatalf("second scan regressed: %d -> %d (ok=%v)", newest, newest2, ok2)
	}
}
