package ui

import (
	"image"
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/visualeditor"
)

// ── layers panel ───────────────────────────────────────────────────────────────

func (e *veEditor) rebuildLayers() {
	e.layersBox.Objects = e.layersBox.Objects[:0]
	// rows in reverse (top layer first, matching visual stacking)
	rows := e.layerRows(e.doc.Root.Children, 0)
	for i := len(rows) - 1; i >= 0; i-- {
		e.layersBox.Add(rows[i])
	}
	e.layersBox.Add(widget.NewSeparator())
	e.layersBox.Add(e.layerActions())
	e.layersBox.Refresh()
}

// layerRows flattens the tree to indented rows (parents before children).
func (e *veEditor) layerRows(layers []*visualeditor.Layer, depth int) []fyne.CanvasObject {
	var out []fyne.CanvasObject
	for _, l := range layers {
		out = append(out, e.layerRow(l, depth))
		if l.IsGroup() {
			out = append(out, e.layerRows(l.Children, depth+1)...)
		}
	}
	return out
}

func (e *veEditor) layerRow(l *visualeditor.Layer, depth int) fyne.CanvasObject {
	eyeIcon := theme.VisibilityIcon()
	if !l.Visible {
		eyeIcon = theme.VisibilityOffIcon()
	}
	eye := widget.NewButtonWithIcon("", eyeIcon, func() {
		e.snapshot(true)
		l.Visible = !l.Visible
		e.autosave()
		e.rebuildLayers()
		e.rerender()
	})
	eye.Importance = widget.LowImportance

	lock := widget.NewButton("L", func() {
		e.snapshot(true)
		l.Locked = !l.Locked
		e.autosave()
		e.rebuildLayers()
	})
	if l.Locked {
		lock.Importance = widget.HighImportance
	} else {
		lock.Importance = widget.LowImportance
	}

	prefix := strings.Repeat("    ", depth)
	if l.IsGroup() {
		prefix += "▸ "
	}
	name := widget.NewButton(prefix+l.Name, func() { e.selectLayer(l.ID) })
	name.Alignment = widget.ButtonAlignLeading
	if l.ID == e.selID {
		name.Importance = widget.HighImportance
	} else {
		name.Importance = widget.LowImportance
	}

	return container.NewBorder(nil, nil, container.NewHBox(eye, lock), nil, name)
}

// layerActions is the footer: reorder / group / ungroup / delete + opacity + blend for selection.
func (e *veEditor) layerActions() fyne.CanvasObject {
	up := widget.NewButtonWithIcon("", theme.MoveUpIcon(), func() { e.reorderSelected(1) })
	down := widget.NewButtonWithIcon("", theme.MoveDownIcon(), func() { e.reorderSelected(-1) })
	group := widget.NewButtonWithIcon("", theme.FolderNewIcon(), func() { e.groupSelected() })
	ungroup := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() { e.ungroupSelected() })
	del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() { e.deleteSelected() })
	for _, b := range []*widget.Button{up, down, group, ungroup, del} {
		b.Importance = widget.LowImportance
	}
	btns := container.NewHBox(
		up, down, sep(), group, ungroup, sep(), del,
		helpIcon("Arrows: reorder within the group (up = toward the front). Folder+: group the "+
			"selected layer. Folder-open: ungroup. Trash: delete."),
	)

	l, _ := e.doc.Find(e.selID)
	if l == nil {
		return container.NewVBox(btns, mutedLabel("Select a layer to edit its opacity + blend."))
	}

	op := widget.NewSlider(0, 1)
	op.Step = 0.01
	op.Value = l.Opacity
	op.OnChanged = func(v float64) {
		e.snapshot(false)
		l.Opacity = v
		e.rerender()
	}
	op.OnChangeEnded = func(float64) { e.autosave() }

	blend := widget.NewSelect(blendNames(), func(s string) {
		e.snapshot(true)
		l.Blend = visualeditor.BlendMode(s)
		e.autosave()
		e.rerender()
	})
	blend.SetSelected(string(l.Blend))

	return container.NewVBox(
		btns,
		container.NewBorder(nil, nil, widget.NewLabel("Opacity"), nil, op),
		container.NewBorder(nil, nil, widget.NewLabel("Blend"), nil, blend),
	)
}

func blendNames() []string {
	out := make([]string, len(visualeditor.BlendModes))
	for i, m := range visualeditor.BlendModes {
		out[i] = string(m)
	}
	return out
}

