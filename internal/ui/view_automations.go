package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/remotectl"
	"rave.page/mate/internal/transcode"
)

// automationsView binds the Automations tab to the automation.Manager facade: a left list of
// automations + right detail, a Schedules section, and a Runs history list. All mutations go
// through the manager; the view re-pulls List/ListSchedules/Runs on refresh.
type automationsView struct {
	u *UI

	// target = "" drives the local engine; else a connected peer node id whose automation
	// manager we drive over remotectl (remote != nil ⇒ all reads/mutations go to the peer).
	target string
	remote *remotectl.Client

	autos     []automation.Automation
	schedules []automation.Schedule
	runs      []automation.Run

	sel int // selected automation index, -1 = none

	autoList  *widget.List
	schedList *widget.List
	runList   *widget.List

	detail  *fyne.Container
	actions *fyne.Container
}

func (u *UI) buildAutomations() fyne.CanvasObject {
	if u.svc.Automations == nil {
		return container.NewCenter(mutedLabel("Automations unavailable (persistence/worker off)."))
	}
	v := &automationsView{u: u, sel: -1}
	v.pull()

	v.autoList = widget.NewList(
		func() int { return len(v.autos) },
		func() fyne.CanvasObject { return newAutoRow() },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(v.autos) {
				fillAutoRow(o, v.autos[id])
			}
		},
	)
	v.autoList.OnSelected = func(id widget.ListItemID) {
		if id >= 0 && id < len(v.autos) {
			v.sel = id
			v.refreshDetail()
		}
	}

	v.schedList = widget.NewList(
		func() int { return len(v.schedules) },
		func() fyne.CanvasObject { l := widget.NewLabel(""); l.Truncation = fyne.TextTruncateEllipsis; return l },
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(v.schedules) {
				o.(*widget.Label).SetText(v.scheduleLabel(v.schedules[id]))
			}
		},
	)
	v.schedList.OnSelected = func(id widget.ListItemID) {
		v.schedList.UnselectAll()
		if id >= 0 && id < len(v.schedules) {
			v.editSchedule(v.schedules[id])
		}
	}

	v.runList = widget.NewList(
		func() int { return len(v.runs) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id >= 0 && id < len(v.runs) {
				r := v.runs[id]
				l := o.(*widget.Label)
				l.Importance = runImportance(r.Status)
				l.SetText(v.runLabel(r))
			}
		},
	)

	v.detail = container.NewVBox()
	v.actions = container.NewHBox()
	v.refreshDetail()

	// Live-refresh the local list when the engine changes - including automations a remote
	// controller creates/edits on this machine over remotectl. Only when showing local.
	if ch, ok := v.u.svc.Automations.(interface{ OnChange(func()) func() }); ok {
		unsub := ch.OnChange(func() {
			if v.remote == nil {
				goUI("automations", v.refresh)
			}
		})
		v.u.closers = append(v.u.closers, unsub)
	}

	// ── automations pane (list + detail) ──
	newBtn := widget.NewButtonWithIcon("New automation", theme.ContentAddIcon(), func() { v.editAutomation(nil) })
	newBtn.Importance = widget.HighImportance
	leftHead := container.NewVBox(newBtn, widget.NewSeparator())
	left := container.NewBorder(leftHead, nil, nil, nil, v.autoList)

	detailScroll := container.NewVScroll(container.NewVBox(
		smallCaps("SELECTED AUTOMATION"), v.actions, widget.NewSeparator(), v.detail,
	))
	autosSplit := container.New(newAdaptiveSplit(0.42), left, detailScroll)

	// ── schedules pane ── (hint on its own line - a wrapping label squeezed between the title
	// and button wraps into an awkward multi-line column)
	newSchedBtn := widget.NewButtonWithIcon("New schedule", theme.ContentAddIcon(), func() { v.editSchedule(automation.Schedule{}) })
	schedHead := container.NewVBox(
		container.NewBorder(nil, nil, smallCaps("SCHEDULES"), newSchedBtn, nil),
		mutedInline("tap a schedule to edit"),
		widget.NewSeparator(),
	)
	schedPane := container.NewBorder(schedHead, nil, nil, nil, v.schedList)

	// ── runs pane ──
	refreshRuns := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() { v.refresh() })
	runHead := container.NewVBox(
		container.NewBorder(nil, nil, smallCaps("RUN HISTORY"), refreshRuns, nil),
		mutedInline("newest first"),
		widget.NewSeparator(),
	)
	runPane := container.NewBorder(runHead, nil, nil, nil, v.runList)

	bottom := container.New(newAdaptiveSplit(0.45), schedPane, runPane)

	split := container.NewVSplit(autosSplit, bottom)
	split.SetOffset(0.55)

	// "Controlling: [This computer ▾]" - switch the whole manager to a paired peer.
	if switcher, ok := v.u.targetSwitcher(v.target, v.setTarget); ok {
		return container.NewBorder(container.NewVBox(switcher, widget.NewSeparator()), nil, nil, nil, split)
	}
	return split
}

