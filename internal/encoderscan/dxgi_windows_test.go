//go:build windows

package encoderscan

import "testing"

// Smoke test: enumAdapters must not crash (COM vtable correctness) and should find ≥1 real adapter
// on a dev box. Logs what it saw for eyeballing LUID-format parity with the PDH sampler.
func TestEnumAdapters(t *testing.T) {
	ads, err := enumAdapters()
	if err != nil {
		t.Fatalf("enumAdapters: %v", err)
	}
	if len(ads) == 0 {
		t.Skip("no hardware adapters (headless/CI)")
	}
	for _, a := range ads {
		t.Logf("adapter luid=%s vram=%.0fMB name=%q", a.LUID, a.VRAMTotalMB, a.Name)
		if a.LUID == "" || a.Name == "" {
			t.Errorf("adapter missing luid/name: %+v", a)
		}
	}
}
