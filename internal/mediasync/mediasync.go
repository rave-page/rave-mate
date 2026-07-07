// Package mediasync keeps an OBS media source's playback cursor locked to a house clock.
//
// The house clock is a TimeSource (v1: WallClock anchored by "start sync now"; a medialink /
// SMPTE house clock can plug in later without touching the chaser). A Chaser polls one media
// input's cursor and applies dead-banded corrections:
//   - error within the dead band (~2 frames): leave it alone (avoid visible micro-jitter);
//   - small error: nudge the cursor by the delta (OffsetMediaInputCursor);
//   - large error, or the source not playing: RESTART + absolute seek to the target.
//
// OBS free-runs each source's clock (no genlock) so corrections are step-jumps - see
// DMX_TIMECODE_RESEARCH.md § OBS. Prefer a VLC Video Source (ms-exact seeks); a Media Source
// snaps seeks to the nearest keyframe (40–80ms), surfaced as an accuracy hint.
package mediasync

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/obs"
)

// MediaController is the OBS media surface a Chaser drives. Satisfied by *obs.Client and by the
// daemon proxies (featurehost.ObsProxy, obscontrol's direct-LAN client). A tiny interface keeps
// the chaser unit-testable with a fake.
type MediaController interface {
	GetMediaInputStatus(ctx context.Context, inputName string) (obs.MediaInputStatus, error)
	SetMediaInputCursor(ctx context.Context, inputName string, cursorMs int) error
	OffsetMediaInputCursor(ctx context.Context, inputName string, offsetMs int) error
	TriggerMediaInputAction(ctx context.Context, inputName, action string) error
}

// Defaults for the control law (used when a SourceConfig field is zero).
const (
	DefaultFps                = 30.0
	DefaultDeadBandFrames     = 2.0
	DefaultRestartThresholdMs = 1500
)

// SourceConfig describes one media input to keep in sync + the control-law tunables.
type SourceConfig struct {
	Endpoint           string  // OBS endpoint/source id this input lives on ("" / "local" = local OBS)
	InputName          string  // OBS input name
	InputKind          string  // obs.Kind* (accuracy hint; empty = unknown)
	StaticOffsetMs     int     // per-source constant offset added to the house position (+ = ahead)
	Fps                float64 // frame rate for the dead-band calc (0 = DefaultFps)
	DeadBandFrames     float64 // no correction under this many frames of error (0 = DefaultDeadBandFrames)
	RestartThresholdMs int     // error at/above which we RESTART+seek instead of nudging (0 = default)
}

// deadBandMs is the no-correction half-band in ms (DeadBandFrames × frame duration).
func (c SourceConfig) deadBandMs() float64 {
	fps := c.Fps
	if fps <= 0 {
		fps = DefaultFps
	}
	frames := c.DeadBandFrames
	if frames <= 0 {
		frames = DefaultDeadBandFrames
	}
	return frames * (1000.0 / fps)
}

// restartMs is the large-error threshold in ms.
func (c SourceConfig) restartMs() float64 {
	if c.RestartThresholdMs > 0 {
		return float64(c.RestartThresholdMs)
	}
	return DefaultRestartThresholdMs
}

// Action is the correction the control law chose for one tick.
type Action int

const (
	ActNone        Action = iota // inside the dead band - leave it
	ActNudge                     // small error - OffsetMediaInputCursor by the delta
	ActRestartSeek               // large error / not playing - RESTART + absolute seek
)

func (a Action) String() string {
	switch a {
	case ActNudge:
		return "nudge"
	case ActRestartSeek:
		return "restart-seek"
	default:
		return "none"
	}
}

// decide picks a correction from the signed error (target − cursor, ms). Positive error = the
// media is behind the target and must advance. nudgeMs is the signed offset for ActNudge.
func decide(errMs float64, cfg SourceConfig) (a Action, nudgeMs int) {
	abs := math.Abs(errMs)
	switch {
	case abs <= cfg.deadBandMs():
		return ActNone, 0
	case abs >= cfg.restartMs():
		return ActRestartSeek, 0
	default:
		return ActNudge, int(math.Round(errMs))
	}
}