// pull re-reads manager state into the view (no widget touches). For a remote target this does
// blocking RPC - always call it off the UI thread (refresh()/setTarget run it in a goroutine).
func (v *automationsView) pull() {
	v.autos = v.dsList()
	v.schedules = v.dsSchedules()
	v.runs = v.dsRuns(50)
	if v.sel >= len(v.autos) {
		v.sel = -1
	}
}

// setTarget switches the manager between local and a connected peer, then reloads off-thread.
func (v *automationsView) setTarget(nodeID string) {
	v.target = nodeID
	v.remote = v.u.remoteClient(nodeID)
	v.sel = -1
	goUI("automations", v.refresh)
}

// rctx is the per-call timeout context for remote RPC.
func rctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), remotectl.DefaultCallTimeout)
}

// remoteErr surfaces a peer RPC failure as a toast (best-effort).
func (v *automationsView) remoteErr(op string, err error) {
	v.u.Notify("rave-mate", "Remote "+op+" failed: "+err.Error())
}

// ── data source (local engine vs remote peer) ────────────────────────────────

func (v *automationsView) dsList() []automation.Automation {
	if v.remote != nil {
		ctx, cancel := rctx()
		defer cancel()
		out, err := v.remote.ListAutomations(ctx)
		if err != nil {
			v.remoteErr("list", err)
			return nil
		}
		return out
	}
	return v.u.svc.Automations.List()
}

func (v *automationsView) dsSchedules() []automation.Schedule {
	if v.remote != nil {
		ctx, cancel := rctx()
		defer cancel()
		out, err := v.remote.ListSchedules(ctx)
		if err != nil {
			return nil
		}
		return out
	}
	return v.u.svc.Automations.ListSchedules()
}

func (v *automationsView) dsRuns(limit int) []automation.Run {
	if v.remote != nil {
		ctx, cancel := rctx()
		defer cancel()
		out, err := v.remote.Runs(ctx, limit)
		if err != nil {
			return nil
		}
		return out
	}
	return v.u.svc.Automations.Runs(limit)
}

func (v *automationsView) dsSave(a automation.Automation) (automation.Automation, error) {
	if v.remote != nil {
		ctx, cancel := rctx()
		defer cancel()
		return v.remote.SaveAutomation(ctx, a)
	}
	return v.u.svc.Automations.Save(a)
}

func (v *automationsView) dsDelete(id string) error {
	if v.remote != nil {
		ctx, cancel := rctx()
		defer cancel()
		return v.remote.DeleteAutomation(ctx, id)
	}
	return v.u.svc.Automations.Delete(id)
}

// dsRunManual runs over the controlled machine's filesystem + worker pool. Remote runs get a
// generous timeout (transcode chains); on client timeout the run still completes on the peer
// and surfaces in its run history.
func (v *automationsView) dsRunManual(id, path string) (automation.Run, error) {
	if v.remote != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		return v.remote.RunManual(ctx, id, path)
	}
	return v.u.svc.Automations.RunManual(context.Background(), id, path)
}

func (v *automationsView) dsSaveSchedule(s automation.Schedule) (automation.Schedule, error) {
	if v.remote != nil {
		ctx, cancel := rctx()
		defer cancel()
		return v.remote.SaveSchedule(ctx, s)
	}
	return v.u.svc.Automations.SaveSchedule(s)
}

func (v *automationsView) dsDeleteSchedule(id string) error {
	if v.remote != nil {
		ctx, cancel := rctx()
		defer cancel()
		return v.remote.DeleteSchedule(ctx, id)
	}
	return v.u.svc.Automations.DeleteSchedule(id)
}

