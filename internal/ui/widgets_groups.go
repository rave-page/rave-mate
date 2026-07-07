package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Button-group helpers - replace the old HBox / HScroll rows of buttons that overflowed
// the window. Four primitives cover every case:
//   - Segmented       → mutually-exclusive switch (mode / filter / sort), 2-5 options
//   - SplitButton     → 6+ options, current selection IS the primary action
//   - ChipMultiSelect → filter chips that can grow; collapsed chip + popover
//   - WrapActions     → independent action buttons; wrap to multiple rows, no scroll
// All four are sized to their parent - none forces horizontal overflow.

// Segmented is a horizontal RadioGroup that wraps to multiple rows on narrow windows.
// Replaces the old "container.NewHBox of mutually-exclusive buttons". The selected
// option is highlighted; clicking another option fires onChanged.
func Segmented(opts []string, selected string, onChanged func(string)) *widget.RadioGroup {
	r := widget.NewRadioGroup(opts, onChanged)
	r.Horizontal = true
	r.Required = true
	if selected != "" {
		r.SetSelected(selected)
	}
	return r
}

// SegmentedButtons builds a wrap-row of mutually-exclusive Buttons styled to
// look like a segmented control (High importance on the active option, Low
// otherwise). Use when per-option disable state is required - Fyne's
// widget.RadioGroup has no per-option disable. Items with `disabled[label]`
// are still rendered but greyed out and non-clickable.
func SegmentedButtons(labels []string, disabled, selected map[string]bool, onPick func(string)) *fyne.Container {
	objs := make([]fyne.CanvasObject, 0, len(labels))
	for _, l := range labels {
		btn := widget.NewButton(l, func() {
			if disabled[l] {
				return
			}
			if onPick != nil {
				onPick(l)
			}
		})
		btn.Importance = lowOrHigh(selected[l])
		if disabled[l] {
			btn.Disable()
		}
		objs = append(objs, btn)
	}
	return WrapActions(objs...)
}

// SplitButton: left half = primary action showing the current selection, right
// half = a small chevron dropdown to pick a different option. Use when there are
// 6+ options, or when the current selection is itself the most useful action
// (e.g. "transcode with preset X" - picking a different preset is the secondary
// affordance). The button's importance flips to High so the current selection
// reads as a primary CTA.
func SplitButton(currentLabel string, options []string, onPick func(string)) fyne.CanvasObject {
	if currentLabel == "" {
		currentLabel = "Select…"
	}
	main := widget.NewButton(currentLabel, nil)
	main.Importance = widget.HighImportance
	main.OnTapped = func() {
		if onPick != nil {
			onPick(currentLabel)
		}
	}
	chev := widget.NewButtonWithIcon("", theme.MenuDropDownIcon(), func() {
		popUpMenuForButton(main, options, onPick)
	})
	chev.Importance = widget.LowImportance
	row := container.NewHBox(main, chev)
	return row
}

// popUpMenuForButton opens an anchored pop-up menu over the given anchor widget
// with one entry per option. Selecting an option fires onPick and dismisses.
// This is the Fyne v2.7+ idiomatic way to expose a "split button" dropdown
// (widget.PopUpMenu + widget.NewPopUp); we keep it tiny so the helpers above
// don't have to know the popup machinery.
func popUpMenuForButton(anchor fyne.CanvasObject, options []string, onPick func(string)) {
	win := currentWindow()
	if win == nil {
		return
	}
	canvas := win.Canvas()
	items := make([]*fyne.MenuItem, 0, len(options))
	for _, opt := range options {
		items = append(items, fyne.NewMenuItem(opt, func() {
			if onPick != nil {
				onPick(opt)
			}
		}))
	}
	menu := fyne.NewMenu("", items...)
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(anchor)
	btnSize := anchor.MinSize()
	pop := widget.NewPopUpMenu(menu, canvas)
	pop.ShowAtPosition(fyne.NewPos(pos.X, pos.Y+btnSize.Height))
}

