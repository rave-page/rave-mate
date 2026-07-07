package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/libsync"
)

// Cross-DJ-software library sync UI (studio Library tab → "Sync…"). Defines sync jobs (scope +
// targets + field-merge rules + optional auto schedule) over the merged music.db and runs them.

// label↔internal mappings for the editor dropdowns.
var (
	syncAppLabels  = []labelVal{{"Traktor", libsync.AppTraktor}, {"Rekordbox", libsync.AppRekordbox}, {"VirtualDJ", libsync.AppVirtualDJ}}
	syncModeLabels = []labelVal{{"Importable file", libsync.ModeFile}, {"Live write-back (app closed)", libsync.ModeWriteback}, {"File tags", libsync.ModeTags}}
	syncScopeKinds = []labelVal{{"All tracks", "all"}, {"Directories", "dirs"}, {"Playlists", "playlists"}, {"Selected tracks", "tracks"}}
	syncRuleFields = []labelVal{{"Beatgrid", "beatgrid"}, {"Cues", "cues"}, {"Rating", "rating"}, {"Key", "key"}, {"Genre", "genre"}, {"BPM", "bpm"}}
)

type labelVal struct{ label, val string }

func labelsOf(lv []labelVal) []string {
	out := make([]string, len(lv))
	for i, x := range lv {
		out[i] = x.label
	}
	return out
}
func valToLabel(lv []labelVal, val string) string {
	for _, x := range lv {
		if x.val == val {
			return x.label
		}
	}
	return ""
}
func labelToVal(lv []labelVal, label string) string {
	for _, x := range lv {
		if x.label == label {
			return x.val
		}
	}
	return ""
}

// doSyncMenu opens the sync-job list dialog.
func (sv *studioView) doSyncMenu() {
	win := currentWindow()
	if win == nil {
		return
	}
	body := container.NewVBox()
	var refresh func()
	refresh = func() {
		body.RemoveAll()
		jobs := sv.syncJobs()
		if len(jobs) == 0 {
			body.Add(mutedLabel("No sync jobs yet. A job pushes your merged library (best metadata, cues, beatgrid per your rules) out to Traktor / Rekordbox / VirtualDJ."))
		}
		for i := range jobs {
			body.Add(sv.syncJobCard(jobs[i].ID, refresh))
		}
		newBtn := widget.NewButtonWithIcon("New sync job", theme.ContentAddIcon(), func() {
			sv.openSyncJobEditor(sv.defaultSyncJob(), true, refresh)
		})
		newBtn.Importance = widget.HighImportance
		body.Add(container.NewPadded(newBtn))
		body.Refresh()
	}
	refresh()

	autoToggle := newToggle(&sv.u.svc.Cfg.Features.LibrarySync.Enabled, func(bool) {
		sv.u.saveCfg()
		sv.reconcileLibSync()
	})
	header := container.NewVBox(
		container.NewBorder(nil, nil, boldLabel("Scheduled auto-sync"), autoToggle, nil),
		mutedLabel("Master switch for every job's schedule. Off = jobs run only when you press Run."),
		widget.NewSeparator(),
	)
	d := dialog.NewCustom("Library sync", "Close", container.NewBorder(header, nil, nil, nil, container.NewVScroll(body)), win)
	d.Resize(fyne.NewSize(580, 620))
	d.Show()
}

// reconcileLibSync re-arms the auto-sync scheduler after a job change (nil-safe).
func (sv *studioView) reconcileLibSync() {
	if sv.u.svc.ReconcileLibSync != nil {
		sv.u.svc.ReconcileLibSync()
	}
}

// syncJobs returns the configured jobs (nil-safe).
func (sv *studioView) syncJobs() []config.SyncJob {
	if sv.u.svc.Cfg == nil {
		return nil
	}
	return sv.u.svc.Cfg.Features.LibrarySync.Jobs
}

// findSyncJob returns the job + its slice index by ID.
func (sv *studioView) findSyncJob(id string) (config.SyncJob, int, bool) {
	for i, j := range sv.syncJobs() {
		if j.ID == id {
			return j, i, true
		}
	}
	return config.SyncJob{}, -1, false
}

