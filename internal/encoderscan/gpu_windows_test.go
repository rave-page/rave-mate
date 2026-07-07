//go:build windows

package encoderscan

import "testing"

// TestSampleGPULive exercises the real PDH GPU-engine sampler read-only: it must not error and any
// returned sample must have a pid + adapter key and sane util. (No GPU work is done.)
func TestSampleGPULive(t *testing.T) {
	samples, err := sampleGPU()
	if err != nil {
		t.Fatalf("sampleGPU: %v", err)
	}
	t.Logf("sampleGPU returned %d process/adapter samples", len(samples))
	for _, s := range samples {
		if s.PID == 0 || s.Adapter == "" {
			t.Errorf("bad sample: %+v", s)
		}
		if s.EncodePct < 0 || s.EncodePct > 1000 || s.DecodePct < 0 || s.DecodePct > 1000 {
			t.Errorf("util out of range: %+v", s)
		}
		if s.EncodePct > 1 || s.DecodePct > 1 {
			t.Logf("active engine: pid=%d adapter=%s enc=%.1f%% dec=%.1f%%", s.PID, s.Adapter, s.EncodePct, s.DecodePct)
		}
	}
}

func TestParseGPUEngineInstance(t *testing.T) {
	name := "pid_1234_luid_0x00000000_0x0001C3D4_phys_0_eng_2_engtype_VideoEncode"
	pid, luid, eng, ok := parseGPUEngineInstance(name)
	if !ok || pid != 1234 || luid != "0x00000000_0x0001c3d4" || eng != "videoencode" {
		t.Fatalf("parse got pid=%d luid=%q eng=%q ok=%v", pid, luid, eng, ok)
	}
}
