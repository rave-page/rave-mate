package ui

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/libsync"
	"rave.page/mate/internal/musiclib"
)

// syncSchedKinds are the auto-sync trigger kinds (label↔internal). "" = off.
var syncSchedKinds = []labelVal{{"Off", ""}, {"Every N minutes", "interval"}, {"Daily", "daily"}, {"Cron", "cron"}, {"When idle", "idle"}}

// openSyncJobEditor edits (or creates) a sync job in a form dialog.
func (sv *studioView) openSyncJobEditor(job config.SyncJob, isNew bool, refresh func()) {
	win := currentWindow()
	if win == nil {
		return
	}
	if job.Rules.FieldSource == nil {
		job.Rules.FieldSource = map[string]string{}
	}

	labelEntry := newEntry()
	labelEntry.SetText(job.Label)

	scopeBody := sv.buildScopeSection(&job)
	targetsBox := sv.buildTargetsSection(&job)
	rulesBox := buildRulesSection(&job)
	schedBox := buildScheduleSection(&job)

	form := container.NewVBox(
		boldLabel("Name"), labelEntry,
		widget.NewSeparator(),
		boldLabel("What to sync"), scopeBody.widget,
		widget.NewSeparator(),
		boldLabel("Where to (targets)"), targetsBox.widget,
		widget.NewSeparator(),
		boldLabel("Merge rules - which software wins per field"), rulesBox.widget,
		widget.NewSeparator(),
		boldLabel("Auto-sync"), schedBox.widget,
	)

	title := "Edit sync job"
	if isNew {
		title = "New sync job"
	}
	d := dialog.NewCustomConfirm(title, "Save", "Cancel", container.NewVScroll(form), func(ok bool) {
		if !ok {
			return
		}
		if l := strings.TrimSpace(labelEntry.Text); l != "" {
			job.Label = l
		}
		scopeBody.commit(&job)
		targetsBox.commit(&job)
		schedBox.commit(&job)
		sv.saveSyncJob(job)
		refresh()
	}, win)
	d.Resize(fyne.NewSize(560, 660))
	d.Show()
}

// section bundles a sub-widget with a commit closure that writes its state into the job on save.
type section struct {
	widget fyne.CanvasObject
	commit func(*config.SyncJob)
}

// buildScopeSection: segmented kind + a per-kind picker (dirs/playlists/selected tracks).
func (sv *studioView) buildScopeSection(job *config.SyncJob) section {
	dirSel := map[string]bool{}
	for _, d := range job.Scope.Dirs {
		dirSel[d] = true
	}

	plByLabel := map[string]int64{}
	plSel := map[string]bool{}
	if sv.u.svc.Lib != nil {
		if rows, err := sv.u.svc.Lib.ListPlaylists(); err == nil {
			for _, r := range rows {
				lbl := playlistLabel(r)
				for plByLabel[lbl] != 0 && plByLabel[lbl] != r.ID {
					lbl += " ·"
				}
				plByLabel[lbl] = r.ID
				if containsID(job.Scope.Playlists, r.ID) {
					plSel[lbl] = true
				}
			}
		}
	}
	captured := append([]string{}, job.Scope.TrackHashes...)

	body := container.NewVBox()
	kindLabel := valToLabel(syncScopeKinds, job.Scope.Kind)
	if kindLabel == "" {
		kindLabel = "All tracks"
	}
	rebuild := func() {
		body.RemoveAll()
		switch labelToVal(syncScopeKinds, kindLabel) {
		case "dirs":
			dirs := distinctDirs(sv.tracks)
			if len(dirs) == 0 {
				body.Add(mutedLabel("No tracks loaded - import a library in the Collection tab first."))
			} else {
				body.Add(mutedLabel("Tick the folders to sync (path · track count):"))
				body.Add(vcChecklist(dirs, dirSel))
			}
		case "playlists":
			labels := sortedKeys(plByLabel)
			if len(labels) == 0 {
				body.Add(mutedLabel("No playlists found."))
			} else {
				body.Add(mutedLabel("Tick the playlists to sync:"))
				body.Add(strChecklist(labels, plSel))
			}
		case "tracks":
			info := mutedLabel(fmt.Sprintf("%d tracks captured.", len(captured)))
			capBtn := widget.NewButtonWithIcon(fmt.Sprintf("Capture collection selection (%d)", len(sv.collSel)), theme.ContentCopyIcon(), func() {
				captured = sv.collSelHashes()
				info.SetText(fmt.Sprintf("%d tracks captured.", len(captured)))
			})
			body.Add(container.NewVBox(mutedLabel("Tick rows in the Collection list, then capture them here."), capBtn, info))
		default:
			body.Add(mutedLabel("Every track in the merged library."))
		}
		body.Refresh()
	}
	seg := Segmented(labelsOf(syncScopeKinds), kindLabel, func(s string) { kindLabel = s; rebuild() })
	rebuild()

	return section{
		widget: container.NewVBox(seg, body),
		commit: func(j *config.SyncJob) {
			j.Scope = config.SyncScope{Kind: labelToVal(syncScopeKinds, kindLabel)}
			switch j.Scope.Kind {
			case "dirs":
				j.Scope.Dirs = trueKeys(dirSel)
			case "playlists":
				var ids []int64
				for lbl, on := range plSel {
					if on {
						if id, ok := plByLabel[lbl]; ok {
							ids = append(ids, id)
						}
					}
				}
				j.Scope.Playlists = ids
			case "tracks":
				j.Scope.TrackHashes = captured
			}
		},
	}
}