// refresh re-pulls + rebuilds every list + the detail (call after any mutation).
func (v *automationsView) refresh() {
	v.pull()
	fyne.Do(func() {
		if v.autoList != nil {
			v.autoList.Refresh()
		}
		if v.schedList != nil {
			v.schedList.Refresh()
		}
		if v.runList != nil {
			v.runList.Refresh()
		}
		v.refreshDetail()
	})
}

func (v *automationsView) selected() (automation.Automation, bool) {
	if v.sel < 0 || v.sel >= len(v.autos) {
		return automation.Automation{}, false
	}
	return v.autos[v.sel], true
}

// refreshDetail rebuilds the right-hand action bar + detail for the current selection.
func (v *automationsView) refreshDetail() {
	a, ok := v.selected()
	if !ok {
		v.actions.Objects = []fyne.CanvasObject{mutedInline("Select an automation.")}
		v.actions.Refresh()
		v.detail.Objects = nil
		v.detail.Refresh()
		return
	}
	run := widget.NewButtonWithIcon("Run now…", theme.MediaPlayIcon(), func() { v.runManual(a) })
	edit := widget.NewButtonWithIcon("Edit", theme.DocumentCreateIcon(), func() { v.editAutomation(&a) })
	del := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() { v.deleteAutomation(a) })
	del.Importance = widget.DangerImportance
	v.actions.Objects = []fyne.CanvasObject{run, edit, del}
	v.actions.Refresh()

	row := func(k, val string) fyne.CanvasObject {
		return container.NewGridWithColumns(2, mutedLabel(k), widget.NewLabel(strOrDash(val)))
	}
	state := "disabled"
	if a.Enabled {
		state = "enabled"
	}
	box := container.NewVBox(
		boldLabel(strOrDash(a.Label)),
		row("State", state),
		row("Watch dir", a.WatchDir),
		row("Extensions", strings.Join(a.Match.Extensions, ", ")),
		row("Actions", fmt.Sprintf("%d step(s)", len(a.Actions))),
	)
	for i, ac := range a.Actions {
		box.Add(row(fmt.Sprintf("  %d.", i+1), actionSummary(ac)))
	}
	if a.LastStatus != "" {
		box.Add(widget.NewSeparator())
		box.Add(row("Last run", joinDot(a.LastRunAt, a.LastStatus)))
		if a.LastError != "" {
			box.Add(row("Last error", a.LastError))
		}
	}
	v.detail.Objects = []fyne.CanvasObject{box}
	v.detail.Refresh()
}

// runManual prompts for a file then runs the automation manually over it. For a remote target
// the file is chosen from the controlled machine's streamed filesystem (never a native dialog
// on either box); locally it uses the native file dialog.
func (v *automationsView) runManual(a automation.Automation) {
	start := func(path string) {
		v.u.Notify("rave-mate", "Running "+strOrDash(a.Label)+" …")
		goUI("automations", func() {
			run, err := v.dsRunManual(a.ID, path)
			if err != nil {
				v.u.Notify("rave-mate", "Automation failed: "+err.Error())
				v.refresh()
				return
			}
			v.u.Notify("rave-mate", fmt.Sprintf("Automation %s - %s", strOrDash(a.Label), run.Status))
			v.refresh()
		})
	}

	if v.remote != nil {
		v.u.showRemoteFileBrowser(v.remote, "Run "+strOrDash(a.Label)+" - pick a file on the controlled computer", start)
		return
	}
	win := currentWindow()
	if win == nil {
		return
	}
	showFileOpen(win, func(rc fyne.URIReadCloser, _ error) {
		if rc == nil {
			return
		}
		path := rc.URI().Path()
		_ = rc.Close()
		start(path)
	})
}

func (v *automationsView) deleteAutomation(a automation.Automation) {
	win := currentWindow()
	if win == nil {
		return
	}
	dialog.ShowConfirm("Delete automation", "Delete "+strOrDash(a.Label)+"?", func(ok bool) {
		if !ok {
			return
		}
		if err := v.dsDelete(a.ID); err != nil {
			v.u.Notify("rave-mate", "Delete failed: "+err.Error())
			return
		}
		v.sel = -1
		v.refresh()
	}, win)
}

