// Package gpubudget is the ONE VRAM admission policy every GPU-allocating open consults BEFORE it
// allocates: native decode pipelines, Spout senders, encode sessions. It replaces the three
// independent static-768-MB floors (mfenc.decgov recycle gate, mediaroute sender-create gate,
// medialink rate governor) with a single cost-vs-headroom decision so rave-mate never tips a
// saturated card into the E_OUTOFVIDEOMEMORY (0x8876017C) interop refusal that kills Spout
// senders/receivers mid-set.
//
// Decision (mode balanced, default since 2026-11-13) = free >= need + margin, need = what OUR processes
// allocate + what a NEW sender makes the receiver allocate; a tight band admits a VERIFIED attempt.
// strict keeps the old free - cost >= geometry reserve. Every mode keeps rave-mate's own total GPU
// footprint under a hard self-cap (.devnotes/VRAM_FULL_CARD_POLICY.md). Costs start as computed
// estimates (W*H*bpp * surface counts) and are REFINED by measurement (per-process dedicated-VRAM
// delta across a successful open, persisted per adapter+driver via the CostStore). Headroom unknown
// => fail OPEN (never gate on a reading the platform can't give), matching internal/gpumem.
//
// OS-neutral: the policy sees plain structs (Headroom, Adapter, Geom) so a Linux VAAPI sender feeds
// the same admission later. Pure + injectable (no GPU, no syscalls here) so every rule is table-tested.
package gpubudget

import (
	"fmt"
	"strings"

	"rave.page/mate/internal/gpumem"
)

// Op is a GPU-allocating operation class with a distinct VRAM cost shape.
type Op string

const (
	OpDecode Op = "decode" // native decode pipeline: D3D11 device + DXVA DPB surfaces + VP + dest view
	OpSender Op = "sender" // Spout sender shared texture + GL/DX interop registration
	OpEncode Op = "encode" // MF encode session: VP NV12 pool + encoder MFT
	// OpReceive is a composed decision (decode + optional new sender + receiver follow); never a cost key.
	OpReceive Op = "receive"
)

// Geom is the frame geometry an op allocates for. Codec selects decode/encode surface shapes.
type Geom struct {
	W, H  int
	FPS   int
	Codec string // "h264","hevc","nrgba",... ("" = treated as h264 for decode, rgba for sender)
}

// Pixels is the frame area (0 on a degenerate geometry).
func (g Geom) Pixels() int {
	if g.W <= 0 || g.H <= 0 {
		return 0
	}
	return g.W * g.H
}

// Bucket is the geometry's cache/reserve bucket, keyed on height (the axis the sender texture pins).
func (g Geom) Bucket() string {
	switch {
	case g.H <= 0:
		return "unknown"
	case g.H <= 720:
		return "720p"
	case g.H <= 1080:
		return "1080p"
	case g.H <= 1440:
		return "1440p"
	case g.H <= 2160:
		return "2160p"
	default:
		return "4320p"
	}
}

// Adapter identifies the GPU + driver a cost is learned against (costs vary by driver version).
type Adapter struct {
	Vendor string // "nvidia","amd","intel",... (lowercased); "" = unknown
	Driver string // driver version string; "" = unknown
}

// Cost is a VRAM cost estimate (MB) for one op at one geometry.
type Cost struct {
	MB       float64
	Measured bool   // true = refined from a real per-process VRAM delta, false = computed estimate
	Source   string // "measured" | "estimate"
}

// Policy configures mode, margin, reserve + self-cap. Zero fields take safe defaults (Mode "" = balanced).
type Policy struct {
	Mode           Mode    // strict | balanced | permissive ("" = balanced)
	MarginMinMB    uint64  // non-strict margin floor (0 = DefaultMarginMinMB)
	MarginPct      float64 // non-strict margin as a fraction of need (0 = DefaultMarginPct)
	ReserveFloorMB uint64  // strict: reserve never below this (default DefaultReserveFloorMB)
	ReserveCfgMB   uint64  // strict: user-configured reserve floor (0 = none); geometry may raise it
	HardCapMB      uint64  // rave-mate's own GPU footprint cap (0 = auto: min(1536, 12% of budget))
}

// DefaultReserveFloorMB is the free-VRAM margin left for OTHER apps' interop after our allocation.
// 768 MB keeps the DECODE_DEST_CONTENDED_HOLD floor as the baseline; geometry raises it for 4K.
const DefaultReserveFloorMB uint64 = 768

// autoCapAbsMB caps rave-mate's own GPU footprint in absolute terms; autoCapPct as a budget fraction.
// A relay should be a light guest on the DJ's card (Resolume owns ~8/12 GB): min(1.5 GB, 12% budget).
const (
	autoCapAbsMB = 1536
	autoCapPct   = 12
)

// Surface-cost constants (bytes/pixel + fixed device overheads). Conservative; measurement overrides.
const (
	bppRGBA = 4.0 // BGRA/RGBA shared texture + VP intermediate
	bppNV12 = 1.5 // DXVA / encoder NV12 surface
	mib     = 1024.0 * 1024.0

	decDeviceMB  = 128 // D3D11 device + decoder MFT + runtime
	encDeviceMB  = 96  // D3D11 device + encoder MFT
	sndInteropMB = 48  // Spout shared-texture registration + GL/DX interop overhead
)

