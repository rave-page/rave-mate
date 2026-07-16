package webui

import (
	"slices"
	"strings"
	"testing"

	"rave.page/mate/internal/automation"
)

// seed returns a form holding a minimal valid interval schedule.
func seed(u *UI) {
	u.as.label, u.as.autoID, u.as.kind = "Nightly sweep", "auto-1", automation.ScheduleInterval
	u.as.autos = []asAuto{{id: "auto-1", label: "Sets"}}
}

func TestAsBuildValidation(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*asSt)
		wantErr string
	}{
		{"no label", func(s *asSt) { s.label = "  " }, "name"},
		{"no automation", func(s *asSt) { s.autoID = "" }, "automation"},
		{"cron: empty", func(s *asSt) { s.kind, s.cronTx = automation.ScheduleCron, "" }, "not valid"},
		{"cron: too few fields", func(s *asSt) { s.kind, s.cronTx = automation.ScheduleCron, "*/15 * *" }, "5 fields"},
		{"cron: out of range", func(s *asSt) { s.kind, s.cronTx = automation.ScheduleCron, "0 99 * * *" }, "out of range"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := &UI{}
			seed(u)
			c.mut(&u.as)
			if _, err := u.asBuild(); err == nil {
				t.Fatalf("%s: saved without error", c.name)
			} else if !strings.Contains(strings.ToLower(err.Error()), c.wantErr) {
				t.Fatalf("%s: err = %q, want it to mention %q", c.name, err, c.wantErr)
			}
		})
	}
}

// A blank interval/idle must materialize the placeholder default, never persist 0: the scheduler
// clamps a 0 interval to one minute and skips an idle schedule with no threshold entirely.
func TestAsBuildMaterializesDefaults(t *testing.T) {
	u := &UI{}
	seed(u)
	got, err := u.asBuild()
	if err != nil {
		t.Fatalf("interval build: %v", err)
	}
	if got.IntervalMinutes != asDefaultInterval {
		t.Errorf("blank interval = %d, want %d (0 would sweep every minute)", got.IntervalMinutes, asDefaultInterval)
	}

	u = &UI{}
	seed(u)
	u.as.kind = automation.ScheduleIdle
	got, err = u.asBuild()
	if err != nil {
		t.Fatalf("idle build: %v", err)
	}
	if got.IdleMinutes != asDefaultIdle {
		t.Errorf("blank idle = %d, want %d (0 never fires)", got.IdleMinutes, asDefaultIdle)
	}
}

// Each kind must persist only the fields it reads - a kind switch must not leave the previous
// kind's values behind for the scheduler to trip over.
func TestAsBuildKeepsOnlyItsKindsFields(t *testing.T) {
	u := &UI{}
	seed(u)
	u.as.kind = automation.ScheduleDaily
	u.as.atHour, u.as.atMinute = 9, 30
	u.as.cronTx, u.as.idle, u.as.interval = "*/15 * * * *", 5, 7 // stale leftovers from other kinds
	got, err := u.asBuild()
	if err != nil {
		t.Fatalf("daily build: %v", err)
	}
	if got.AtHour != 9 || got.AtMinute != 30 {
		t.Errorf("daily at = %02d:%02d, want 09:30", got.AtHour, got.AtMinute)
	}
	if got.CronExpr != "" || got.IdleMinutes != 0 || got.IntervalMinutes != 0 {
		t.Errorf("daily carried other kinds' fields: cron=%q idle=%d interval=%d",
			got.CronExpr, got.IdleMinutes, got.IntervalMinutes)
	}
}

// LastFiredAt is the scheduler's own bookkeeping: an edit through the form must carry it through,
// never reset it.
func TestAsBuildPreservesLastFired(t *testing.T) {
	u := &UI{}
	seed(u)
	u.as.id, u.as.lastFired = "sched-1", "2026-07-15T22:00:00Z"
	got, err := u.asBuild()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.LastFiredAt != "2026-07-15T22:00:00Z" {
		t.Errorf("LastFiredAt = %q, want it carried through the edit", got.LastFiredAt)
	}
}

// The gate lists must reach the engine as fresh, trimmed slices - load() only ever joins the
// service cache's aliased slices for display.
func TestAsBuildGates(t *testing.T) {
	u := &UI{}
	seed(u)
	u.as.reqIdle = 5
	u.as.reqApps, u.as.exclApps = " Traktor , rekordbox ", "obs64,,  Traktor.exe "
	got, err := u.asBuild()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.RequireIdleMinutes != 5 {
		t.Errorf("RequireIdleMinutes = %d, want 5", got.RequireIdleMinutes)
	}
	if want := []string{"Traktor", "rekordbox"}; !slices.Equal(got.RequireAppsRunning, want) {
		t.Errorf("RequireAppsRunning = %v, want %v", got.RequireAppsRunning, want)
	}
	if want := []string{"obs64", "Traktor.exe"}; !slices.Equal(got.ExcludeAppsRunning, want) {
		t.Errorf("ExcludeAppsRunning = %v, want %v (blank entries dropped)", got.ExcludeAppsRunning, want)
	}
	if got := asSplitCSV("  ,  "); got != nil {
		t.Errorf("blank list = %v, want nil (= no gate)", got)
	}
}

