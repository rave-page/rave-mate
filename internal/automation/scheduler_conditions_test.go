package automation

import (
	"sync"
	"testing"
	"time"
)

type fakeAct struct {
	idle   time.Duration
	idleOK bool
	procs  map[string]bool
	procOK bool
}

func (f fakeAct) IdleDuration() (time.Duration, bool)       { return f.idle, f.idleOK }
func (f fakeAct) RunningProcesses() (map[string]bool, bool) { return f.procs, f.procOK }

func TestGateBlock(t *testing.T) {
	procs := map[string]bool{"traktor.exe": true, "traktor": true}
	cases := []struct {
		name    string
		sc      Schedule
		act     fakeAct
		blocked bool
	}{
		{"idle-too-short", Schedule{RequireIdleMinutes: 5}, fakeAct{idle: 2 * time.Minute, idleOK: true}, true},
		{"idle-ok", Schedule{RequireIdleMinutes: 5}, fakeAct{idle: 10 * time.Minute, idleOK: true}, false},
		{"idle-unsupported-failopen", Schedule{RequireIdleMinutes: 5}, fakeAct{idleOK: false}, false},
		{"exclude-running", Schedule{ExcludeAppsRunning: []string{"Traktor"}}, fakeAct{procs: procs, procOK: true}, true},
		{"exclude-not-running", Schedule{ExcludeAppsRunning: []string{"OBS"}}, fakeAct{procs: procs, procOK: true}, false},
		{"require-missing", Schedule{RequireAppsRunning: []string{"OBS"}}, fakeAct{procs: procs, procOK: true}, true},
		{"require-present", Schedule{RequireAppsRunning: []string{"Traktor.exe"}}, fakeAct{procs: procs, procOK: true}, false},
		{"apps-unsupported-failopen", Schedule{ExcludeAppsRunning: []string{"Traktor"}}, fakeAct{procOK: false}, false},
	}
	for _, c := range cases {
		if _, blocked := gateBlock(c.sc, c.act); blocked != c.blocked {
			t.Errorf("%s: blocked=%v want %v", c.name, blocked, c.blocked)
		}
	}
}

// fireCounter is a thread-safe fire tally for the eval tests.
type fireCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (f *fireCounter) fire(id string) {
	f.mu.Lock()
	if f.n == nil {
		f.n = map[string]int{}
	}
	f.n[id]++
	f.mu.Unlock()
}
func (f *fireCounter) count(id string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.n[id] }

func TestEvalCronDedupePerMinute(t *testing.T) {
	fc := &fireCounter{}
	s := NewScheduler(nopLogger{}, fc.fire)
	s.act = fakeAct{idleOK: false}
	s.byID = map[string]Schedule{"c1": {ID: "c1", Kind: ScheduleCron, CronExpr: "* * * * *"}}

	now := time.Date(2026, 6, 5, 10, 30, 0, 0, time.Local)
	s.evalTick(now)
	s.evalTick(now.Add(20 * time.Second)) // same minute → deduped
	if got := fc.count("c1"); got != 1 {
		t.Fatalf("cron fired %d times in one minute, want 1", got)
	}
	s.evalTick(now.Add(1 * time.Minute)) // next minute → fires again
	if got := fc.count("c1"); got != 2 {
		t.Fatalf("cron fired %d times across two minutes, want 2", got)
	}
}

func TestEvalIdleFiresOncePerIdlePeriod(t *testing.T) {
	fc := &fireCounter{}
	s := NewScheduler(nopLogger{}, fc.fire)
	s.byID = map[string]Schedule{"i1": {ID: "i1", Kind: ScheduleIdle, IdleMinutes: 5}}
	now := time.Now()

	s.act = fakeAct{idle: 10 * time.Minute, idleOK: true}
	s.evalTick(now)
	s.evalTick(now) // still idle → no re-fire
	if got := fc.count("i1"); got != 1 {
		t.Fatalf("idle fired %d times while continuously idle, want 1", got)
	}

	s.act = fakeAct{idle: 0, idleOK: true} // user active again → re-arm
	s.evalTick(now)
	if got := fc.count("i1"); got != 1 {
		t.Fatalf("idle fired during active period, count=%d want 1", got)
	}

	s.act = fakeAct{idle: 6 * time.Minute, idleOK: true} // idle again → fires once more
	s.evalTick(now)
	if got := fc.count("i1"); got != 2 {
		t.Fatalf("idle re-fire count=%d want 2", got)
	}
}

func TestEvalIdleGatedByApp(t *testing.T) {
	fc := &fireCounter{}
	s := NewScheduler(nopLogger{}, fc.fire)
	s.byID = map[string]Schedule{
		"i1": {ID: "i1", Kind: ScheduleIdle, IdleMinutes: 5, ExcludeAppsRunning: []string{"Traktor"}},
	}
	s.act = fakeAct{idle: 10 * time.Minute, idleOK: true, procs: map[string]bool{"traktor": true}, procOK: true}
	s.evalTick(time.Now())
	if got := fc.count("i1"); got != 0 {
		t.Fatalf("idle fired %d despite excluded app running, want 0", got)
	}
}
