package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
)

// appGroupsView binds the App Groups tab to config.Features.AppGroups: a left list of groups
// (name + "n/m running") and a right detail with Launch / Edit / Delete. Groups persist in config;
// launching relaunches every not-running app so a DJ rig comes back after a crash.
type appGroupsView struct {
	u   *UI
	sel int // selected group index, -1 = none

	list    *widget.List
	detail  *fyne.Container
	actions *fyne.Container
}

func (u *UI) buildAppGroups() fyne.CanvasObject {
	if u.svc.AppGroups == nil {
		return container.NewCenter(mutedLabel("App groups unavailable."))
	}
	v := &appGroupsView{u: u, sel: -1}

	v.list = widget.NewList(
		func() int { return len(v.groups()) },
		func() fyne.CanvasObject {
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, nil, mutedInline(""), name)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			gs := v.groups()
			if id < 0 || id >= len(gs) {
				return
			}
			c := o.(*fyne.Container)
			name := c.Objects[0].(*widget.Label)
			sub := c.Objects[1].(*widget.Label)
			g := gs[id]
			name.SetText(strOrDash(g.Name))
			r, t := v.u.svc.AppGroups.RunningCount(g)
			sub.SetText(fmt.Sprintf("%d/%d up", r, t))
		},
	)
	v.list.OnSelected = func(id widget.ListItemID) {
		v.sel = id
		v.refreshDetail()
	}

	newBtn := widget.NewButtonWithIcon("New group", theme.ContentAddIcon(), func() { v.editGroup(-1) })
	newBtn.Importance = widget.HighImportance
	left := container.NewBorder(container.NewVBox(newBtn, widget.NewSeparator()), nil, nil, nil, v.list)

	v.actions = container.NewHBox()
	v.detail = container.NewVBox()
	v.refreshDetail()
	detailScroll := container.NewVScroll(container.NewVBox(
		smallCaps("SELECTED GROUP"), v.actions, widget.NewSeparator(), v.detail,
	))

	hint := mutedLabel("Relaunch a set of DJ-rig apps after a crash. Already-running apps are skipped; launched apps outlive rave-mate. Trigger from here, a keybind, or `rave-mate ctl launch-group <id>`.")
	body := container.New(newAdaptiveSplit(0.42), left, detailScroll)
	return container.NewBorder(container.NewVBox(hint, widget.NewSeparator()), nil, nil, nil, body)
}

func (v *appGroupsView) groups() []config.AppGroup {
	if v.u.svc.Cfg == nil {
		return nil
	}
	return v.u.svc.Cfg.Features.AppGroups.Groups
}

func (v *appGroupsView) selected() (config.AppGroup, int, bool) {
	gs := v.groups()
	if v.sel < 0 || v.sel >= len(gs) {
		return config.AppGroup{}, -1, false
	}
	return gs[v.sel], v.sel, true
}

func (v *appGroupsView) refresh() {
	if v.list != nil {
		v.list.Refresh()
	}
	v.refreshDetail()
}

func (v *appGroupsView) refreshDetail() {
	g, _, ok := v.selected()
	if !ok {
		v.actions.Objects = []fyne.CanvasObject{mutedInline("Select a group.")}
		v.actions.Refresh()
		v.detail.Objects = nil
		v.detail.Refresh()
		return
	}
	launch := widget.NewButtonWithIcon("Launch", theme.MediaPlayIcon(), func() { v.launch(g) })
	launch.Importance = widget.HighImportance
	edit := widget.NewButtonWithIcon("Edit", theme.DocumentCreateIcon(), func() { v.editGroup(v.sel) })
	del := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() { v.deleteGroup(g) })
	del.Importance = widget.DangerImportance
	v.actions.Objects = []fyne.CanvasObject{launch, edit, del}
	v.actions.Refresh()

	r, t := v.u.svc.AppGroups.RunningCount(g)
	box := container.NewVBox(
		boldLabel(strOrDash(g.Name)),
		mutedLabel(fmt.Sprintf("id: %s · %d/%d running", g.ID, r, t)),
	)
	if len(g.Apps) == 0 {
		box.Add(mutedInline("No apps yet - Edit to add."))
	}
	for i, a := range g.Apps {
		box.Add(container.NewGridWithColumns(2, mutedLabel(fmt.Sprintf("  %d.", i+1)), widget.NewLabel(appRefSummary(a))))
	}
	v.detail.Objects = []fyne.CanvasObject{box}
	v.detail.Refresh()
}