// ── automation editor ────────────────────────────────────────────────────────

// editAutomation opens the create/edit dialog. nil base = new automation.
func (v *automationsView) editAutomation(base *automation.Automation) {
	win := currentWindow()
	if win == nil {
		return
	}
	a := automation.Automation{Enabled: true}
	if base != nil {
		a = *base
	}

	labelE := newEntry()
	labelE.SetText(a.Label)
	labelE.SetPlaceHolder("Label")

	dirE := newEntry()
	dirE.SetText(a.WatchDir)
	dirE.SetPlaceHolder(`Watch directory, e.g. D:\Incoming`)

	enabled := widget.NewCheck("Enabled", nil)
	enabled.SetChecked(a.Enabled)

	extE := newEntry()
	extE.SetText(strings.Join(a.Match.Extensions, ", "))
	extE.SetPlaceHolder(".wav, .mp4 - empty = any")

	ed := &actionChainEditor{u: v.u}
	ed.build(a.Actions)

	form := container.NewVBox(
		mutedLabel("Watch a folder; run an action chain on each matching file. Originals are never modified."),
		smallCaps("LABEL"), labelE,
		smallCaps("WATCH DIRECTORY"), folderPickerRow(dirE),
		enabled,
		smallCaps("EXTENSIONS"), extE,
		widget.NewSeparator(),
		smallCaps("ACTION CHAIN"), ed.box,
	)
	content := container.NewVScroll(form)
	content.SetMinSize(fyne.NewSize(560, 460))

	save := func() {
		out := a
		out.Label = strings.TrimSpace(labelE.Text)
		out.WatchDir = strings.TrimSpace(dirE.Text)
		out.Enabled = enabled.Checked
		out.Match.Extensions = normalizeExts(extE.Text)
		out.Actions = ed.collect()
		goUI("automations", func() {
			if _, err := v.dsSave(out); err != nil {
				v.u.Notify("rave-mate", "Save failed: "+err.Error())
				return
			}
			v.refresh()
		})
	}

	d := dialog.NewCustomConfirm("Automation", "Save", "Cancel", content, func(ok bool) {
		if ok {
			save()
		}
	}, win)
	d.Resize(fyne.NewSize(600, 520))
	d.Show()
}

// actionChainEditor renders an editable ordered list of actions; each row = a type Select,
// a preset Select (transcode/trim-silence) and an output-dir Entry (transcode/move/copy).
type actionChainEditor struct {
	u    *UI
	box  *fyne.Container
	rows []*actionRow
}

type actionRow struct {
	root      *fyne.Container
	typeSel   *widget.Select
	presetSel *widget.Select
	outE      *widget.Entry
	outRow    fyne.CanvasObject // outE + Browse… (toggled as a unit)
	renameBox *fyne.Container   // template + match-window (rename-from-event only)
	tmplE     *widget.Entry
	bufE      *widget.Entry
	presets   []transcode.Preset
}

var actionTypeLabels = []string{"trim-silence", "transcode", "move-to", "copy-to", "rename-from-event"}

// renameTokens lists the template tokens applyTemplate substitutes (see
// automation/apievents.go). Kept in sync by hand - the engine owns the canonical set.
var renameTokens = []string{"{YYYY-MM-DD}", "{venueSlug}", "{eventSlug}", "{originalBasename}", "{ext}"}

// renameTokenBar renders each template token as a low-importance button that copies the
// token to the clipboard, so the user can paste it into the "RENAME TO" field instead of
// retyping it. Laid out as a wrap-row so the row never widens the dialog - no horizontal
// scroll, no clipped buttons.
func (e *actionChainEditor) renameTokenBar() fyne.CanvasObject {
	items := []fyne.CanvasObject{mutedInline("copy token:")}
	for _, tok := range renameTokens {
		tok := tok
		b := widget.NewButton(tok, func() {
			fyne.CurrentApp().Clipboard().SetContent(tok)
			e.u.Notify("rave-mate", "Copied "+tok)
		})
		b.Importance = widget.LowImportance
		items = append(items, b)
	}
	return WrapActions(items...)
}

