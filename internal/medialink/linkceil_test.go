package medialink

import "testing"

func TestLinkCeilKbps(t *testing.T) {
	// 1 Gb link at 50% => 500 Mbps = 500000 kbps.
	if got := LinkCeilKbps(1000, 50); got != 500000 {
		t.Fatalf("1Gb@50%% = %d, want 500000", got)
	}
	// Default fraction when 0.
	if got := LinkCeilKbps(1000, 0); got != 1000*1000*DefaultLinkFracPct/100 {
		t.Fatalf("default fraction wrong: %d", got)
	}
	// Unknown speed => 0 (no static ceiling; congestion controller bounds it).
	if LinkCeilKbps(0, 50) != 0 || LinkCeilKbps(-1, 50) != 0 {
		t.Fatal("unknown link speed must be 0")
	}
	// Fraction clamps to 100.
	if got := LinkCeilKbps(100, 150); got != 100000 {
		t.Fatalf("over-100%% clamp = %d, want 100000", got)
	}
	// A 1 Gb link is far above the 4K60 model (~35 Mbps) => never binds on LAN.
	if LinkCeilKbps(1000, 50) <= QualityTargetKbps("h264", "nvenc", 3840, 2160, 60, ContentUnknown) {
		t.Fatal("1Gb link ceiling should be well above the 4K model")
	}
}
