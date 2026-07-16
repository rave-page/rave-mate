package mocapmaster

// store_test.go - sanity-gate rejections (out-of-bounds hips, bad norm, staleness), packet-level
// rejects, and primary election across stream keys.

import (
	"testing"
	"time"

	"rave.page/mate/internal/mocapnode"
	"rave.page/mate/internal/mocappanel"
)

var t0 = time.Date(2026, 7, 16, 22, 0, 0, 0, time.UTC)

// goldenStoreConfig matches the golden frame: S=22, golden stage bounds.
func goldenStoreConfig() StoreConfig {
	gh, _ := mocappanel.GoldenFrame()
	return StoreConfig{BoneSlots: gh.BoneSlots, StageMin: gh.StageMin, StageSize: gh.StageSize}
}

func goldenPacket(at time.Time) mocapnode.Packet {
	gh, gd := mocappanel.GoldenFrame()
	return mocapnode.Packet{CapturedAt: at, Header: gh, Dancers: gd}
}

func mustStore(t *testing.T, cfg StoreConfig) *PoseStore {
	t.Helper()
	s, err := NewPoseStore(cfg)
	if err != nil {
		t.Fatalf("NewPoseStore: %v", err)
	}
	return s
}

func TestStoreAcceptGolden(t *testing.T) {
	s := mustStore(t, goldenStoreConfig())
	if err := s.Accept(goldenPacket(t0)); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	got := s.ActiveDancers(t0)
	if len(got) != 2 || got[0].LocalID != 7 || got[1].LocalID != 9 {
		t.Fatalf("want dancers [7 9], got %+v", got)
	}
	// Golden dancer 0 hips ~ (1.25, 1.0, -0.5) under the golden bounds.
	want := [3]float64{1.25, 1.0, -0.5}
	for i := 0; i < 3; i++ {
		if d := got[0].HipsPos[i] - want[i]; d < -1e-3 || d > 1e-3 {
			t.Fatalf("hips[%d] = %v, want ~%v", i, got[0].HipsPos[i], want[i])
		}
	}
	if !s.Live(t0) {
		t.Fatal("store must be live right after an accepted packet")
	}
}

func TestStoreRejectsOutOfBoundsHips(t *testing.T) {
	s := mustStore(t, goldenStoreConfig())
	pkt := goldenPacket(t0)
	// Node claims a 10x-larger stage: same q values now decode far outside the configured
	// bounds (+5% slack) -> both dancers rejected, packet still accepted.
	pkt.Header.StageMin = [3]float64{-80, 0, -60}
	pkt.Header.StageSize = [3]float64{160, 40, 120}
	if err := s.Accept(pkt); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got := s.ActiveDancers(t0); len(got) != 0 {
		t.Fatalf("out-of-bounds hips must exclude dancers, got %+v", got)
	}
	h := s.Health(t0)
	if len(h) != 1 || h[0].DancerRejects != 2 {
		t.Fatalf("want 2 dancer rejects, health %+v", h)
	}
}

func TestStoreRejectsBadNorm(t *testing.T) {
	s := mustStore(t, goldenStoreConfig())
	pkt := goldenPacket(t0)
	// Dancer 0: zero wire word on CORE slot 0 (hips) - norm rule rejects, undefined root ->
	// whole dancer rejected. Dancer 1: zero wire word on non-core slot 2 -> only that bone absent.
	pkt.Dancers[0].Rots[0] = 0
	pkt.Dancers[1].Rots[2] = 0
	if err := s.Accept(pkt); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	got := s.ActiveDancers(t0)
	if len(got) != 1 || got[0].LocalID != 9 {
		t.Fatalf("want only dancer 9, got %+v", got)
	}
	if got[0].Present[2] || got[0].Quats[2] != [4]float64{} {
		t.Fatal("norm-invalid non-core bone must be absent")
	}
	if !got[0].Present[0] {
		t.Fatal("dancer 9 core bones must stay present")
	}
	if h := s.Health(t0); h[0].DancerRejects != 1 {
		t.Fatalf("want 1 dancer reject, health %+v", h)
	}
}

func TestStoreStaleExclusion(t *testing.T) {
	s := mustStore(t, goldenStoreConfig())
	if err := s.Accept(goldenPacket(t0)); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if got := s.ActiveDancers(t0.Add(400 * time.Millisecond)); len(got) != 2 {
		t.Fatalf("within window: want 2 dancers, got %d", len(got))
	}
	after := t0.Add(DefaultStaleness + time.Millisecond)
	if got := s.ActiveDancers(after); len(got) != 0 {
		t.Fatalf("past staleness window: want 0 dancers, got %d", len(got))
	}
	if s.Live(after) {
		t.Fatal("stale stream must not be live")
	}
}

func TestStoreRejectsBoneSlotsMismatch(t *testing.T) {
	s := mustStore(t, goldenStoreConfig())
	pkt := goldenPacket(t0)
	pkt.Header.BoneSlots = 21 // stride change = stream restart, never silently accepted
	if err := s.Accept(pkt); err == nil {
		t.Fatal("boneSlots mismatch must reject the packet")
	}
	if got := s.ActiveDancers(t0); len(got) != 0 {
		t.Fatalf("rejected packet must not populate dancers, got %+v", got)
	}
}

func TestStorePrimaryElection(t *testing.T) {
	s := mustStore(t, goldenStoreConfig())

	pktA := goldenPacket(t0) // stream A: golden nonce 0xBEEF, dancers 7/9
	if err := s.Accept(pktA); err != nil {
		t.Fatalf("Accept A: %v", err)
	}

	pktB := goldenPacket(t0.Add(100 * time.Millisecond)) // stream B: same tag, new nonce
	pktB.Header.SessionNonce = 0x1111
	pktB.Dancers[0].LocalID = 3
	pktB.Dancers[1].LocalID = 4
	if err := s.Accept(pktB); err != nil {
		t.Fatalf("Accept B: %v", err)
	}

	// A is primary and still fresh -> B does not preempt.
	if got := s.ActiveDancers(t0.Add(150 * time.Millisecond)); len(got) != 2 || got[0].LocalID != 7 {
		t.Fatalf("primary must stay on stream A while fresh, got %+v", got)
	}

	// A goes stale; B keeps sending -> B takes over within one window (world-restart handover).
	late := t0.Add(700 * time.Millisecond)
	pktB2 := goldenPacket(late)
	pktB2.Header.SessionNonce = 0x1111
	pktB2.Dancers[0].LocalID = 3
	pktB2.Dancers[1].LocalID = 4
	if err := s.Accept(pktB2); err != nil {
		t.Fatalf("Accept B2: %v", err)
	}
	got := s.ActiveDancers(late.Add(50 * time.Millisecond))
	if len(got) != 2 || got[0].LocalID != 3 || got[1].LocalID != 4 {
		t.Fatalf("stream B must take over after A staled, got %+v", got)
	}

	hs := s.Health(late.Add(50 * time.Millisecond))
	if len(hs) != 2 {
		t.Fatalf("want 2 streams in health, got %+v", hs)
	}
	for _, h := range hs {
		if wantPrimary := h.Key.SessionNonce == 0x1111; h.Primary != wantPrimary {
			t.Fatalf("primary flag wrong: %+v", hs)
		}
	}
}