func (e *actionChainEditor) build(initial []automation.Action) {
	e.box = container.NewVBox()
	// Trailing "Add action" button must exist first - addRow inserts each row before it
	// (e.box.Objects[:len-1]); building it last would underflow on the first initial row.
	add := widget.NewButtonWithIcon("Add action", theme.ContentAddIcon(), func() {
		e.addRow(automation.Action{Type: automation.ActionTranscode})
	})
	e.box.Add(add)
	for _, a := range initial {
		e.addRow(a)
	}
}

func (e *actionChainEditor) presetList() []transcode.Preset {
	var custom []transcode.Preset
	if e.u.svc.Cfg != nil {
		custom = e.u.svc.Cfg.Features.Transcode.Presets
	}
	return transcode.AllPresets(custom)
}

func (e *actionChainEditor) addRow(a automation.Action) {
	r := &actionRow{presets: e.presetList()}
	presetLabels := make([]string, 0, len(r.presets))
	for _, p := range r.presets {
		presetLabels = append(presetLabels, p.Label)
	}

	r.presetSel = widget.NewSelect(presetLabels, nil)
	for _, p := range r.presets {
		if p.ID == a.PresetID {
			r.presetSel.SetSelected(p.Label)
			break
		}
	}

	r.outE = newEntry()
	r.outE.SetText(a.OutputDir)
	r.outE.SetPlaceHolder("Output dir (empty = alongside)")
	r.outRow = folderPickerRow(r.outE)

	// rename-from-event: name the file after the booked event matched on its timestamp.
	r.tmplE = newEntry()
	r.tmplE.SetText(a.Template)
	r.tmplE.SetPlaceHolder("{YYYY-MM-DD}_{venueSlug}_{eventSlug}{ext}")
	r.bufE = newEntry()
	if a.BufferMinutes > 0 {
		r.bufE.SetText(strconv.Itoa(a.BufferMinutes))
	}
	r.bufE.SetPlaceHolder("± minutes (default 180)")
	r.renameBox = container.NewVBox(
		container.NewBorder(nil, nil, smallCaps("RENAME TO"), nil, r.tmplE),
		e.renameTokenBar(),
		container.NewBorder(nil, nil, smallCaps("MATCH WINDOW"), nil, r.bufE),
	)

	r.typeSel = widget.NewSelect(actionTypeLabels, func(s string) { applyActionRowVisibility(r, s) })
	if a.Type == "" {
		a.Type = automation.ActionTranscode
	}
	r.typeSel.SetSelected(string(a.Type))

	remove := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
	remove.Importance = widget.LowImportance
	remove.OnTapped = func() { e.removeRow(r) }

	top := container.NewBorder(nil, nil, smallCaps("TYPE"), remove, r.typeSel)
	r.root = container.NewVBox(top, r.presetSel, r.outRow, r.renameBox, widget.NewSeparator())
	applyActionRowVisibility(r, string(a.Type))

	e.rows = append(e.rows, r)
	// insert before the trailing "Add action" button
	e.box.Objects = append(e.box.Objects[:len(e.box.Objects)-1], r.root, e.box.Objects[len(e.box.Objects)-1])
	e.box.Refresh()
}

func (e *actionChainEditor) removeRow(r *actionRow) {
	for i, x := range e.rows {
		if x == r {
			e.rows = append(e.rows[:i], e.rows[i+1:]...)
			break
		}
	}
	objs := make([]fyne.CanvasObject, 0, len(e.box.Objects))
	for _, o := range e.box.Objects {
		if o != r.root {
			objs = append(objs, o)
		}
	}
	e.box.Objects = objs
	e.box.Refresh()
}

// collect builds the action chain from the editor rows.
func (e *actionChainEditor) collect() []automation.Action {
	out := make([]automation.Action, 0, len(e.rows))
	for _, r := range e.rows {
		t := automation.ActionType(r.typeSel.Selected)
		ac := automation.Action{Type: t}
		switch t {
		case automation.ActionTranscode, automation.ActionTrimSilence:
			for _, p := range r.presets {
				if p.Label == r.presetSel.Selected {
					ac.PresetID = p.ID
					break
				}
			}
			ac.OutputDir = strings.TrimSpace(r.outE.Text)
		case automation.ActionMove, automation.ActionCopy:
			ac.OutputDir = strings.TrimSpace(r.outE.Text)
		case automation.ActionRename:
			ac.Template = strings.TrimSpace(r.tmplE.Text)
			ac.BufferMinutes = atoiDefault(strings.TrimSpace(r.bufE.Text), 0)
		}
		out = append(out, ac)
	}
	return out
}

