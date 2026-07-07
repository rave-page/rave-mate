package ui

import (
	"fmt"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/musiclib"
)

// Compound Collection filters: Genre (clustered into related-genre families) + Label, each a
// multi-select chip-popover. Combined with the existing Search + Key filters as AND, so any
// number of filters narrow the list together. See applyCollFilter.

// valueCount is one filterable value + how many tracks carry it (drives the "(N)" chip counts).
type valueCount struct {
	value string
	n     int
}

// valueInFilter reports whether v passes a multi-select filter set (empty set = pass all).
func valueInFilter(v string, sel map[string]bool) bool {
	if !anyKeySelected(sel) {
		return true
	}
	return sel[v]
}

// distinctGenreFamilies returns the related-genre families present (with counts), most-common first.
func distinctGenreFamilies(tracks []musiclib.Track) []valueCount {
	counts := map[string]int{}
	for _, t := range tracks {
		if f := musiclib.GenreFamily(t.Genre); f != "" {
			counts[f]++
		}
	}
	return sortedValueCounts(counts)
}

// distinctLabels returns the record labels present (with counts), most-common first.
func distinctLabels(tracks []musiclib.Track) []valueCount {
	counts := map[string]int{}
	for _, t := range tracks {
		if l := strings.TrimSpace(t.Label); l != "" {
			counts[l]++
		}
	}
	return sortedValueCounts(counts)
}

// sortedValueCounts orders a value→count map by count desc, then value asc.
func sortedValueCounts(counts map[string]int) []valueCount {
	out := make([]valueCount, 0, len(counts))
	for v, n := range counts {
		out = append(out, valueCount{v, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return strings.ToLower(out[i].value) < strings.ToLower(out[j].value)
	})
	return out
}

// multiSelectChip builds a "Label (N)" chip that opens a scrollable checkbox popover over values.
// Mutates sel; onChange repaints the list. Empty value set → no chip.
func (sv *studioView) multiSelectChip(label string, values []valueCount, sel map[string]bool, onChange func()) fyne.CanvasObject {
	if len(values) == 0 {
		return layoutSpacer()
	}
	btn := widget.NewButton(chipLabel(label, countSelected(sel)), nil)
	btn.Importance = widget.LowImportance
	btn.OnTapped = func() { sv.openCheckFilterPopover(btn, label, values, sel, onChange) }
	return btn
}

// openCheckFilterPopover shows a checkbox list (one per value, with track count) anchored to the
// chip. Toggling a box updates the filter set + chip count + repaints the list live.
func (sv *studioView) openCheckFilterPopover(anchor fyne.CanvasObject, label string, values []valueCount, sel map[string]bool, onChange func()) {
	win := currentWindow()
	if win == nil {
		return
	}
	chipBtn, _ := anchor.(*widget.Button)
	updateChip := func() {
		if chipBtn != nil {
			chipBtn.SetText(chipLabel(label, countSelected(sel)))
		}
	}
	list := container.NewVBox()
	for _, vc := range values {
		v := vc.value
		ch := widget.NewCheck(fmt.Sprintf("%s (%d)", v, vc.n), func(on bool) {
			if on {
				sel[v] = true
			} else {
				delete(sel, v)
			}
			updateChip()
			if onChange != nil {
				onChange()
			}
		})
		ch.SetChecked(sel[v])
		list.Add(ch)
	}
	scroll := container.NewVScroll(list)
	scroll.SetMinSize(fyne.NewSize(260, 340))
	clr := widget.NewButton("Clear", func() {
		clear(sel)
		for _, o := range list.Objects {
			if c, ok := o.(*widget.Check); ok {
				c.SetChecked(false)
			}
		}
		updateChip()
		if onChange != nil {
			onChange()
		}
	})
	clr.Importance = widget.LowImportance
	box := container.NewVBox(smallCaps(strings.ToUpper(label)), scroll, widget.NewSeparator(), container.NewHBox(clr))
	pop := widget.NewPopUp(box, win.Canvas())
	pop.Show()
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(anchor)
	pop.Move(fyne.NewPos(pos.X+anchor.MinSize().Width-pop.MinSize().Width, pos.Y+anchor.MinSize().Height+4))
}
