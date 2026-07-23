package featurehost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

// PlayerProxy is the daemon-side stand-in for the subprocessed audio engine: the same surface
// the UI used in-process (play / togglePause / seek / stop / position / state / attachUI), backed
// by a local mirror that streamed tick/end events keep current - so the Fyne thread never does
// IPC and never touches the audio engine. seek is fire-and-forget (no round-trip = no stall).
// A decode/codec/oto fault crashes only the child; the host restarts it and the next play is clean.
type PlayerProxy struct {
	host *Host
	log  *logbus.Bus

	mu        sync.Mutex
	appCtx    context.Context
	mirror    State
	onTick    func(cur, total float64)
	onEnd     func()
	observers map[int]playerObs        // extra tick/end listeners (detached now-playing window)
	obsSeq    int                      // observer id source
	dispatch  func(func())             // UI-thread dispatcher (fyne.Do); default = direct call
	notify    func(title, body string) // decode-failure toast

	volMu   sync.Mutex
	vol     *float64 // last SetVolume; re-pushed to the child after every (re)spawn
	preGain float64  // last SetPreGainDB (0 = off); re-pushed like vol
}

// playerObs is one extra tick/end listener, independent of the single AttachUI panel sink.
type playerObs struct {
	onTick func(cur, total float64)
	onEnd  func()
}

// NewPlayerProxy builds the proxy + its host. The child spawns on Bind (pre-warmed) so the first
// play is instant. The child runs the native internal/audio engine (ffmpeg fallback for AAC/M4A).
func NewPlayerProxy(log *logbus.Bus) (*PlayerProxy, error) {
	p := &PlayerProxy{log: log, dispatch: func(fn func()) { fn() }}
	h, err := New(Options{
		Name: "player",
		Log:  log,
		Init: func() any { return struct{}{} },
		OnEvent: map[string]func(json.RawMessage){
			"tick":   p.onTickEvent,
			"end":    p.onEndEvent,
			"perror": p.onErrorEvent,
		},
		OnDown: p.onDown,
	})
	if err != nil {
		return nil, err
	}
	p.host = h
	return p, nil
}

// Host exposes the supervising host (SetNotifier, Stats).
func (p *PlayerProxy) Host() *Host { return p.host }

// SetDispatcher wires the UI-thread dispatcher (fyne.Do) so tick/end/toast callbacks land on the
// Fyne thread. SetNotify wires the decode-failure toast. Call both once at UI wiring.
func (p *PlayerProxy) SetDispatcher(fn func(func())) {
	p.mu.Lock()
	if fn != nil {
		p.dispatch = fn
	}
	p.mu.Unlock()
}

// SetNotify wires the user-facing decode-failure toast.
func (p *PlayerProxy) SetNotify(fn func(title, body string)) {
	p.mu.Lock()
	p.notify = fn
	p.mu.Unlock()
}

// Bind sets the app lifetime context (the child's lifetime) and pre-warms the child so the first
// play has no spawn latency. Call once at app wiring.
func (p *PlayerProxy) Bind(ctx context.Context) {
	p.mu.Lock()
	p.appCtx = ctx
	p.mu.Unlock()
	debuglog.Go(p.log, "feature:player", func() { _ = p.ensureUp() }) // pre-warm
}

// ── event handlers (run on the host reader goroutine) ──

func (p *PlayerProxy) onTickEvent(data json.RawMessage) {
	var t playerTick
	if json.Unmarshal(data, &t) != nil {
		return
	}
	p.mu.Lock()
	p.mirror.Cur, p.mirror.Total, p.mirror.Playing = t.Cur, t.Total, true
	// A tick may CONFIRM a pause but must never spuriously un-pause. A stale poll-tick sampled just
	// before a previewRelease pause can reach the wire AFTER the confirming tick; an unconditional
	// write would clobber Paused back to false, dropping the next spam-press into the silent
	// SeekExplicit-without-unpause branch (the "have to hit Stop first" bug). Every real resume goes
	// through an RPC (togglePause/playFrom/previewFrom) that rewrites the mirror directly, so a tick
	// never needs to clear Paused - only set it.
	if t.Paused {
		p.mirror.Paused = true
	}
	cb, disp := p.onTick, p.dispatch
	obs := p.tickObservers()
	p.mu.Unlock()
	if cb != nil {
		disp(func() { cb(t.Cur, t.Total) })
	}
	for _, fn := range obs {
		disp(func() { fn(t.Cur, t.Total) })
	}
}

func (p *PlayerProxy) onEndEvent(json.RawMessage) { p.fireEnd() }
func (p *PlayerProxy) onDown()                    { p.fireEnd() }

func (p *PlayerProxy) fireEnd() {
	p.mu.Lock()
	p.mirror = State{}
	cb, disp := p.onEnd, p.dispatch
	obs := p.endObservers()
	p.mu.Unlock()
	if cb != nil {
		disp(cb)
	}
	for _, fn := range obs {
		disp(fn)
	}
}

