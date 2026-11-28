//go:build linux && pipewire

package pwcapture

import (
	"math/rand"
	"testing"
)

// Pure PTS-grid gate (rpw_grid_gate) + effective-rate rule (rpw_eff_fps): no GPU, no PipeWire daemon.

func TestEffFPS(t *testing.T) {
	for _, c := range []struct{ enc, cap, want int }{
		{60, 0, 60}, {60, 15, 15}, {60, 60, 60}, {60, 120, 60}, {30, 1, 1}, {0, 0, 0}, {0, 15, 15}, {60, -5, 60},
	} {
		if g := effFPS(c.enc, c.cap); g != c.want {
			t.Errorf("effFPS(%d,%d)=%d want %d", c.enc, c.cap, g, c.want)
		}
	}
}

// runGrid feeds srcFPS frames for secs (jitterNs uniform +/-) through the gate at effFPS(encFPS, capFPS)
// and returns the passed count over the last half (steady state) and the max passed interval (ms).
func runGrid(t *testing.T, srcFPS, encFPS, capFPS int, jitterNs int64, secs int) (perSec float64, maxIvMs float64) {
	t.Helper()
	var g gridState
	rng := rand.New(rand.NewSource(1))
	fps := effFPS(encFPS, capFPS)
	capped := capFPS > 0 && capFPS < encFPS
	n := srcFPS * secs
	period := int64(1e9) / int64(srcFPS)
	half := int64(secs) * 1e9 / 2
	var last int64
	passed := 0
	for i := 1; i <= n; i++ {
		pts := int64(i) * period
		if jitterNs > 0 {
			pts += rng.Int63n(2*jitterNs) - jitterNs
		}
		if !g.gate(pts, fps, capped) {
			continue
		}
		if pts >= half {
			passed++
			if last > 0 {
				if iv := float64(pts-last) / 1e6; iv > maxIvMs {
					maxIvMs = iv
				}
			}
			last = pts
		}
	}
	return float64(passed) / (float64(secs) / 2), maxIvMs
}

func TestGridGateTable(t *testing.T) {
	for _, c := range []struct {
		name           string
		src, enc, cap  int
		jitter         int64
		wantLo, wantHi float64 // passed frames/s (steady state)
		maxIvMs        float64 // 0 = unchecked
	}{
		{"rate source passes", 60, 60, 0, 0, 59.5, 60.5, 17.5},
		{"jittery rate source passes (no header PTS)", 60, 60, 0, 3e6, 59.5, 60.5, 0},
		{"120 -> 60 decimates evenly", 120, 60, 0, 0, 59.5, 60.5, 17.5},
		{"cap 15 on a 60 route", 60, 60, 15, 0, 14.5, 15.5, 68},
		{"cap 15 on a 30 route", 30, 30, 15, 0, 14.5, 15.5, 68},
		{"cap 25 on a 30 route binds (pass-through heuristic bypassed)", 30, 30, 25, 0, 24, 26, 0},
		{"cap 15 on a jittery 60 route", 60, 60, 15, 3e6, 14.5, 15.5, 90}, // slot + 1 source frame + jitter
		{"cap above the source rate passes everything", 30, 60, 45, 0, 29.5, 30.5, 34},
		{"cap 1 (lower bound)", 60, 60, 1, 0, 0.9, 1.1, 1300},
	} {
		got, maxIv := runGrid(t, c.src, c.enc, c.cap, c.jitter, 20)
		if got < c.wantLo || got > c.wantHi {
			t.Errorf("%s: %.2f fps passed, want [%.1f, %.1f]", c.name, got, c.wantLo, c.wantHi)
		}
		if c.maxIvMs > 0 && maxIv > c.maxIvMs {
			t.Errorf("%s: max passed interval %.1f ms > %.1f (uneven)", c.name, maxIv, c.maxIvMs)
		}
	}
}

// A live cap change on one running grid (no reset): 60 -> cap 15 -> cleared -> back to every frame.
func TestGridGateLiveCapChange(t *testing.T) {
	var g gridState
	period := int64(1e9) / 60
	pts := int64(0)
	count := func(secs int, fps int, capped bool) int {
		n := 0
		for i := 0; i < 60*secs; i++ {
			pts += period
			if g.gate(pts, fps, capped) {
				n++
			}
		}
		return n
	}
	if n := count(2, 60, false); n < 119 {
		t.Fatalf("uncapped: %d/120 passed", n)
	}
	count(1, 15, true) // settle
	if n := count(4, 15, true); n < 58 || n > 62 {
		t.Fatalf("cap 15: %d passed in 4 s, want ~60", n)
	}
	count(1, 60, false) // cleared: settle
	if n := count(2, 60, false); n < 119 {
		t.Fatalf("cap cleared: %d/120 passed, want every frame", n)
	}
}
