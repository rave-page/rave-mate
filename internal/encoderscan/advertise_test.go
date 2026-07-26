package encoderscan

import (
	"reflect"
	"slices"
	"testing"
)

const (
	luidNV = "0x00000000_0x0000c1a1"
	luidIN = "0x00000000_0x0000b0b0"
)

func twoGPUReport() Report {
	return Report{
		ProtectedAdapter: map[string]bool{},
		ProtectedFamily:  map[EncoderFamily]bool{},
		AdapterEncPct:    map[string]float64{luidNV: 40, luidIN: 5},
		AdapterNames:     map[string]string{luidNV: "NVIDIA GeForce RTX 4090", luidIN: "Intel(R) UHD Graphics 770"},
		AdapterVRAMFree:  map[string]float64{luidNV: 12000, luidIN: 4000}, // iGPU budget is shared RAM = GBs
	}
}

func TestDevicesBindFamilyToAdapterByVendor(t *testing.T) {
	devs := Devices([]string{"hevc_nvenc", "h264_qsv", "h264_mf", "libx264"}, twoGPUReport(), 33)
	if len(devs) != 4 {
		t.Fatalf("want 4 devices, got %d", len(devs))
	}
	nv, qsv, mf, cpu := devs[0], devs[1], devs[2], devs[3]
	if nv.Key != luidNV || nv.LoadPct != 40 || nv.VRAMFree != 12000 || nv.Name == "" {
		t.Errorf("nvenc must bind to the NVIDIA adapter with its live util+VRAM: %+v", nv)
	}
	if qsv.Key != luidIN || qsv.LoadPct != 5 || qsv.VRAMFree != 4000 {
		t.Errorf("qsv must bind to the Intel adapter: %+v", qsv)
	}
	if mf.Key != "" || mf.LoadPct != 40 || mf.VRAMFree != -1 {
		t.Errorf("MF has no vendor signature: expect busiest-adapter load + unknown VRAM, got %+v", mf)
	}
	if !cpu.IsCPU || cpu.LoadPct != 33 {
		t.Errorf("libx264 must be the CPU device carrying system CPU%%: %+v", cpu)
	}
	for _, d := range devs {
		if d.Sessions != -1 {
			t.Errorf("%s: HW session counts are NOT obtainable without a vendor SDK - must stay unknown", d.Encoder)
		}
	}
}

func TestDevicesAmbiguousVendorFallsBackToBusiest(t *testing.T) {
	rep := twoGPUReport()
	rep.AdapterNames[luidIN] = "NVIDIA GeForce RTX 3060" // two NVIDIA GPUs → ambiguous join
	devs := Devices([]string{"hevc_nvenc"}, rep, 10)
	if devs[0].Key != "" || devs[0].LoadPct != 40 || devs[0].VRAMFree != -1 {
		t.Errorf("ambiguous vendor match must stay conservative (busiest util, unknown VRAM): %+v", devs[0])
	}
}

// OBS live on NVENC + a free Intel iGPU: withhold the nvenc tiers, advertise QSV.
func TestPlanAdvertiseWithholdsProtectedWhenHardwareAlternativeExists(t *testing.T) {
	rep := twoGPUReport()
	rep.ProtectedFamily[FamilyNVENC] = true
	ap := PlanAdvertise([]string{"hevc_nvenc", "h264_nvenc", "h264_qsv", "libx264"}, rep, 20, ExhaustReduceQuality)
	if !reflect.DeepEqual(ap.Withheld, []string{"h264_nvenc", "hevc_nvenc"}) {
		t.Errorf("want both nvenc encoders withheld, got %v", ap.Withheld)
	}
	if slices.Contains(ap.Encoders, "hevc_nvenc") || slices.Contains(ap.Encoders, "h264_nvenc") {
		t.Errorf("withheld encoders must not be advertised: %v", ap.Encoders)
	}
	if !slices.Contains(ap.Encoders, "h264_qsv") {
		t.Errorf("the free hardware encoder must survive: %v", ap.Encoders)
	}
	if ap.Encoders[0] != "h264_qsv" {
		t.Errorf("planner pick (idle iGPU) must lead the advertisement: %v", ap.Encoders)
	}
}

// OBS live on NVENC but the ONLY alternative is software: keep nvenc (demoted). Dropping to libx264
// at 1080p60 costs the machine more than sharing the encode engine.
func TestPlanAdvertiseKeepsProtectedWhenOnlySoftwareRemains(t *testing.T) {
	rep := twoGPUReport()
	delete(rep.AdapterNames, luidIN)
	rep.ProtectedFamily[FamilyNVENC] = true
	ap := PlanAdvertise([]string{"hevc_nvenc", "libx264"}, rep, 20, ExhaustReduceQuality)
	if len(ap.Withheld) != 0 {
		t.Errorf("must not withhold the only hardware encoder: %v", ap.Withheld)
	}
	if got := ap.Encoders[len(ap.Encoders)-1]; got != "hevc_nvenc" {
		t.Errorf("protected hardware encoder must be demoted last, got order %v", ap.Encoders)
	}
	if len(ap.Notes) == 0 {
		t.Error("the trade-off must be explained in a note")
	}
}

func TestPlanAdvertiseNoProtectionAdvertisesEverything(t *testing.T) {
	ap := PlanAdvertise([]string{"hevc_nvenc", "h264_qsv", "libx264"}, twoGPUReport(), 20, ExhaustReduceQuality)
	if len(ap.Encoders) != 3 || len(ap.Withheld) != 0 {
		t.Fatalf("want all 3 advertised, none withheld: %+v", ap)
	}
	if ap.Plan.Action != ActionEncode {
		t.Errorf("idle machine must plan a full-quality encode, got %s (%s)", ap.Plan.Action, ap.Plan.Reason)
	}
	if ap.Encoders[0] != "h264_qsv" {
		t.Errorf("least-loaded device leads: %v", ap.Encoders)
	}
}

// Every encoder protected → advertise all of them (an empty advertisement makes the peer refuse the
// route or fall back to raw video).
func TestPlanAdvertiseAllProtectedStillAdvertises(t *testing.T) {
	rep := twoGPUReport()
	rep.ProtectedFamily[FamilyNVENC] = true
	ap := PlanAdvertise([]string{"hevc_nvenc", "h264_nvenc"}, rep, 20, ExhaustReduceQuality)
	if len(ap.Encoders) != 2 || len(ap.Withheld) != 0 {
		t.Fatalf("all-protected must still advertise everything: %+v", ap)
	}
	if len(ap.Notes) == 0 {
		t.Error("want a note explaining unavoidable contention")
	}
}

// A pause verdict (no headroom anywhere) must NOT empty the advertisement.
func TestPlanAdvertisePauseStillAdvertises(t *testing.T) {
	rep := twoGPUReport()
	rep.AdapterEncPct[luidNV] = 99
	rep.AdapterEncPct[luidIN] = 99
	ap := PlanAdvertise([]string{"hevc_nvenc", "h264_qsv"}, rep, 99, ExhaustPause)
	if ap.Plan.Action != ActionPause {
		t.Fatalf("saturated devices must plan pause, got %s", ap.Plan.Action)
	}
	if len(ap.Encoders) != 2 {
		t.Errorf("pause must not suppress the advertisement, got %v", ap.Encoders)
	}
}

func TestPlanAdvertiseEmptyInput(t *testing.T) {
	ap := PlanAdvertise(nil, twoGPUReport(), 10, ExhaustReduceQuality)
	if len(ap.Encoders) != 0 || ap.Plan.Action != ActionPause {
		t.Errorf("empty in → empty out + pause, got %+v", ap)
	}
}
