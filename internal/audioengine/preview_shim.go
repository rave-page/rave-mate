package audioengine

// Cue-edit transport shim for the legacy beep engine, so it satisfies the same playerBackend the
// native engine does. Beep has no RAM preload, so Preload is a no-op and the hold-audition is
// PlayFrom + pause-and-reseek — same behavior as before the native engine, just without the
// 0-latency Space the native path gives.

// PreviewFrom plays from startSec (hold-Space press). Same as PlayFrom for the legacy engine.
func (e *Engine) PreviewFrom(path string, startSec float64) error { return e.PlayFrom(path, startSec) }

// PreviewRelease pauses and (if fallbackSec>=0) re-seeks there — the cue cursor the press started
// from, so the playhead snaps back on key-up.
func (e *Engine) PreviewRelease(fallbackSec float64) {
	st := e.State()
	if st.Playing && !st.Paused {
		e.TogglePause()
	}
	if fallbackSec >= 0 {
		e.SeekTo(fallbackSec, true)
	}
}

// Preload is a no-op for the legacy engine (no RAM buffer). Returns nil so callers don't special-case.
func (e *Engine) Preload(string) error { return nil }
