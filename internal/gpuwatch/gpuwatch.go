// Package gpuwatch detects when rave-mate is wedged by a GPU fault - a Windows display-driver
// reset (TDR) or a UI window that has stopped responding - and hands the fault to a recovery
// callback. Goal: a driver crash never leaves the app stuck in a broken state that forces a
// manual kill. Detection is OS-level (not an in-app heartbeat) because the failure mode is a
// live-but-frozen render thread: the event queue may still drain while rendering is dead, so a
// self-ping would keep succeeding. Real detectors exist only on Windows; elsewhere Start no-ops.
package gpuwatch

import (
	"context"
	"time"

	"rave.page/mate/internal/logbus"
)

// FaultKind classifies a detected GPU fault.
type FaultKind string

const (
	FaultHungWindow FaultKind = "hung-window" // main window stopped responding (IsHungAppWindow)
	FaultTDR        FaultKind = "tdr"         // OS logged a display-driver reset (System event log)
)

// Fault is one detected event handed to OnFault.
type Fault struct {
	Kind   FaultKind
	Detail string // human-readable cause (driver name, hang duration)
}

// Options configures the watchdog.
type Options struct {
	Log *logbus.Bus
	// OnFault fires once a fault is confirmed. Called on a background goroutine; a
	// FaultHungWindow handler typically restarts the process and never returns.
	OnFault func(Fault)
	// HangAfter is how long the window must stay unresponsive before FaultHungWindow (default 12s
	// - long enough to ride out heavy loads / modal drags, short enough to auto-recover fast).
	HangAfter time.Duration
	// Poll is the detector cadence (default 2s).
	Poll time.Duration
}

func (o *Options) applyDefaults() {
	if o.HangAfter <= 0 {
		o.HangAfter = 12 * time.Second
	}
	if o.Poll <= 0 {
		o.Poll = 2 * time.Second
	}
}

// Start launches the detectors bound to ctx (non-blocking). No-op where no real detector exists.
func Start(ctx context.Context, opt Options) {
	opt.applyDefaults()
	if opt.OnFault == nil {
		return
	}
	start(ctx, opt)
}
