//go:build linux && pipewire

package pwcapture

import "testing"

// These tests exercise the pure constant-quality ceiling controller (rva_qp_next, via the qpNext cgo
// seam) with INJECTED rates - no GPU, no VA. They prove: over the ceiling QP rises above the quality
// target (rate-limited) toward the 51 cap; comfortably under it releases back to the target but only
// after a sustained run (hysteresis, no pumping); it never drops below the target (the quality floor);
// and a far overshoot clamps faster than a mild one.

// subBits is the bits emitted in one ~250ms sub-window at `mult` x the ceiling.
func subBits(budget int64, sub, fps int, mult float64) int64 {
	return int64(float64(budget)*mult) * int64(sub) / int64(fps)
}

func TestQPControllerRaisesAndReleases(t *testing.T) {
	target, fps := 23, 60
	sub := fps / 4 // ~4 ceiling checks/s
	budget := int64(10_000_000)
	under := 0
	qp := target

	// Sustained 2x overshoot: QP must rise above target and reach the 51 cap.
	for i := 0; i < 60; i++ {
		qp = qpNext(qp, target, subBits(budget, sub, fps, 2.0), sub, fps, budget, &under)
	}
	if qp <= target {
		t.Fatalf("ceiling must raise QP above the quality target: qp=%d target=%d", qp, target)
	}
	if qp != 51 {
		t.Fatalf("sustained 2x overshoot must drive QP to the 51 cap, got %d", qp)
	}

	// A single comfortable window must NOT release (that would be pumping).
	before := qp
	qp = qpNext(qp, target, subBits(budget, sub, fps, 0.5), sub, fps, budget, &under)
	if qp != before {
		t.Fatalf("released on a single comfortable window (pumping): %d -> %d", before, qp)
	}

	// Sustained comfortable run releases QP all the way back to the target.
	for i := 0; i < 400; i++ {
		qp = qpNext(qp, target, subBits(budget, sub, fps, 0.5), sub, fps, budget, &under)
	}
	if qp != target {
		t.Fatalf("sustained comfortable run must release QP back to the target, got %d", qp)
	}

	// Never below the floor, even at ~0 rate.
	if g := qpNext(target, target, 0, sub, fps, budget, &under); g != target {
		t.Fatalf("QP must not drop below the quality floor: %d", g)
	}
}

func TestQPControllerDeadBand(t *testing.T) {
	target, fps, sub := 23, 60, 15
	budget := int64(20_000_000)
	under := 0
	qp := 30 // above target: the ceiling had been binding
	for i := 0; i < 100; i++ {
		qp = qpNext(qp, target, subBits(budget, sub, fps, 1.0), sub, fps, budget, &under)
	}
	if qp != 30 {
		t.Fatalf("a rate at the ceiling must hold QP steady (no pumping), got %d", qp)
	}
}

func TestQPControllerFarOverStepsFaster(t *testing.T) {
	target, fps, sub := 23, 60, 15
	budget := int64(10_000_000)
	u1, u2 := 0, 0
	mild := qpNext(target, target, subBits(budget, sub, fps, 1.2), sub, fps, budget, &u1)
	far := qpNext(target, target, subBits(budget, sub, fps, 2.0), sub, fps, budget, &u2)
	// Proportional (+6 QP ~ half the bits, aim 0.95x): 1.2x -> +3, 2.0x -> +7 - one window converges.
	if !(mild == target+3 && far == target+7) {
		t.Fatalf("step sizes wrong: mild=%d (want %d) far=%d (want %d)", mild, target+3, far, target+7)
	}
	if huge := qpNext(target, target, subBits(budget, sub, fps, 10), sub, fps, budget, &u1); huge != target+8 {
		t.Fatalf("step must cap at +8 per window, got +%d", huge-target)
	}
}
