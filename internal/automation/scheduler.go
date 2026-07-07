package automation

import (
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/sysactivity"
)

// evalInterval is how often cron + idle schedules are evaluated. Cron has minute granularity;
// two ticks per minute are deduped via cronLast.
const evalInterval = 30 * time.Second

// Scheduler arms per-schedule timers (interval/daily) + a single eval loop (cron/idle) in the
// daemon and fires the user callback on tick - gated by each schedule's idle/running-app
// conditions. Idle/process gating is Windows-only (fails open elsewhere).
type Scheduler struct {
	log  Logger
	fire func(scheduleID string)
	act  sysactivity.Activity

	mu      sync.Mutex
	gen     uint64   // bumped on each Set/Stop; stale timers self-discard
	cancels []func() // one teardown per armed schedule + the eval loop

	byID      map[string]Schedule  // current schedules (gate lookup + eval)
	cronLast  map[string]time.Time // last cron fire (truncated to minute) - dedupes 2 ticks/min
	idleFired map[string]bool      // idle-kind: fired this idle period (re-arms when active again)
}

// NewScheduler builds a scheduler; fire(scheduleID) runs on a timer goroutine per tick.
func NewScheduler(log Logger, fire func(scheduleID string)) *Scheduler {
	return &Scheduler{
		log: log, fire: fire, act: sysactivity.New(),
		byID: map[string]Schedule{}, cronLast: map[string]time.Time{}, idleFired: map[string]bool{},
	}
}

// Set replaces all schedules: cancels existing timers, (re)arms one per Enabled schedule, and
// starts the cron/idle eval loop when any cron/idle schedule is present.
func (s *Scheduler) Set(schedules []Schedule) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	gen := s.gen

	s.byID = make(map[string]Schedule, len(schedules))
	evalNeeded := false
	for _, sc := range schedules {
		if !sc.Enabled {
			continue
		}
		s.byID[sc.ID] = sc
		switch sc.Kind {
		case ScheduleInterval:
			s.armIntervalLocked(sc)
		case ScheduleDaily:
			s.armDailyLocked(sc, gen)
		case ScheduleCron, ScheduleIdle:
			evalNeeded = true
		}
	}
	s.pruneEvalStateLocked()
	if evalNeeded {
		s.startEvalLoopLocked(gen)
	}
}

// Stop stops every timer/ticker and clears state. Idempotent; safe to call repeatedly.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.byID = map[string]Schedule{}
}

// stopLocked tears down all armed schedules and invalidates in-flight re-arms. Caller holds mu.
func (s *Scheduler) stopLocked() {
	for _, c := range s.cancels {
		c()
	}
	s.cancels = nil
	s.gen++ // any pending callback for the old gen becomes a no-op
}

// pruneEvalStateLocked drops eval state for schedules that no longer exist. Caller holds mu.
func (s *Scheduler) pruneEvalStateLocked() {
	for id := range s.cronLast {
		if _, ok := s.byID[id]; !ok {
			delete(s.cronLast, id)
		}
	}
	for id := range s.idleFired {
		if _, ok := s.byID[id]; !ok {
			delete(s.idleFired, id)
		}
	}
}

// armIntervalLocked starts a ticker firing every IntervalMinutes (clamped ≥1). Caller holds mu.
func (s *Scheduler) armIntervalLocked(sc Schedule) {
	mins := max(sc.IntervalMinutes, 1)
	tk := time.NewTicker(time.Duration(mins) * time.Minute)
	done := make(chan struct{})
	s.cancels = append(s.cancels, func() {
		tk.Stop()
		close(done)
	})
	id := sc.ID
	go func() {
		defer debuglog.Recover(nil, source, false) // nil bus: scheduler decoupled via Logger iface
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				s.fireGated(id)
			}
		}
	}()
	s.log.Info(source, "scheduler armed interval", map[string]any{
		"scheduleId": id, "label": sc.Label, "everyMinutes": mins,
	})
}

// armDailyLocked schedules next AtHour:AtMinute; re-arms for the next day after firing. Caller holds mu.
func (s *Scheduler) armDailyLocked(sc Schedule, gen uint64) {
	next := nextDaily(time.Now(), sc.AtHour, sc.AtMinute)
	t := s.scheduleDailyAtLocked(sc, gen, next)
	s.cancels = append(s.cancels, func() { t.Stop() })
	s.log.Info(source, "scheduler armed daily", map[string]any{
		"scheduleId": sc.ID, "label": sc.Label,
		"atHour": sc.AtHour, "atMinute": sc.AtMinute, "next": next.Format(time.RFC3339),
	})
}