// buildTargetsSection: an add/remove list of (app, mode, output path) rows.
func (sv *studioView) buildTargetsSection(job *config.SyncJob) section {
	type trow struct{ app, mode, out string }
	rows := []*trow{}
	for _, t := range job.Targets {
		rows = append(rows, &trow{t.App, t.Mode, t.OutputPath})
	}
	if len(rows) == 0 {
		rows = append(rows, &trow{libsync.AppRekordbox, libsync.ModeFile, ""})
	}

	box := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		box.RemoveAll()
		for i := range rows {
			r := rows[i]
			appSel := widget.NewSelect(labelsOf(syncAppLabels), func(s string) { r.app = labelToVal(syncAppLabels, s) })
			appSel.SetSelected(valToLabel(syncAppLabels, r.app))
			modeSel := widget.NewSelect(labelsOf(syncModeLabels), func(s string) { r.mode = labelToVal(syncModeLabels, s) })
			modeSel.SetSelected(valToLabel(syncModeLabels, r.mode))
			out := newEntry()
			out.SetPlaceHolder("output path (blank = auto-detect)")
			out.SetText(r.out)
			out.OnChanged = func(s string) { r.out = s }
			rm := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { rows = dropTarget(rows, r); rebuild() })
			rm.Importance = widget.LowImportance
			box.Add(container.NewVBox(
				container.NewGridWithColumns(2, appSel, modeSel),
				container.NewBorder(nil, nil, nil, rm, out),
				widget.NewSeparator(),
			))
		}
		add := widget.NewButtonWithIcon("Add target", theme.ContentAddIcon(), func() {
			rows = append(rows, &trow{libsync.AppTraktor, libsync.ModeFile, ""})
			rebuild()
		})
		add.Importance = widget.LowImportance
		box.Add(add)
		box.Refresh()
	}
	rebuild()

	return section{
		widget: box,
		commit: func(j *config.SyncJob) {
			var ts []config.SyncTarget
			for _, r := range rows {
				if r.app != "" && r.mode != "" {
					ts = append(ts, config.SyncTarget{App: r.app, Mode: r.mode, OutputPath: strings.TrimSpace(r.out)})
				}
			}
			j.Targets = ts
		},
	}
}

// buildRulesSection: per-field source dropdowns + hotcue/tags switches (mutate job in place).
func buildRulesSection(job *config.SyncJob) section {
	box := container.NewVBox()
	for _, f := range syncRuleFields {
		field := f.val
		opts := append([]string{"Auto"}, labelsOf(syncAppLabels)...)
		curLabel := "Auto"
		if v := job.Rules.FieldSource[field]; v != "" {
			curLabel = valToLabel(syncAppLabels, v)
		}
		sel := widget.NewSelect(opts, func(s string) {
			if s == "Auto" {
				delete(job.Rules.FieldSource, field)
			} else {
				job.Rules.FieldSource[field] = labelToVal(syncAppLabels, s)
			}
		})
		sel.SetSelected(curLabel)
		box.Add(container.NewBorder(nil, nil, widget.NewLabel(f.label), nil, sel))
	}
	hotcues := widget.NewCheck("Convert hotcues → memory cues (where supported)", func(b bool) { job.Rules.HotcuesToMemory = b })
	hotcues.SetChecked(job.Rules.HotcuesToMemory)
	writeTags := widget.NewCheck("Also write metadata into the audio file tags (MP3/FLAC)", func(b bool) { job.Rules.WriteFileTags = b })
	writeTags.SetChecked(job.Rules.WriteFileTags)
	box.Add(hotcues)
	box.Add(writeTags)
	return section{widget: box, commit: func(*config.SyncJob) {}}
}