// ── inspector ────────────────────────────────────────────────────────────────

func (e *veEditor) rebuildInspector() {
	e.inspector.Objects = e.inspector.Objects[:0]
	l, _ := e.doc.Find(e.selID)
	if l == nil {
		e.inspector.Add(mutedLabel("No layer selected."))
		e.inspector.Refresh()
		return
	}

	nameEntry := widget.NewEntry()
	nameEntry.SetText(l.Name)
	nameEntry.OnChanged = func(s string) { l.Name = s } // list label refreshes on next rebuild
	e.inspector.Add(labeledRow("Name", nameEntry))

	// Transform (skip W/H for the doc-sized root-level groups where 0 = auto).
	e.inspector.Add(labeledRow("X", e.numEntry(l.Transform.X, func(v float64) { l.Transform.X = v })))
	e.inspector.Add(labeledRow("Y", e.numEntry(l.Transform.Y, func(v float64) { l.Transform.Y = v })))
	if !l.IsGroup() {
		e.inspector.Add(labeledRow("W", e.numEntry(l.W, func(v float64) { l.W = v })))
		e.inspector.Add(labeledRow("H", e.numEntry(l.H, func(v float64) { l.H = v })))
	}
	e.inspector.Add(labeledRow("Scale X", e.numEntry(l.Transform.ScaleX, func(v float64) { l.Transform.ScaleX = v })))
	e.inspector.Add(labeledRow("Scale Y", e.numEntry(l.Transform.ScaleY, func(v float64) { l.Transform.ScaleY = v })))
	e.inspector.Add(labeledRow("Rotation", e.numEntry(l.Transform.Rotation, func(v float64) { l.Transform.Rotation = v })))

	switch l.Kind {
	case visualeditor.KindText:
		e.inspectorText(l)
	case visualeditor.KindSolid:
		e.inspectorSolid(l)
	case visualeditor.KindGradient:
		e.inspectorGradient(l)
	case visualeditor.KindImage:
		e.inspectorImage(l)
	}
	e.inspector.Refresh()
}

func (e *veEditor) inspectorText(l *visualeditor.Layer) {
	t := l.Text
	if t == nil {
		return
	}
	content := widget.NewMultiLineEntry()
	content.SetText(t.Content)
	content.Wrapping = fyne.TextWrapWord
	content.OnChanged = func(s string) { t.Content = s; e.rerender() }
	e.inspector.Add(labeledRow("Text", content))
	e.inspector.Add(helpRow("Placeholders", "Insert {track.title} {track.artist} {track.bpm} "+
		"{track.key} {time} {date}. They fill live from what's playing."))

	fam := widget.NewSelect(e.comp.Fonts().Families(), func(s string) { e.snapshot(true); t.FontFamily = s; e.autosave(); e.rerender() })
	fam.SetSelected(t.FontFamily)
	e.inspector.Add(labeledRow("Font", fam))
	e.inspector.Add(labeledRow("Size", e.numEntry(t.FontSize, func(v float64) { t.FontSize = v })))
	e.inspector.Add(labeledRow("Letter sp.", e.numEntry(t.LetterSpacing, func(v float64) { t.LetterSpacing = v })))
	e.inspector.Add(labeledRow("Line ht.", e.numEntry(t.LineHeight, func(v float64) { t.LineHeight = v })))

	align := widget.NewSelect([]string{"left", "center", "right"}, func(s string) {
		e.snapshot(true)
		t.Align = visualeditor.Align(s)
		e.autosave()
		e.rerender()
	})
	if t.Align == "" {
		t.Align = visualeditor.AlignLeft
	}
	align.SetSelected(string(t.Align))
	e.inspector.Add(labeledRow("Align", align))
	e.inspector.Add(e.colorRow("Color", t.Color.NRGBA(), func(c color.NRGBA) { t.Color = visualeditor.FromNRGBA(c); e.rerender() }))
}

func (e *veEditor) inspectorSolid(l *visualeditor.Layer) {
	if l.Solid == nil {
		return
	}
	e.inspector.Add(e.colorRow("Fill", l.Solid.Color.NRGBA(), func(c color.NRGBA) {
		l.Solid.Color = visualeditor.FromNRGBA(c)
		e.rerender()
	}))
}