func (v *appGroupsView) launch(g config.AppGroup) {
	v.u.Notify("rave-mate", "Launching "+strOrDash(g.Name)+" …")
	goUI("appgroups", func() {
		started, skipped, err := v.u.svc.AppGroups.LaunchGroup(g.ID)
		if err != nil {
			v.u.Notify("rave-mate", "Launch failed: "+err.Error())
			return
		}
		v.u.Notify("rave-mate", fmt.Sprintf("%s - %d started, %d already running", strOrDash(g.Name), len(started), len(skipped)))
		fyne.Do(v.refresh)
	})
}

func (v *appGroupsView) deleteGroup(g config.AppGroup) {
	win := currentWindow()
	if win == nil {
		return
	}
	dialog.ShowConfirm("Delete group", "Delete "+strOrDash(g.Name)+"?", func(okc bool) {
		if !okc {
			return
		}
		gs := v.groups()
		for i := range gs {
			if gs[i].ID == g.ID {
				v.u.svc.Cfg.Features.AppGroups.Groups = append(gs[:i], gs[i+1:]...)
				break
			}
		}
		v.sel = -1
		v.u.saveCfg()
		v.refresh()
	}, win)
}

// editGroup opens the create/edit dialog. idx=-1 = new group.
func (v *appGroupsView) editGroup(idx int) {
	win := currentWindow()
	if win == nil {
		return
	}
	g := config.AppGroup{ID: fmt.Sprintf("grp-%d", time.Now().UnixNano())}
	if idx >= 0 && idx < len(v.groups()) {
		g = v.groups()[idx]
	}

	nameE := newEntry()
	nameE.SetText(g.Name)
	nameE.SetPlaceHolder("Group name, e.g. VR DJ rig")

	ed := &appRowsEditor{u: v.u}
	ed.build(g.Apps)

	form := container.NewVBox(
		smallCaps("NAME"), nameE,
		widget.NewSeparator(),
		smallCaps("APPS"),
		mutedInline("MATCH NAME (e.g. vrchat.exe) skips launch if already running; empty = exe filename. DELAY staggers launch."),
		ed.box,
	)
	content := container.NewVScroll(form)
	content.SetMinSize(fyne.NewSize(560, 460))

	save := func() {
		out := g
		out.Name = strings.TrimSpace(nameE.Text)
		out.Apps = ed.collect()
		gs := v.groups()
		replaced := false
		for i := range gs {
			if gs[i].ID == out.ID {
				gs[i] = out
				replaced = true
				break
			}
		}
		if !replaced {
			gs = append(gs, out)
			v.sel = len(gs) - 1
		}
		v.u.svc.Cfg.Features.AppGroups.Groups = gs
		v.u.saveCfg()
		v.refresh()
	}

	d := dialog.NewCustomConfirm("Application group", "Save", "Cancel", content, func(okc bool) {
		if okc {
			save()
		}
	}, win)
	d.Resize(fyne.NewSize(600, 520))
	d.Show()
}

// appRefSummary renders one app as a single detail line.
func appRefSummary(a config.AppRef) string {
	s := strOrDash(a.Path)
	var tags []string
	if len(a.Args) > 0 {
		tags = append(tags, fmt.Sprintf("%d arg(s)", len(a.Args)))
	}
	if a.DelayMs > 0 {
		tags = append(tags, fmt.Sprintf("+%dms", a.DelayMs))
	}
	if a.Elevated {
		tags = append(tags, "admin")
	}
	if len(tags) > 0 {
		s += " (" + strings.Join(tags, ", ") + ")"
	}
	return s
}

