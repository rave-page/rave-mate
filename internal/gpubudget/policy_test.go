package gpubudget

import (
	"testing"
	"time"

	"rave.page/mate/internal/gpumem"
)

// rigBudget is the DJ PC: RTX 3060 (12288 MB), decode hevc 2160p LEARNED at 114 MB (installed gpucost.json
// 2026-11-13), sender unmeasured (estimate 80 MB). free/self are live-mutable.
func rigBudget(t *testing.T, mode Mode, free *uint64, present *bool, self *uint64) *Budget {
	t.Helper()
	adp := Adapter{Vendor: "nvidia", Driver: "NVIDIA GeForce RTX 3060@32.0.16.1088"}
	st := NewCostStore("", nil)
	if !st.Observe(adp, OpDecode, Geom{W: 3840, H: 2160, Codec: "hevc"}, 114) {
		t.Fatal("seed learned cost")
	}
	return New(Options{
		Policy: Policy{Mode: mode},
		Store:  st,
		Headroom: func() gpumem.Headroom {
			return gpumem.Headroom{Present: *present, FreeMB: *free, BudgetMB: 12288}
		},
		Self:    func() (uint64, bool) { return *self, true },
		Adapter: func() Adapter { return adp },
	})
}

// cpu4K is the software-decode light-path cost at the nominal coded size (mfenc.CPUDecodeCostMB(3840,2160)).
const cpu4K = 8 + 3840*2160*1.5/(1024*1024) + 3840*2160*4/(1024*1024)

// Incident 2026-11-13 profiles (Parsec NVENC failed at 150-400 MB free): HW only while the SYSTEM floor
// stays free; below it the light CPU path down to the HARD floor; below that HELD.
func TestAdmitReceivePresetsOnAFullCard(t *testing.T) {
	cases := []struct {
		name      string
		mode      Mode
		free      uint64
		present   bool
		newSender bool
		cpuOK     bool
		want      string // "hw", "hw-attempt", "cpu", "held"
	}{
		// 4K HEVC + NEW sender: HW need 114 + 80 + 32 follow = 226; CPU need ~51 + 80 + 32 = 163.
		{"idle card 5100MB balanced", ModeBalanced, 5100, true, true, true, "hw"},
		{"1000MB balanced leaves 774 >= 700", ModeBalanced, 1000, true, true, true, "hw"},
		{"900MB balanced -> cpu (HW would leave 674)", ModeBalanced, 900, true, true, true, "cpu"},
		{"tonight 646MB balanced -> cpu, never HW", ModeBalanced, 646, true, true, true, "cpu"},
		{"incident 350MB new dest -> held", ModeBalanced, 350, true, true, true, "held"},
		{"incident 350MB kept dest -> cpu", ModeBalanced, 350, true, false, true, "cpu"},
		{"150MB kept dest -> held (below hard floor)", ModeBalanced, 150, true, false, true, "held"},
		{"646MB balanced, cpu fallback off -> held", ModeBalanced, 646, true, true, false, "held"},
		{"646MB strict -> cpu (HW reserve 1152)", ModeStrict, 646, true, true, true, "cpu"},
		{"646MB permissive -> hw (floor 400)", ModePermissive, 646, true, true, true, "hw"},
		{"600MB permissive -> hw verified attempt", ModePermissive, 600, true, true, true, "hw-attempt"},
		{"headroom unknown fails open", ModeBalanced, 0, false, true, true, "hw"},
		{"default mode is balanced", "", 646, true, true, true, "cpu"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			free, present, self := c.free, c.present, uint64(0)
			b := rigBudget(t, c.mode, &free, &present, &self)
			d := b.AdmitReceiveTiered(3840, 2160, 60, "hevc", c.newSender, c.cpuOK, cpu4K)
			got := "held"
			switch {
			case d.Admit && d.DecodeMode == "cpu":
				got = "cpu"
			case d.Admit && d.Attempt:
				got = "hw-attempt"
			case d.Admit:
				got = "hw"
			}
			if got != c.want {
				t.Fatalf("got %s want %s: %s", got, c.want, d.String())
			}
			if got == "hw" && present && free-uint64(d.NeedMB) < SystemFloorMB(Policy{Mode: c.mode}) && c.mode != ModeStrict {
				t.Fatalf("HW admitted below the system floor: %s", d.String())
			}
		})
	}
}

