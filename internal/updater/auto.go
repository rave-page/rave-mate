package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/selfupdate"
)

// Auto is the UNATTENDED update loop for a headless instance (Linux `--service` sender - nobody
// clicks "Install"): poll → writable? → download + verify (Ed25519 manifest + sha256) → probe
// the new binary → wait until idle → Mark + swap (keeps .prev) → hand off for re-exec.
// Never loops on a hopeless build: an unwritable install or a payload that fails verification is
// skipped (one log line) until the feed's build changes; a probe failure pins the build bad.
type Auto struct {
	cfg AutoConfig

	mu   sync.Mutex
	st   AutoStatus
	skip map[int]string      // build → reason it is not retried this process (notwritable, checksum)
	once map[string]struct{} // log-once keys for the current feed build (reset on build change)
	seen int                 // feed build the once-keys belong to
	// errOnce: distinct feed-check errors already logged (bounded: cleared on the next success)
	errOnce map[string]struct{}
}

// AutoFeed is the selfupdate.Updater surface Auto drives (test seam).
type AutoFeed interface {
	Enabled() bool
	Available(ctx context.Context) (*selfupdate.Release, bool, error)
	Download(ctx context.Context, rel *selfupdate.Release, onProgress func(done, total int64)) (AutoStaged, error)
}

// AutoStaged is a verified, staged payload (selfupdate.Staged).
type AutoStaged interface {
	Probe(ctx context.Context, want int) error
	Install() error
	Discard()
}

// AutoGuard is the rollback guard surface (selfupdate.Guard).
type AutoGuard interface {
	IsBad(build int) bool
	PinBad(build int) error
	Mark(from, to int) error
	Clear() error
}

// AutoConfig wires an Auto.
type AutoConfig struct {
	Feed    AutoFeed
	Guard   AutoGuard
	Current int // this binary's build
	Log     *logbus.Bus
	// Busy returns a non-empty reason while applying would interrupt a live set (media route,
	// live stream). The swap waits until Busy has been "" continuously for IdleFor.
	Busy func() string
	// Restart is handed the installed release; it must shut the app down cleanly and re-exec.
	Restart func(rel *selfupdate.Release)

	Interval   time.Duration // poll cadence (0 = DefaultInterval)
	FirstDelay time.Duration // settle after boot (0 = firstDelay)
	IdleFor    time.Duration // continuous idle needed before the swap (0 = 2m)
	IdlePoll   time.Duration // Busy re-check cadence while deferred (0 = 30s)
	DLTimeout  time.Duration // total cap on one download (0 = 30m)
}

// AutoStatus is the loop's state for ctl/status.
type AutoStatus struct {
	State  string // off|idle|uptodate|available|notwritable|downloading|probefailed|pinnedbad|deferred|applying|error
	Build  int    // feed build the state refers to (0 = none)
	Detail string
}

// String renders one status token, e.g. "notwritable(build 1235: /opt/rave-mate/rave-mate ...)".
func (s AutoStatus) String() string {
	var parts []string
	if s.Build > 0 {
		parts = append(parts, fmt.Sprintf("build %d", s.Build))
	}
	if s.Detail != "" {
		parts = append(parts, s.Detail)
	}
	if len(parts) == 0 {
		return s.State
	}
	return s.State + "(" + strings.Join(parts, ": ") + ")"
}

// NotWritableFix is the operator instruction logged + shown when the exe can't be replaced.
const NotWritableFix = "install rave-mate in a directory owned by the service user (e.g. ~/.local/share/rave-mate/rave-mate) and point the unit's ExecStart there, or disable with RAVE_MATE_NO_AUTOUPDATE=1"

// NewAuto builds the loop (no goroutines).
func NewAuto(cfg AutoConfig) *Auto {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.FirstDelay <= 0 {
		cfg.FirstDelay = firstDelay
	}
	if cfg.IdleFor <= 0 {
		cfg.IdleFor = 2 * time.Minute
	}
	if cfg.IdlePoll <= 0 {
		cfg.IdlePoll = 30 * time.Second
	}
	if cfg.DLTimeout <= 0 {
		cfg.DLTimeout = 30 * time.Minute
	}
	st := AutoStatus{State: "idle"}
	if cfg.Feed == nil || !cfg.Feed.Enabled() {
		st = AutoStatus{State: "off", Detail: "unstamped build (no feed)"}
	}
	return &Auto{cfg: cfg, st: st, skip: map[int]string{}, once: map[string]struct{}{}, errOnce: map[string]struct{}{}}
}

