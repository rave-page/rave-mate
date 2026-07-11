// Package updater is the app-wide self-update state machine over shared/selfupdate: a periodic
// (5-min) feed poll, once-per-version first-detection notification (persisted), and an explicit
// idle → available → downloading → downloaded(verified) → staged(needs-restart) progression that
// every surface (nav rail, tray menu, settings card) renders from. Signature/checksum failures
// surface as Status.Err and never advance the machine - an unverified build is never installed.
package updater

import (
	"context"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/selfupdate"
)

// State is the update lifecycle position.
type State int

const (
	Idle        State = iota // no update known
	Available                // newer release on the feed, not downloaded
	Downloading              // download + verification in flight (Progress live)
	Downloaded               // payload staged next to the exe, all checksums verified
	Staged                   // swapped over the exe on disk - restart finishes the update
)

// Status is a render-ready snapshot of the machine.
type Status struct {
	State    State
	Rel      *selfupdate.Release // non-nil in every state but Idle
	Progress float64             // 0..1 while Downloading
	Err      string              // last failure ("" = none); state never advances on error
	Checked  bool                // at least one poll completed (Idle+!Checked = "not checked yet")
}

// Installer applies a downloaded+verified update (selfupdate.Staged).
type Installer interface{ Install() error }

// Feed abstracts selfupdate.Updater (test seam).
type Feed interface {
	Enabled() bool
	Available(ctx context.Context) (*selfupdate.Release, bool, error)
	Download(ctx context.Context, rel *selfupdate.Release, onProgress func(done, total int64)) (Installer, error)
}

// feedAdapter lifts *selfupdate.Updater's concrete *Staged return to the Installer interface.
type feedAdapter struct{ u *selfupdate.Updater }