// scheduleDailyAtLocked arms one AfterFunc for `at`; on fire (if still current gen) re-arms next day. Caller holds mu.
func (s *Scheduler) scheduleDailyAtLocked(sc Schedule, gen uint64, at time.Time) *time.Timer {
	return time.AfterFunc(time.Until(at), func() {
		s.mu.Lock()
		stale := gen != s.gen
		s.mu.Unlock()
		if stale {
			return // canceled by a Set/Stop while waiting
		}
		s.fireGated(sc.ID)
		s.mu.Lock()
		defer s.mu.Unlock()
		if gen != s.gen {
			return
		}
		next := s.scheduleDailyAtLocked(sc, gen, nextDaily(time.Now(), sc.AtHour, sc.AtMinute))
		s.cancels = append(s.cancels, func() { next.Stop() })
	})
}

// startEvalLoopLocked starts the cron/idle evaluation ticker for this gen. Caller holds mu.
func (s *Scheduler) startEvalLoopLocked(gen uint64) {
	done := make(chan struct{})
	s.cancels = append(s.cancels, func() { close(done) })
	go func() {
		defer debuglog.Recover(nil, source, false) // nil bus: scheduler decoupled via Logger iface
		tk := time.NewTicker(evalInterval)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				s.mu.Lock()
				stale := gen != s.gen
				s.mu.Unlock()
				if stale {
					return
				}
				s.evalTick(time.Now())
			}
		}
	}()
}

// evalTick checks every cron + idle schedule once. Cron dedupes per minute; idle fires once per
// idle period (re-arms when the system becomes active again).
func (s *Scheduler) evalTick(now time.Time) {
	minute := now.Truncate(time.Minute)
	s.mu.Lock()
	scheds := make([]Schedule, 0, len(s.byID))
	for _, sc := range s.byID {
		scheds = append(scheds, sc)
	}
	act := s.act
	s.mu.Unlock()

	idleDur, idleOK := act.IdleDuration()
	for _, sc := range scheds {
		switch sc.Kind {
		case ScheduleCron:
			ce, err := parseCron(sc.CronExpr)
			if err != nil {
				continue
			}
			if !ce.matches(now) {
				continue
			}
			s.mu.Lock()
			already := s.cronLast[sc.ID].Equal(minute)
			if !already {
				s.cronLast[sc.ID] = minute
			}
			s.mu.Unlock()
			if !already {
				s.fireGated(sc.ID)
			}
		case ScheduleIdle:
			if sc.IdleMinutes <= 0 || !idleOK {
				continue
			}
			threshold := time.Duration(sc.IdleMinutes) * time.Minute
			s.mu.Lock()
			fired := s.idleFired[sc.ID]
			cross := idleDur >= threshold && !fired
			if cross {
				s.idleFired[sc.ID] = true
			} else if idleDur < threshold && fired {
				s.idleFired[sc.ID] = false
			}
			s.mu.Unlock()
			if cross {
				s.fireGated(sc.ID)
			}
		}
	}
}

// fireGated looks up the schedule, checks its idle/running-app gates, then fires (or skips).
func (s *Scheduler) fireGated(id string) {
	s.mu.Lock()
	sc, ok := s.byID[id]
	act := s.act
	s.mu.Unlock()
	if !ok {
		return
	}
	if reason, blocked := gateBlock(sc, act); blocked {
		s.log.Info(source, "scheduler gated (skipped)", map[string]any{
			"scheduleId": id, "label": sc.Label, "reason": reason,
		})
		return
	}
	s.doFire(id)
}

// gateBlock reports whether a schedule's gates block firing, and why. Idle/process gates fail
// open when the platform can't report them (non-Windows), so automations still run there.
func gateBlock(sc Schedule, act sysactivity.Activity) (string, bool) {
	if sc.RequireIdleMinutes > 0 {
		if d, ok := act.IdleDuration(); ok && d < time.Duration(sc.RequireIdleMinutes)*time.Minute {
			return "system not idle long enough", true
		}
	}
	if len(sc.RequireAppsRunning) > 0 || len(sc.ExcludeAppsRunning) > 0 {
		if set, ok := act.RunningProcesses(); ok {
			for _, name := range sc.RequireAppsRunning {
				if !sysactivity.Running(set, name) {
					return "required app not running: " + name, true
				}
			}
			for _, name := range sc.ExcludeAppsRunning {
				if sysactivity.Running(set, name) {
					return "excluded app running: " + name, true
				}
			}
		}
	}
	return "", false
}

// doFire invokes the user callback with panic recovery so a panic can't kill the scheduler.
func (s *Scheduler) doFire(id string) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Warn(source, "scheduler fire panic recovered", map[string]any{
				"scheduleId": id, "panic": r,
			})
		}
	}()
	s.log.Info(source, "scheduler fire", map[string]any{"scheduleId": id})
	if s.fire != nil {
		s.fire(id)
	}
}

// nextDaily returns the next local hour:min occurrence at/after now (tomorrow if today's passed).
func nextDaily(now time.Time, hour, min int) time.Time {
	loc := now.Location()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