func (e *veEditor) inspectorGradient(l *visualeditor.Layer) {
	g := l.Gradient
	if g == nil || len(g.Stops) < 2 {
		return
	}
	e.inspector.Add(labeledRow("Angle", e.numEntry(g.Angle, func(v float64) { g.Angle = v })))
	first, last := 0, len(g.Stops)-1
	e.inspector.Add(e.colorRow("Start", g.Stops[first].Color.NRGBA(), func(c color.NRGBA) {
		g.Stops[first].Color = visualeditor.FromNRGBA(c)
		e.rerender()
	}))
	e.inspector.Add(e.colorRow("End", g.Stops[last].Color.NRGBA(), func(c color.NRGBA) {
		g.Stops[last].Color = visualeditor.FromNRGBA(c)
		e.rerender()
	}))
}

func (e *veEditor) inspectorImage(l *visualeditor.Layer) {
	if l.Image == nil {
		return
	}
	path := widget.NewEntry()
	path.SetText(l.Image.Path)
	path.OnChanged = func(s string) { l.Image.Path = s; e.rerender() }
	browse := widget.NewButtonWithIcon("Browse…", theme.FolderOpenIcon(), func() { e.pickImageForSelected() })
	e.inspector.Add(labeledRow("Path", container.NewBorder(nil, nil, nil, browse, path)))

	fit := widget.NewSelect([]string{"cover", "contain", "stretch"}, func(s string) {
		e.snapshot(true)
		l.Image.Fit = visualeditor.ImageFit(s)
		e.autosave()
		e.rerender()
	})
	if l.Image.Fit == "" {
		l.Image.Fit = visualeditor.FitCover
	}
	fit.SetSelected(string(l.Image.Fit))
	e.inspector.Add(labeledRow("Fit", fit))
}

// ── shared inspector widgets ───────────────────────────────────────────────────

// numEntry is a float entry that applies on every valid change (coalesced undo + rerender).
func (e *veEditor) numEntry(val float64, set func(float64)) fyne.CanvasObject {
	en := widget.NewEntry()
	en.SetText(trimFloat(val))
	en.OnChanged = func(s string) {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return
		}
		e.snapshot(false)
		set(v)
		e.rerender()
	}
	return en
}

func (e *veEditor) colorRow(label string, cur color.NRGBA, pick func(color.NRGBA)) fyne.CanvasObject {
	sw := canvas.NewRectangle(cur)
	sw.SetMinSize(fyne.NewSize(28, 20))
	sw.StrokeColor = colBorder
	sw.StrokeWidth = 1
	btn := widget.NewButton("Pick…", func() {
		win := currentWindow()
		if win == nil {
			return
		}
		p := dialog.NewColorPicker(label, "Choose a color", func(c color.Color) {
			e.snapshot(true)
			nc := toNRGBA(c)
			sw.FillColor = nc
			sw.Refresh()
			pick(nc)
			e.autosave()
		}, win)
		p.Advanced = true
		p.Show()
	})
	return labeledRow(label, container.NewBorder(nil, nil, sw, nil, btn))
}

func labeledRow(label string, w fyne.CanvasObject) fyne.CanvasObject {
	l := widget.NewLabel(label)
	l.Importance = widget.LowImportance
	return container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(84, 34), l), nil, w)
}

// helpRow is a (wrapping) muted label with a trailing "?" help icon. Uses Border, NOT HBox:
// a word-wrapping label reports a near-zero min width, so HBox starves it to ~1 char and the
// text wraps vertically (one glyph per line). Border gives the label all remaining width and
// pins the icon at its MinSize on the right.
func helpRow(label, help string) fyne.CanvasObject {
	return container.NewBorder(nil, nil, nil, helpIcon(help), mutedLabel(label))
}

// ── insert panel + save template ────────────────────────────────────────────

