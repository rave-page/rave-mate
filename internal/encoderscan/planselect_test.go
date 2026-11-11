package encoderscan

import "testing"

func TestPixelRate(t *testing.T) {
	if r := PixelRate(1920, 1080, 60); r != 1920*1080*60 {
		t.Fatalf("pixel rate = %.0f", r)
	}
	if PixelRate(0, 1080, 60) != 0 || PixelRate(1920, 1080, 0) != 0 {
		t.Fatal("degenerate geometry must be 0 (gate skipped)")
	}
}

func TestPlanEncodeForThroughputGate(t *testing.T) {
	// gpu0 is idle (most headroom) but measured too slow for 4K60; gpu1 is idle and fast enough.
	req := PixelRate(3840, 2160, 60) // ~498 Mpps
	devs := []Device{
		{Key: "gpu0", Name: "slow", Family: FamilyNVENC, Encoder: "hevc_nvenc", LoadPct: 5, VRAMFree: -1, Sessions: -1, ThroughputPPS: 200e6},
		{Key: "gpu1", Name: "fast", Family: FamilyNVENC, Encoder: "hevc_nvenc", LoadPct: 20, VRAMFree: -1, Sessions: -1, ThroughputPPS: 900e6},
	}
	p := PlanEncodeFor(devs, DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality, req)
	if p.Device != "gpu1" {
		t.Fatalf("must skip the device that cannot sustain 4K60, got %+v", p)
	}
	// Unmeasured devices (throughput 0) are NOT gated - behaves like PlanEncode (most headroom).
	devs[0].ThroughputPPS, devs[1].ThroughputPPS = 0, 0
	if p := PlanEncodeFor(devs, DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality, req); p.Device != "gpu0" {
		t.Fatalf("unmeasured must fall back to most-headroom (gpu0), got %+v", p)
	}
}

func TestPlanEncodeBackwardCompatible(t *testing.T) {
	// PlanEncode == PlanEncodeFor(...,0): most headroom wins, no throughput gate.
	p := PlanEncode(devMix(70, 5, 20), DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality)
	if p.Action != ActionEncode || p.Device != "gpu1" {
		t.Fatalf("want gpu1, got %+v", p)
	}
}

func TestPlanDecodeSingleGPU(t *testing.T) {
	cands := []DecodeCandidate{
		{Key: "gpu0", Vendor: "nvidia", LoadPct: 20, VRAMAdmit: true, VRAMKnown: true, ThroughputPPS: 900e6},
		{IsCPU: true, LoadPct: 30},
	}
	p := PlanDecode(cands, PixelRate(3840, 2160, 60), 90, true)
	if p.Action != DecodeHW || p.Device != "gpu0" {
		t.Fatalf("want hardware decode on gpu0, got %+v", p)
	}
}

func TestPlanDecodeSaturatedHolds(t *testing.T) {
	// Single GPU, VRAM-pressured (not admitted) + GPU-destination route => HOLD, never CPU decode.
	cands := []DecodeCandidate{
		{Key: "gpu0", Vendor: "nvidia", LoadPct: 20, VRAMAdmit: false, VRAMKnown: true, ThroughputPPS: 900e6},
		{IsCPU: true, LoadPct: 30},
	}
	if p := PlanDecode(cands, PixelRate(3840, 2160, 60), 90, true); p.Action != DecodeHold {
		t.Fatalf("VRAM-pressured GPU-dest route must HOLD, not CPU-decode: %+v", p)
	}
	// Same pressure but a CPU-sink route (not GPU-dest) may software-decode.
	if p := PlanDecode(cands, PixelRate(1920, 1080, 30), 90, false); p.Action != DecodeSW {
		t.Fatalf("CPU-sink route may software-decode under pressure: %+v", p)
	}
}

func TestPlanDecodePicksLeastBusyAdapter(t *testing.T) {
	// dGPU busy decoding, iGPU idle => place decode on the idle iGPU (frees the dGPU for the sender).
	cands := []DecodeCandidate{
		{Key: "dgpu", Vendor: "nvidia", LoadPct: 80, VRAMAdmit: true, VRAMKnown: true},
		{Key: "igpu", Vendor: "intel", LoadPct: 5, VRAMAdmit: true, VRAMKnown: true},
	}
	if p := PlanDecode(cands, 0, 90, true); p.Device != "igpu" {
		t.Fatalf("want decode on the least-busy adapter (igpu), got %+v", p)
	}
}

func TestPlanDecodeThroughputGate(t *testing.T) {
	// The only admitted hw adapter cannot sustain 4K60 => fall to CPU software (route is CPU-sink).
	cands := []DecodeCandidate{
		{Key: "gpu0", Vendor: "nvidia", LoadPct: 10, VRAMAdmit: true, VRAMKnown: true, ThroughputPPS: 100e6},
		{IsCPU: true, LoadPct: 20},
	}
	if p := PlanDecode(cands, PixelRate(3840, 2160, 60), 90, false); p.Action != DecodeSW {
		t.Fatalf("too-slow hw must fall to CPU on a CPU-sink route: %+v", p)
	}
}