// tickObservers / endObservers snapshot the registered listener callbacks (caller holds mu).
func (p *PlayerProxy) tickObservers() []func(cur, total float64) {
	if len(p.observers) == 0 {
		return nil
	}
	out := make([]func(cur, total float64), 0, len(p.observers))
	for _, o := range p.observers {
		if o.onTick != nil {
			out = append(out, o.onTick)
		}
	}
	return out
}

func (p *PlayerProxy) endObservers() []func() {
	if len(p.observers) == 0 {
		return nil
	}
	out := make([]func(), 0, len(p.observers))
	for _, o := range p.observers {
		if o.onEnd != nil {
			out = append(out, o.onEnd)
		}
	}
	return out
}

// AddObserver registers an extra tick/end listener (e.g. a detached now-playing window),
// independent of the single AttachUI panel sink. Returns a remove func; callbacks land on the
// dispatcher thread (fyne.Do).
func (p *PlayerProxy) AddObserver(onTick func(cur, total float64), onEnd func()) func() {
	p.mu.Lock()
	if p.observers == nil {
		p.observers = map[int]playerObs{}
	}
	id := p.obsSeq
	p.obsSeq++
	p.observers[id] = playerObs{onTick: onTick, onEnd: onEnd}
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.observers, id)
		p.mu.Unlock()
	}
}

func (p *PlayerProxy) onErrorEvent(data json.RawMessage) {
	var e playerError
	if json.Unmarshal(data, &e) != nil {
		return
	}
	p.mu.Lock()
	n, disp := p.notify, p.dispatch
	p.mu.Unlock()
	if n != nil {
		disp(func() { n("Playback error", e.Msg) })
	}
}

// ── player surface (used by the UI; signatures mirror the old in-process player) ──

func (p *PlayerProxy) ensureUp() error {
	p.mu.Lock()
	appCtx := p.appCtx
	p.mu.Unlock()
	if appCtx == nil {
		return errors.New("player proxy not bound")
	}
	if err := p.host.Start(appCtx); err != nil {
		return err
	}
	deadline := time.Now().Add(initTimeout)
	for !p.host.Running() {
		if time.Now().After(deadline) {
			return errors.New("player service didn't come up")
		}
		time.Sleep(30 * time.Millisecond)
	}
	p.pushVolume() // fresh child: re-apply the persisted global gain
	return nil
}

// playParams / seekParams are the wire bodies (typed; new optional fields keep old children
// compatible - they ignore what they don't parse).
type playParams struct {
	Path     string  `json:"path"`
	StartSec float64 `json:"startSec,omitempty"`
}

type seekParams struct {
	Sec      float64 `json:"sec"`
	Explicit bool    `json:"explicit,omitempty"`
}

// A large ffmpeg-decoded file (long AAC/M4A under the RAM-preload cap) fully decodes to RAM inside
// the child Handle BEFORE the Call responds; a near-cap (~46 min) decode under concurrent OBS/VRChat
// load runs tens of seconds. The old 10-15s Call timeouts spuriously failed it (false "playback
// error" toast + the mirror never updated, while the child kept decoding and audio started late).
// Size the timeouts to cover a worst-case decode; the ceEnter Preload (background) normally caches
// the track before the first press, and load() dedups a press that races the in-flight preload.
const (
	playCallTimeout    = 60 * time.Second
	ctlCallTimeout     = 5 * time.Second // small control RPCs (setVolume)
	preloadCallTimeout = 120 * time.Second
)

// Play decodes + starts path in the child, replacing any current playback.
func (p *PlayerProxy) Play(path string) error { return p.PlayFrom(path, 0) }

// PlayFrom starts path at startSec: the child decodes at the offset directly, so there is
// no position-0 blip and no seek respawn (instant cue audition).
func (p *PlayerProxy) PlayFrom(path string, startSec float64) error {
	if err := p.ensureUp(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), playCallTimeout)
	defer cancel()
	raw, err := p.host.Call(ctx, "play", playParams{Path: path, StartSec: startSec})
	if err != nil {
		return err
	}
	var st State
	_ = json.Unmarshal(raw, &st)
	p.mu.Lock()
	p.mirror = st
	p.mu.Unlock()
	return nil
}

// PreviewFrom starts a cue-edit hold-audition at startSec (the child remembers it as the
// snap-back point). With the native engine on a RAM-preloaded track this is 0-latency.
func (p *PlayerProxy) PreviewFrom(path string, startSec float64) error {
	if err := p.ensureUp(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), playCallTimeout)
	defer cancel()
	raw, err := p.host.Call(ctx, "previewFrom", playParams{Path: path, StartSec: startSec})
	if err != nil {
		return err
	}
	var st State
	_ = json.Unmarshal(raw, &st)
	p.mu.Lock()
	p.mirror = st
	p.mu.Unlock()
	return nil
}