// syncJobCard renders one job with run / dry-run / edit / delete.
func (sv *studioView) syncJobCard(id string, refresh func()) fyne.CanvasObject {
	job, idx, ok := sv.findSyncJob(id)
	if !ok {
		return layoutSpacer()
	}
	enable := newToggle(&sv.u.svc.Cfg.Features.LibrarySync.Jobs[idx].Enabled, func(bool) { sv.u.saveCfg(); sv.reconcileLibSync() })

	run := widget.NewButtonWithIcon("Run", theme.MediaPlayIcon(), func() { sv.runSyncJob(id, false) })
	run.Importance = widget.HighImportance
	dry := widget.NewButtonWithIcon("Dry-run", theme.SearchIcon(), func() { sv.runSyncJob(id, true) })
	edit := widget.NewButtonWithIcon("Edit", theme.DocumentCreateIcon(), func() {
		j, _, _ := sv.findSyncJob(id)
		sv.openSyncJobEditor(j, false, refresh)
	})
	del := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() {
		dialog.ShowConfirm("Delete sync job", "Delete \""+job.Label+"\"?", func(yes bool) {
			if yes {
				sv.deleteSyncJob(id)
				refresh()
			}
		}, currentWindow())
	})

	desc := syncJobSummary(job)
	if job.LastSummary != "" {
		desc += "\nLast: " + job.LastSummary
	}
	return featureCard(jobTitle(job), desc, enable, nil, WrapActions(run, dry, edit, del))
}

func jobTitle(j config.SyncJob) string {
	if j.Label != "" {
		return j.Label
	}
	return "Untitled sync"
}

// syncJobSummary describes a job's scope + targets in one line.
func syncJobSummary(j config.SyncJob) string {
	scope := valToLabel(syncScopeKinds, j.Scope.Kind)
	if scope == "" {
		scope = "All tracks"
	}
	switch j.Scope.Kind {
	case "dirs":
		scope = fmt.Sprintf("%d directories", len(j.Scope.Dirs))
	case "playlists":
		scope = fmt.Sprintf("%d playlists", len(j.Scope.Playlists))
	case "tracks":
		scope = fmt.Sprintf("%d selected tracks", len(j.Scope.TrackHashes))
	}
	var tg []string
	for _, t := range j.Targets {
		tg = append(tg, valToLabel(syncAppLabels, t.App)+"·"+valToLabel(syncModeLabels, t.Mode))
	}
	if len(tg) == 0 {
		return scope + " → (no targets)"
	}
	return scope + " → " + strings.Join(tg, ", ")
}

// defaultSyncJob seeds a new job.
func (sv *studioView) defaultSyncJob() config.SyncJob {
	return config.SyncJob{
		ID:      fmt.Sprintf("job-%d", time.Now().UnixNano()),
		Label:   "New sync",
		Scope:   config.SyncScope{Kind: "all"},
		Targets: []config.SyncTarget{{App: libsync.AppRekordbox, Mode: libsync.ModeFile}},
		Rules:   config.SyncRules{FieldSource: map[string]string{}},
	}
}

// runSyncJob runs (or dry-runs) a job off the UI thread, persisting the result.
func (sv *studioView) runSyncJob(id string, dry bool) {
	job, _, ok := sv.findSyncJob(id)
	if !ok {
		return
	}
	if sv.u.svc.Lib == nil {
		sv.u.Notify("rave-mate", "Library DB unavailable.")
		return
	}
	if sv.syncBusy {
		sv.u.Notify("rave-mate", "A sync is already running.")
		return
	}
	sv.syncBusy = true
	verb := "Syncing"
	if dry {
		verb = "Previewing"
	}
	sv.u.Notify("rave-mate", verb+" \""+job.Label+"\"…")
	debuglog.Go(sv.u.svc.Log, "dj-sync-ui", func() {
		res, err := libsync.Run(sv.u.svc.Lib, job, dry)
		fyne.Do(func() {
			sv.syncBusy = false
			if err != nil {
				sv.u.Notify("rave-mate", "Sync failed: "+err.Error())
				return
			}
			if !dry {
				if _, idx, ok := sv.findSyncJob(id); ok {
					sv.u.svc.Cfg.Features.LibrarySync.Jobs[idx].LastRunAt = time.Now().UTC().Format(time.RFC3339)
					sv.u.svc.Cfg.Features.LibrarySync.Jobs[idx].LastSummary = res.Summary()
					sv.u.saveCfg()
				}
			}
			sv.u.Notify("rave-mate", res.Summary())
		})
	})
}

// deleteSyncJob removes a job by ID + persists.
func (sv *studioView) deleteSyncJob(id string) {
	jobs := sv.syncJobs()
	out := jobs[:0:0]
	for _, j := range jobs {
		if j.ID != id {
			out = append(out, j)
		}
	}
	sv.u.svc.Cfg.Features.LibrarySync.Jobs = out
	sv.u.saveCfg()
	sv.reconcileLibSync()
}

// saveSyncJob upserts a job + persists.
func (sv *studioView) saveSyncJob(job config.SyncJob) {
	jobs := sv.syncJobs()
	for i := range jobs {
		if jobs[i].ID == job.ID {
			jobs[i] = job
			sv.u.svc.Cfg.Features.LibrarySync.Jobs = jobs
			sv.u.saveCfg()
			sv.reconcileLibSync()
			return
		}
	}
	sv.u.svc.Cfg.Features.LibrarySync.Jobs = append(jobs, job)
	sv.u.saveCfg()
	sv.reconcileLibSync()
}
