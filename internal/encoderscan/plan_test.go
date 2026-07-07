package encoderscan

import "testing"

// devMix models the user's rig with live per-device load: NVENC (gpu0), AMF (gpu1), CPU.
func devMix(gpu0Load, gpu1Load, cpuLoad float64) []Device {
	return []Device{
		{Key: "gpu0", Name: "RTX", Family: FamilyNVENC, Encoder: "hevc_nvenc", LoadPct: gpu0Load, VRAMFree: -1, Sessions: -1},
		{Key: "gpu1", Name: "RX", Family: FamilyAMF, Encoder: "hevc_amf", LoadPct: gpu1Load, VRAMFree: -1, Sessions: -1},
		{Key: "", Name: "CPU", Family: FamilyX264, Encoder: "libx264", IsCPU: true, LoadPct: cpuLoad, VRAMFree: -1, Sessions: -1},
	}
}

func TestPlanPicksMostHeadroom(t *testing.T) {
	// gpu0 busy (70%), gpu1 idle (5%) → pick gpu1 (most headroom), full quality.
	p := PlanEncode(devMix(70, 5, 20), DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality)
	if p.Action != ActionEncode || p.Device != "gpu1" || p.ScalePct != 100 {
		t.Fatalf("want full-quality encode on gpu1, got %+v", p)
	}
}

func TestPlanAvoidsPushingDeviceOverCeiling(t *testing.T) {
	// gpu0 at 70%: +25% engine = 95% > 85% ceiling → unsafe. gpu1 at 65%: +25% = 90% > 85 → unsafe.
	// CPU at 10%: +60% = 70% ≤ 85 → the only safe device. (No device may be pushed to crash.)
	p := PlanEncode(devMix(70, 65, 10), DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality)
	if p.Action != ActionEncode || !p.IsEmptyCPU() {
		t.Fatalf("want CPU (only device with headroom), got %+v", p)
	}
}

func TestPlanReducesWhenNothingFitsFull(t *testing.T) {
	// Every device near its ceiling at full quality, but reduced (half cost) fits somewhere.
	// gpu0 70 (+25=95 no; +12.5=82.5 yes), gpu1 80, cpu 40 (+60=100 no; +30=70 yes)
	p := PlanEncode(devMix(70, 80, 40), DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality)
	if p.Action != ActionReduce || p.ScalePct != reduceScalePct {
		t.Fatalf("want reduced-quality encode, got %+v", p)
	}
}

func TestPlanPausesWhenPolicyPauseAndNoFullHeadroom(t *testing.T) {
	// Same tight load but policy=pause → pause instead of reducing.
	p := PlanEncode(devMix(70, 80, 40), DefaultEncodeCost(), DefaultCeilings(), ExhaustPause)
	if p.Action != ActionPause {
		t.Fatalf("want pause, got %+v", p)
	}
}

func TestPlanPausesWhenAllSaturated(t *testing.T) {
	// Everything pegged - even reduced won't fit → pause (never crash another encoder).
	p := PlanEncode(devMix(95, 95, 95), DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality)
	if p.Action != ActionPause {
		t.Fatalf("want pause when all saturated, got %+v", p)
	}
}

func TestPlanRespectsNVENCSessionReserve(t *testing.T) {
	// gpu0 idle but only 1 NVENC session free → taking it leaves 0 (< reserve 1) → unsafe.
	// gpu1 idle with sessions unknown → safe. Must pick gpu1, not exhaust gpu0's sessions.
	devs := devMix(5, 5, 20)
	devs[0].Sessions = 1 // gpu0: one session left
	p := PlanEncode(devs, DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality)
	if p.Device == "gpu0" {
		t.Fatalf("must not take gpu0's last NVENC session, got %+v", p)
	}
}

func TestPlanPausesWithNoDevices(t *testing.T) {
	if p := PlanEncode(nil, DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality); p.Action != ActionPause {
		t.Fatalf("want pause with no devices, got %+v", p)
	}
}

// IsEmptyCPU is a test helper: the plan landed on the CPU device (empty key).
func (p Plan) IsEmptyCPU() bool { return p.Device == "" && p.Family == FamilyX264 }