// Status returns the current state.
func (a *Auto) Status() AutoStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st
}

func (a *Auto) set(st AutoStatus) {
	a.mu.Lock()
	a.st = st
	a.mu.Unlock()
}

// logOnce logs at most once per (feed build, key); the key set resets when the build changes.
func (a *Auto) logOnce(build int, key string, warn bool, msg string, f map[string]any) {
	a.mu.Lock()
	if build != a.seen {
		a.seen, a.once = build, map[string]struct{}{}
	}
	_, dup := a.once[key]
	a.once[key] = struct{}{}
	a.mu.Unlock()
	if dup || a.cfg.Log == nil {
		return
	}
	if warn {
		a.cfg.Log.Warn("update", msg, f)
	} else {
		a.cfg.Log.Info("update", msg, f)
	}
}

// Run polls until ctx ends or an update is handed to Restart.
func (a *Auto) Run(ctx context.Context) {
	if a.cfg.Feed == nil || !a.cfg.Feed.Enabled() {
		return
	}
	delay := a.cfg.FirstDelay
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		switch a.Cycle(ctx) {
		case CycleApplied:
			return
		case CycleFailed:
			fails = min(fails+1, maxBackoffMul)
		default:
			fails = 0
		}
		delay = a.cfg.Interval << fails
	}
}

// CycleResult is one Cycle's outcome (drives Run's backoff).
type CycleResult int

// Cycle outcomes.
const (
	CycleOK      CycleResult = iota // nothing to do / skipped / deferred to the next poll
	CycleFailed                     // transient failure: back off
	CycleApplied                    // swapped + handed to Restart: the loop ends
)

// Cycle runs one poll → apply pass (exported for tests).
func (a *Auto) Cycle(ctx context.Context) CycleResult {
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	rel, avail, err := a.cfg.Feed.Available(cctx)
	cancel()
	if err != nil {
		a.set(AutoStatus{State: "error", Detail: err.Error()})
		a.mu.Lock()
		_, dup := a.errOnce[err.Error()]
		if len(a.errOnce) >= 16 {
			dup = true // cap: a long outage with varying messages logs at most 16 lines
		} else {
			a.errOnce[err.Error()] = struct{}{}
		}
		a.mu.Unlock()
		if !dup && a.cfg.Log != nil { // repeats stay quiet until a check succeeds again
			a.cfg.Log.Warn("update", "feed check failed", map[string]any{"error": err.Error()})
		}
		return CycleFailed
	}
	a.mu.Lock()
	clear(a.errOnce)
	a.mu.Unlock()
	if !avail {
		a.set(AutoStatus{State: "uptodate", Build: a.cfg.Current})
		return CycleOK
	}
	b := rel.Build
	if a.cfg.Guard != nil && a.cfg.Guard.IsBad(b) {
		a.set(AutoStatus{State: "pinnedbad", Build: b, Detail: "rolled back or failed probe; waiting for a newer build"})
		a.logOnce(b, "bad", true, "update skipped: build pinned bad", map[string]any{"build": b})
		return CycleOK
	}
	a.mu.Lock()
	_, skipped := a.skip[b]
	a.mu.Unlock()
	if skipped { // status already says why; stay quiet until the build changes
		return CycleOK
	}

	a.set(AutoStatus{State: "downloading", Build: b})
	a.logOnce(b, "avail", false, "update available - downloading", map[string]any{"build": b, "version": rel.Version})
	dctx, dcancel := context.WithTimeout(ctx, a.cfg.DLTimeout)
	staged, err := a.cfg.Feed.Download(dctx, rel, nil)
	dcancel()
	if err != nil {
		var nw *selfupdate.NotWritableError
		if errors.As(err, &nw) {
			a.skipBuild(b, "notwritable")
			a.set(AutoStatus{State: "notwritable", Build: b, Detail: nw.Path + " not writable by this user"})
			a.logOnce(b, "notwritable", true, "update available but "+nw.Path+" is not writable by this user - "+NotWritableFix,
				map[string]any{"build": b, "path": nw.Path, "error": nw.Err.Error()})
			return CycleOK
		}
		if strings.Contains(err.Error(), "checksum mismatch") || strings.Contains(err.Error(), "byte cap") {
			a.skipBuild(b, "verify")
			a.set(AutoStatus{State: "error", Build: b, Detail: "payload failed verification: " + err.Error()})
			a.logOnce(b, "verify", true, "update refused: payload failed verification", map[string]any{"build": b, "error": err.Error()})
			return CycleOK
		}
		a.set(AutoStatus{State: "error", Build: b, Detail: err.Error()})
		a.logOnce(b, "dl:"+err.Error(), true, "update download failed", map[string]any{"build": b, "error": err.Error()})
		return CycleFailed
	}
	if err := staged.Probe(ctx, b); err != nil {
		staged.Discard()
		if a.cfg.Guard != nil {
			_ = a.cfg.Guard.PinBad(b) // best-effort: unpinned = re-probed next build change only (skip below)
		}
		a.skipBuild(b, "probe")
		a.set(AutoStatus{State: "probefailed", Build: b, Detail: err.Error()})
		a.logOnce(b, "probe", true, "update refused: new binary failed its start probe - build pinned bad", map[string]any{"build": b, "error": err.Error()})
		return CycleOK
	}

	if !a.waitIdle(ctx, b) {
		staged.Discard()
		return CycleOK
	}

	if a.cfg.Guard != nil {
		if err := a.cfg.Guard.Mark(a.cfg.Current, b); err != nil {
			// No marker = no rollback guard: refuse to swap unguarded.
			staged.Discard()
			a.set(AutoStatus{State: "error", Build: b, Detail: "rollback marker: " + err.Error()})
			a.logOnce(b, "mark", true, "update aborted: cannot write rollback marker", map[string]any{"build": b, "error": err.Error()})
			return CycleFailed
		}
	}
	if err := staged.Install(); err != nil {
		if a.cfg.Guard != nil {
			_ = a.cfg.Guard.Clear()
		}
		a.set(AutoStatus{State: "error", Build: b, Detail: "install: " + err.Error()})
		a.logOnce(b, "install", true, "update install failed", map[string]any{"build": b, "error": err.Error()})
		return CycleFailed
	}
	a.set(AutoStatus{State: "applying", Build: b, Detail: "restarting into the new build"})
	if a.cfg.Log != nil {
		a.cfg.Log.Info("update", "installed - restarting into the new build", map[string]any{"from": a.cfg.Current, "build": b, "version": rel.Version})
	}
	if a.cfg.Restart != nil {
		a.cfg.Restart(rel)
	}
	return CycleApplied
}