func (e *veEditor) showInsertPanel() {
	win := currentWindow()
	if win == nil {
		return
	}
	grid := container.NewVBox()
	scroll := container.NewVScroll(grid)
	scroll.SetMinSize(fyne.NewSize(420, 420))
	dlg := dialog.NewCustom("Insert template / component", "Close", scroll, win)

	for _, tpl := range e.store.All() {
		tpl := tpl
		thumb := canvas.NewImageFromImage(e.templateThumb(tpl))
		thumb.FillMode = canvas.ImageFillContain
		thumb.SetMinSize(fyne.NewSize(160, 90))
		kind := "component"
		if tpl.Builtin {
			kind = "preset"
		}
		insert := widget.NewButton("Insert", func() {
			e.insertTemplate(tpl)
			dlg.Hide()
		})
		insert.Importance = widget.HighImportance
		row := container.NewBorder(nil, nil, thumb, insert,
			container.NewVBox(
				widget.NewLabelWithStyle(tpl.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				mutedLabel(kind)))
		grid.Add(row)
		grid.Add(widget.NewSeparator())
	}
	dlg.Resize(fyne.NewSize(480, 520))
	dlg.Show()
}

// insertTemplate deep-copies a template's group into the document and selects it.
func (e *veEditor) insertTemplate(tpl visualeditor.Template) {
	e.snapshot(true)
	inst := tpl.Instantiate()
	e.doc.Root.Children = append(e.doc.Root.Children, inst)
	e.selID = inst.ID
	e.autosave()
	e.rebuildAll()
}

// templateThumb renders a small preview of a template on its authoring canvas.
func (e *veEditor) templateThumb(tpl visualeditor.Template) image.Image {
	w, h := tpl.W, tpl.H
	if w <= 0 || h <= 0 {
		w, h = veDefaultW, veDefaultH
	}
	d := visualeditor.NewDocument(w, h)
	d.Root.Children = append(d.Root.Children, tpl.Instantiate())
	// fresh compositor so thumbnails never disturb the editor's cache
	c := visualeditor.NewCompositor(e.comp.Fonts(), visualeditor.LoadImageFile)
	return c.Render(d, e.provider)
}

func (e *veEditor) saveAsTemplate() {
	win := currentWindow()
	if win == nil {
		return
	}
	l, _ := e.doc.Find(e.selID)
	target := l
	title := "Save selected group as a component"
	if target == nil || !target.IsGroup() {
		target = e.doc.Root
		title = "Save the whole document as a component"
	}
	nameEntry := widget.NewEntry()
	nameEntry.SetText(strings.TrimSpace(pickName(target.Name)))
	form := dialog.NewForm("Save template", "Save", "Cancel",
		[]*widget.FormItem{widget.NewFormItem("Name", nameEntry)},
		func(ok bool) {
			if !ok || strings.TrimSpace(nameEntry.Text) == "" {
				return
			}
			if err := e.store.Save(nameEntry.Text, target, e.doc.W, e.doc.H); err != nil {
				dialog.ShowError(err, win)
			}
		}, win)
	form.Resize(fyne.NewSize(360, 160))
	_ = title
	form.Show()
}

func (e *veEditor) pickImageForSelected() {
	win := currentWindow()
	if win == nil {
		return
	}
	l, _ := e.doc.Find(e.selID)
	if l == nil || l.Image == nil {
		return
	}
	showFileOpen(win, func(rc fyne.URIReadCloser, err error) {
		if err != nil || rc == nil {
			return
		}
		defer func() { _ = rc.Close() }()
		e.snapshot(true)
		l.Image.Path = rc.URI().Path()
		e.autosave()
		e.rebuildInspector()
		e.rerender()
	}, ".png", ".jpg", ".jpeg", ".gif")
}

func (e *veEditor) showCanvasSize() {
	win := currentWindow()
	if win == nil {
		return
	}
	wEntry := widget.NewEntry()
	wEntry.SetText(strconv.Itoa(e.doc.W))
	hEntry := widget.NewEntry()
	hEntry.SetText(strconv.Itoa(e.doc.H))
	form := dialog.NewForm("Canvas size", "Apply", "Cancel",
		[]*widget.FormItem{
			widget.NewFormItem("Width", wEntry),
			widget.NewFormItem("Height", hEntry),
		}, func(ok bool) {
			if !ok {
				return
			}
			w, err1 := strconv.Atoi(strings.TrimSpace(wEntry.Text))
			h, err2 := strconv.Atoi(strings.TrimSpace(hEntry.Text))
			if err1 != nil || err2 != nil || w < 1 || h < 1 {
				return
			}
			e.snapshot(true)
			e.doc.W, e.doc.H = w, h
			e.autosave()
			e.rebuildAll()
		}, win)
	form.Resize(fyne.NewSize(320, 180))
	form.Show()
}

// ── small helpers ──────────────────────────────────────────────────────────────

func trimFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func pickName(base string) string {
	if strings.TrimSpace(base) == "" {
		return "My component"
	}
	return base
}
