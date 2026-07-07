package ui

import (
	"fyne.io/fyne/v2"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

// nativeVPlayer adapts the shared native audio engine (nativePlayer) to the vplayer transport, so the
// ONE shared playerControls widget drives audio exactly like video - no more hand-rolled per-panel
// audio transports. The audio engine is a singleton (one file at a time) that starts idle, so this
// binds a path and implements the optional idle/onEnd capabilities playerControls probes for (video
// engines auto-start playback and don't need them). Decode-init runs in the player child process;
// play() is dispatched off-thread so a click never blocks the Fyne UI thread.
type nativeVPlayer struct {
	p      *nativePlayer
	path   string
	log    *logbus.Bus
	notify func(title, msg string)
	onTick func(cur, total float64)
	onEnd  func()
}

func newNativeVPlayer(p *nativePlayer, path string, log *logbus.Bus, notify func(title, msg string)) *nativeVPlayer {
	return &nativeVPlayer{p: p, path: path, log: log, notify: notify}
}

// mine reports whether the shared engine is currently loaded with this adapter's path.
func (a *nativeVPlayer) mine() bool { return a.p.state().path == a.path }

// ensurePlaying (re)starts this adapter's path, wiring our tick/end sinks first. Off-thread.
func (a *nativeVPlayer) ensurePlaying() {
	a.p.attachUI(a.onTick, a.onEnd)
	debuglog.Go(a.log, "player-adapt-play", func() {
		if err := a.p.play(a.path); err != nil && a.notify != nil {
			fyne.Do(func() { a.notify("rave-mate", "Play failed: "+err.Error()) })
		}
	})
}

// reattach redirects the shared engine's live tick/end callbacks to this adapter (used when the
// modal opens over an already-playing set so the transport reflects it immediately).
func (a *nativeVPlayer) reattach() { a.p.attachUI(a.onTick, a.onEnd) }

func (a *nativeVPlayer) Play() { a.ensurePlaying() }

// TogglePause doubles as start: if our path isn't the live one (or is stopped) it starts playback
// (optimistically un-paused); otherwise it flips pause on the running engine.
func (a *nativeVPlayer) TogglePause() bool {
	st := a.p.state()
	if st.path != a.path || !st.playing {
		a.ensurePlaying()
		return false
	}
	return a.p.togglePause()
}

func (a *nativeVPlayer) Seek(sec float64) {
	if a.mine() {
		a.p.seek(sec)
	}
}

// SetVolume is a no-op: the native audio engine has no volume control yet (playerControls hides the
// slider for audio via pcOpts.hasVolume).
func (a *nativeVPlayer) SetVolume(int) {}

func (a *nativeVPlayer) Position() (cur, total float64) {
	if !a.mine() {
		return 0, 0
	}
	c, t, _ := a.p.position()
	return c, t
}

func (a *nativeVPlayer) OnTick(fn func(cur, total float64)) { a.onTick = fn }

// OnEnd is the optional playerControls capability: reset the transport when playback finishes.
func (a *nativeVPlayer) OnEnd(fn func()) { a.onEnd = fn }

func (a *nativeVPlayer) Close() {
	if a.mine() {
		a.p.stop()
	}
}
