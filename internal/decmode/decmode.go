// Package decmode is the per-route decode-mode policy: HW (DXVA decode in the decode child) <-> CPU
// (software decode in the same child, uploaded into the SAME kept destination) <-> HELD (not even the
// destination can exist). Owner directive 2026-11-13: fall back to CPU decoding when VRAM is short and
// return to HW once there is enough VRAM with a buffer - adaptively, without flapping.
//
// Policy only (Go, <= 1 Hz ticks): it never touches a frame. The route driver executes a Switch
// (request IDR, open the new decoder, feed it from the next keyframe - see FeedGate - swap, release the
// old one) and reports the outcome. Pure + clock-injected: every rule is table-tested GPU-free.
// Design: .devnotes/VRAM_FULL_CARD_POLICY.md section 1e.
package decmode

import (
	"fmt"
	"runtime"
	"time"
)

// Mode is a route's decode mode.
type Mode string

const (
	HW   Mode = "hw"
	CPU  Mode = "cpu"
	Held Mode = "held"
)

// Config tunes the hysteresis. Zero fields take the defaults below.
type Config struct {
	// PromoteBufferMB is the free VRAM required ON TOP of the HW need before promoting CPU -> HW
	// (0 = auto: max(512 MB, 2 x HW need)).
	PromoteBufferMB uint64
	Dwell           time.Duration // promotion headroom must hold this long (default 45 s)
	MaxDwell        time.Duration // dwell back-off cap after failed promotions (default 10 min)
	MinResidency    time.Duration // minimum time in a mode before a NON-forced switch (default 30 s)
	StableReset     time.Duration // HW stable this long resets the dwell back-off (default 60 s)
}

const (
	defaultDwell        = 45 * time.Second
	defaultMaxDwell     = 10 * time.Minute
	defaultMinResidency = 30 * time.Second
	defaultStableReset  = 60 * time.Second
	defaultBufferMinMB  = 512
)

func (c Config) dwell() time.Duration        { return orDur(c.Dwell, defaultDwell) }
func (c Config) maxDwell() time.Duration     { return orDur(c.MaxDwell, defaultMaxDwell) }
func (c Config) minResidency() time.Duration { return orDur(c.MinResidency, defaultMinResidency) }
func (c Config) stableReset() time.Duration  { return orDur(c.StableReset, defaultStableReset) }

func orDur(v, d time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return d
}

// PromoteFloorMB is the free VRAM a promotion needs: HW need + buffer.
func (c Config) PromoteFloorMB(hwNeedMB uint64) uint64 {
	buf := c.PromoteBufferMB
	if buf == 0 {
		buf = defaultBufferMinMB
		if 2*hwNeedMB > buf {
			buf = 2 * hwNeedMB
		}
	}
	return hwNeedMB + buf
}

// Input is one policy tick's view of the world.
type Input struct {
	Now       time.Time
	Present   bool   // headroom known (false => fail open: HW stays/starts, no demotion on a blind read)
	FreeMB    uint64 // primary-adapter free VRAM
	HWAdmit   bool   // the unified budget admits (or keeps) the HW decode pipeline now
	HWNeedMB  uint64 // learned/estimated HW decode cost (OUR processes)
	CPUNeedMB uint64 // CPU-mode GPU cost (coded-size NV12 upload + VP); destination excluded
	DestOK    bool   // the nominal destination exists (kept) or is admitted
	HWFailed  bool   // the HW pipeline failed for VRAM since the last tick (open/attempt/recycle)
}

// Switch is a requested mode change the route driver must execute (nil = stay).
type Switch struct {
	From, To Mode
	Reason   string
	Forced   bool // safety switch (HW lost / destination lost): bypasses residency
}

