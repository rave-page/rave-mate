package gpubudget

import (
	"fmt"
	"strings"
	"sync"

	"rave.page/mate/internal/gpumem"
)

// Budget is the live admission controller: it gathers headroom + self-footprint + the learned cost
// for an op/geometry and returns the Admit decision. ONE instance is shared by every gate (decode
// recycle, sender create, encode open) so the whole app runs a single VRAM policy. Safe for
// concurrent use; the providers are cheap cached reads (never per-frame).
type Budget struct {
	mu       sync.Mutex
	pol      Policy
	store    *CostStore
	headroom func() gpumem.Headroom // primary-adapter headroom (cached sample); nil => fail open
	self     func() (uint64, bool)  // rave-mate's own GPU footprint MB across its processes; nil => unknown
	selfNow  func() (uint64, bool)  // synchronous self footprint for ctl; nil => falls back to self
	adapter  func() Adapter         // adapter identity for cost keys; nil => unknown

	lastMu sync.Mutex
	last   map[Op]Decision // most recent decision per op (ctl explainability)
}

// Options wires a Budget. All funcs are nil-safe (nil => that input is unknown / fail open).
type Options struct {
	Policy   Policy
	Store    *CostStore
	Headroom func() gpumem.Headroom
	Self     func() (uint64, bool) // async, non-blocking (admission path)
	SelfNow  func() (uint64, bool) // synchronous (ctl Report); nil => falls back to Self
	Adapter  func() Adapter
}

// New builds a Budget from Options.
func New(o Options) *Budget {
	return &Budget{pol: o.Policy, store: o.Store, headroom: o.Headroom, self: o.Self, selfNow: o.SelfNow,
		adapter: o.Adapter, last: map[Op]Decision{}}
}

// Policy returns the active policy (diagnostics).
func (b *Budget) Policy() Policy { return b.pol }

// inputs gathers the live admission inputs.
func (b *Budget) inputs() Inputs {
	var in Inputs
	if b.headroom != nil {
		in.Headroom = b.headroom()
	}
	if b.self != nil {
		in.SelfUsedMB, in.SelfKnown = b.self()
	}
	return in
}

func (b *Budget) adapterID() Adapter {
	if b.adapter != nil {
		return b.adapter()
	}
	return Adapter{}
}

// cost returns the learned-or-estimated cost for an op/geometry.
func (b *Budget) cost(op Op, g Geom) Cost {
	return b.store.Cost(b.adapterID(), op, g)
}

// Admit evaluates admission for an arbitrary op/geometry and records it for explainability.
func (b *Budget) Admit(op Op, g Geom) Decision {
	d := Admit(b.inputs(), op, g, b.cost(op, g), b.pol)
	b.record(op, d)
	return d
}

// marginText renders the policy's slack rule for the report header.
func (b *Budget) marginText() string {
	if b.pol.Mode.Resolved() == ModeStrict {
		return fmt.Sprintf("reserveFloor=%dMB (geometry-scaled)", b.ReserveFloorMB())
	}
	minMB, pct := b.pol.MarginMinMB, b.pol.MarginPct
	if minMB == 0 {
		minMB = DefaultMarginMinMB
	}
	if pct <= 0 {
		pct = DefaultMarginPct
	}
	return fmt.Sprintf("margin=max(%dMB,%.0f%% need) collapseFloor=%dMB", minMB, pct*100, CollapseFloorMB)
}

// GovernorFloorMB is the free-VRAM floor the receive-side rate ladder protects under this policy. strict
// keeps the configured value (0 = the ladder's 1024 default). Non-strict: a running route allocates
// nothing, and trimming bitrate/fps frees no VRAM, so a 1 GB floor on a card another app keeps 95 % full
// would degrade every route permanently for no gain. balanced 96 MB (healthy >= 192), permissive 64.
func GovernorFloorMB(mode Mode, cfgMB uint64) uint64 {
	switch mode.Resolved() {
	case ModeStrict:
		return cfgMB
	case ModePermissive:
		return 64
	default:
		return 96
	}
}

// AdmitDecode gates a native decode pipeline open/rebuild.
func (b *Budget) AdmitDecode(w, h, fps int, codec string) Decision {
	return b.Admit(OpDecode, Geom{W: w, H: h, FPS: fps, Codec: codec})
}

// AdmitSender gates a NEW Spout sender: our texture (+ interop on the GL backend) plus the receiver
// follow cost the new handle triggers in Resolume.
func (b *Budget) AdmitSender(w, h int) Decision {
	g := Geom{W: w, H: h, Codec: "nrgba"}
	d := AdmitNeed(b.inputs(), OpSender, g, b.cost(OpSender, g), FollowCostMB(g), b.pol)
	b.record(OpSender, d)
	return d
}

