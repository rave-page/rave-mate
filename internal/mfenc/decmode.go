package mfenc

import (
	"fmt"
	"sync"
	"time"

	"rave.page/mate/internal/decmode"
)

// Adaptive decode mode seams (.devnotes/VRAM_FULL_CARD_POLICY.md 1e). Policy lives in internal/decmode;
// these seams let featurehost wire config + the unified VRAM budget without mfenc importing gpubudget.

// DecCPUFallback, when non-nil and enabled, lets a GPU-destination decode route run in SOFTWARE mode
// (decode child, no DXVA, upload into the SAME destination) instead of HOLDING when the HW pipeline
// cannot be admitted or kept, and promotes it back to HW with hysteresis. threads bounds the software
// decoder (already clamped by decmode.Threads). nil = disabled (pre-1e behaviour: hold).
var DecCPUFallback func() (enabled bool, threads int, cfg decmode.Config)

// DecAdmitCPU, when non-nil, admits a SOFTWARE-mode decode (coded-size NV12 upload texture + VP; the
// destination is excluded). nil = admit (the CPU mode is ~16 MB at 1080p - it only refuses on a card
// that has collapsed outright).
var DecAdmitCPU func(inW, inH, outW, outH, fps int) DecVerdict

// CPUDecodeCostMB estimates SOFTWARE-mode GPU memory: the coded-size NV12 upload texture + one VP
// intermediate + device/VP objects. The destination (exists anyway) is not counted.
func CPUDecodeCostMB(inW, inH int) float64 {
	px := float64(inW * inH)
	if px <= 0 {
		px = 1920 * 1080
	}
	const mib = 1024.0 * 1024.0
	return 8 + px*1.5/mib + px*4/mib
}

// decodeCaps is the coded-height cap each destination in CPU mode asks of its sender (decmode.CapHeight).
// Bounded: one entry per live decode session, deleted when the session leaves CPU mode or closes.
var (
	decodeCapMu sync.Mutex
	decodeCaps  = map[string]int{}
)

func setDecodeCap(dst string, h int) {
	decodeCapMu.Lock()
	defer decodeCapMu.Unlock()
	if h <= 0 {
		delete(decodeCaps, dst)
		return
	}
	decodeCaps[dst] = h
}

// DecodeCapHeight is the tightest coded-height cap any CPU-mode decode session currently asks for
// (0 = none). The receive rate governor min()s it into its hint (medialink.DecodeCapHeight seam). It
// is GLOBAL across routes on purpose-limited grounds: the routeIO carries no destination identity, and
// a rig runs one heavy route; a second HW route under a CPU-mode cap is the documented trade.
func DecodeCapHeight() int {
	decodeCapMu.Lock()
	defer decodeCapMu.Unlock()
	best := 0
	for _, h := range decodeCaps {
		if best == 0 || h < best {
			best = h
		}
	}
	return best
}

// DecodeModeStatus is one session's visible decode mode (ctl routes / encoder-scan).
type DecodeModeStatus struct {
	Dest    string
	Status  decmode.Status
	CapH    int
	FreeMB  uint64
	HWNeed  uint64
	CPUNeed uint64
}

var (
	decModesMu sync.Mutex
	decModes   = map[string]DecodeModeStatus{} // bounded: one per live decode session
)

func publishMode(s DecodeModeStatus) {
	decModesMu.Lock()
	decModes[s.Dest] = s
	decModesMu.Unlock()
}

func dropMode(dst string) {
	decModesMu.Lock()
	delete(decModes, dst)
	decModesMu.Unlock()
	setDecodeCap(dst, 0)
}

// Text renders the visible mode line (ctl routes "degraded"/engine field, logs).
func (s DecodeModeStatus) Text(now time.Time) string {
	t := s.Status.Text(now) + fmt.Sprintf("; free %d MB, HW needs %d MB, CPU mode needs %d MB", s.FreeMB, s.HWNeed, s.CPUNeed)
	if s.CapH > 0 {
		t += fmt.Sprintf("; sender coded height capped at %dp", s.CapH)
	}
	return t
}

// modeText returns the visible mode line for a destination ("" = unknown).
func modeText(dst string) string {
	decModesMu.Lock()
	s, ok := decModes[dst]
	decModesMu.Unlock()
	if !ok {
		return ""
	}
	return s.Text(time.Now())
}

// DecodeModes snapshots every live session's decode mode.
func DecodeModes() []DecodeModeStatus {
	decModesMu.Lock()
	defer decModesMu.Unlock()
	out := make([]DecodeModeStatus, 0, len(decModes))
	for _, s := range decModes {
		out = append(out, s)
	}
	return out
}

// modeInput builds one decmode tick input for a session. hwResident: the HW pipeline is running (its
// "keep" verdict is "not held"); in CPU mode hwAdmit asks whether a NEW HW pipeline would be admitted.
func modeInput(now time.Time, mode decmode.Mode, free uint64, present bool, hw DecVerdict, held bool, cpuNeed uint64) decmode.Input {
	in := decmode.Input{Now: now, Present: present, FreeMB: free, HWNeedMB: uint64(hw.CostMB + 0.5),
		CPUNeedMB: cpuNeed, DestOK: true}
	switch mode {
	case decmode.HW:
		in.HWAdmit = !held // resident pipeline: kept unless a rebuild it needs was refused
		in.HWFailed = held
	default:
		in.HWAdmit = hw.Allow
	}
	return in
}
