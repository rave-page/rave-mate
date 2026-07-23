package audio

import (
	"errors"
	"io"
	"math"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
)

// outLatency is the oto device buffer. ~15ms => low output latency (vs beep's 100ms) while still
// underrun-safe on Windows WASAPI. Seeks Reset() this buffer so a new position is audible within it.
const outLatency = 15 * time.Millisecond

// outputPlayer is the slice of oto.Player the engine uses — seam for a fake in tests (no device).
type outputPlayer interface {
	Play()
	Pause()
	Reset()
	IsPlaying() bool
	BufferedSize() int
	SetVolume(float64)
	Close() error
}

// otoContext is the process-global audio device (one physical output). Initialized once.
var (
	ctxOnce  sync.Once
	otoCtx   *oto.Context
	ctxErr   error
	ctxReady chan struct{}
)

func initContext() error {
	ctxOnce.Do(func() {
		otoCtx, ctxReady, ctxErr = oto.NewContext(&oto.NewContextOptions{
			SampleRate:   deviceRate,
			ChannelCount: deviceChannels,
			Format:       oto.FormatFloat32LE,
			BufferSize:   outLatency,
		})
	})
	if ctxErr != nil {
		return ctxErr
	}
	<-ctxReady
	return nil
}

// newOutput builds a real oto player from r. Overridden in tests to inject a fake device.
var newOutput = func(r io.Reader) (outputPlayer, error) {
	if err := initContext(); err != nil {
		return nil, err
	}
	return otoCtx.NewPlayer(r), nil
}

// Engine is the native low-latency player: one track loaded at a time, decoded into a RAM buffer
// (instant seek, 0-latency Space) or streamed with indexed seek. Transport supports the cue-edit
// hold-to-preview (play from the cursor; release snaps back to where playback started).
type Engine struct {
	mu      sync.Mutex
	player  outputPlayer
	src     *source
	format  Format // device format (always deviceRate/deviceChannels)
	path    string
	vol     float64
	preGain float64 // linear pre-listen gain (0 = unity; see SetPreGainDB)

	previewReturn int64 // frame to snap back to on PreviewRelease (-1 = not previewing)

	// deferred RAM upgrade: EnsureRAM decodes in the caller's goroutine; a track playing at
	// completion parks the buffer here and the next repositioning adopts it glitch-free.
	pendingRAM *source
	ramBusy    bool
}

// NewEngine builds an idle engine. The device is opened lazily on the first Load.
func NewEngine() *Engine {
	return &Engine{format: Format{SampleRate: deviceRate, Channels: deviceChannels}, vol: 1, previewReturn: -1}
}

// Loaded reports the currently-loaded path ("" = none).
func (e *Engine) Loaded() string { e.mu.Lock(); defer e.mu.Unlock(); return e.path }

// Load decodes path with the native decoder and readies playback (paused at 0), streaming
// from disk immediately; EnsureRAM upgrades in the background. Replaces any current track.
// Returns ErrUnsupported for a format with no native decoder (caller builds an ffmpeg-backed
// audio.Decoder and calls LoadDecoder).
func (e *Engine) Load(path string) error {
	dec, err := Open(path)
	if err != nil {
		return err
	}
	return e.LoadDecoder(dec, path)
}

// LoadDecoder readies playback from an already-opened Decoder (native OR the ffmpeg bridge for
// AAC/M4A). Takes ownership of dec. ALWAYS stream-first: a RAM preload here used to decode the
// whole file synchronously inside PlayFrom (seconds for FLAC, tens of seconds on the ffmpeg
// bridge) - the reported "first media load hangs". Callers that want the RAM buffer (cue-edit
// 0-latency Space) follow up with EnsureRAM.
func (e *Engine) LoadDecoder(dec Decoder, path string) error {
	s := newStreamSource(dec) // dec owned by the stream source now
	player, err := newOutput(s)
	if err != nil {
		_ = s.Close()
		return err
	}
	e.mu.Lock()
	old, oldSrc, oldPend := e.player, e.src, e.pendingRAM
	e.player, e.src, e.path, e.previewReturn, e.pendingRAM = player, s, path, -1, nil
	player.SetVolume(e.vol)
	if e.preGain != 0 {
		s.setGain(e.preGain)
	}
	e.mu.Unlock()
	if old != nil {
		old.Pause()
		_ = old.Close()
	}
	if oldSrc != nil {
		_ = oldSrc.Close()
	}
	if oldPend != nil {
		_ = oldPend.Close()
	}
	return nil
}