// Status is the visible state (ctl routes / encoder-scan / Peers).
type Status struct {
	Mode        Mode
	Pending     Mode // switch in flight ("" = none)
	Since       time.Time
	Why         string
	LastSwitch  time.Time
	LastReason  string
	Dwell       time.Duration // current promotion dwell (grows after failed promotions)
	ReadySince  time.Time     // promotion headroom has held since (zero = not ready)
	FailedPromo int
}

// Controller is one route's decode-mode state machine. Not safe for concurrent use (one policy
// goroutine per route; Status copies are handed out).
type Controller struct {
	cfg        Config
	mode       Mode
	pending    Mode
	pendFrom   Mode
	since      time.Time
	why        string
	lastSwitch time.Time
	lastReason string
	readySince time.Time
	dwell      time.Duration
	failed     int
	hwStableAt time.Time
}

// New starts a controller in mode m at now.
func New(cfg Config, m Mode, now time.Time, why string) *Controller {
	return &Controller{cfg: cfg, mode: m, since: now, why: why, dwell: cfg.dwell()}
}

// Mode returns the current (committed) mode.
func (c *Controller) Mode() Mode { return c.mode }

// Status snapshots the visible state.
func (c *Controller) Status() Status {
	return Status{Mode: c.mode, Pending: c.pending, Since: c.since, Why: c.why, LastSwitch: c.lastSwitch,
		LastReason: c.lastReason, Dwell: c.dwell, ReadySince: c.readySince, FailedPromo: c.failed}
}

// Tick evaluates one policy step. Returns a Switch to execute, or nil. While a switch is pending no
// new one is issued (the driver must report Done first).
func (c *Controller) Tick(in Input) *Switch {
	if c.pending != "" {
		return nil
	}
	resident := in.Now.Sub(c.since) >= c.cfg.minResidency()
	// HELD: only when not even the destination can exist. Forced from any mode.
	if !in.DestOK {
		c.readySince = time.Time{}
		if c.mode != Held {
			return c.request(Held, "destination cannot exist (VRAM)", true)
		}
		return nil
	}
	cpuFits := !in.Present || in.FreeMB >= in.CPUNeedMB
	switch c.mode {
	case Held:
		if in.HWAdmit || !in.Present {
			return c.request(HW, "destination available; HW decode admitted", true)
		}
		if cpuFits {
			return c.request(CPU, fmt.Sprintf("destination available; HW decode refused (free %d MB < HW need %d MB + margin) - CPU decode", in.FreeMB, in.HWNeedMB), true)
		}
		return nil
	case HW:
		if in.Present && (in.HWFailed || !in.HWAdmit) {
			// Safety demotion: never wait out residency with a frozen/refused HW pipeline.
			if cpuFits {
				why := "HW decode refused by the VRAM budget"
				if in.HWFailed {
					why = "HW decode failed under VRAM pressure"
				}
				return c.request(CPU, fmt.Sprintf("%s (free %d MB, HW need %d MB)", why, in.FreeMB, in.HWNeedMB), true)
			}
			return nil // nothing lighter fits; HW stays as is (resident pipelines allocate nothing)
		}
		if !c.hwStableAt.IsZero() && in.Now.Sub(c.hwStableAt) >= c.cfg.stableReset() {
			c.failed, c.dwell, c.hwStableAt = 0, c.cfg.dwell(), time.Time{}
		}
		return nil
	case CPU:
		floor := c.cfg.PromoteFloorMB(in.HWNeedMB)
		ready := in.Present && in.HWAdmit && in.FreeMB >= floor
		if !ready {
			c.readySince = time.Time{}
			return nil
		}
		if c.readySince.IsZero() {
			c.readySince = in.Now
		}
		if in.Now.Sub(c.readySince) < c.dwell || !resident {
			return nil
		}
		return c.request(HW, fmt.Sprintf("free %d MB >= HW need %d MB + buffer (floor %d MB) for %s", in.FreeMB, in.HWNeedMB, floor, c.dwell), false)
	}
	return nil
}

