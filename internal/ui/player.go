package ui

import (
	"rave.page/mate/internal/audioengine"
	"rave.page/mate/internal/featurehost"
)

// nativePlayer adapts the subprocessed audio engine (featurehost.PlayerProxy) to the lowercase
// surface the player panels use. Playback now runs in the `player` child process - a decode hang,
// codec fault, or oto crash kills only that child, never the UI; seek is fire-and-forget so a
// slow MP3 Seek never reaches the Fyne thread. All panels share ONE proxy (one file at a time).
type nativePlayer struct{ proxy *featurehost.PlayerProxy }

// isPlayable reports whether the native engine can decode this file (else use "Open").
func isPlayable(path string) bool { return audioengine.IsPlayable(path) }

// playerState is a snapshot for (re)building a panel that reflects live playback.
type playerState struct {
	path    string
	playing bool
	paused  bool
	cur     float64
	total   float64
}

func (p *nativePlayer) play(path string) error { return p.proxy.Play(path) }
func (p *nativePlayer) seek(sec float64)       { p.proxy.Seek(sec) }
func (p *nativePlayer) stop()                  { p.proxy.Stop() }
func (p *nativePlayer) togglePause() bool      { return p.proxy.TogglePause() }

func (p *nativePlayer) position() (cur, total float64, ok bool) { return p.proxy.Position() }

func (p *nativePlayer) attachUI(onTick func(cur, total float64), onEnd func()) {
	p.proxy.AttachUI(onTick, onEnd)
}

// addObserver registers an extra tick/end listener (detached now-playing window) without
// displacing the in-panel AttachUI sink. Returns a remove func.
func (p *nativePlayer) addObserver(onTick func(cur, total float64), onEnd func()) func() {
	return p.proxy.AddObserver(onTick, onEnd)
}

func (p *nativePlayer) state() playerState {
	s := p.proxy.State()
	return playerState{path: s.Path, playing: s.Playing, paused: s.Paused, cur: s.Cur, total: s.Total}
}
