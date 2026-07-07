package app

import (
	"encoding/json"
	"sync"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/libsync"
)

// djSyncSched owns the auto-sync scheduler (reuses the automation Scheduler) + the armed-job set.
type djSyncSched struct {
	mu    sync.Mutex
	sched *automation.Scheduler
	armed []string // job IDs currently scheduled (for LIBSYNC-AUTO-STATUS)
}

// startDJSyncScheduler builds the auto-sync scheduler (fire = run the job non-dry) + reconciles
// against current config. Safe to call once during app init (both windowed + service modes).
func (c *appControl) startDJSyncScheduler() {
	if c.lib == nil {
		return
	}
	c.djSched.mu.Lock()
	c.djSched.sched = automation.NewScheduler(c.log, func(id string) { c.SyncDJ(id, false) })
	c.djSched.mu.Unlock()
	c.ReconcileLibSync()
}

// ReconcileLibSync (re)arms the auto-sync schedules from current config. Called on app init and
// whenever the UI mutates sync jobs. No-op until the scheduler is built. Disabling the feature
// clears all schedules.
func (c *appControl) ReconcileLibSync() {
	c.djSched.mu.Lock()
	defer c.djSched.mu.Unlock()
	if c.djSched.sched == nil {
		return
	}
	ls := c.cfg.Features.LibrarySync
	var scheds []automation.Schedule
	var armed []string
	if ls.Enabled {
		for _, j := range ls.Jobs {
			if !j.Enabled || !j.Auto.Enabled || j.Auto.Kind == "" {
				continue
			}
			scheds = append(scheds, syncScheduleToAutomation(j))
			armed = append(armed, j.ID)
		}
	}
	c.djSched.sched.Set(scheds)
	c.djSched.armed = armed
	c.log.Info("dj-sync", "auto-sync reconciled", map[string]any{"enabled": ls.Enabled, "armed": len(armed)})
}

// syncScheduleToAutomation maps a job's SyncSchedule onto an automation.Schedule, defaulting the
// write-back safety gate (skip while a target DJ app is open) when the user didn't set one.
func syncScheduleToAutomation(j config.SyncJob) automation.Schedule {
	a := j.Auto
	exclude := a.ExcludeAppsRunning
	if len(exclude) == 0 {
		exclude = defaultExcludeApps(j)
	}
	return automation.Schedule{
		ID: j.ID, Label: j.Label, Enabled: true, AutomationID: "libsync:" + j.ID,
		Kind:            automation.ScheduleKind(a.Kind),
		IntervalMinutes: a.IntervalMinutes, AtHour: a.AtHour, AtMinute: a.AtMinute,
		CronExpr: a.CronExpr, IdleMinutes: a.IdleMinutes,
		ExcludeAppsRunning: exclude,
	}
}

// defaultExcludeApps returns the process names to avoid running an auto write-back against while
// open. Only write-back targets gate (importable-file + tags writes are safe any time).
func defaultExcludeApps(j config.SyncJob) []string {
	set := map[string]bool{}
	for _, t := range j.Targets {
		if t.Mode != libsync.ModeWriteback {
			continue
		}
		switch t.App {
		case libsync.AppTraktor:
			set["Traktor"] = true
		case libsync.AppRekordbox:
			set["rekordbox"] = true
		case libsync.AppVirtualDJ:
			set["VirtualDJ"] = true
		}
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	return out
}

// DJSyncAutoStatus returns the auto-sync scheduler state as one-line JSON (ctl LIBSYNC-AUTO-STATUS).
func (c *appControl) DJSyncAutoStatus() string {
	c.djSched.mu.Lock()
	armed := append([]string{}, c.djSched.armed...)
	running := c.djSched.sched != nil
	c.djSched.mu.Unlock()
	b, err := json.Marshal(map[string]any{
		"enabled":    c.cfg.Features.LibrarySync.Enabled,
		"scheduler":  running,
		"armed_jobs": armed,
	})
	if err != nil {
		return `{"scheduler":false}`
	}
	return string(b)
}
