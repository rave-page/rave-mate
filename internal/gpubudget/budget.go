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
	adapter  func() Adapter         // adapter identity for cost keys; nil => unknown

	lastMu sync.Mutex
	last   map[Op]Decision // most recent decision per op (ctl explainability)
}

// Options wires a Budget. All funcs are nil-safe (nil => that input is unknown / fail open).
type Options struct {
	Policy   Policy
	Store    *CostStore
	Headroom func() gpumem.Headroom
	Self     func() (uint64, bool)
	Adapter  func() Adapter
}

// New builds a Budget from Options.
func New(o Options) *Budget {
	return &Budget{pol: o.Policy, store: o.Store, headroom: o.Headroom, self: o.Self, adapter: o.Adapter,
		last: map[Op]Decision{}}
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
	in := b.inputs()
	d := Admit(in, op, g, b.cost(op, g), b.pol)
	b.lastMu.Lock()
	b.last[op] = d
	b.lastMu.Unlock()
	return d
}

// AdmitDecode gates a native decode pipeline open/rebuild.
func (b *Budget) AdmitDecode(w, h, fps int, codec string) Decision {
	return b.Admit(OpDecode, Geom{W: w, H: h, FPS: fps, Codec: codec})
}

// AdmitSender gates a new Spout sender + interop registration.
func (b *Budget) AdmitSender(w, h int) Decision {
	return b.Admit(OpSender, Geom{W: w, H: h, Codec: "nrgba"})
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
	if in.Headroom.Present {
		cap := SelfCapMB(in.Headroom.BudgetMB, b.pol)
		self := "self=?"
		if in.SelfKnown {
			self = fmt.Sprintf("self=%dMB", in.SelfUsedMB)
		}
		fmt.Fprintf(&sb, "vram budget: free=%dMB budget=%dMB cap=%dMB %s reserveFloor=%dMB\n",
			in.Headroom.FreeMB, in.Headroom.BudgetMB, cap, self, b.ReserveFloorMB())
	} else {
		fmt.Fprintf(&sb, "vram budget: headroom UNKNOWN (fail open - admission never gates); reserveFloor=%dMB\n", b.ReserveFloorMB())
	}
	b.lastMu.Lock()
	for _, op := range []Op{OpDecode, OpSender, OpEncode} {
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
	return strings.TrimRight(sb.String(), "\n")
}