func TestPlanDecodeUnknownVRAMFailsOpen(t *testing.T) {
	// VRAM reading unavailable (VRAMKnown=false) => treat as admitted (fail open), pick hardware.
	cands := []DecodeCandidate{{Key: "gpu0", Vendor: "nvidia", LoadPct: 10, VRAMKnown: false}}
	if p := PlanDecode(cands, 0, 90, true); p.Action != DecodeHW {
		t.Fatalf("unknown VRAM must fail open to hardware decode: %+v", p)
	}
}

func TestResolveDevicePlanUnmeasuredStaysEngineDefault(t *testing.T) {
	adapters := []AdapterInfo{{LUID: "gpu0", Name: "RTX 3060"}}
	devs := []Device{{Key: "gpu0", Name: "RTX 3060", Family: FamilyNVENC, Encoder: "h264_nvenc", LoadPct: 10, VRAMFree: -1, Sessions: -1}} // throughput 0
	d := ResolveDevicePlan(PolicyAuto, "", adapters, Report{}, devs, PixelRate(3840, 2160, 60))
	if d.Pinned() || d.Index != -1 {
		t.Fatalf("unmeasured auto must stay engine-default, got %+v", d)
	}
}

func TestResolveDevicePlanMeasuredPicksAdapter(t *testing.T) {
	adapters := []AdapterInfo{{LUID: "igpu", Name: "Intel"}, {LUID: "dgpu", Name: "RTX 3060"}}
	// Both measured; dgpu idle + fast, igpu busy. Plan should pick dgpu (most headroom, sustains).
	devs := []Device{
		{Key: "igpu", Name: "Intel", Family: FamilyQSV, Encoder: "h264_qsv", LoadPct: 80, VRAMFree: -1, Sessions: -1, ThroughputPPS: 300e6},
		{Key: "dgpu", Name: "RTX 3060", Family: FamilyNVENC, Encoder: "h264_nvenc", LoadPct: 10, VRAMFree: -1, Sessions: -1, ThroughputPPS: 900e6},
	}
	d := ResolveDevicePlan(PolicyAuto, "", adapters, Report{}, devs, PixelRate(3840, 2160, 60))
	if d.LUID != "dgpu" || d.Index != 1 {
		t.Fatalf("measured auto must pick the sustaining least-busy adapter dgpu(idx1), got %+v", d)
	}
}

func TestResolveDevicePlanMeasuredButNoneSustainsFallsBack(t *testing.T) {
	adapters := []AdapterInfo{{LUID: "gpu0", Name: "RTX 3060"}}
	// Measured but too slow for 4K60 => plan pauses => engine default (no worse than before).
	devs := []Device{{Key: "gpu0", Family: FamilyNVENC, Encoder: "h264_nvenc", LoadPct: 10, VRAMFree: -1, Sessions: -1, ThroughputPPS: 100e6}}
	d := ResolveDevicePlan(PolicyAuto, "", adapters, Report{}, devs, PixelRate(3840, 2160, 60))
	if d.Pinned() {
		t.Fatalf("no-sustain auto must fall back to engine default, got %+v", d)
	}
}

func TestResolveDevicePlanPinUnchanged(t *testing.T) {
	adapters := []AdapterInfo{{LUID: "0xa_0xb", Name: "RTX"}}
	d := ResolveDevicePlan(PolicyPin, "0xa_0xb", adapters, Report{}, nil, 0)
	if d.LUID != "0xa_0xb" || d.Index != 0 {
		t.Fatalf("pin must be unchanged by the plan path, got %+v", d)
	}
}

func TestBenchAllowed(t *testing.T) {
	ok := BenchGate{HeadroomPresent: true, FreeMB: 6000, ReserveMB: 768, BenchCostMB: 400, MachineIdle: true}
	if allow, _ := BenchAllowed(ok); !allow {
		t.Fatal("ample idle headroom must allow a benchmark")
	}
	cases := []BenchGate{
		{HeadroomPresent: false, FreeMB: 9000, MachineIdle: true},                                 // blind
		{HeadroomPresent: true, FreeMB: 9000, MachineIdle: true, ProtectedBusy: true},             // protected live
		{HeadroomPresent: true, FreeMB: 9000, MachineIdle: false},                                 // not idle
		{HeadroomPresent: true, FreeMB: 900, ReserveMB: 768, BenchCostMB: 400, MachineIdle: true}, // not enough free
	}
	for i, g := range cases {
		if allow, reason := BenchAllowed(g); allow {
			t.Fatalf("case %d must be denied, got allow (%s)", i, reason)
		}
	}
}
