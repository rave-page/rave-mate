package gpubudget

import (
	"testing"

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

func TestAdmitReceivePresetsOnAFullCard(t *testing.T) {
	type want int
	const (
		hold want = iota
		attempt
		admit
	)
	cases := []struct {
		name      string
		mode      Mode
		free      uint64
		present   bool
		newSender bool
		want      want
	}{
		// 4K HEVC receive + NEW sender: need = 114 + 80 + 32 follow = 226, margin 64 => required 290.
		{"tonight 646MB balanced", ModeBalanced, 646, true, true, admit},
		{"tonight 646MB strict (old log line)", ModeStrict, 646, true, true, hold},
		{"95pct full (614MB) balanced", ModeBalanced, 614, true, true, admit},
		{"95pct full (614MB) permissive", ModePermissive, 614, true, true, admit},
		{"95pct full (614MB) strict", ModeStrict, 614, true, true, hold},
		{"tight 270MB balanced -> attempt", ModeBalanced, 270, true, true, attempt},
		{"tight 240MB balanced -> hold", ModeBalanced, 240, true, true, hold},
		{"tight 240MB permissive -> attempt", ModePermissive, 240, true, true, attempt},
		{"99pct full (123MB) balanced", ModeBalanced, 123, true, true, hold},
		{"99pct full (123MB) permissive", ModePermissive, 123, true, true, hold},
		{"headroom unknown fails open", ModeBalanced, 0, false, true, admit},
		// Kept destination: nothing new in Resolume, only the decoder: need 114, required 178.
		{"kept dest 200MB balanced", ModeBalanced, 200, true, false, admit},
		{"kept dest 150MB balanced", ModeBalanced, 150, true, false, attempt},
		{"default mode is balanced", "", 646, true, true, admit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			free, present, self := c.free, c.present, uint64(0)
			b := rigBudget(t, c.mode, &free, &present, &self)
			d := b.AdmitReceive(3840, 2160, 60, "hevc", c.newSender)
			got := hold
			switch {
			case d.Admit && d.Attempt:
				got = attempt
			case d.Admit:
				got = admit
			}
			if got != c.want {
				t.Fatalf("got %d want %d: %s", got, c.want, d.String())
			}
		})
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