// buildScheduleSection: trigger kind + interval/cron inputs (commit writes them back).
func buildScheduleSection(job *config.SyncJob) section {
	kindLabel := valToLabel(syncSchedKinds, job.Auto.Kind)
	if kindLabel == "" {
		kindLabel = "Off"
	}
	interval := newEntry()
	interval.SetText(strconv.Itoa(job.Auto.IntervalMinutes))
	interval.SetPlaceHolder("minutes")
	cron := newEntry()
	cron.SetText(job.Auto.CronExpr)
	cron.SetPlaceHolder("min hour dom mon dow")
	seg := Segmented(labelsOf(syncSchedKinds), kindLabel, func(s string) { kindLabel = s })

	grid := container.NewGridWithColumns(2,
		container.NewBorder(nil, nil, widget.NewLabel("Interval"), nil, interval),
		container.NewBorder(nil, nil, widget.NewLabel("Cron"), nil, cron),
	)
	body := container.NewVBox(seg, grid,
		mutedLabel("Auto-sync runs only while the Library-sync feature is enabled in Settings. Live write-back is skipped while the target DJ app is open."))

	return section{
		widget: body,
		commit: func(j *config.SyncJob) {
			j.Auto.Kind = labelToVal(syncSchedKinds, kindLabel)
			j.Auto.Enabled = j.Auto.Kind != ""
			if n, err := strconv.Atoi(strings.TrimSpace(interval.Text)); err == nil {
				j.Auto.IntervalMinutes = n
			}
			j.Auto.CronExpr = strings.TrimSpace(cron.Text)
		},
	}
}

// collSelHashes returns the portable hashes of the currently-ticked collection rows.
func (sv *studioView) collSelHashes() []string {
	var out []string
	seen := map[string]bool{}
	for path, on := range sv.collSel {
		if !on {
			continue
		}
		t, ok := sv.byPath[path]
		if !ok {
			continue
		}
		h := libdb.TrackHash(t.Artist, t.Title, t.DurationSec)
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}

// vcChecklist renders an inline, scrollable checkbox list for value+count options (e.g. dirs).
// Inline (not a popover) so it positions correctly inside a modal dialog.
func vcChecklist(values []valueCount, sel map[string]bool) fyne.CanvasObject {
	box := container.NewVBox()
	for _, vc := range values {
		v := vc.value
		ch := widget.NewCheck(fmt.Sprintf("%s  ·  %d", v, vc.n), func(b bool) { sel[v] = b })
		ch.SetChecked(sel[v])
		box.Add(ch)
	}
	return scrollMin(box, 220)
}

// strChecklist renders an inline, scrollable checkbox list for plain string options (e.g. playlists).
func strChecklist(opts []string, sel map[string]bool) fyne.CanvasObject {
	box := container.NewVBox()
	for _, o := range opts {
		v := o
		ch := widget.NewCheck(v, func(b bool) { sel[v] = b })
		ch.SetChecked(sel[v])
		box.Add(ch)
	}
	return scrollMin(box, 220)
}

// scrollMin wraps content in a vertical scroll with a fixed minimum height.
func scrollMin(o fyne.CanvasObject, h float32) fyne.CanvasObject {
	sc := container.NewVScroll(o)
	sc.SetMinSize(fyne.NewSize(0, h))
	return sc
}

// distinctDirs returns the distinct parent directories of the loaded tracks, with track counts.
func distinctDirs(tracks []musiclib.Track) []valueCount {
	counts := map[string]int{}
	for _, t := range tracks {
		if t.Path == "" {
			continue
		}
		counts[filepath.Dir(t.Path)]++
	}
	out := make([]valueCount, 0, len(counts))
	for d, n := range counts {
		out = append(out, valueCount{d, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].value < out[j].value })
	return out
}

// playlistLabel renders a playlist row as "Folder/Name" (or just "Name").
func playlistLabel(r libdb.PlaylistRow) string {
	if r.Folder != "" {
		return r.Folder + "/" + r.Name
	}
	return r.Name
}

func containsID(ids []int64, id int64) bool { return slices.Contains(ids, id) }

func trueKeys(m map[string]bool) []string {
	var out []string
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dropTarget[T any](s []*T, x *T) []*T {
	out := s[:0:0]
	for _, v := range s {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}
