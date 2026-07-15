// Package appgroups launches named sets of DJ-rig apps for crash recovery: after a VR-PC crash,
// relaunch every app in a group that isn't already running. Recovered apps are started fully
// detached (no kill-on-close job) so they outlive a rave-mate crash. Triggered from the UI, a
// VR/MIDI keybind, or `rave-mate ctl launch-group <id>`.
package appgroups

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/elevate"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/sysactivity"
	"rave.page/mate/internal/sysexec"
)

// procCacheTTL bounds staleness of the running-process snapshot used for count/status reads. Reads
// reuse ONE OS process enumeration per window instead of one walk per group per render/tick.
const procCacheTTL = time.Second

// Starter launches one app; injectable for tests. elevated → UAC relaunch (Windows).
type Starter func(path string, args []string, workDir string, elevated bool) error

// Service launches configured application groups.
type Service struct {
	groups func() []config.AppGroup
	act    sysactivity.Activity
	log    *logbus.Bus
	start  Starter

	// procCache: short-TTL running-process snapshot shared by count/status reads so the UI render
	// thread does one enumeration per window (async-refreshed once warm), never one per group.
	procMu   sync.Mutex
	procSet  map[string]bool // last snapshot; replaced wholesale (never mutated) so reads stay safe
	procOK   bool
	procAt   time.Time // when procSet was taken
	procHave bool      // a snapshot has been taken at least once
	procBusy bool      // a background refresh is in flight
}

// New builds the service reading groups live from cfg with the platform process detector.
func New(log *logbus.Bus, groups func() []config.AppGroup) *Service {
	return &Service{groups: groups, act: sysactivity.New(), log: log, start: defaultStart}
}

// defaultStart launches detached; elevated → UAC relaunch, falling back to a normal start on
// platforms without an elevation path.
func defaultStart(path string, args []string, workDir string, elevated bool) error {
	if elevated {
		if err := elevate.StartElevated(path, args, workDir); err != elevate.ErrUnsupported {
			return err // ok, or a real elevation error - don't fall back
		}
	}
	return sysexec.StartDetached(path, args, workDir)
}

// matchName is the process name used to test whether an app is already running. Splits on BOTH
// separators (not filepath.Base) so a Windows path like `C:\a\x.exe` still yields the basename when
// this runs/tests on Linux (CI), where filepath.Base wouldn't treat "\" as a separator.
func matchName(a config.AppRef) string {
	if n := strings.TrimSpace(a.MatchName); n != "" {
		return n
	}
	p := a.Path
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// markRunning records a just-launched app in the running set (lowercased, with + without ext) so a
// duplicate later in the same group isn't double-launched.
func markRunning(set map[string]bool, name string) {
	n := sysactivity.NormalizeName(name)
	if n == "" {
		return
	}
	set[n] = true
	if i := strings.LastIndexByte(n, '.'); i > 0 {
		set[n[:i]] = true
	}
}

// Group returns the configured group by id + ok.
func (s *Service) Group(id string) (config.AppGroup, bool) {
	for _, g := range s.groups() {
		if g.ID == id {
			return g, true
		}
	}
	return config.AppGroup{}, false
}

// Counts is a group's running/total app tally for UI status.
type Counts struct{ Running, Total int }

// countIn tallies a group's running/total apps against a running-process set.
func countIn(set map[string]bool, g config.AppGroup) Counts {
	var c Counts
	for _, a := range g.Apps {
		if strings.TrimSpace(a.Path) == "" {
			continue
		}
		c.Total++
		if sysactivity.Running(set, matchName(a)) {
			c.Running++
		}
	}
	return c
}

// RunningCount reports how many of a group's apps are currently running (for UI status).
func (s *Service) RunningCount(g config.AppGroup) (running, total int) {
	set, _ := s.snapshot()
	c := countIn(set, g)
	return c.Running, c.Total
}

// RunningCounts tallies running/total for every group against ONE cached process snapshot - avoids
// the N process-table walks a per-group RunningCount loop caused per render/tick. Order matches groups.
func (s *Service) RunningCounts(groups []config.AppGroup) []Counts {
	set, _ := s.snapshot()
	out := make([]Counts, len(groups))
	for i, g := range groups {
		out[i] = countIn(set, g)
	}
	return out
}

// snapshot returns the running-process set, cached for procCacheTTL. First call blocks so the first
// render is accurate; later stale calls return the cached set and refresh in the background, so the
// UI render/act thread never blocks on a process enumeration once warm.
func (s *Service) snapshot() (map[string]bool, bool) {
	s.procMu.Lock()
	have := s.procHave
	set, ok := s.procSet, s.procOK
	if have && time.Since(s.procAt) >= procCacheTTL && !s.procBusy {
		s.procBusy = true
		debuglog.Go(s.log, "appgroups", s.refresh) // off-thread + panic-guarded; updates cache for the next read
	}
	s.procMu.Unlock()
	if have {
		return set, ok
	}
	return s.refreshNow() // cold: enumerate synchronously, then cache
}

// refreshNow enumerates processes, stores the snapshot, and returns it (clears the busy flag).
func (s *Service) refreshNow() (map[string]bool, bool) {
	set, ok := s.act.RunningProcesses()
	s.procMu.Lock()
	s.procSet, s.procOK, s.procAt, s.procHave, s.procBusy = set, ok, time.Now(), true, false
	s.procMu.Unlock()
	return set, ok
}

// refresh re-enumerates off the caller's goroutine (keeps the UI render/act thread unblocked).
func (s *Service) refresh() { _, _ = s.refreshNow() }

// LaunchGroup starts every app in the group not already running (matched by MatchName or the exe
// basename). Snapshots the running set once. Returns the launched + skipped (already-running) app
// names. A per-app launch failure is logged and skipped, not fatal.
func (s *Service) LaunchGroup(id string) (started, skipped []string, err error) {
	g, ok := s.Group(id)
	if !ok {
		return nil, nil, fmt.Errorf("appgroups: no group %q", id)
	}
	set, _ := s.act.RunningProcesses() // ok=false (non-Windows) → nil → launch all (fail open)
	if set == nil {
		set = map[string]bool{}
	}
	for _, a := range g.Apps {
		if strings.TrimSpace(a.Path) == "" {
			continue
		}
		name := matchName(a)
		if sysactivity.Running(set, name) {
			skipped = append(skipped, name)
			continue
		}
		if a.DelayMs > 0 {
			time.Sleep(time.Duration(a.DelayMs) * time.Millisecond)
		}
		if e := s.start(a.Path, a.Args, a.WorkDir, a.Elevated); e != nil {
			if s.log != nil {
				s.log.Warn("appgroups", "launch failed: "+name+": "+e.Error(), nil)
			}
			continue
		}
		markRunning(set, name)
		started = append(started, name)
	}
	if s.log != nil {
		s.log.Info("appgroups", fmt.Sprintf("group %q: %d started, %d already running", g.Name, len(started), len(skipped)), nil)
	}
	return started, skipped, nil
}
