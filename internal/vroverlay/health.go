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

// vrHealth tracks connected-session health from each tick's OpenVR call outcomes.
type vrHealth struct {
	consecFail int // consecutive ticks where every call attempted failed
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

// dead reports whether the session has been fully unresponsive long enough to force a reconnect.
func (h *vrHealth) dead() bool { return h.consecFail >= vrDeadBudget }

// reset clears the dead-run (new session / after a reconnect).
func (h *vrHealth) reset() { h.consecFail = 0 }