// Inputs are the live admission inputs (gathered by Budget; injected directly in tests).
type Inputs struct {
	Headroom   gpumem.Headroom // primary-adapter free/budget; Present=false => fail open
	SelfUsedMB uint64          // rave-mate's own total GPU footprint across its processes (0 if unknown)
	SelfKnown  bool            // false => self-cap dimension is skipped (fail open on that axis only)
}

// Decision is the admission verdict with every number behind it (for logs + ctl explainability).
type Decision struct {
	Admit bool
	// Attempt: admitted only as a VERIFIED attempt (headroom tight): the caller must run the
	// allocation where failure is contained (child/helper, never the daemon), re-read headroom after
	// it and back out + hold on failure or collapse (see VerifyAttempt).
	Attempt      bool
	Reason       string
	Mode         Mode
	Op           Op
	Geom         Geom
	CostMB       float64 // OUR processes' allocation
	FollowMB     float64 // receiver-side follow cost a NEW sender triggers (Resolume's copy)
	NeedMB       float64 // CostMB + FollowMB
	MarginMB     uint64  // non-strict margin; strict: 0
	CostMeasured bool
	ReserveMB    uint64 // strict: geometry reserve; non-strict: need + margin (the "required free")
	Present      bool
	FreeMB       uint64
	SelfUsedMB   uint64
	SelfCapMB    uint64
	SelfKnown    bool
}

// EstimateCostMB computes the VRAM an op allocates at g, from surface counts * bytes/pixel.
func EstimateCostMB(op Op, g Geom) float64 {
	px := float64(g.Pixels())
	if px <= 0 {
		px = 1920 * 1080 // degenerate geometry: assume 1080p so a gate still has a number
	}
	switch op {
	case OpDecode:
		// DPB (reference pool) + a VP RGBA intermediate + a VP NV12 intermediate + device overhead.
		// The destination shared texture belongs to OpSender, not re-allocated here.
		dpb := float64(dpbSurfaces(g.Codec))
		return decDeviceMB + (px*bppNV12*dpb)/mib + (px*bppRGBA)/mib + (px*bppNV12)/mib
	case OpSender:
		// One shared BGRA texture + interop registration overhead.
		return sndInteropMB + (px*bppRGBA)/mib
	case OpEncode:
		// A small VP NV12 input pool + one RGBA staging + device overhead.
		pool := 4.0
		return encDeviceMB + (px*bppNV12*pool)/mib + (px*bppRGBA)/mib
	default:
		return (px * bppRGBA) / mib
	}
}

// VendorOf extracts a cost-key vendor token from an adapter name ("NVIDIA GeForce RTX 3060" ->
// "nvidia"). Unknown => the first word lowercased, or "" when empty.
func VendorOf(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "nvidia"), strings.Contains(n, "geforce"), strings.Contains(n, "rtx"), strings.Contains(n, "quadro"):
		return "nvidia"
	case strings.Contains(n, "amd"), strings.Contains(n, "radeon"):
		return "amd"
	case strings.Contains(n, "intel"), strings.Contains(n, "arc"):
		return "intel"
	}
	if f := strings.Fields(n); len(f) > 0 {
		return f[0]
	}
	return ""
}

// dpbSurfaces is the decode reference-pool surface count estimate per codec (H.264 keeps more refs).
func dpbSurfaces(codec string) int {
	switch codec {
	case "hevc", "h265":
		return 10
	default: // h264 + unknown
		return 12
	}
}

// geomReserveMB is the geometry-scaled reserve: a larger frame leaves MORE headroom for the
// receiver app's interop. Floors at DefaultReserveFloorMB (1080p and below).
func geomReserveMB(g Geom) uint64 {
	switch g.Bucket() {
	case "1440p":
		return 896
	case "2160p":
		return 1152
	case "4320p":
		return 1536
	default: // 720p/1080p/unknown
		return DefaultReserveFloorMB
	}
}

// Reserve resolves the free-VRAM margin to leave after an op's allocation: max(configured-or-floor,
// geometry reserve). budgetMB is unused today but kept so a future per-adapter reserve can scale.
func Reserve(op Op, g Geom, budgetMB uint64, pol Policy) uint64 {
	floor := pol.ReserveFloorMB
	if floor == 0 {
		floor = DefaultReserveFloorMB
	}
	if pol.ReserveCfgMB > floor {
		floor = pol.ReserveCfgMB
	}
	if gr := geomReserveMB(g); gr > floor {
		return gr
	}
	return floor
}

// SelfCapMB resolves rave-mate's own GPU-footprint cap: explicit policy, else min(1536, 12% budget).
func SelfCapMB(budgetMB uint64, pol Policy) uint64 {
	if pol.HardCapMB > 0 {
		return pol.HardCapMB
	}
	cap := uint64(autoCapAbsMB)
	if budgetMB > 0 {
		if pct := budgetMB * autoCapPct / 100; pct < cap {
			cap = pct
		}
	}
	if cap == 0 {
		cap = autoCapAbsMB
	}
	return cap
}

