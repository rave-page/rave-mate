package encoderscan

import (
	"fmt"
	"sort"
	"strings"
)

// planselect.go - DELIBERATE resource selection (owner intent: "figure out the proper resource for
// each encode and decode"). OS-NEUTRAL: every input is a plain struct (adapters, util, measured
// pixels/sec, VRAM admission), so a Linux VAAPI sender feeds the same functions later. Pure +
// table-tested. VRAM admission itself lives in gpubudget; here it arrives as a per-candidate bool so
// one policy governs it.

// throughputMargin is the safety factor a device's measured pixels/sec must clear above the route's
// requested pixel rate before it is chosen (encode jitter + overhead headroom).
const throughputMargin = 1.15

// PixelRate is W*H*fps - the throughput a route demands. 0 inputs => 0 (gate skipped).
func PixelRate(w, h, fps int) float64 {
	if w <= 0 || h <= 0 || fps <= 0 {
		return 0
	}
	return float64(w) * float64(h) * float64(fps)
}

// sustains reports whether a measured device can carry reqPPS with margin. Unknown throughput
// (<=0) or no requirement (reqPPS<=0) => true (the sustain gate is skipped, same spirit as the
// VRAMFree<0 ceiling skip).
func sustains(throughputPPS, reqPPS float64) bool {
	if throughputPPS <= 0 || reqPPS <= 0 {
		return true
	}
	return throughputPPS >= reqPPS*throughputMargin
}

// PlanEncodeFor is PlanEncode plus a measured-throughput sustain gate: a device whose measured
// pixels/sec cannot carry reqPPS (with margin) is rejected before the headroom ranking, so `auto`
// never places a route on silicon that cannot keep up. reqPPS<=0 or all-unmeasured => identical to
// PlanEncode. Falls back from full to reduced quality (halved reqPPS) like PlanEncode.
func PlanEncodeFor(devices []Device, cost EncodeCost, ceil Ceilings, policy ExhaustionPolicy, reqPPS float64) Plan {
	if len(devices) == 0 {
		return Plan{Action: ActionPause, Reason: "no usable encoders"}
	}
	if d := placeSustaining(devices, cost, ceil, reqPPS); d != nil {
		return Plan{Action: ActionEncode, Family: d.Family, Encoder: d.Encoder, Device: d.Key,
			ScalePct: 100, Reason: fmt.Sprintf("%s: most headroom (load %.0f%%+%.0f%% ≤ %.0f%%)%s",
				devLabel(*d), d.LoadPct, cost.EnginePct, ceil.MaxLoadPct, sustainNote(*d, reqPPS))}
	}
	if policy == ExhaustPause {
		return Plan{Action: ActionPause, Reason: "no device has full-quality headroom (or can sustain the pixel rate); policy=pause"}
	}
	// Reduced quality halves both the cost and the pixel rate the device must sustain.
	if d := placeSustaining(devices, scaleCost(cost, reduceScalePct), ceil, reqPPS*reduceScalePct/100); d != nil {
		return Plan{Action: ActionReduce, Family: d.Family, Encoder: d.Encoder, Device: d.Key,
			ScalePct: reduceScalePct, Reason: fmt.Sprintf("reduced to %d%% to fit %s", reduceScalePct, devLabel(*d))}
	}
	return Plan{Action: ActionPause, Reason: "no device has headroom even reduced (or can sustain it)"}
}

