package vroverlay

// In-daemon VR device-lost recovery (task #3). Start's supervise loop already reconnects on a clean
// SteamVR Quit event (PollQuit) and on SteamVR not running. The remaining gap: a session that goes
// invalid WITHOUT a Quit event - SteamVR killed/crashed, the runtime handle goes stale, a device is
// lost - where every OpenVR call starts failing but PollQuit stays false. The connected loop would then
// tick against a dead runtime forever (the "stuck, must kill it" state). vrHealth watches per-tick call
// outcomes and trips after a sustained run of fully-failed ticks, so the loop breaks → Start re-inits.

// vrDeadBudget is how many consecutive all-failing ticks force a reconnect (~5s at the 100ms tick).
// Long enough that a transient blip (one bad frame) never reconnects a healthy session; short enough
// that a genuinely dead session recovers in seconds instead of hanging.
const vrDeadBudget = 50

// vrTexFailBudget: consecutive SetTexture (SetOverlayRaw) failures - no intervening success - that
// force an in-place reconnect. The all-fail tick detector above misses a post-TDR session because
// Show/SetTransform are void in C (always "succeed") while raw uploads keep failing; counting the
// upload path directly closes that gap (GPU_RESILIENCE_PLAN P0). SetTexture already retries via
// recreate() internally, so 10 straight failures = session-level breakage, not one wedged overlay.
const vrTexFailBudget = 10

// vrHealth tracks connected-session health from each tick's OpenVR call outcomes.
// VR-goroutine only - unsynchronized.
type vrHealth struct {
	consecFail int // consecutive ticks where every call attempted failed
	texFails   int // consecutive SetTexture failures across all overlays; any success resets
}

// observe folds one tick's outcome in: attempts = OpenVR calls made, fails = how many returned an error.
// A tick that made calls and had ALL of them fail extends the dead-run; any success resets it. A tick
// that made no calls (nothing to render) is neutral - it neither trips nor clears the counter, so an
// idle session isn't mistaken for a dead one.
func (h *vrHealth) observe(attempts, fails int) {
	switch {
	case attempts == 0:
		// neutral
	case fails == attempts:
		h.consecFail++
	default:
		h.consecFail = 0
	}
}

// observeTex folds one SetTexture outcome in.
func (h *vrHealth) observeTex(err error) {
	if err != nil {
		h.texFails++
	} else {
		h.texFails = 0
	}
}

// dead reports whether the session has been fully unresponsive long enough to force a reconnect.
func (h *vrHealth) dead() bool { return h.consecFail >= vrDeadBudget }

// texDead reports a persistent texture-upload failure streak (post-TDR dead compositor).
func (h *vrHealth) texDead() bool { return h.texFails >= vrTexFailBudget }

// reset clears the dead-runs (new session / after a reconnect).
func (h *vrHealth) reset() { h.consecFail, h.texFails = 0, 0 }