// PreviewRelease ends the hold-audition: stop + snap the playhead back to fallbackSec (the cursor
// the press started from; <0 = plain pause). Fire-and-forget (hot key-up path).
func (p *PlayerProxy) PreviewRelease(fallbackSec float64) {
	if p.host.Running() {
		_ = p.host.Send("previewRelease", struct {
			FallbackSec float64 `json:"fallbackSec"`
		}{fallbackSec})
	}
	// Optimistic mirror: the child pauses + snaps to fallbackSec. Reflect it NOW - the confirming
	// tick is ~200ms out, and a fast re-press before it lands must see Paused so it unpauses the
	// warm decoder instead of forcing a full re-decode (the "have to hit Stop first" bug).
	p.mu.Lock()
	if p.mirror.Playing {
		p.mirror.Paused = true
		if fallbackSec >= 0 {
			p.mirror.Cur = fallbackSec
		}
	}
	p.mu.Unlock()
}

// Preload decodes path into the child (RAM preload if it fits) without playing, so the first
// cue-edit Space is instant. Best-effort; a failure just means the first press pays the decode.
func (p *PlayerProxy) Preload(path string) error {
	if err := p.ensureUp(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), preloadCallTimeout)
	defer cancel()
	_, err := p.host.Call(ctx, "preload", playParams{Path: path})
	return err
}

// SetVolume pushes the global output gain (0..1) to the child engine. Fire-and-forget on a
// down child - the daemon re-pushes it after every (re)spawn via ensureUp.
func (p *PlayerProxy) SetVolume(v float64) {
	p.volMu.Lock()
	p.vol = &v
	p.volMu.Unlock()
	if !p.host.Running() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctlCallTimeout)
	defer cancel()
	_, _ = p.host.Call(ctx, "setVolume", volumeParams{Volume: v})
}

// pushVolume re-applies the last SetVolume + pre-gain after a (re)spawn. Caller ensured the
// child is up.
func (p *PlayerProxy) pushVolume() {
	p.volMu.Lock()
	v, g := p.vol, p.preGain
	p.volMu.Unlock()
	if v != nil {
		ctx, cancel := context.WithTimeout(context.Background(), ctlCallTimeout)
		_, _ = p.host.Call(ctx, "setVolume", volumeParams{Volume: *v})
		cancel()
	}
	if g != 0 {
		ctx, cancel := context.WithTimeout(context.Background(), ctlCallTimeout)
		_, _ = p.host.Call(ctx, "setPreGain", preGainParams{DB: g})
		cancel()
	}
}

// SetPreGainDB pushes the loudness pre-listen gain (dB on the decoded samples; 0 = off).
// Fire-and-forget like SetVolume; re-pushed after every (re)spawn.
func (p *PlayerProxy) SetPreGainDB(db float64) {
	p.volMu.Lock()
	p.preGain = db
	p.volMu.Unlock()
	if !p.host.Running() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ctlCallTimeout)
	defer cancel()
	_, _ = p.host.Call(ctx, "setPreGain", preGainParams{DB: db})
}

type volumeParams struct {
	Volume float64 `json:"volume"`
}

type preGainParams struct {
	DB float64 `json:"db"`
}

// TogglePause flips play/pause; returns the resulting paused state.
func (p *PlayerProxy) TogglePause() bool {
	if !p.host.Running() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := p.host.Call(ctx, "togglePause", nil)
	if err != nil {
		return p.State().Paused
	}
	var r struct {
		Paused bool `json:"paused"`
	}
	_ = json.Unmarshal(raw, &r)
	p.mu.Lock()
	p.mirror.Paused = r.Paused
	p.mu.Unlock()
	return r.Paused
}

// Seek jumps to sec - fire-and-forget so a slow decoder Seek never reaches the UI thread.
func (p *PlayerProxy) Seek(sec float64) {
	if p.host.Running() {
		_ = p.host.Send("seek", seekParams{Sec: sec})
	}
}

// SeekExplicit is a user-intent seek (cue audition / waveform click): bypasses the
// decoder's near-position noop guard so beat-precise seeks land exactly. Fire-and-forget.
func (p *PlayerProxy) SeekExplicit(sec float64) {
	if p.host.Running() {
		_ = p.host.Send("seek", seekParams{Sec: sec, Explicit: true})
	}
}

// Stop halts playback. Safe when idle / child down. Uses "halt" (not "stop"): "stop" is the
// reserved featurehost lifecycle method that exits the child - sending it here would kill the
// pre-warmed player process and trip the host's crash toast + restart on every window close.
func (p *PlayerProxy) Stop() {
	if p.host.Running() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = p.host.Call(ctx, "halt", nil)
		cancel()
	}
	p.mu.Lock()
	p.mirror = State{}
	p.mu.Unlock()
}

// Position returns the mirrored current + total seconds (updated ≤200ms by tick events).
func (p *PlayerProxy) Position() (cur, total float64, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mirror.Cur, p.mirror.Total, p.mirror.Playing
}

// State returns the mirrored playback snapshot.
func (p *PlayerProxy) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mirror
}

// AttachUI sets the tick/end callbacks the panel currently showing the playing file drives.
// Passing nils detaches.
func (p *PlayerProxy) AttachUI(onTick func(cur, total float64), onEnd func()) {
	p.mu.Lock()
	p.onTick, p.onEnd = onTick, onEnd
	p.mu.Unlock()
}
