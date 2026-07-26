package encoderscan

import (
	"fmt"
	"sort"
	"strings"
)

// advertise.go turns the (previously diagnostic-only) headroom planner into the thing that shapes
// what medialink ADVERTISES to peers. It is pure: Report in, encoder list out - every OS edge was
// already resolved by Scan/Detect.

// Devices builds planner devices from the encoder names this node can actually use, binding each to
// a GPU adapter by VENDOR NAME (nvenc→NVIDIA, amf→AMD/Radeon, qsv→Intel) so per-adapter encode util
// and free VRAM are the REAL numbers for that encoder instead of "the busiest adapter".
//
// What stays unknown, deliberately:
//   - Sessions: -1 always. HW encode-session counts exist only in vendor SDKs (NVML/AMF/oneVPL) -
//     no new deps, so the planner skips that ceiling.
//   - Adapter binding for MF / unrecognized HW families, and for machines with two same-vendor GPUs
//     (ambiguous): LoadPct falls back to the busiest adapter's util (conservative) and VRAMFree to -1.
//     Exact per-encoder LUID pinning is the device-selection work, not this join.
func Devices(encoders []string, rep Report, cpuPct float64) []Device {
	var busiest float64
	for _, v := range rep.AdapterEncPct {
		if v > busiest {
			busiest = v
		}
	}
	out := make([]Device, 0, len(encoders))
	for _, name := range encoders {
		fam := FamilyFromOBSID(name)
		d := Device{Family: fam, Encoder: name, VRAMFree: -1, Sessions: -1}
		if !fam.IsHardware() {
			d.IsCPU, d.LoadPct = true, cpuPct
			out = append(out, d)
			continue
		}
		if luid := adapterForFamily(fam, rep.AdapterNames); luid != "" {
			d.Key, d.Name = luid, rep.AdapterNames[luid]
			d.LoadPct = rep.AdapterEncPct[luid]
			if free, ok := rep.AdapterVRAMFree[luid]; ok {
				d.VRAMFree = free
			}
		} else {
			d.LoadPct = busiest // unbindable family (MF/custom) or ambiguous vendor → conservative
		}
		out = append(out, d)
	}
	return out
}

// familyVendors maps an encoder family to the substrings its adapter description carries.
var familyVendors = map[EncoderFamily][]string{
	FamilyNVENC: {"nvidia", "geforce", "quadro", "rtx", "gtx"},
	FamilyAMF:   {"amd", "radeon"},
	FamilyQSV:   {"intel", "arc"},
}

// adapterForFamily resolves family → adapter LUID by vendor name. "" when the family has no vendor
// signature (MF wraps any MFT), no adapter matches, or SEVERAL do (two same-vendor GPUs: ambiguous,
// so stay conservative rather than guess the wrong device).
func adapterForFamily(fam EncoderFamily, names map[string]string) string {
	vendors := familyVendors[fam]
	if len(vendors) == 0 || len(names) == 0 {
		return ""
	}
	var hits []string
	for luid, name := range names {
		lc := strings.ToLower(name)
		for _, v := range vendors {
			if strings.Contains(lc, v) {
				hits = append(hits, luid)
				break
			}
		}
	}
	if len(hits) != 1 {
		return ""
	}
	return hits[0]
}

// AdvertisePlan is the planner's verdict about an encoder advertisement.
type AdvertisePlan struct {
	Encoders []string // what to advertise, best-placed first (never empty when the input wasn't)
	Withheld []string // encoders dropped to keep off a critical consumer's silicon
	Plan     Plan     // the raw placement decision (which device, action, reason) - diagnostics
	Notes    []string // human-readable "why this list" lines
}