func (f feedAdapter) Enabled() bool { return f.u.Enabled() }
func (f feedAdapter) Available(ctx context.Context) (*selfupdate.Release, bool, error) {
	return f.u.Available(ctx)
}
func (f feedAdapter) Download(ctx context.Context, rel *selfupdate.Release, onProgress func(done, total int64)) (Installer, error) {
	s, err := f.u.Download(ctx, rel, onProgress)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// WrapFeed adapts the shared selfupdate.Updater to the Feed seam.
func WrapFeed(u *selfupdate.Updater) Feed { return feedAdapter{u} }

const (
	// DefaultInterval is the feed poll cadence (cheap: one ~1 KB manifest GET + .sig).
	DefaultInterval = 5 * time.Minute
	// maxBackoffMul caps the failure backoff at interval<<maxBackoffMul (5m → 80m): repeated
	// failures (offline) must not hot-loop, per the idle-CPU discipline.
	maxBackoffMul = 4
	firstDelay    = 30 * time.Second // settle time before the first check after launch
	checkTimeout  = 30 * time.Second
	dlTimeout     = 10 * time.Minute
)

// Config wires a Manager. Notify fires once per newly-seen version (persist seam via
// LastNotified/SetNotified survives restarts); OnChange fires on every status change
// (both run on manager goroutines - marshal to your UI thread yourself).
type Config struct {
	Feed         Feed
	Interval     time.Duration // 0 = DefaultInterval
	Log          *logbus.Bus
	LastNotified func() string
	SetNotified  func(v string)
	Notify       func(rel *selfupdate.Release)
	OnChange     func(st Status)
}

// Manager owns the update state machine. Build with New; poll with Run.
type Manager struct {
	cfg  Config
	gate logbus.Gate
	kick chan struct{} // CheckNow wakeup for Run's timer

	mu    sync.Mutex
	st    Status
	inst  Installer
	busy  bool // download/install goroutine in flight
	fails int  // consecutive check failures (backoff)
}

// New builds a Manager (no goroutines yet).
func New(cfg Config) *Manager {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	return &Manager{cfg: cfg, kick: make(chan struct{}, 1)}
}

// Status returns the current snapshot.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

// Enabled reports whether the feed can be polled (false on a dev build).
func (m *Manager) Enabled() bool { return m.cfg.Feed != nil && m.cfg.Feed.Enabled() }

// Run polls the feed until stop closes: first check after a short settle, then every Interval,
// backing off (doubling, capped) while checks fail so an offline box never hot-loops.
func (m *Manager) Run(stop <-chan struct{}) {
	if !m.Enabled() {
		return
	}
	delay := firstDelay
	for {
		select {
		case <-stop:
			return
		case <-time.After(delay):
		case <-m.kick:
		}
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		m.Check(ctx)
		cancel()
		delay = m.nextDelay()
	}
}

// CheckNow wakes Run for an immediate poll (settings "Check for updates").
func (m *Manager) CheckNow() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// nextDelay returns the wait before the next poll: Interval, doubled per consecutive failure,
// capped at Interval<<maxBackoffMul.
func (m *Manager) nextDelay() time.Duration {
	m.mu.Lock()
	fails := m.fails
	m.mu.Unlock()
	if fails > maxBackoffMul {
		fails = maxBackoffMul
	}
	return m.cfg.Interval << fails
}

// Check polls the feed once. No-op while a download is in flight or an update is already
// downloaded/staged (never fight or supersede in-progress work; restart picks up the next
// release on the following poll).
func (m *Manager) Check(ctx context.Context) {
	if !m.Enabled() {
		return
	}
	m.mu.Lock()
	if m.busy || m.st.State >= Downloading {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	rel, avail, err := m.cfg.Feed.Available(ctx)
	m.mu.Lock()
	prev := m.st
	notify := false
	if err != nil {
		m.fails++
		// Errors during check (offline, feed down, manifest signature invalid) surface on the
		// CURRENT state - an Available card keeps its release, Idle stays idle.
		m.st.Err = err.Error()
	} else {
		m.fails = 0
		switch {
		case !avail:
			m.st = Status{State: Idle}
		case m.st.State == Idle || m.st.Rel == nil || m.st.Rel.Version != rel.Version || m.st.Rel.Build != rel.Build:
			m.st = Status{State: Available, Rel: rel}
			if m.cfg.Notify != nil && (m.cfg.LastNotified == nil || m.cfg.LastNotified() != rel.Version) {
				if m.cfg.SetNotified != nil {
					m.cfg.SetNotified(rel.Version)
				}
				notify = true
			}
		default: // same release still pending
			m.st.Err = ""
		}
	}
	m.st.Checked = true
	st := m.st
	m.mu.Unlock()

	m.logCheck(st, err)
	if notify {
		m.cfg.Notify(rel)
	}
	if st != prev {
		m.emit(st)
	}
}

// logCheck logs poll outcomes through the Gate: state transitions + hourly refresh only, so a
// repeating failure (offline laptop) doesn't spam the bus every 5 minutes.
func (m *Manager) logCheck(st Status, err error) {
	if m.cfg.Log == nil {
		return
	}
	switch {
	case err != nil:
		if n, ok := m.gate.Should("fail:"+err.Error(), time.Hour); ok {
			f := map[string]any{"error": err.Error()}
			if n > 0 {
				f["suppressed"] = n
			}
			m.cfg.Log.Warn("update", "feed check failed", f)
		}
	case st.State == Available:
		if _, ok := m.gate.Should("avail:"+st.Rel.Version, 0); ok {
			m.cfg.Log.Info("update", "update available", map[string]any{"version": st.Rel.Version, "build": st.Rel.Build})
		}
	default:
		if _, ok := m.gate.Should("ok", 0); ok { // transition back to healthy/idle
			m.cfg.Log.Info("update", "up to date", nil)
		}
	}
}

// StartDownload begins the download+verify phase (from Available only; one in flight).
func (m *Manager) StartDownload() {
	m.mu.Lock()
	if m.busy || m.st.State != Available || m.st.Rel == nil {
		m.mu.Unlock()
		return
	}
	m.busy = true
	rel := m.st.Rel
	m.st.State, m.st.Progress, m.st.Err = Downloading, 0, ""
	st := m.st
	m.mu.Unlock()
	m.emit(st)
	go m.download(rel)
}

// download runs the blocking download (own goroutine via StartDownload; called directly in tests).
func (m *Manager) download(rel *selfupdate.Release) {
	ctx, cancel := context.WithTimeout(context.Background(), dlTimeout)
	defer cancel()
	lastPct := -1
	inst, err := m.cfg.Feed.Download(ctx, rel, func(done, total int64) {
		if total <= 0 {
			return
		}
		pct := int(float64(done) / float64(total) * 100)
		if pct == lastPct {
			return
		}
		lastPct = pct
		m.mu.Lock()
		m.st.Progress = float64(pct) / 100
		st := m.st
		m.mu.Unlock()
		m.emit(st)
	})
	m.mu.Lock()
	m.busy = false
	if err != nil {
		// Verification/download failure: back to Available, error surfaced. NEVER installed.
		m.st.State, m.st.Progress, m.st.Err = Available, 0, err.Error()
	} else {
		m.inst = inst
		m.st.State, m.st.Progress, m.st.Err = Downloaded, 1, ""
	}
	st := m.st
	m.mu.Unlock()
	if m.cfg.Log != nil {
		if err != nil {
			m.cfg.Log.Warn("update", "download failed", map[string]any{"version": rel.Version, "error": err.Error()})
		} else {
			m.cfg.Log.Info("update", "downloaded + verified", map[string]any{"version": rel.Version, "build": rel.Build})
		}
	}
	m.emit(st)
}

// Install swaps the downloaded build over the running exe (from Downloaded only).
func (m *Manager) Install() {
	m.mu.Lock()
	if m.busy || m.st.State != Downloaded || m.inst == nil {
		m.mu.Unlock()
		return
	}
	m.busy = true
	inst := m.inst
	m.mu.Unlock()
	go m.install(inst)
}

// install runs the blocking swap (own goroutine via Install; called directly in tests).
func (m *Manager) install(inst Installer) {
	err := inst.Install()
	m.mu.Lock()
	m.busy = false
	if err != nil {
		m.st.Err = err.Error() // stays Downloaded; user may retry
	} else {
		m.inst = nil
		m.st.State, m.st.Err = Staged, ""
	}
	st := m.st
	m.mu.Unlock()
	if m.cfg.Log != nil {
		if err != nil {
			m.cfg.Log.Warn("update", "install failed", map[string]any{"error": err.Error()})
		} else {
			m.cfg.Log.Info("update", "installed - restart to finish", map[string]any{"version": verOf(st.Rel)})
		}
	}
	m.emit(st)
}

func (m *Manager) emit(st Status) {
	if m.cfg.OnChange != nil {
		m.cfg.OnChange(st)
	}
}

func verOf(rel *selfupdate.Release) string {
	if rel == nil {
		return ""
	}
	return rel.Version
}
