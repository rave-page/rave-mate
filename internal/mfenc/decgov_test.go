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

func TestVramPressureSignature(t *testing.T) {
	pressure := []string{
		"open timeout", "reopen refused: E_OUTOFVIDEOMEMORY", "hr=0x8876017C",
		"CreateTexture2D failed: out of memory", "OpenSharedResource failed",
		"insufficient video memory",
	}
	for _, s := range pressure {
		if !vramPressureSignature(s) {
			t.Errorf("expected %q classified as VRAM pressure", s)
		}
	}
	capability := []string{
		"decoder MFT not found", "unsupported codec profile", "foreign adapter",
		"child protocol v1", "",
	}
	for _, s := range capability {
		if vramPressureSignature(s) {
			t.Errorf("expected %q classified as a capability failure, not VRAM pressure", s)
		}
	}
}

func TestReopenFailedForVRAM(t *testing.T) {
	const floor = 768
	present := func(free uint64) gpumem.Headroom { return gpumem.Headroom{Present: true, FreeMB: free} }
	// Signature wins regardless of headroom.
	if !reopenFailedForVRAM("open timeout", present(5000), floor) {
		t.Fatal("signature must classify as VRAM pressure even with ample headroom")
	}
	// No signature but headroom now below floor => pressure.
	if !reopenFailedForVRAM("decoder MFT gone", present(200), floor) {
		t.Fatal("headroom below floor must classify as VRAM pressure")
	}
	// Genuine capability failure with healthy headroom => NOT pressure (may count toward pin).
	if reopenFailedForVRAM("unsupported profile", present(5000), floor) {
		t.Fatal("capability failure with headroom must not be VRAM pressure")
	}
	// Headroom unknown + capability error => fails safe to NOT-pressure (counts toward pin).
	if reopenFailedForVRAM("unsupported profile", gpumem.Headroom{}, floor) {
		t.Fatal("unknown headroom + capability error must not be VRAM pressure")
	}
}

func TestDecAdmitSeam(t *testing.T) {
	old := DecAdmit
	defer func() { DecAdmit = old }()
	DecAdmit = func(_, _, _, _, _ int, _ bool) DecVerdict {
		return DecVerdict{Allow: false, Present: true, FreeMB: 300, ReserveMB: 1152, Reason: "test deny"}
	}
	v := DecAdmit(3840, 2160, 3840, 2160, 60, false)
	if v.Allow {
		t.Fatal("wired DecAdmit must be able to deny")
	}
}
