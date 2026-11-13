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

// CPUDecodeCostMB estimates SOFTWARE-mode GPU memory: device/VP objects + the coded-size NV12 upload
// texture. The destination (exists anyway) is not counted. Measured on the RTX 3060 (2026-11-13,
// TestSWDecodeCodedSizes, child VRAM over its device baseline): 4K coded +14 MB, 1080p +5, 720p +3
// (HW DXVA at the same sizes: +84 / +23 / +12). The VP allocates no BGRA intermediate.
func CPUDecodeCostMB(inW, inH int) float64 {
	px := float64(inW * inH)
	if px <= 0 {
		px = 1920 * 1080
	}
	const mib = 1024.0 * 1024.0
	return 8 + px*1.5/mib
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