// AdmitReceive gates a whole receive in ONE decision: the decode pipeline, plus - when newSender - the
// destination sender and its receiver follow cost. A parked/kept destination (newSender=false) is
// already paid for and triggers nothing new in Resolume.
func (b *Budget) AdmitReceive(w, h, fps int, codec string, newSender bool) Decision {
	gd := Geom{W: w, H: h, FPS: fps, Codec: codec}
	cost := b.cost(OpDecode, gd)
	follow := 0.0
	if newSender {
		gs := Geom{W: w, H: h, Codec: "nrgba"}
		sc := b.cost(OpSender, gs)
		cost = Cost{MB: cost.MB + sc.MB, Measured: cost.Measured && sc.Measured, Source: "composed"}
		follow = FollowCostMB(gs)
	}
	d := AdmitNeed(b.inputs(), OpReceive, gd, cost, follow, b.pol)
	b.record(OpReceive, d)
	return d
}

// Headroom returns the current primary-adapter headroom (attempt verification). Absent => Present=false.
func (b *Budget) Headroom() gpumem.Headroom {
	if b.headroom == nil {
		return gpumem.Headroom{}
	}
	return b.headroom()
}

func (b *Budget) record(op Op, d Decision) {
	b.lastMu.Lock()
	b.last[op] = d
	b.lastMu.Unlock()
}

// AdmitEncode gates a native encode session open.
func (b *Budget) AdmitEncode(w, h, fps int, codec string) Decision {
	return b.Admit(OpEncode, Geom{W: w, H: h, FPS: fps, Codec: codec})
}

// Observe records a measured cost for an op/geometry against the live adapter (refines the estimate).
func (b *Budget) Observe(op Op, g Geom, mb float64) bool {
	return b.store.Observe(b.adapterID(), op, g, mb)
}

// Throughput returns the measured pixels/sec for (op,g) against the live adapter (ok=false unmeasured).
func (b *Budget) Throughput(op Op, g Geom) (float64, bool) {
	return b.store.Throughput(b.adapterID(), op, g)
}

// ObserveThroughput records a measured throughput (pixels/sec) from the micro-benchmark.
func (b *Budget) ObserveThroughput(op Op, g Geom, pps float64) bool {
	return b.store.ObserveThroughput(b.adapterID(), op, g, pps)
}

// ReloadStore re-reads the shared cost file so this budget sees measurements written by another
// process (the daemon's ctl view picks up the media child's benchmark results).
func (b *Budget) ReloadStore() { b.store.Reload() }

// ReserveFloorMB is the configured/default reserve floor (for the rate governor's ladder floor).
func (b *Budget) ReserveFloorMB() uint64 {
	floor := b.pol.ReserveFloorMB
	if floor == 0 {
		floor = DefaultReserveFloorMB
	}
	if b.pol.ReserveCfgMB > floor {
		floor = b.pol.ReserveCfgMB
	}
	return floor
}

// Report renders the budget + last per-op decisions + learned costs for `ctl perf` / `ctl encoder-scan`.
func (b *Budget) Report() string {
	var sb strings.Builder
	in := b.inputs()
	// ctl: a synchronous self-footprint sweep beats "unknown" (admission uses the async value).
	if b.selfNow != nil {
		in.SelfUsedMB, in.SelfKnown = b.selfNow()
	}
	if in.Headroom.Present {
		cap := SelfCapMB(in.Headroom.BudgetMB, b.pol)
		self := "self=?"
		if in.SelfKnown {
			self = fmt.Sprintf("self=%dMB", in.SelfUsedMB)
		}
		fmt.Fprintf(&sb, "vram budget: policy=%s free=%dMB budget=%dMB cap=%dMB %s %s\n",
			b.pol.Mode.Resolved(), in.Headroom.FreeMB, in.Headroom.BudgetMB, cap, self, b.marginText())
	} else {
		fmt.Fprintf(&sb, "vram budget: policy=%s headroom UNKNOWN (fail open - admission never gates); %s\n",
			b.pol.Mode.Resolved(), b.marginText())
	}
	b.lastMu.Lock()
	for _, op := range []Op{OpReceive, OpDecode, OpSender, OpEncode} {
		if d, ok := b.last[op]; ok {
			fmt.Fprintf(&sb, "  last %s\n", d.String())
		}
	}
	b.lastMu.Unlock()
	if lc := b.store.Snapshot(); len(lc) > 0 {
		sb.WriteString("  learned costs (adapter|driver|op|codec|bucket = MB xN):\n")
		for _, c := range lc {
			fmt.Fprintf(&sb, "    %s = %.0fMB x%d\n", c.Key, c.MB, c.Samples)
		}
	}
	if lt := b.store.ThroughputSnapshot(); len(lt) > 0 {
		sb.WriteString("  measured throughput (adapter|driver|op|codec|bucket = Mpps xN):\n")
		for _, c := range lt {
			fmt.Fprintf(&sb, "    %s = %.0f Mpps x%d\n", c.Key, c.MB/1e6, c.Samples)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