func (a *Auto) skipBuild(b int, reason string) {
	a.mu.Lock()
	a.skip[b] = reason
	a.mu.Unlock()
}

// waitIdle blocks until Busy has reported idle continuously for IdleFor (false when ctx ends).
// Rule: a live set is never cut - the swap only happens after IdleFor without a media route /
// live stream; while busy the status reads deferred(<reason>), logged once per build.
func (a *Auto) waitIdle(ctx context.Context, b int) bool {
	if a.cfg.Busy == nil {
		return true
	}
	var idleSince time.Time
	for {
		if reason := a.cfg.Busy(); reason != "" {
			idleSince = time.Time{}
			a.set(AutoStatus{State: "deferred", Build: b, Detail: reason})
			a.logOnce(b, "deferred", false, "update ready - deferred while busy", map[string]any{"build": b, "reason": reason})
		} else {
			if idleSince.IsZero() {
				idleSince = time.Now()
			}
			if time.Since(idleSince) >= a.cfg.IdleFor {
				return true
			}
			a.set(AutoStatus{State: "deferred", Build: b, Detail: "waiting for idle"})
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(a.cfg.IdlePoll):
		}
	}
}

// WrapAutoFeed adapts *selfupdate.Updater to AutoFeed.
func WrapAutoFeed(u *selfupdate.Updater) AutoFeed { return autoFeed{u} }

type autoFeed struct{ u *selfupdate.Updater }

func (f autoFeed) Enabled() bool { return f.u.Enabled() }
func (f autoFeed) Available(ctx context.Context) (*selfupdate.Release, bool, error) {
	return f.u.Available(ctx)
}
func (f autoFeed) Download(ctx context.Context, rel *selfupdate.Release, p func(done, total int64)) (AutoStaged, error) {
	s, err := f.u.Download(ctx, rel, p)
	if err != nil {
		return nil, err
	}
	return s, nil
}