// EnsureRAM upgrades the loaded track to the fully-decoded RAM source (instant seek,
// 0-latency Space). The decode runs in the caller's goroutine - call from a background one.
// Idle/paused transport swaps immediately; a playing one parks the buffer and the next
// repositioning (PlayFrom/SeekTo/PreviewFrom) adopts it, where the swap is free. open reopens
// the path with whichever decoder served it (native / ffmpeg bridge). Skips oversized and
// already-RAM tracks; dedups concurrent upgrades.
func (e *Engine) EnsureRAM(path string, open func() (Decoder, error)) error {
	e.mu.Lock()
	skip := e.path != path || e.src == nil || e.src.ram != nil || e.pendingRAM != nil || e.ramBusy
	if !skip {
		devBytes := e.src.Total() * deviceBytes * deviceChannels
		skip = devBytes <= 0 || devBytes > preloadMaxBytes
	}
	if skip {
		e.mu.Unlock()
		return nil
	}
	e.ramBusy = true
	e.mu.Unlock()

	dec, err := open()
	var ram *source
	if err == nil {
		ram, err = newRAMSource(dec) // the heavy decode, off-lock
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ramBusy = false
	if err != nil {
		return err
	}
	if e.path != path || e.src == nil || e.src.ram != nil {
		_ = ram.Close() // track changed mid-decode
		return nil
	}
	if e.player != nil && e.player.IsPlaying() {
		e.pendingRAM = ram
		return nil
	}
	return e.adoptRAMLocked(ram)
}

// adoptRAMLocked swaps the current source for ram, preserving position, stop bound and
// play/pause state. Caller holds e.mu.
func (e *Engine) adoptRAMLocked(ram *source) error {
	pos := e.src.Pos()
	stopAt := e.src.stopAtNow()
	wasPlaying := e.player != nil && e.player.IsPlaying()
	player, err := newOutput(ram)
	if err != nil {
		_ = ram.Close()
		return err
	}
	_ = ram.SeekTo(pos, true)
	ram.setStopAt(stopAt)
	old, oldSrc := e.player, e.src
	e.player, e.src = player, ram
	player.SetVolume(e.vol)
	if e.preGain != 0 {
		ram.setGain(e.preGain)
	}
	if wasPlaying {
		player.Play()
	}
	if old != nil {
		old.Pause()
		_ = old.Close()
	}
	if oldSrc != nil {
		_ = oldSrc.Close()
	}
	return nil
}

// adoptPendingLocked consumes a parked RAM upgrade at a repositioning boundary (the caller
// re-seeks right after, so the handoff is glitch-free). Caller holds e.mu.
func (e *Engine) adoptPendingLocked() {
	if e.pendingRAM == nil {
		return
	}
	ram := e.pendingRAM
	e.pendingRAM = nil
	_ = e.adoptRAMLocked(ram)
}

// PreloadedRAM reports whether the loaded track is fully in RAM (instant seek / 0-latency Space).
func (e *Engine) PreloadedRAM() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.src != nil && e.src.ram != nil
}

// estimateDeviceBytes returns the device-rate PCM byte size, or -1 if unknown (=> stream).
func estimateDeviceBytes(nativeFrames int64, sf Format) int64 {
	if nativeFrames <= 0 || sf.SampleRate <= 0 {
		return -1
	}
	devFrames := nativeFrames * int64(deviceRate) / int64(sf.SampleRate)
	return devFrames * deviceBytes * deviceChannels
}

// PlayFrom seeks to sec and starts playback.
func (e *Engine) PlayFrom(sec float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.src == nil {
		return
	}
	e.adoptPendingLocked() // repositioning boundary: free RAM-upgrade handoff
	e.previewReturn = -1
	_ = e.src.SeekTo(e.format.SecondsToFrame(sec), true)
	e.src.setStopAt(-1)
	e.resetAndPlay()
}

// Play resumes from the current position.
func (e *Engine) Play() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.player != nil {
		e.player.Play()
	}
}

// Pause halts without moving the position.
func (e *Engine) Pause() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.player != nil {
		e.player.Pause()
	}
}

// TogglePause flips play/pause; returns the resulting paused state.
func (e *Engine) TogglePause() (paused bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.player == nil {
		return true
	}
	if e.player.IsPlaying() {
		e.player.Pause()
		return true
	}
	e.player.Play()
	return false
}