// applyActionRowVisibility shows/hides the preset / output-dir / rename fields per action type.
func applyActionRowVisibility(r *actionRow, typ string) {
	t := automation.ActionType(typ)
	wantsPreset := t == automation.ActionTranscode || t == automation.ActionTrimSilence
	// rename-from-event renames in place (proposes a path beside the source) - no output dir.
	wantsOut := wantsPreset || t == automation.ActionMove || t == automation.ActionCopy
	wantsRename := t == automation.ActionRename
	showHide(r.presetSel, wantsPreset)
	showHide(r.outRow, wantsOut)
	showHide(r.renameBox, wantsRename)
}

// showHide toggles an object's visibility from a bool.
func showHide(o fyne.CanvasObject, show bool) {
	if show {
		o.Show()
	} else {
		o.Hide()
	}
}

// ── schedule editor ──────────────────────────────────────────────────────────

func (v *automationsView) editSchedule(base automation.Schedule) {
	win := currentWindow()
	if win == nil {
		return
	}
	s := base
	if s.Kind == "" {
		s.Kind = automation.ScheduleInterval
	}
	if s.IntervalMinutes == 0 {
		s.IntervalMinutes = 60
	}

	labelE := newEntry()
	labelE.SetText(s.Label)
	labelE.SetPlaceHolder("Label")

	autoLabels := make([]string, 0, len(v.autos))
	idByLabel := map[string]string{}
	curAutoLabel := ""
	for _, a := range v.autos {
		autoLabels = append(autoLabels, a.Label)
		idByLabel[a.Label] = a.ID
		if a.ID == s.AutomationID {
			curAutoLabel = a.Label
		}
	}
	autoSel := widget.NewSelect(autoLabels, nil)
	if curAutoLabel != "" {
		autoSel.SetSelected(curAutoLabel)
	}

	enabled := widget.NewCheck("Enabled", nil)
	enabled.SetChecked(s.Enabled)

	intervalE := newEntry()
	intervalE.SetText(strconv.Itoa(s.IntervalMinutes))
	intervalE.SetPlaceHolder("Minutes")
	intervalRow := container.NewBorder(nil, nil, smallCaps("EVERY (MIN)"), nil, intervalE)

	hourSel := widget.NewSelect(numStrings(0, 23), nil)
	hourSel.SetSelected(twoDigit(s.AtHour))
	minSel := widget.NewSelect(numStrings(0, 59), nil)
	minSel.SetSelected(twoDigit(s.AtMinute))
	dailyRow := container.NewBorder(nil, nil, smallCaps("AT"), nil, container.NewHBox(hourSel, mutedLabel(":"), minSel))

	cronE := newEntry()
	cronE.SetText(s.CronExpr)
	cronE.SetPlaceHolder("min hour dom month dow")
	cronRow := container.NewVBox(
		container.NewBorder(nil, nil, smallCaps("CRON"), nil, cronE),
		mutedInline("5 fields - */15 * * * * (every 15m) · 0 9 * * 1-5 (09:00 weekdays)"),
	)

	idleE := newEntry()
	idleE.SetText(strconv.Itoa(idleDefault(s.IdleMinutes)))
	idleE.SetPlaceHolder("Minutes idle")
	idleRow := container.NewBorder(nil, nil, smallCaps("WHEN IDLE (MIN)"), nil, idleE)

	// Gates (apply to any kind) - Windows-only.
	reqIdleE := newEntry()
	if s.RequireIdleMinutes > 0 {
		reqIdleE.SetText(strconv.Itoa(s.RequireIdleMinutes))
	}
	reqIdleE.SetPlaceHolder("0 = no idle gate")
	reqAppsE := newEntry()
	reqAppsE.SetText(strings.Join(s.RequireAppsRunning, ", "))
	reqAppsE.SetPlaceHolder("only run if these run (comma list)")
	exclAppsE := newEntry()
	exclAppsE.SetText(strings.Join(s.ExcludeAppsRunning, ", "))
	exclAppsE.SetPlaceHolder("skip if any run, e.g. Traktor")

	showForKind := func(k automation.ScheduleKind) {
		intervalRow.Hide()
		dailyRow.Hide()
		cronRow.Hide()
		idleRow.Hide()
		switch k {
		case automation.ScheduleDaily:
			dailyRow.Show()
		case automation.ScheduleCron:
			cronRow.Show()
		case automation.ScheduleIdle:
			idleRow.Show()
		default:
			intervalRow.Show()
		}
	}
	kindSel := widget.NewSelect(
		[]string{string(automation.ScheduleInterval), string(automation.ScheduleDaily), string(automation.ScheduleCron), string(automation.ScheduleIdle)},
		func(k string) { showForKind(automation.ScheduleKind(k)) },
	)
	kindSel.SetSelected(string(s.Kind))

	form := container.NewVBox(
		mutedLabel("Re-run an automation on a timer / cron / when idle - optionally gated by running apps."),
		smallCaps("LABEL"), labelE,
		smallCaps("AUTOMATION"), autoSel,
		enabled,
		smallCaps("KIND"), kindSel,
		intervalRow, dailyRow, cronRow, idleRow,
		widget.NewSeparator(),
		smallCaps("CONDITIONS (WINDOWS) - OPTIONAL"),
		container.NewBorder(nil, nil, smallCaps("REQUIRE IDLE (MIN)"), nil, reqIdleE),
		container.NewBorder(nil, nil, smallCaps("REQUIRE APPS"), nil, reqAppsE),
		container.NewBorder(nil, nil, smallCaps("EXCLUDE APPS"), nil, exclAppsE),
	)

	save := func() {
		out := s
		out.Label = strings.TrimSpace(labelE.Text)
		out.Enabled = enabled.Checked
		out.AutomationID = idByLabel[autoSel.Selected]
		out.Kind = automation.ScheduleKind(kindSel.Selected)
		switch out.Kind {
		case automation.ScheduleDaily:
			out.AtHour = atoiDefault(hourSel.Selected, 0)
			out.AtMinute = atoiDefault(minSel.Selected, 0)
		case automation.ScheduleCron:
			out.CronExpr = strings.TrimSpace(cronE.Text)
		case automation.ScheduleIdle:
			out.IdleMinutes = atoiDefault(strings.TrimSpace(idleE.Text), 10)
		default:
			out.IntervalMinutes = atoiDefault(strings.TrimSpace(intervalE.Text), 60)
		}
		out.RequireIdleMinutes = atoiDefault(strings.TrimSpace(reqIdleE.Text), 0)
		out.RequireAppsRunning = splitCSV(reqAppsE.Text)
		out.ExcludeAppsRunning = splitCSV(exclAppsE.Text)
		if out.Kind == automation.ScheduleCron {
			if err := automation.ValidateCron(out.CronExpr); err != nil {
				v.u.Notify("rave-mate", "Invalid cron: "+err.Error())
				return
			}
		}
		goUI("automations", func() {
			if _, err := v.dsSaveSchedule(out); err != nil {
				v.u.Notify("rave-mate", "Schedule save failed: "+err.Error())
				return
			}
			v.refresh()
		})
	}

	body := container.NewVBox(form)
	if s.ID != "" {
		del := widget.NewButtonWithIcon("Delete schedule", theme.DeleteIcon(), func() {
			dialog.ShowConfirm("Delete schedule", "Delete "+strOrDash(s.Label)+"?", func(ok bool) {
				if !ok {
					return
				}
				if err := v.dsDeleteSchedule(s.ID); err != nil {
					v.u.Notify("rave-mate", "Delete failed: "+err.Error())
					return
				}
				v.refresh()
			}, win)
		})
		del.Importance = widget.DangerImportance
		body.Add(widget.NewSeparator())
		body.Add(del)
	}

	content := container.NewVScroll(body)
	content.SetMinSize(fyne.NewSize(460, 360))
	d := dialog.NewCustomConfirm("Schedule", "Save", "Cancel", content, func(ok bool) {
		if ok {
			save()
		}
	}, win)
	d.Resize(fyne.NewSize(500, 420))
	d.Show()
}