// ── app-rows editor ──────────────────────────────────────────────────────────

// appRowsEditor renders an editable ordered list of app rows; mirrors actionChainEditor.
type appRowsEditor struct {
	u    *UI
	box  *fyne.Container
	rows []*appRowW
}

type appRowW struct {
	root     *fyne.Container
	pathE    *widget.Entry
	argsE    *widget.Entry
	matchE   *widget.Entry
	delayE   *widget.Entry
	elevated *widget.Check
}

func (e *appRowsEditor) build(initial []config.AppRef) {
	e.box = container.NewVBox()
	// Trailing "Add app" must exist first - addRow inserts each row before it.
	add := widget.NewButtonWithIcon("Add app", theme.ContentAddIcon(), func() { e.addRow(config.AppRef{}) })
	e.box.Add(add)
	for _, a := range initial {
		e.addRow(a)
	}
}

func (e *appRowsEditor) addRow(a config.AppRef) {
	r := &appRowW{}
	r.pathE = newEntry()
	r.pathE.SetText(a.Path)
	r.pathE.SetPlaceHolder(`Executable path, e.g. C:\Program Files\VRChat\VRChat.exe`)

	r.argsE = widget.NewMultiLineEntry()
	r.argsE.SetText(strings.Join(a.Args, "\n"))
	r.argsE.SetPlaceHolder("Arguments - one per line (optional)")

	r.matchE = newEntry()
	r.matchE.SetText(a.MatchName)
	r.matchE.SetPlaceHolder("Match name (optional), e.g. vrchat.exe")

	r.delayE = newEntry()
	if a.DelayMs > 0 {
		r.delayE.SetText(strconv.Itoa(a.DelayMs))
	}
	r.delayE.SetPlaceHolder("Delay ms (optional)")

	r.elevated = widget.NewCheck("Run as admin (UAC)", nil)
	r.elevated.SetChecked(a.Elevated)

	remove := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
	remove.Importance = widget.LowImportance
	remove.OnTapped = func() { e.removeRow(r) }

	top := container.NewBorder(nil, nil, smallCaps("PATH"), remove, nil)
	r.root = container.NewVBox(
		top,
		filePickerRow(r.pathE),
		container.NewBorder(nil, nil, smallCaps("ARGS"), nil, r.argsE),
		container.NewBorder(nil, nil, smallCaps("MATCH NAME"), nil, r.matchE),
		container.NewBorder(nil, nil, smallCaps("DELAY MS"), nil, r.delayE),
		r.elevated,
		widget.NewSeparator(),
	)

	e.rows = append(e.rows, r)
	e.box.Objects = append(e.box.Objects[:len(e.box.Objects)-1], r.root, e.box.Objects[len(e.box.Objects)-1])
	e.box.Refresh()
}

func (e *appRowsEditor) removeRow(r *appRowW) {
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

func (e *appRowsEditor) collect() []config.AppRef {
	out := make([]config.AppRef, 0, len(e.rows))
	for _, r := range e.rows {
		path := strings.TrimSpace(r.pathE.Text)
		if path == "" {
			continue // drop empty rows
		}
		out = append(out, config.AppRef{
			Path:      path,
			Args:      splitLinesNonEmpty(r.argsE.Text),
			MatchName: strings.TrimSpace(r.matchE.Text),
			DelayMs:   atoiDefault(strings.TrimSpace(r.delayE.Text), 0),
			Elevated:  r.elevated.Checked,
		})
	}
	return out
}

// splitLinesNonEmpty splits on newlines into trimmed, non-empty args.
func splitLinesNonEmpty(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