// SeekTo repositions to sec (sample-accurate). Resets the oto buffer so the new position is audible
// immediately, preserving play/pause state. explicit=false (follow-slider) coalesces a near seek on
// a streaming source; explicit=true (cue/waveform click) always lands exactly.
func (e *Engine) SeekTo(sec float64, explicit bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.src == nil {
		return
	}
	e.adoptPendingLocked() // repositioning boundary: free RAM-upgrade handoff
	wasPlaying := e.player != nil && e.player.IsPlaying()
	_ = e.src.SeekTo(e.format.SecondsToFrame(sec), explicit)
	if e.player != nil {
		e.player.Reset()
		if wasPlaying {
			e.player.Play()
		}
	}
}

// PreviewFrom starts playback at sec and remembers it as the return point. Hold-to-audition:
// call on key-down. The playhead advances while playing.
func (e *Engine) PreviewFrom(sec float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.src == nil {
		return
	}
	e.adoptPendingLocked() // repositioning boundary: free RAM-upgrade handoff
	frame := e.format.SecondsToFrame(sec)
	e.previewReturn = frame
	_ = e.src.SeekTo(frame, true)
	e.src.setStopAt(-1)
	e.resetAndPlay()
}

// PreviewRelease stops playback and snaps the position back to where PreviewFrom started
// (key-up in a hold-to-audition). fallbackSec covers a release whose press went through plain
// PlayFrom (no preview anchor): <0 = plain pause, >=0 = snap there instead.
func (e *Engine) PreviewRelease(fallbackSec float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.player != nil {
		e.player.Pause()
	}
	ret := e.previewReturn
	if ret < 0 && fallbackSec >= 0 {
		ret = e.format.SecondsToFrame(fallbackSec)
	}
	if e.src != nil && ret >= 0 {
		_ = e.src.SeekTo(ret, true)
		if e.player != nil {
			e.player.Reset() // drop the ~15ms already-buffered so we're truly back at the return frame
		}
	}
	e.previewReturn = -1
}

// IsPlaying reports whether the device is actively pulling (false when paused or drained).
func (e *Engine) IsPlaying() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.player != nil && e.player.IsPlaying()
}

// Drained reports whether playback reached the track's natural end (source read to EOF). This is
// authoritative for natural-end detection - a pause (PreviewRelease snap-back near the tail) does
// NOT set it, so it can't be mistaken for EOF the way a position>=total heuristic could.
func (e *Engine) Drained() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.src != nil && e.src.reachedEnd()
}

// resetAndPlay drops any buffered audio and starts from the source cursor (caller holds mu).
func (e *Engine) resetAndPlay() {
	if e.player == nil {
		return
	}
	e.player.Reset()
	e.player.Play()
}

// Position returns the AUDIBLE current + total seconds and whether a track is loaded. Audible =
// source cursor minus oto's still-buffered frames (so the readout matches what's heard).
func (e *Engine) Position() (cur, total float64, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.src == nil {
		return 0, 0, false
	}
	buffered := int64(0)
	if e.player != nil {
		buffered = int64(e.player.BufferedSize()) / (deviceBytes * deviceChannels)
	}
	audible := e.src.Pos() - buffered
	if audible < 0 {
		audible = 0
	}
	tot := e.src.Total()
	total = 0
	if tot >= 0 {
		total = e.format.FrameToSeconds(tot)
	}
	return e.format.FrameToSeconds(audible), total, true
}

// SetPreGainDB sets a decibel pre-gain applied to the decoded samples BEFORE the output
// volume (loudness pre-listen: audition the export's planned constant gain). Unlike the
// 0..1 output volume it can BOOST (+dB); samples are clamped at ±1 in the source. Survives
// track loads until changed; 0 dB = off.
func (e *Engine) SetPreGainDB(db float64) {
	g := math.Pow(10, db/20)
	e.mu.Lock()
	e.preGain = g
	if e.src != nil {
		e.src.setGain(g)
	}
	e.mu.Unlock()
}

// SetVolume sets output gain (0..1).
func (e *Engine) SetVolume(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	e.mu.Lock()
	e.vol = v
	if e.player != nil {
		e.player.SetVolume(v)
	}
	e.mu.Unlock()
}

// Stop halts playback and frees the current track.
func (e *Engine) Stop() {
	e.mu.Lock()
	player, src := e.player, e.src
	e.player, e.src, e.path, e.previewReturn = nil, nil, "", -1
	e.mu.Unlock()
	if player != nil {
		player.Pause()
		_ = player.Close()
	}
	if src != nil {
		_ = src.Close()
	}
}

// errNoTrack is returned by control calls with nothing loaded.
var errNoTrack = errors.New("audio: no track loaded")