// Admit is the pure admission rule for an op whose whole cost lands in OUR processes (no receiver
// follow term). See AdmitNeed.
func Admit(in Inputs, op Op, g Geom, cost Cost, pol Policy) Decision {
	return AdmitNeed(in, op, g, cost, 0, pol)
}

// AdmitNeed is the pure admission rule. need = cost (OUR allocation) + followMB (what a NEW sender makes
// a receiver allocate). Per mode:
//   - strict:     free - cost >= geometry reserve (the pre-2026-11-13 rule; follow ignored).
//   - balanced:   free - need >= margin => admit; free - need >= margin/2 => verified ATTEMPT; else hold.
//   - permissive: free - need >= margin => admit; free >= need => verified ATTEMPT; else hold.
//
// margin = max(MarginMinMB, MarginPct*need). Every mode also enforces self + cost <= self-cap.
// Headroom absent => admit (fail open). Self-footprint unknown => the cap axis is skipped.
func AdmitNeed(in Inputs, op Op, g Geom, cost Cost, followMB float64, pol Policy) Decision {
	mode := pol.Mode.Resolved()
	if mode == ModeStrict {
		followMB = 0
	}
	need := cost.MB + followMB
	d := Decision{
		Mode: mode, Op: op, Geom: g, CostMB: cost.MB, FollowMB: followMB, NeedMB: need,
		CostMeasured: cost.Measured, Present: in.Headroom.Present, FreeMB: in.Headroom.FreeMB,
		SelfUsedMB: in.SelfUsedMB, SelfKnown: in.SelfKnown,
	}
	if !in.Headroom.Present {
		d.Admit, d.Reason = true, "headroom unknown - fail open"
		return d
	}
	cap := SelfCapMB(in.Headroom.BudgetMB, pol)
	d.SelfCapMB = cap
	if in.SelfKnown && float64(in.SelfUsedMB)+cost.MB > float64(cap) {
		d.Reason = fmt.Sprintf("would exceed rave-mate GPU cap: self %d MB + cost %.0f MB > cap %d MB",
			in.SelfUsedMB, cost.MB, cap)
		return d
	}
	free := float64(in.Headroom.FreeMB)
	if mode == ModeStrict {
		reserve := Reserve(op, g, in.Headroom.BudgetMB, pol)
		d.ReserveMB = reserve
		if free-cost.MB < float64(reserve) {
			d.Reason = fmt.Sprintf("insufficient VRAM: free %d MB - cost %.0f MB < reserve %d MB (strict)",
				in.Headroom.FreeMB, cost.MB, reserve)
			return d
		}
		d.Admit = true
		d.Reason = fmt.Sprintf("ok: free %d MB - cost %.0f MB >= reserve %d MB (strict; self %d/%d MB)",
			in.Headroom.FreeMB, cost.MB, reserve, in.SelfUsedMB, cap)
		return d
	}
	margin := MarginMB(need, pol)
	d.MarginMB = margin
	d.ReserveMB = uint64(need + 0.5) + margin
	attemptFloor := need + float64(margin)/2
	if mode == ModePermissive {
		attemptFloor = need
	}
	switch {
	case free-need >= float64(margin):
		d.Admit = true
		d.Reason = fmt.Sprintf("ok: free %d MB >= need %.0f MB + margin %d MB (%s)", in.Headroom.FreeMB, need, margin, mode)
	case free >= attemptFloor:
		d.Admit, d.Attempt = true, true
		d.Reason = fmt.Sprintf("tight: free %d MB, need %.0f MB + margin %d MB - verified attempt (%s)",
			in.Headroom.FreeMB, need, margin, mode)
	default:
		d.Reason = fmt.Sprintf("insufficient VRAM: free %d MB < need %.0f MB (ours %.0f + receiver %.0f) + margin %d MB (%s)",
			in.Headroom.FreeMB, need, cost.MB, followMB, margin, mode)
	}
	return d
}

// String renders a Decision for a log line / ctl row.
func (d Decision) String() string {
	verb := "ADMIT"
	switch {
	case !d.Admit:
		verb = "DENY"
	case d.Attempt:
		verb = "ATTEMPT"
	}
	src := "est"
	if d.CostMeasured {
		src = "measured"
	}
	if !d.Present {
		return fmt.Sprintf("%s %s %dx%d: %s", verb, d.Op, d.Geom.W, d.Geom.H, d.Reason)
	}
	return fmt.Sprintf("%s %s %dx%d need=%.0fMB(ours %.0f %s + recv %.0f) required=%dMB free=%dMB self=%d/%dMB policy=%s: %s",
		verb, d.Op, d.Geom.W, d.Geom.H, d.NeedMB, d.CostMB, src, d.FollowMB, d.ReserveMB, d.FreeMB,
		d.SelfUsedMB, d.SelfCapMB, d.Mode, d.Reason)
}
