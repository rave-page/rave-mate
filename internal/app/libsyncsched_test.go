package app

import (
	"slices"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/libsync"
)

func TestSyncScheduleToAutomation(t *testing.T) {
	j := config.SyncJob{
		ID: "j1", Label: "Nightly", Auto: config.SyncSchedule{Kind: "interval", IntervalMinutes: 30},
		Targets: []config.SyncTarget{{App: libsync.AppTraktor, Mode: libsync.ModeWriteback}},
	}
	a := syncScheduleToAutomation(j)
	if a.ID != "j1" || string(a.Kind) != "interval" || a.IntervalMinutes != 30 {
		t.Fatalf("mapping: %+v", a)
	}
	// write-back target with no explicit gate → defaults to skip-while-Traktor-open.
	if !slices.Contains(a.ExcludeAppsRunning, "Traktor") {
		t.Errorf("expected default Traktor exclude gate, got %v", a.ExcludeAppsRunning)
	}
}

func TestDefaultExcludeAppsOnlyWriteback(t *testing.T) {
	// file + tags targets are safe any time → no gate.
	j := config.SyncJob{Targets: []config.SyncTarget{
		{App: libsync.AppRekordbox, Mode: libsync.ModeFile},
		{App: libsync.AppTraktor, Mode: libsync.ModeTags},
	}}
	if got := defaultExcludeApps(j); len(got) != 0 {
		t.Errorf("file/tags targets should not gate, got %v", got)
	}
	// a write-back rekordbox target gates on rekordbox.
	j2 := config.SyncJob{Targets: []config.SyncTarget{{App: libsync.AppRekordbox, Mode: libsync.ModeWriteback}}}
	if got := defaultExcludeApps(j2); !slices.Contains(got, "rekordbox") {
		t.Errorf("rekordbox writeback should gate on rekordbox, got %v", got)
	}
}

func TestExplicitExcludeOverridesDefault(t *testing.T) {
	j := config.SyncJob{
		Auto:    config.SyncSchedule{Kind: "cron", CronExpr: "0 4 * * *", ExcludeAppsRunning: []string{"Custom.exe"}},
		Targets: []config.SyncTarget{{App: libsync.AppTraktor, Mode: libsync.ModeWriteback}},
	}
	a := syncScheduleToAutomation(j)
	if len(a.ExcludeAppsRunning) != 1 || a.ExcludeAppsRunning[0] != "Custom.exe" {
		t.Errorf("explicit gate should win, got %v", a.ExcludeAppsRunning)
	}
}