// A valid cron must round-trip through the form untouched, and the live verdict must agree with
// the build - both call the engine's ValidateCron, so they can never drift apart.
func TestAsCronRoundTripAndVerdict(t *testing.T) {
	u := &UI{}
	seed(u)
	u.as.kind, u.as.cronTx = automation.ScheduleCron, " 0 9 * * 1-5 "
	got, err := u.asBuild()
	if err != nil {
		t.Fatalf("cron build: %v", err)
	}
	if got.CronExpr != "0 9 * * 1-5" {
		t.Errorf("CronExpr = %q, want it trimmed", got.CronExpr)
	}
	if v := asCronVerdict("0 9 * * 1-5"); !strings.Contains(v, "hint--ok") {
		t.Errorf("verdict for a valid expr = %q, want the ok tone", v)
	}
	if v := asCronVerdict("0 99 * * *"); !strings.Contains(v, "hint--bad") {
		t.Errorf("verdict for a bad expr = %q, want the bad tone", v)
	}
	if v := asCronVerdict("   "); !strings.Contains(v, "hint--warn") {
		t.Errorf("verdict for a blank expr = %q, want the warn tone", v)
	}
}

// autoChainDeletes drives the run-now acknowledgement gate + the schedule warning, so it must see
// a delete anywhere - not just as the last step (chains saved before ValidateActions existed can
// carry one mid-chain).
func TestAutoChainDeletes(t *testing.T) {
	del := automation.Action{Type: automation.ActionDelete}
	move := automation.Action{Type: automation.ActionMove, OutputDir: `C:\out`}
	cases := []struct {
		name string
		acts []automation.Action
		want bool
	}{
		{"empty", nil, false},
		{"no delete", []automation.Action{move}, false},
		{"terminal delete", []automation.Action{move, del}, true},
		{"mid-chain delete (legacy)", []automation.Action{del, move}, true},
	}
	for _, c := range cases {
		if got := autoChainDeletes(c.acts); got != c.want {
			t.Errorf("%s: autoChainDeletes = %v, want %v", c.name, got, c.want)
		}
	}
}

// The run-now button is gated on a file and, for an erasing chain, on the acknowledgement.
// Cases mutate an arSt in place - arSt carries a sync.Mutex, so a table of values would copy it.
func TestArRunnableGating(t *testing.T) {
	del := []automation.Action{{Type: automation.ActionDelete}}
	safe := []automation.Action{{Type: automation.ActionTranscode, PresetID: "remux"}}
	cases := []struct {
		name string
		mut  func(*arSt)
		want bool
	}{
		{"no file", func(s *arSt) { s.acts = safe }, false},
		{"blank file", func(s *arSt) { s.acts, s.file = safe, "   " }, false},
		{"file, safe chain", func(s *arSt) { s.acts, s.file = safe, `C:\a.wav` }, true},
		{"erasing chain, unacknowledged", func(s *arSt) { s.acts, s.file = del, `C:\a.wav` }, false},
		{"erasing chain, acknowledged", func(s *arSt) { s.acts, s.file, s.ack = del, `C:\a.wav`, true }, true},
		{"own run in flight", func(s *arSt) {
			s.acts, s.file, s.autoID, s.runningID = safe, `C:\a.wav`, "a1", "a1"
		}, false},
		{"another automation's run in flight", func(s *arSt) {
			s.acts, s.file, s.autoID, s.runningID = safe, `C:\a.wav`, "a1", "a2"
		}, true},
	}
	for _, c := range cases {
		var s arSt
		c.mut(&s)
		if got := s.runnable(); got != c.want {
			t.Errorf("%s: runnable = %v, want %v", c.name, got, c.want)
		}
	}
}

// The card summary must describe what the scheduler actually arms, including the defaults a blank
// field materializes.
func TestAutoTriggerSummary(t *testing.T) {
	cases := []struct {
		name string
		s    automation.Schedule
		want string
	}{
		{"interval", automation.Schedule{Kind: automation.ScheduleInterval, IntervalMinutes: 30}, "30"},
		{"interval blank → default", automation.Schedule{Kind: automation.ScheduleInterval}, "60"},
		{"daily", automation.Schedule{Kind: automation.ScheduleDaily, AtHour: 9, AtMinute: 5}, "09:05"},
		{"cron", automation.Schedule{Kind: automation.ScheduleCron, CronExpr: "*/15 * * * *"}, "*/15 * * * *"},
		{"idle blank → default", automation.Schedule{Kind: automation.ScheduleIdle}, "10"},
	}
	for _, c := range cases {
		if got := autoTriggerSummary(c.s); !strings.Contains(got, c.want) {
			t.Errorf("%s: summary = %q, want it to mention %q", c.name, got, c.want)
		}
	}
}
