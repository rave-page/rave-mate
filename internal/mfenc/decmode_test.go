package mfenc

import (
	"testing"
	"time"

	"rave.page/mate/internal/decmode"
)

// HW mode: a RESIDENT pipeline is kept however low free VRAM is (it allocates nothing more); only a
// refused rebuild (held) demotes. CPU mode: promotion asks whether a NEW HW pipeline is admitted.
func TestModeInputKeepVsAdmit(t *testing.T) {
	now := time.Unix(1, 0)
	refused := DecVerdict{Allow: false, CostMB: 114}
	in := modeInput(now, decmode.HW, 40, true, refused, false, 16)
	if !in.HWAdmit || in.HWFailed {
		t.Fatalf("resident HW pipeline must be kept on low free VRAM: %+v", in)
	}
	in = modeInput(now, decmode.HW, 40, true, refused, true, 16)
	if in.HWAdmit || !in.HWFailed {
		t.Fatalf("held rebuild must demote: %+v", in)
	}
	in = modeInput(now, decmode.CPU, 40, true, refused, false, 16)
	if in.HWAdmit || in.HWNeedMB != 114 {
		t.Fatalf("CPU mode promotion follows HW admission: %+v", in)
	}
}

// End-to-end policy over the seam inputs: full card -> CPU (forced), headroom back -> HW only after the
// dwell, and the cap is set in CPU mode and lifted on promotion.
func TestAdaptiveModeCycleWithCap(t *testing.T) {
	now := time.Unix(100, 0)
	c := decmode.New(decmode.Config{}, decmode.HW, now, "start")
	dst := "rave-mate link VRC"
	defer setDecodeCap(dst, 0)
	step := func(free uint64, admit, held bool) *decmode.Switch {
		in := modeInput(now, c.Mode(), free, true, DecVerdict{Allow: admit, CostMB: 114}, held, 16)
		sw := c.Tick(in)
		if sw != nil {
			c.Done(now, true, sw.Reason)
			capH := decmode.CapHeight(c.Mode(), 60, nil)
			setDecodeCap(dst, capH)
		}
		return sw
	}
	if sw := step(150, false, true); sw == nil || sw.To != decmode.CPU {
		t.Fatalf("held HW on a full card must go CPU: %+v", sw)
	}
	if DecodeCapHeight() != 720 {
		t.Fatalf("CPU mode must cap the sender (unmeasured => 720), got %d", DecodeCapHeight())
	}
	now = now.Add(time.Minute)
	step(2000, true, false) // ready: dwell starts
	now = now.Add(46 * time.Second)
	if sw := step(2000, true, false); sw == nil || sw.To != decmode.HW {
		t.Fatalf("headroom held past the dwell must promote: %+v", sw)
	}
	if DecodeCapHeight() != 0 {
		t.Fatalf("promotion must lift the cap, got %d", DecodeCapHeight())
	}
}

func TestDecodeCapHeightIsTightest(t *testing.T) {
	setDecodeCap("a", 1080)
	setDecodeCap("b", 720)
	defer func() { setDecodeCap("a", 0); setDecodeCap("b", 0) }()
	if h := DecodeCapHeight(); h != 720 {
		t.Fatalf("tightest cap = %d, want 720", h)
	}
	dropMode("b")
	if h := DecodeCapHeight(); h != 1080 {
		t.Fatalf("dropMode must clear its cap: %d", h)
	}
}

// CPU mode is ~destination-only: 4K coded costs ~50 MB, a capped 720p route ~13 MB, vs 114 MB learned
// for 4K HEVC DXVA on the rig (and 343 MB estimated).
func TestCPUDecodeCostIsSmall(t *testing.T) {
	if c := CPUDecodeCostMB(1280, 720); c < 8 || c > 16 {
		t.Fatalf("720p CPU-mode cost %.1f MB", c)
	}
	if c := CPUDecodeCostMB(3840, 2160); c >= 114 {
		t.Fatalf("4K CPU-mode cost %.1f MB must stay below the HW pipeline", c)
	}
}
