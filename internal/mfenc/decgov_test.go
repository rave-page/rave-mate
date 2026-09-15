package mfenc

import (
	"testing"

	"rave.page/mate/internal/gpumem"
)

func TestRecycleAllowed(t *testing.T) {
	const floor = 768
	cases := []struct {
		name string
		h    gpumem.Headroom
		want bool
	}{
		{"unknown fails open", gpumem.Headroom{}, true},
		{"ample free allows", gpumem.Headroom{Present: true, FreeMB: 2000}, true},
		{"at floor allows", gpumem.Headroom{Present: true, FreeMB: floor}, true},
		{"below floor holds", gpumem.Headroom{Present: true, FreeMB: 200}, false},
		{"zero free holds", gpumem.Headroom{Present: true, FreeMB: 0}, false},
	}
	for _, c := range cases {
		if got := recycleAllowed(c.h, floor); got != c.want {
			t.Errorf("%s: recycleAllowed(free=%d,floor=%d)=%v want %v", c.name, c.h.FreeMB, floor, got, c.want)
		}
	}
}

// TestDecHeadroomInjection proves the force-injection env drives the gate to HOLD without a real
// card, so the whole hold path is verifiable off-set.
func TestDecHeadroomInjection(t *testing.T) {
	t.Setenv("RAVE_MATE_GPUMEM_FORCE_FREE_MB", "100")
	old := DecHeadroom
	DecHeadroom = func() gpumem.Headroom { return gpumem.ReadHeadroom(nil) }
	defer func() { DecHeadroom = old }()
	if recycleAllowed(DecHeadroom(), DecRebuildFloorMB) {
		t.Fatalf("forced 100MB free (< floor %d) must HOLD the recycle", DecRebuildFloorMB)
	}
}

func TestDecHeadroomDefaultFailsOpen(t *testing.T) {
	t.Setenv("RAVE_MATE_GPUMEM_FORCE_FREE_MB", "")
	if !recycleAllowed(DecHeadroom(), DecRebuildFloorMB) {
		t.Fatal("default seam must fail open (recycle allowed when headroom unknown)")
	}
}
