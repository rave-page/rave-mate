package encoderscan

import (
	"sort"
	"strings"
)

// plan.go - the encode-placement policy. APP-AGNOSTIC: it decides purely on each device's live
// headroom (what's utilized and how much, system-wide), never on which app is encoding. Goal: adding
// a medialink encode must never push any device past a safe ceiling and make another encoder fail.
// The four crash vectors, all measured per device: encode-engine util, HW encode sessions, VRAM,
// and (CPU encoders) CPU load.

// PlanAction is what medialink should do for a source right now.
type PlanAction string

const (
	ActionEncode PlanAction = "encode" // encode at full source quality on the chosen device
	ActionReduce PlanAction = "reduce" // encode at reduced resolution to fit remaining headroom
	ActionPause  PlanAction = "pause"  // no device has safe headroom - don't encode (protect others)
)

// ExhaustionPolicy is the user's choice when no device has full-quality headroom.
type ExhaustionPolicy string

const (
	ExhaustReduceQuality ExhaustionPolicy = "reduce" // downscale to fit remaining headroom
	ExhaustPause         ExhaustionPolicy = "pause"  // pause the rave-mate source instead
)

// Device is a candidate encode target with its LIVE, app-agnostic headroom (totals across ALL
// processes on that device). Unknown fields (<0) are skipped, so detection degrades safely.
type Device struct {
	Key      string        // adapter LUID key; "" = CPU
	Name     string        // human label
	Family   EncoderFamily // nvenc/amf/qsv/x264
	Encoder  string        // concrete ffmpeg encoder ("hevc_nvenc", "hevc_amf", "libx264", …)
	IsCPU    bool          // CPU encoder (libx264/265) - LoadPct is system CPU, no sessions/VRAM
	LoadPct  float64       // current utilization %: GPU VideoEncode engine, or system CPU if IsCPU
	VRAMFree float64       // free VRAM MB (<0 = unknown → VRAM ceiling skipped)
	Sessions int           // remaining HW encode sessions (<0 = unknown → session ceiling skipped)
	// ThroughputPPS is the MEASURED pixels/sec this device sustains for this encoder+codec (filled by
	// the wiring layer from the gpubudget benchmark cache; <=0 = unmeasured → the sustain gate is
	// skipped, so an unmeasured rig behaves exactly as before). OS-neutral: a Linux VAAPI device fills
	// the same field.
	ThroughputPPS float64
}

// Ceilings bound how loaded a device may become AFTER adding our encode without risking others.
type Ceilings struct {
	MaxLoadPct      float64 // projected engine/CPU util must stay under this (default 85)
	MinSessionsFree int     // leave at least this many HW sessions free after ours (default 1)
	MinVRAMFreeMB   float64 // leave at least this much VRAM free after ours (default 512)
}

// DefaultCeilings are conservative "never crash another encoder" limits.
func DefaultCeilings() Ceilings {
	return Ceilings{MaxLoadPct: 85, MinSessionsFree: 1, MinVRAMFreeMB: 512}
}

// EncodeCost is what one route's encode adds to a device, at full quality. Reduced quality scales
// EnginePct/CPUPct/VRAMMB down. Conservative defaults; refined once we measure real routes.
type EncodeCost struct {
	EnginePct float64 // GPU encode-engine % it adds
	CPUPct    float64 // CPU % it adds (x264)
	VRAMMB    float64 // VRAM it allocates
}

// DefaultEncodeCost is a conservative 1080p60-HEVC-class estimate.
func DefaultEncodeCost() EncodeCost { return EncodeCost{EnginePct: 25, CPUPct: 60, VRAMMB: 400} }

// Plan is the chosen strategy for one source.
type Plan struct {
	Action   PlanAction
	Family   EncoderFamily
	Encoder  string // concrete ffmpeg encoder ("" when Action==Pause)
	Device   string // device key ("" = CPU / any)
	ScalePct int    // 100 = source; <100 when Action==Reduce
	Reason   string
}

// PlanEncode places a source app-agnostically: at each quality tier (full, then reduced if the
// policy allows) it finds the device with the MOST headroom that stays under every ceiling after
// adding the (scaled) encode cost; if none is safe at any tier, it pauses. Never picks a device
// that would breach a ceiling - that's the "don't crash another encoder" guarantee. Equivalent to
// PlanEncodeFor with no pixel-rate requirement (the measured-throughput sustain gate is skipped).
func PlanEncode(devices []Device, cost EncodeCost, ceil Ceilings, policy ExhaustionPolicy) Plan {
	return PlanEncodeFor(devices, cost, ceil, policy, 0)
}

// reduceScalePct is the resolution scale applied when downscaling to fit leftover headroom.
const reduceScalePct = 50

// place returns the safe device with the most post-add headroom, or nil if none stays under ceilings.
func place(devices []Device, cost EncodeCost, ceil Ceilings) *Device {
	var safe []Device
	for _, d := range devices {
		if deviceSafe(d, cost, ceil) {
			safe = append(safe, d)
		}
	}
	if len(safe) == 0 {
		return nil
	}
	sort.SliceStable(safe, func(i, j int) bool {
		hi, hj := headroom(safe[i], cost, ceil), headroom(safe[j], cost, ceil)
		if hi != hj {
			return hi > hj // most headroom first - the safest pick
		}
		if safe[i].IsCPU != safe[j].IsCPU {
			return !safe[i].IsCPU // tie-break: prefer HW (frees the CPU for audio/DAW)
		}
		return codecRank(safe[i].Encoder) > codecRank(safe[j].Encoder) // then codec efficiency
	})
	best := safe[0]
	return &best
}

// deviceSafe reports whether adding cost keeps the device under every applicable ceiling.
func deviceSafe(d Device, cost EncodeCost, ceil Ceilings) bool {
	add := cost.EnginePct
	if d.IsCPU {
		add = cost.CPUPct
	}
	if d.LoadPct+add > ceil.MaxLoadPct {
		return false
	}
	if !d.IsCPU {
		if d.Sessions >= 0 && d.Sessions-1 < ceil.MinSessionsFree { // ours takes one session
			return false
		}
		if d.VRAMFree >= 0 && d.VRAMFree-cost.VRAMMB < ceil.MinVRAMFreeMB {
			return false
		}
	}
	return true
}

// headroom is how far under the load ceiling the device sits after adding cost (higher = safer).
func headroom(d Device, cost EncodeCost, ceil Ceilings) float64 {
	add := cost.EnginePct
	if d.IsCPU {
		add = cost.CPUPct
	}
	return ceil.MaxLoadPct - (d.LoadPct + add)
}

func scaleCost(c EncodeCost, scalePct int) EncodeCost {
	f := float64(scalePct) / 100
	return EncodeCost{EnginePct: c.EnginePct * f, CPUPct: c.CPUPct * f, VRAMMB: c.VRAMMB * f}
}

func devLabel(d Device) string {
	if d.Name != "" {
		return d.Name
	}
	if d.IsCPU {
		return "CPU"
	}
	if d.Key != "" {
		return string(d.Family) + "/" + d.Key
	}
	return string(d.Family)
}

// codecRank prefers more-efficient codecs on a tie (HEVC > AV1-ish > H264 > MJPEG).
func codecRank(enc string) int {
	switch {
	case strings.Contains(enc, "hevc"), strings.Contains(enc, "265"):
		return 3
	case strings.Contains(enc, "av1"):
		return 2
	case strings.Contains(enc, "264"):
		return 1
	default:
		return 0
	}
}