// ChipMultiSelect: collapsed view is a single button "Label (N selected)" that
// opens a popover with one Check per option. Use for filter chips that can grow
// (pinned folders, tag filter, kind filter, etc.). onChange fires with the
// updated selection map whenever a checkbox is toggled.
func ChipMultiSelect(label string, options []string, selected map[string]bool, onChange func(map[string]bool)) fyne.CanvasObject {
	if selected == nil {
		selected = map[string]bool{}
	}
	count := 0
	for _, v := range selected {
		if v {
			count++
		}
	}
	btn := widget.NewButton(chipLabel(label, count), nil)
	btn.Importance = widget.LowImportance
	btn.OnTapped = func() {
		openChipPopover(btn, label, options, selected, onChange, btn)
	}
	return btn
}

// chipLabel formats the collapsed chip text: "Label", "Label (1)", "Label (3)".
func chipLabel(label string, n int) string {
	if n == 0 {
		return label
	}
	return label + " (" + itoa(n) + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// openChipPopover builds a popover with one Check per option and a "Clear" link.
// Toggling any Check mutates the shared `selected` map and fires onChange; the
// chip label re-renders via the onChange callback (caller's responsibility).
// Long option lists (imported genre taxonomies run to hundreds) get a filter
// entry + a height-capped scroll so the popover never overflows the canvas.
func openChipPopover(anchor fyne.CanvasObject, label string, options []string, selected map[string]bool, onChange func(map[string]bool), chipBtn *widget.Button) {
	win := currentWindow()
	if win == nil {
		return
	}
	canvas := win.Canvas()
	refreshChip := func() {
		count := 0
		for _, v := range selected {
			if v {
				count++
			}
		}
		if chipBtn != nil {
			chipBtn.SetText(chipLabel(label, count))
		}
	}
	list := container.NewVBox()
	build := func(filter string) {
		q := strings.ToLower(strings.TrimSpace(filter))
		list.Objects = list.Objects[:0]
		for _, opt := range options {
			if q != "" && !strings.Contains(strings.ToLower(opt), q) {
				continue
			}
			chk := widget.NewCheck(opt, nil)
			chk.SetChecked(selected[opt])
			chk.OnChanged = func(on bool) {
				selected[opt] = on
				if onChange != nil {
					onChange(selected)
				}
				refreshChip()
			}
			list.Add(chk)
		}
		list.Refresh()
	}
	build("")
	box := container.NewVBox()
	if len(options) > 12 {
		f := newEntry()
		f.SetPlaceHolder("Filter options…")
		f.OnChanged = build
		box.Add(f)
	}
	maxH := canvas.Size().Height * 0.5
	if maxH > 480 {
		maxH = 480
	}
	if need := float32(len(options))*38 + 8; need < maxH {
		maxH = need
	}
	scroll := container.NewVScroll(list)
	scroll.SetMinSize(fyne.NewSize(280, maxH))
	box.Add(scroll)
	if len(options) > 1 {
		box.Add(widget.NewSeparator())
		clr := widget.NewButton("Clear", func() {
			for k := range selected {
				selected[k] = false
			}
			if onChange != nil {
				onChange(selected)
			}
			if chipBtn != nil {
				chipBtn.SetText(chipLabel(label, 0))
			}
		})
		clr.Importance = widget.LowImportance
		box.Add(clr)
	}
	pop := widget.NewPopUp(box, canvas)
	pop.Show()
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(anchor)
	btnSize := anchor.MinSize()
	pop.Move(fyne.NewPos(pos.X, pos.Y+btnSize.Height+4))
}

// WrapActions lays out independent action buttons with NewRowWrapLayout so they
// flow to multiple rows on narrow windows instead of clipping. Replaces
// container.NewHBox(button, button, …) toolbars that overflowed. Returns
// *fyne.Container so callers can mutate (Add, Hide/Show, Remove) after creation.
func WrapActions(buttons ...fyne.CanvasObject) *fyne.Container {
	objs := make([]fyne.CanvasObject, 0, len(buttons))
	for _, b := range buttons {
		if b != nil {
			objs = append(objs, b)
		}
	}
	return container.New(layout.NewRowWrapLayout(), objs...)
}
