package mocapmaster

// master_test.go - end-to-end: mocapnode packet (golden dancers as payload) -> PoseStore ->
// region render through the vrslgrid Overlay seam -> DecodeRegion == golden, frameCounter
// advancing per rendered frame.

import (
	"image"
	"reflect"
	"testing"
	"time"

	"rave.page/mate/internal/mocappanel"
	"rave.page/mate/internal/vrslgrid"
)

func TestMasterEndToEnd(t *testing.T) {
	gh, gd := mocappanel.GoldenFrame()
	now := t0
	m, err := New(Config{
		BoneSlots: gh.BoneSlots,
		StageMin:  gh.StageMin, StageSize: gh.StageSize,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The mocapnode packet callback seam: mocapnode.Config{OnPacket: m.OnPacket}.
	m.OnPacket(goldenPacket(t0))

	render := func() *image.RGBA {
		return vrslgrid.RenderComposite(mapReader{}, vrslgrid.CompositeSpec{
			Universes: []int{0}, Mono: true, Extended: true, Overlay: m.Overlay(),
		})
	}

	h, dancers, err := DecodeRegion(render())
	if err != nil {
		t.Fatalf("DecodeRegion: %v", err)
	}
	want := RegionHeader{
		Version:      RegionVersion,
		Flags:        RegionFlagLive,
		BoneSlots:    gh.BoneSlots,
		DancerCount:  gh.DancerCount,
		FrameCounter: 0, // master-owned counter, not the packet's
		StageMin:     gh.StageMin,
		StageSize:    gh.StageSize,
	}
	if h != want {
		t.Fatalf("header mismatch:\n got %+v\nwant %+v", h, want)
	}
	if !reflect.DeepEqual(dancers, gd) {
		t.Fatalf("dancers must survive store -> region -> decode exactly:\n got %+v\nwant %+v", dancers, gd)
	}

	// frameCounter++ per rendered frame (spillover liveness).
	h2, _, err := DecodeRegion(render())
	if err != nil {
		t.Fatalf("DecodeRegion frame 2: %v", err)
	}
	if h2.FrameCounter != 1 {
		t.Fatalf("frameCounter must advance per render: got %d want 1", h2.FrameCounter)
	}

	// Stale store: region still renders (liveness keeps advancing) but goes not-live and empty.
	now = t0.Add(DefaultStaleness + time.Millisecond)
	h3, dancers3, err := DecodeRegion(render())
	if err != nil {
		t.Fatalf("DecodeRegion stale: %v", err)
	}
	if h3.Flags&RegionFlagLive != 0 || h3.DancerCount != 0 || len(dancers3) != 0 {
		t.Fatalf("stale store must render empty non-live region, got %+v (%d dancers)", h3, len(dancers3))
	}
	if h3.FrameCounter != 2 {
		t.Fatalf("frameCounter must keep advancing while stale: got %d want 2", h3.FrameCounter)
	}
}