// Free VRAM oscillating every second between 150 and 700 MB (as measured on the rig 03:18-03:37):
// a HW pipeline is NEVER admitted; a kept destination runs CPU when the instantaneous read allows it.
func TestOscillatingFullCardNeverAdmitsHW(t *testing.T) {
	free, present, self := uint64(0), true, uint64(0)
	b := rigBudget(t, ModeBalanced, &free, &present, &self)
	for i := 0; i < 600; i++ {
		free = 150 + uint64((i*137)%551) // 150..700
		d := b.AdmitReceiveTiered(3840, 2160, 60, "hevc", false, true, cpu4K)
		if d.Admit && d.DecodeMode == "hw" {
			t.Fatalf("HW admitted at free %d MB: %s", free, d.String())
		}
		if d.Admit && float64(free)-d.NeedMB < float64(DefaultHardFloorMB) {
			t.Fatalf("light path below the hard floor at %d MB", free)
		}
	}
}
func TestAdmitSelfCapBindsEveryMode(t *testing.T) {
	for _, m := range []Mode{ModeStrict, ModeBalanced, ModePermissive} {
		free, present, self := uint64(8000), true, uint64(1400) // cap on 12288 = 1474
		b := rigBudget(t, m, &free, &present, &self)
		if d := b.AdmitReceive(3840, 2160, 60, "hevc", true); d.Admit {
			t.Fatalf("%s: self-cap breach must hold: %s", m, d.String())
		}
	}
}

func TestMarginAndFollow(t *testing.T) {
	if m := MarginMB(100, Policy{}); m != DefaultMarginMinMB {
		t.Fatalf("small need margin = %d, want floor %d", m, DefaultMarginMinMB)
	}
	if m := MarginMB(1000, Policy{}); m != 250 {
		t.Fatalf("1000MB need margin = %d, want 250", m)
	}
	if m := MarginMB(1000, Policy{MarginMinMB: 300, MarginPct: 0.1}); m != 300 {
		t.Fatalf("configured margin = %d, want 300", m)
	}
	if f := FollowCostMB(Geom{W: 3840, H: 2160}); f < 31 || f > 32 {
		t.Fatalf("4K follow = %.1f, want ~31.6", f)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": ModeBalanced, "Strict": ModeStrict, " permissive ": ModePermissive, "balanced": ModeBalanced} {
		if got, ok := ParseMode(in); !ok || got != want {
			t.Fatalf("ParseMode(%q) = %s,%v want %s", in, got, ok, want)
		}
	}
	if got, ok := ParseMode("yolo"); ok || got != ModeBalanced {
		t.Fatalf("unknown mode must fall back to balanced with ok=false, got %s,%v", got, ok)
	}
}

func TestVerifyAttempt(t *testing.T) {
	if ok, _ := VerifyAttempt(gpumem.Headroom{}); !ok {
		t.Fatal("unknown headroom after attempt must keep (fail open)")
	}
	if ok, _ := VerifyAttempt(gpumem.Headroom{Present: true, FreeMB: 20}); ok {
		t.Fatal("collapse below floor must back out")
	}
	if ok, _ := VerifyAttempt(gpumem.Headroom{Present: true, FreeMB: 120}); !ok {
		t.Fatal("healthy after must keep")
	}
}

func TestGovernorFloorByMode(t *testing.T) {
	if f := GovernorFloorMB(ModeStrict, 0); f != 0 {
		t.Fatalf("strict keeps configured/default floor, got %d", f)
	}
	if f := GovernorFloorMB(ModeBalanced, 2000); f != 96 {
		t.Fatalf("balanced floor %d", f)
	}
	if f := GovernorFloorMB(ModePermissive, 0); f != 64 {
		t.Fatalf("permissive floor %d", f)
	}
}

// A 1 s 150<->700 MB oscillation averages to a steady value: the governor tier stops flipping.
func TestFreeAveragerDampsOscillation(t *testing.T) {
	a := NewFreeAverager(10 * time.Second)
	now := time.Unix(1, 0)
	var lo, hi uint64 = 1 << 62, 0
	for i := 0; i < 120; i++ {
		now = now.Add(time.Second)
		free := uint64(150)
		if i%2 == 1 {
			free = 700
		}
		v := a.Add(now, free)
		if i >= 20 {
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	if hi-lo > 60 || lo < 380 || hi > 470 {
		t.Fatalf("averaged free must sit near 425 MB with small ripple, got %d..%d", lo, hi)
	}
}