// Status is a Chaser's live, published state (per endpoint+source).
type Status struct {
	Endpoint          string    `json:"endpoint"`
	Source            string    `json:"source"`
	Kind              string    `json:"kind"`
	Active            bool      `json:"active"`  // house clock running → chasing
	Playing           bool      `json:"playing"` // OBS media state is playing/buffering
	CursorMs          int64     `json:"cursorMs"`
	TargetMs          int64     `json:"targetMs"`
	ErrorMs           int64     `json:"errorMs"`
	LastAction        string    `json:"lastAction"`
	CorrectionsPerMin float64   `json:"correctionsPerMin"`
	KeyframeSnapped   bool      `json:"keyframeSnapped"` // this kind's seeks snap to keyframes
	Hint              string    `json:"hint,omitempty"`
	Err               string    `json:"err,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// Chaser locks one media input to a TimeSource. Not safe for concurrent Tick calls (one owner
// goroutine drives Tick on a ticker); Status is safe to read concurrently.
type Chaser struct {
	cfg MediaControllerCfg
	ts  TimeSource

	now func() time.Time // injectable clock for corrections/min + timestamps (tests)

	ticking atomic.Bool // guards against overlapping ticks (slow OBS round-trip vs the driver cadence)

	mu      sync.Mutex
	status  Status
	corrLog []time.Time // wall times of applied corrections (trailing 60s)
}

// MediaControllerCfg binds a SourceConfig to the controller that reaches it.
type MediaControllerCfg struct {
	SourceConfig
	Ctrl MediaController
}

// NewChaser builds a chaser for one source against a house clock.
func NewChaser(mc MediaControllerCfg, ts TimeSource) *Chaser {
	return &Chaser{
		cfg: mc,
		ts:  ts,
		now: time.Now,
		status: Status{
			Endpoint:        mc.Endpoint,
			Source:          mc.InputName,
			Kind:            mc.InputKind,
			KeyframeSnapped: obs.SeekKeyframeSnapped(mc.InputKind),
			Hint:            accuracyHint(mc.InputKind),
			LastAction:      "idle",
		},
	}
}

// accuracyHint explains the seek accuracy of a source kind for the UI/status.
func accuracyHint(kind string) string {
	switch kind {
	case obs.KindVLCSource:
		return "VLC source: seeks are millisecond-exact."
	case obs.KindMediaSource, obs.KindMediaSource2:
		return "Media Source: seeks snap to the nearest keyframe (~40–80ms). Use a VLC source for tighter sync."
	default:
		return ""
	}
}

// Config returns the chaser's source config (for reconcile comparison).
func (c *Chaser) Config() SourceConfig { return c.cfg.SourceConfig }

// Status returns a snapshot of the chaser's live state.
func (c *Chaser) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Tick performs one control step: read the house clock, read the media cursor, and correct.
// A stopped house clock leaves the source untouched (marks the status idle). Returns any OBS
// error (the caller decides whether to log/retry).
func (c *Chaser) Tick(ctx context.Context) error {
	if !c.ticking.CompareAndSwap(false, true) {
		return nil // a previous tick is still in flight (slow OBS) - skip this one
	}
	defer c.ticking.Store(false)

	pos, running := c.ts.Position()
	if !running {
		c.update(func(s *Status) {
			s.Active = false
			s.LastAction = "idle"
			s.UpdatedAt = c.now()
		})
		return nil
	}
	targetMs := pos.Milliseconds() + int64(c.cfg.StaticOffsetMs)

	st, err := c.cfg.Ctrl.GetMediaInputStatus(ctx, c.cfg.InputName)
	if err != nil {
		c.update(func(s *Status) {
			s.Active = true
			s.Err = err.Error()
			s.UpdatedAt = c.now()
		})
		return err
	}

	// Cold start: a stopped/ended/paused source won't advance from PLAY → RESTART then seek.
	if !st.Playing() {
		var applyErr error
		if e := c.cfg.Ctrl.TriggerMediaInputAction(ctx, c.cfg.InputName, obs.MediaActionRestart); e != nil {
			applyErr = e
		} else if e := c.cfg.Ctrl.SetMediaInputCursor(ctx, c.cfg.InputName, int(targetMs)); e != nil {
			applyErr = e
		}
		c.recordCorrection()
		corr := c.correctionsPerMin()
		c.update(func(s *Status) {
			s.Active, s.Playing = true, false
			s.TargetMs, s.CursorMs = targetMs, st.Cursor.Milliseconds()
			s.ErrorMs = targetMs - st.Cursor.Milliseconds()
			s.LastAction = "cold-start"
			s.CorrectionsPerMin = corr
			s.setErr(applyErr)
			s.UpdatedAt = c.now()
		})
		return applyErr
	}

	cursorMs := st.Cursor.Milliseconds()
	errMs := float64(targetMs - cursorMs)
	action, nudge := decide(errMs, c.cfg.SourceConfig)

	var applyErr error
	switch action {
	case ActNudge:
		applyErr = c.cfg.Ctrl.OffsetMediaInputCursor(ctx, c.cfg.InputName, nudge)
	case ActRestartSeek:
		if e := c.cfg.Ctrl.TriggerMediaInputAction(ctx, c.cfg.InputName, obs.MediaActionRestart); e != nil {
			applyErr = e
		} else {
			applyErr = c.cfg.Ctrl.SetMediaInputCursor(ctx, c.cfg.InputName, int(targetMs))
		}
	}
	if action != ActNone {
		c.recordCorrection()
	}
	corr := c.correctionsPerMin()
	c.update(func(s *Status) {
		s.Active, s.Playing = true, true
		s.TargetMs, s.CursorMs, s.ErrorMs = targetMs, cursorMs, targetMs-cursorMs
		s.LastAction = action.String()
		s.CorrectionsPerMin = corr
		s.setErr(applyErr)
		s.UpdatedAt = c.now()
	})
	return applyErr
}

// recordCorrection logs a correction time + prunes entries older than 60s.
func (c *Chaser) recordCorrection() {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	kept := c.corrLog[:0]
	for _, t := range c.corrLog {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	c.corrLog = append(kept, now)
}

// correctionsPerMin returns the count of corrections in the trailing 60s.
func (c *Chaser) correctionsPerMin() float64 {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	n := 0
	for _, t := range c.corrLog {
		if t.After(cutoff) {
			n++
		}
	}
	return float64(n)
}

// update mutates the status under lock.
func (c *Chaser) update(fn func(*Status)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.status)
}

// setErr sets/clears the status error string.
func (s *Status) setErr(err error) {
	if err != nil {
		s.Err = err.Error()
	} else {
		s.Err = ""
	}
}
