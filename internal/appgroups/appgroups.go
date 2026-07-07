// Package appgroups launches named sets of DJ-rig apps for crash recovery: after a VR-PC crash,
// relaunch every app in a group that isn't already running. Recovered apps are started fully
// detached (no kill-on-close job) so they outlive a rave-mate crash. Triggered from the UI, a
// VR/MIDI keybind, or `rave-mate ctl launch-group <id>`.
package appgroups

import (
	"fmt"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/elevate"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/sysactivity"
	"rave.page/mate/internal/sysexec"
)

// Starter launches one app; injectable for tests. elevated → UAC relaunch (Windows).
type Starter func(path string, args []string, workDir string, elevated bool) error

// Service launches configured application groups.
type Service struct {
	groups func() []config.AppGroup
	act    sysactivity.Activity
	log    *logbus.Bus
	start  Starter
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

// RunningCount reports how many of a group's apps are currently running (for UI status).
func (s *Service) RunningCount(g config.AppGroup) (running, total int) {
	set, _ := s.act.RunningProcesses()
	for _, a := range g.Apps {
		if strings.TrimSpace(a.Path) == "" {
			continue
		}
		total++
		if sysactivity.Running(set, matchName(a)) {
			running++
		}
	}
	return running, total
}

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