// PlanAdvertise applies the headroom planner to what this node advertises to peers. Two effects, in
// increasing strength:
//
//  1. ORDER - the planner's chosen encoder first, then the rest by descending headroom, demoted
//     (protected-silicon) encoders last. NOTE: medialink.Negotiate treats the advertised list as a
//     SET (tier order lives in codecTiers, and the sender's own preference/pin arrives through
//     NegotiateOpts from config - not from this order), so ORDER is still diagnostic; only (2)
//     changes the negotiation outcome.
//  2. WITHHOLD - an encoder whose silicon a CRITICAL consumer holds (OBS actively streaming/
//     recording, Parsec) is dropped from the advertisement, but ONLY when a non-protected HARDWARE
//     encoder remains. We never degrade to software-only or to an empty list: a CPU tier at 1080p60
//     (or a refused route whose fallback is RAW video) hurts the machine far more than sharing an
//     encode engine does.
//
// ActionPause never suppresses the advertisement for the same reason - it is reported as a note.
func PlanAdvertise(encoders []string, rep Report, cpuPct float64, policy ExhaustionPolicy) AdvertisePlan {
	ap := AdvertisePlan{}
	if len(encoders) == 0 {
		ap.Plan = Plan{Action: ActionPause, Reason: "nothing advertised (probe pending / no ffmpeg)"}
		return ap
	}
	devs := Devices(encoders, rep, cpuPct)
	var free, prot []Device
	for _, d := range devs {
		if protectedDevice(d, rep) {
			prot = append(prot, d)
		} else {
			free = append(free, d)
		}
	}
	cands := free
	if len(cands) == 0 {
		cands = devs
		ap.Notes = append(ap.Notes, "every advertised encoder shares silicon with a protected consumer - contention is unavoidable, advertising all")
	}
	ap.Plan = PlanEncode(cands, DefaultEncodeCost(), DefaultCeilings(), policy)
	ap.Encoders = orderCandidates(cands, ap.Plan)

	// Withhold protected encoders only when a hardware alternative survives.
	hwFree := false
	for _, d := range free {
		if !d.IsCPU {
			hwFree = true
			break
		}
	}
	if len(prot) > 0 {
		if hwFree {
			for _, d := range prot {
				ap.Withheld = append(ap.Withheld, d.Encoder)
			}
			sort.Strings(ap.Withheld)
			ap.Notes = append(ap.Notes, fmt.Sprintf("withheld [%s]: a critical consumer holds that silicon and a free hardware encoder exists",
				strings.Join(ap.Withheld, " ")))
		} else if len(free) > 0 { // only software is free → keep the protected HW, just last
			for _, d := range prot {
				ap.Encoders = append(ap.Encoders, d.Encoder)
			}
			ap.Notes = append(ap.Notes, "kept protected hardware encoders (demoted): the only alternative is a software tier, which costs the machine more than sharing the encode engine")
		}
	}
	switch ap.Plan.Action {
	case ActionReduce:
		ap.Notes = append(ap.Notes, "headroom is tight: "+ap.Plan.Reason)
	case ActionPause:
		ap.Notes = append(ap.Notes, "planner would PAUSE ("+ap.Plan.Reason+") - advertisement kept anyway: a peer with no codec falls back to raw video, which is worse")
	}
	return ap
}

// protectedDevice reports whether a critical consumer (OBS live / Parsec) holds this device's
// adapter or encoder family.
func protectedDevice(d Device, rep Report) bool {
	if d.IsCPU {
		return false // ProtectedFamily never carries the CPU family; a CPU tier is bounded elsewhere
	}
	if d.Key != "" && rep.ProtectedAdapter[d.Key] {
		return true
	}
	return rep.ProtectedFamily[d.Family]
}

// orderCandidates renders the candidate devices as encoder names: the planner's pick first, then by
// descending headroom (stable, so equal-headroom devices keep probe order).
func orderCandidates(devs []Device, plan Plan) []string {
	cost, ceil := DefaultEncodeCost(), DefaultCeilings()
	ordered := make([]Device, len(devs))
	copy(ordered, devs)
	sort.SliceStable(ordered, func(i, j int) bool {
		if plan.Encoder != "" {
			if ordered[i].Encoder == plan.Encoder {
				return true
			}
			if ordered[j].Encoder == plan.Encoder {
				return false
			}
		}
		return headroom(ordered[i], cost, ceil) > headroom(ordered[j], cost, ceil)
	})
	out := make([]string, 0, len(ordered))
	for _, d := range ordered {
		out = append(out, d.Encoder)
	}
	return out
}

// String renders the advertisement decision for the ctl diagnostic.
func (a AdvertisePlan) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "advertise: [%s]\n", strings.Join(a.Encoders, " "))
	if len(a.Withheld) > 0 {
		fmt.Fprintf(&b, "withheld: [%s]\n", strings.Join(a.Withheld, " "))
	}
	fmt.Fprintf(&b, "plan: %s family=%s encoder=%s device=%s scale=%d%%  (%s)\n",
		a.Plan.Action, a.Plan.Family, a.Plan.Encoder, a.Plan.Device, a.Plan.ScalePct, a.Plan.Reason)
	for _, n := range a.Notes {
		b.WriteString("note: " + n + "\n")
	}
	return b.String()
}