// ── list rows + labels ───────────────────────────────────────────────────────

func newAutoRow() fyne.CanvasObject {
	icon := widget.NewIcon(theme.MediaPlayIcon())
	name := widget.NewLabel("")
	name.Truncation = fyne.TextTruncateEllipsis
	sub := mutedInline("") // short on/off - wrapping label here would stack vertically
	return container.NewBorder(nil, nil, icon, sub, name)
}

func fillAutoRow(o fyne.CanvasObject, a automation.Automation) {
	c := o.(*fyne.Container)
	icon := c.Objects[1].(*widget.Icon)
	name := c.Objects[0].(*widget.Label)
	sub := c.Objects[2].(*widget.Label)
	name.SetText(strOrDash(a.Label))
	// Watch dir shows in the detail pane; the row keeps just a short on/off so the right cell
	// stays narrow (a long path here overflows the list row).
	if a.Enabled {
		icon.SetResource(theme.MediaPlayIcon())
		sub.SetText("on")
	} else {
		icon.SetResource(theme.MediaPauseIcon())
		sub.SetText("off")
	}
}

func (v *automationsView) scheduleLabel(s automation.Schedule) string {
	target := s.AutomationID
	for _, a := range v.autos {
		if a.ID == s.AutomationID {
			target = a.Label
			break
		}
	}
	recur := ""
	switch s.Kind {
	case automation.ScheduleDaily:
		recur = fmt.Sprintf("daily %s:%s", twoDigit(s.AtHour), twoDigit(s.AtMinute))
	case automation.ScheduleCron:
		recur = "cron " + s.CronExpr
	case automation.ScheduleIdle:
		recur = fmt.Sprintf("idle %dm", s.IdleMinutes)
	default:
		recur = fmt.Sprintf("every %dm", s.IntervalMinutes)
	}
	// Gate hint so the list shows why a schedule might be held back.
	gate := ""
	if len(s.ExcludeAppsRunning) > 0 {
		gate = " ⊘" + strings.Join(s.ExcludeAppsRunning, ",")
	}
	if len(s.RequireAppsRunning) > 0 {
		gate += " ⊙" + strings.Join(s.RequireAppsRunning, ",")
	}
	if s.RequireIdleMinutes > 0 {
		gate += fmt.Sprintf(" ⏾%dm", s.RequireIdleMinutes)
	}
	state := "off"
	if s.Enabled {
		state = "on"
	}
	return fmt.Sprintf("%s · %s · %s · %s%s", strOrDash(s.Label), recur, strOrDash(target), state, gate)
}