// placeSustaining is place() restricted to devices that sustain reqPPS.
func placeSustaining(devices []Device, cost EncodeCost, ceil Ceilings, reqPPS float64) *Device {
	var ok []Device
	for _, d := range devices {
		if sustains(d.ThroughputPPS, reqPPS) {
			ok = append(ok, d)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	return place(ok, cost, ceil)
}

func sustainNote(d Device, reqPPS float64) string {
	if d.ThroughputPPS <= 0 || reqPPS <= 0 {
		return ""
	}
	return fmt.Sprintf(" sustains %.0f/%.0f Mpps", d.ThroughputPPS/1e6, reqPPS/1e6)
}

// ResolveDevicePlan resolves the encode adapter for one route. pin/avoid keep ResolveDevice's
// meaning. AUTO becomes plan-driven: once the candidate silicon is MEASURED, the throughput-aware
// planner picks the adapter (sustains the pixel rate + most headroom + VRAM); UNMEASURED stays at the
// engine default (adapter 0 / native-first) so a fresh machine is exactly as before. devices = the
// live inventory for the NEGOTIATED encoder (one per candidate adapter), with LoadPct / VRAMFree /
// ThroughputPPS filled by the wiring layer. reqPPS = W*H*fps. Never errors; an unresolvable pick
// degrades to the engine default with the reason recorded.
func ResolveDevicePlan(policy, pin string, adapters []AdapterInfo, rep Report, devices []Device, reqPPS float64) DeviceChoice {
	if NormalizePolicy(policy) != PolicyAuto {
		return ResolveDevice(policy, pin, adapters, rep)
	}
	measured := false
	for _, d := range devices {
		if d.ThroughputPPS > 0 {
			measured = true
			break
		}
	}
	if !measured {
		return DeviceChoice{Index: -1, Reason: "automatic (unmeasured - engine default)"}
	}
	plan := PlanEncodeFor(devices, DefaultEncodeCost(), DefaultCeilings(), ExhaustReduceQuality, reqPPS)
	if plan.Action == ActionPause || plan.Device == "" { // pause, or CPU/software: no GPU to pin
		return DeviceChoice{Index: -1, Reason: "automatic (plan: " + plan.Reason + ")"}
	}
	for i, a := range adapters {
		if strings.EqualFold(a.LUID, plan.Device) {
			return DeviceChoice{LUID: a.LUID, Index: i, Name: a.Name, Reason: "plan-driven: " + plan.Reason}
		}
	}
	return DeviceChoice{Index: -1, Reason: "automatic (planned adapter not enumerable - engine default)"}
}

// ── decode placement ──

// DecodeAction is the chosen decode tier for a route.
type DecodeAction string

const (
	DecodeHW   DecodeAction = "hw"   // hardware decode on the chosen adapter
	DecodeSW   DecodeAction = "sw"   // CPU/software (ffmpeg) decode
	DecodeHold DecodeAction = "hold" // no safe path (VRAM-pressured GPU-dest route) - hold
)

// DecodeCandidate is one decode path. OS-neutral: a Linux VAAPI render node fills the same fields.
type DecodeCandidate struct {
	Key           string  // adapter LUID; "" = CPU/software
	Name          string  // human label
	Vendor        string  // nvidia/amd/intel for the hardware path
	IsCPU         bool    // software (ffmpeg) decode
	LoadPct       float64 // current VideoDecode engine util (CPU% if IsCPU); <0 = unknown
	VRAMAdmit     bool    // gpubudget admitted a decode pipeline on this adapter (GPU-dest cost+reserve)
	VRAMKnown     bool    // false => VRAMAdmit is not authoritative (fail open: treat as admitted)
	ThroughputPPS float64 // measured decode pixels/sec; <=0 = unmeasured (sustain gate skipped)
}

// DecodePlan is the chosen decode strategy for one route.
type DecodePlan struct {
	Action DecodeAction
	Device string // adapter LUID ("" = CPU)
	Vendor string
	Reason string
}

// PlanDecode picks the decode resource for a GPU-destination route: the HARDWARE adapter with the
// most decode headroom that (a) sustains reqPPS, (b) stays under maxLoadPct, (c) is VRAM-admitted;
// else CPU software decode when allowed; else HOLD (every hardware path is VRAM-pressured and the
// route renders to a GPU texture, so a CPU decode would still force a GPU upload/interop on a full
// card - the DECODE_DEST crash). gpuDestRoute=false (a CPU sink) lets SW decode run freely.
func PlanDecode(cands []DecodeCandidate, reqPPS, maxLoadPct float64, gpuDestRoute bool) DecodePlan {
	var hw []DecodeCandidate
	var cpu *DecodeCandidate
	anyHWPressured := false
	for i, c := range cands {
		if c.IsCPU {
			cc := cands[i]
			cpu = &cc
			continue
		}
		admitted := c.VRAMAdmit || !c.VRAMKnown // unknown VRAM => fail open (admitted)
		if !admitted {
			anyHWPressured = true
			continue
		}
		if maxLoadPct > 0 && c.LoadPct >= 0 && c.LoadPct > maxLoadPct {
			continue
		}
		if !sustains(c.ThroughputPPS, reqPPS) {
			continue
		}
		hw = append(hw, c)
	}
	if len(hw) > 0 {
		sort.SliceStable(hw, func(i, j int) bool { return decodeHeadroom(hw[i]) > decodeHeadroom(hw[j]) })
		best := hw[0]
		return DecodePlan{Action: DecodeHW, Device: best.Key, Vendor: best.Vendor,
			Reason: fmt.Sprintf("%s hardware decode: least busy (%.0f%%)%s", decLabel(best), maxF(best.LoadPct), sustainNote(Device{ThroughputPPS: best.ThroughputPPS}, reqPPS))}
	}
	// No hardware path. A GPU-destination route under VRAM pressure must NOT fall to CPU decode (it
	// re-uploads + re-registers Spout interop on a full card = the crash): HOLD instead.
	if gpuDestRoute && anyHWPressured {
		return DecodePlan{Action: DecodeHold, Reason: "all hardware decode paths are VRAM-pressured; a CPU decode of a GPU-destination route would re-register interop on a full card - holding"}
	}
	if cpu != nil {
		return DecodePlan{Action: DecodeSW, Device: "", Vendor: "cpu",
			Reason: "no hardware decode available - CPU software decode"}
	}
	return DecodePlan{Action: DecodeHold, Reason: "no decode path available - holding"}
}

// decodeHeadroom ranks a hw decode candidate (lower util = safer). Unknown util sorts as idle.
func decodeHeadroom(c DecodeCandidate) float64 {
	if c.LoadPct < 0 {
		return 100
	}
	return 100 - c.LoadPct
}

func decLabel(c DecodeCandidate) string {
	if c.Name != "" {
		return c.Name
	}
	if c.Vendor != "" {
		return c.Vendor
	}
	return c.Key
}

func maxF(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

// ── benchmark admission ──

// BenchGate is the input to BenchAllowed: whether a GPU micro-benchmark may run RIGHT NOW.
type BenchGate struct {
	HeadroomPresent bool   // false => no reading; skip (never benchmark on a card we can't measure)
	FreeMB          uint64 // primary-adapter free VRAM
	ReserveMB       uint64 // the budget's reserve floor; the bench must leave at least this free
	BenchCostMB     uint64 // VRAM the bench itself allocates (must fit under free-reserve)
	MachineIdle     bool   // sysactivity says the user is idle enough
	ProtectedBusy   bool   // OBS/Parsec/VRChat is encoding (never contend their silicon)
}

// BenchAllowed reports whether a bounded GPU benchmark may run now, with the reason. A benchmark
// allocates real VRAM + engine time, so it runs ONLY with a headroom reading, enough free VRAM above
// the reserve for its own cost, an idle machine, and no protected encoder live. Pure + tested.
func BenchAllowed(g BenchGate) (bool, string) {
	if !g.HeadroomPresent {
		return false, "no VRAM reading - will not benchmark blind"
	}
	if g.ProtectedBusy {
		return false, "a protected encoder (OBS/Parsec/VRChat) is live - never contend its silicon"
	}
	if !g.MachineIdle {
		return false, "machine not idle - deferring the benchmark"
	}
	if float64(g.FreeMB)-float64(g.BenchCostMB) < float64(g.ReserveMB) {
		return false, fmt.Sprintf("not enough free VRAM: free %d MB - bench %d MB < reserve %d MB", g.FreeMB, g.BenchCostMB, g.ReserveMB)
	}
	return true, "ok"
}