func (c *Controller) request(to Mode, why string, forced bool) *Switch {
	c.pending, c.pendFrom = to, c.mode
	return &Switch{From: c.mode, To: to, Reason: why, Forced: forced}
}

// Done reports the outcome of the pending switch. ok=false keeps the old mode; a failed promotion
// doubles the dwell (capped) so a card hovering at the threshold cannot flap.
func (c *Controller) Done(now time.Time, ok bool, why string) {
	if c.pending == "" {
		return
	}
	to, from := c.pending, c.pendFrom
	c.pending, c.pendFrom = "", ""
	c.readySince = time.Time{}
	if !ok {
		c.lastReason = fmt.Sprintf("%s -> %s FAILED: %s", from, to, why)
		c.lastSwitch = now
		if from == CPU && to == HW {
			c.failed++
			c.dwell *= 2
			if c.dwell > c.cfg.maxDwell() {
				c.dwell = c.cfg.maxDwell()
			}
			c.since = now // restart residency in CPU
		}
		return
	}
	c.mode, c.since, c.why, c.lastSwitch = to, now, why, now
	c.lastReason = fmt.Sprintf("%s -> %s: %s", from, to, why)
	if to == HW {
		c.hwStableAt = now
	}
}

// FeedGate admits access units into a FRESH decoder only from the first keyframe: a P-frame on a
// decoder with no reference produces garbage or an error. One gate per new decoder.
type FeedGate struct{ open bool }

// Admit reports whether this AU may be fed; it opens on the first keyframe and stays open.
func (g *FeedGate) Admit(isKey bool) bool {
	if !g.open && isKey {
		g.open = true
	}
	return g.open
}

// Open reports whether the gate has seen its keyframe.
func (g *FeedGate) Open() bool { return g.open }

// CapHeight picks the coded-height cap for CPU mode: the tallest candidate whose MEASURED software
// decode rate sustains routeFPS with 25 % margin. measured maps coded height -> decoded fps (absent =
// unmeasured). No data => 720 (conservative). HW mode => 0 (no cap; promotion lifts the cap).
func CapHeight(m Mode, routeFPS int, measured map[int]float64) int {
	if m != CPU {
		return 0
	}
	if routeFPS <= 0 {
		routeFPS = 60
	}
	need := float64(routeFPS) * 1.25
	cands := []int{1080, 720, 540}
	any := false
	for _, h := range cands {
		if fps, ok := measured[h]; ok {
			any = true
			if fps >= need {
				return h
			}
		}
	}
	if !any {
		return 720
	}
	return cands[len(cands)-1] // even 540 measured short: lowest cap; fps may sag, never HW on a full card
}

// Threads clamps the software decoder's worker threads: configured (0 = 4), at most half the logical
// CPUs (the DJ PC runs audio + visuals), at least 1.
func Threads(cfg, numCPU int) int {
	if numCPU <= 0 {
		numCPU = runtime.NumCPU()
	}
	t := cfg
	if t <= 0 {
		t = 4
	}
	if half := numCPU / 2; t > half {
		t = half
	}
	if t < 1 {
		t = 1
	}
	return t
}

// Text renders a Status for ctl.
func (s Status) Text(now time.Time) string {
	out := fmt.Sprintf("decode mode %s for %s (%s)", s.Mode, now.Sub(s.Since).Round(time.Second), s.Why)
	if s.Pending != "" {
		out += fmt.Sprintf("; switching to %s", s.Pending)
	}
	if s.Mode == CPU {
		if s.ReadySince.IsZero() {
			out += fmt.Sprintf("; promotion waits for headroom (dwell %s)", s.Dwell)
		} else {
			out += fmt.Sprintf("; promotion headroom held %s of %s", now.Sub(s.ReadySince).Round(time.Second), s.Dwell)
		}
	}
	if s.LastReason != "" {
		out += fmt.Sprintf("; last switch %s ago: %s", now.Sub(s.LastSwitch).Round(time.Second), s.LastReason)
	}
	return out
}