func (v *automationsView) runLabel(r automation.Run) string {
	when := r.FinishedAt
	if when == "" {
		when = r.StartedAt
	}
	auto := r.AutomationID
	for _, a := range v.autos {
		if a.ID == r.AutomationID {
			auto = a.Label
			break
		}
	}
	return fmt.Sprintf("%s · %s · %s · %s", when, strings.ToUpper(r.Status), strOrDash(auto), baseName(r.FilePath))
}

func runImportance(status string) widget.Importance {
	switch status {
	case "error":
		return widget.DangerImportance
	case "success":
		return widget.SuccessImportance
	case "partial":
		return widget.WarningImportance
	default:
		return widget.MediumImportance
	}
}

func actionSummary(a automation.Action) string {
	switch a.Type {
	case automation.ActionTranscode, automation.ActionTrimSilence:
		return joinDot(string(a.Type), strOrDash(a.PresetID))
	case automation.ActionMove, automation.ActionCopy:
		return joinDot(string(a.Type), strOrDash(a.OutputDir))
	case automation.ActionRename:
		tmpl := a.Template
		if tmpl == "" {
			tmpl = "default template"
		}
		return joinDot(string(a.Type), tmpl)
	default:
		return string(a.Type)
	}
}

// ── small helpers ────────────────────────────────────────────────────────────

// normalizeExts splits a comma list into lower-case, dot-prefixed extensions.
func normalizeExts(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, ".") {
			p = "." + p
		}
		out = append(out, p)
	}
	return out
}

func numStrings(lo, hi int) []string {
	out := make([]string, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, twoDigit(i))
	}
	return out
}

func twoDigit(n int) string { return fmt.Sprintf("%02d", n) }

// idleDefault returns n if positive, else a sensible 10-minute default for the idle field.
func idleDefault(n int) int {
	if n > 0 {
		return n
	}
	return 10
}

// splitCSV splits a comma list into trimmed, non-empty entries (app-name lists).
func splitCSV(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}
