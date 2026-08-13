package surfacepub

import (
	"strings"
	"testing"
)

// The ring is the ONLY buffer between producer and compositor, so its cap is the whole
// bounded-queue story for this path. Assert it in frames and in bytes, both.
func TestRingCapIsBoundedInFramesAndBytes(t *testing.T) {
	if Slots != 2 {
		t.Fatalf("ring depth %d: the design fixes it at 2 (present/acquire) and the Zig consumer's max_slots must match", Slots)
	}
	if got := RingBytes(1920, 1080); got != 1920*1080*4*2 {
		t.Fatalf("RingBytes(1920x1080) = %d", got)
	}
	if RingBytes(MaxDim, 2160) <= MaxRingBytes {
		t.Fatalf("a 4K ring (%d B) must EXCEED the cap (%d B) - otherwise the cap is not a cap", RingBytes(MaxDim, 2160), MaxRingBytes)
	}
	if err := ValidGeometry(MaxDim, 2160); err == nil {
		t.Fatal("ValidGeometry accepted a ring past MaxRingBytes")
	}
	if err := ValidGeometry(1920, 1080); err != nil {
		t.Fatalf("ValidGeometry(1920x1080): %v", err)
	}
	for _, bad := range [][2]int{{0, 0}, {MinDim - 1, 480}, {640, MinDim - 1}, {MaxDim + 1, 480}} {
		if err := ValidGeometry(bad[0], bad[1]); err == nil {
			t.Fatalf("ValidGeometry(%dx%d) accepted", bad[0], bad[1])
		}
	}
}

// A consumer asking for something absurd mid-resize must be SERVED, clamped - not refused, which
// would leave the surface stuck on its old generation for as long as the drag lasts.
func TestClampGeometryStaysInsideEveryBound(t *testing.T) {
	for _, in := range [][2]int{{0, 0}, {10, 10}, {1264, 492}, {3840, 2160}, {99999, 99999}} {
		w, h := ClampGeometry(in[0], in[1])
		if err := ValidGeometry(w, h); err != nil {
			t.Fatalf("ClampGeometry(%dx%d) = %dx%d, still invalid: %v", in[0], in[1], w, h, err)
		}
	}
	if w, h := ClampGeometry(1264, 492); w != 1264 || h != 492 {
		t.Fatalf("a legal size must pass through untouched, got %dx%d", w, h)
	}
}

// The names are the whole handshake: a typo here is a surface that silently never binds.
func TestNamesAreSessionScopedAndGenerationStamped(t *testing.T) {
	if got := CtlName("test-hole"); got != `Local\rave-surface-test-hole-ctl` {
		t.Fatalf("CtlName = %q", got)
	}
	if got := TexName("test-hole", 3, 1); got != `Local\rave-surface-test-hole-g3-s1` {
		t.Fatalf("TexName = %q", got)
	}
	// Global\ needs SeCreateGlobalPrivilege; a user-session daemon must never require admin.
	if strings.HasPrefix(CtlName("x"), `Global\`) {
		t.Fatal("control block must not live in the global namespace")
	}
	if TexName("x", 1, 0) == TexName("x", 2, 0) {
		t.Fatal("a new generation must produce a new name, else CreateSharedHandle collides with the open one")
	}
}
