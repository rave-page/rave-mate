package ui

import (
	"fmt"
	"image/color"
	"maps"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/musiclib"
)

// Harmonic-key features of the Studio library: key filters on the Browse /
// Collection / playlist lists and the Camelot-wheel panel on the selected track.
// Filter sets are keyed by keyLabel ("8A · Am") - the same string the chips show.

// setKeyRef updates the harmonic reference (the selected track's key) and
// re-renders every visible list so row pills recolor against it.
func (sv *studioView) setKeyRef(keyText string) {
	if k, ok := musiclib.ParseKey(keyText); ok {
		sv.keyRef = &k
	} else {
		sv.keyRef = nil
	}
	for _, l := range []*widget.List{sv.collList, sv.browseList, sv.playList, sv.plTrackList} {
		if l != nil {
			l.Refresh()
		}
	}
}

// anyKeySelected reports whether a filter set has at least one active key.
func anyKeySelected(sel map[string]bool) bool {
	for _, v := range sel {
		if v {
			return true
		}
	}
	return false
}

// keyMatches applies a key filter set to a track's raw key text.
func keyMatches(keyText string, sel map[string]bool) bool {
	if !anyKeySelected(sel) {
		return true
	}
	k, ok := musiclib.ParseKey(keyText)
	return ok && sel[keyLabel(k)]
}

// presentKeys counts tracks per normalized key.
func presentKeys(tracks []musiclib.Track) map[musiclib.Key]int {
	seen := map[musiclib.Key]int{}
	for _, t := range tracks {
		if k, ok := musiclib.ParseKey(t.Key); ok {
			seen[k]++
		}
	}
	return seen
}

// keyFilterChip builds a "Key (N)" chip that opens the harmonic wheel popover -
// a 24-cell Camelot grid colored by relation to the selected track's key
// (sv.keyRef, read at open time): saturated = in the filter, tinted = tracks
// present, dim = no tracks (not tappable). Same look as the detail-panel wheel.
func (sv *studioView) keyFilterChip(tracks []musiclib.Track, sel map[string]bool, onChange func()) fyne.CanvasObject {
	present := presentKeys(tracks)
	if len(present) == 0 {
		return layoutSpacer()
	}
	btn := widget.NewButton(chipLabel("Key", countSelected(sel)), nil)
	btn.Importance = widget.LowImportance
	btn.OnTapped = func() {
		sv.openKeyWheelPopover(btn, present, sel, onChange)
	}
	return btn
}

// countSelected counts active entries of a filter set.
func countSelected(sel map[string]bool) int {
	n := 0
	for _, v := range sel {
		if v {
			n++
		}
	}
	return n
}

// keyCellColors styles one wheel cell from (relation base, in-filter, present).
func keyCellColors(k musiclib.Key, ref *musiclib.Key, selected, present bool) (bg, fg color.Color) {
	base := color.Color(colBrandBase) // no reference → selection reads as brand
	if ref != nil {
		if c, ok := keyRelColor(musiclib.KeyRelation(*ref, k)); ok {
			base = c
		} else {
			base = colMuted // dissonant
		}
	}
	switch {
	case selected:
		return base, colBackground
	case present:
		return withAlpha(base, 0x36), colForeground
	default:
		return withAlpha(colSecondary, 0x50), colMuted
	}
}

// openKeyWheelPopover shows the multi-select harmonic wheel for a key filter.
// Tapping a cell toggles it in sel and fires onChange; "Harmonic" selects the
// four keys compatible with the reference; "Clear" empties the filter.
func (sv *studioView) openKeyWheelPopover(anchor fyne.CanvasObject, present map[musiclib.Key]int, sel map[string]bool, onChange func()) {
	win := currentWindow()
	if win == nil {
		return
	}
	chipBtn, _ := anchor.(*widget.Button)
	ref := sv.keyRef
	wheel := newKeyWheel(280, ref, sel, present, func(musiclib.Key) {
		if chipBtn != nil {
			chipBtn.SetText(chipLabel("Key", countSelected(sel)))
		}
		if onChange != nil {
			onChange()
		}
	})
	restyle := func() { // Harmonic/Clear mutate sel behind the wheel's back
		wheel.Refresh()
		if chipBtn != nil {
			chipBtn.SetText(chipLabel("Key", countSelected(sel)))
		}
	}
	box := container.NewVBox(wheel)
	actions := []fyne.CanvasObject{}
	if ref != nil {
		box.Add(harmonicLegend())
		harm := widget.NewButton("Harmonic", func() {
			clear(sel)
			maps.Copy(sel, harmonicSet(*ref))
			restyle()
			if onChange != nil {
				onChange()
			}
		})
		harm.Importance = widget.LowImportance
		actions = append(actions, harm)
	}
	clr := widget.NewButton("Clear", func() {
		clear(sel)
		restyle()
		if onChange != nil {
			onChange()
		}
	})
	clr.Importance = widget.LowImportance
	actions = append(actions, clr)
	box.Add(widget.NewSeparator())
	box.Add(container.NewHBox(actions...))

	pop := widget.NewPopUp(box, win.Canvas())
	pop.Show()
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(anchor)
	pop.Move(fyne.NewPos(pos.X+anchor.MinSize().Width-pop.MinSize().Width, pos.Y+anchor.MinSize().Height+4))
}

// layoutSpacer is a zero-size placeholder for an absent chip.
func layoutSpacer() fyne.CanvasObject { return container.NewWithoutLayout() }

// harmonicSet returns the filter set of all keys compatible with k
// (same, relative, ±1 - the colored wheel positions).
func harmonicSet(k musiclib.Key) map[string]bool {
	sel := map[string]bool{}
	for _, minor := range []bool{true, false} {
		for num := 1; num <= 12; num++ {
			o := musiclib.Key{Num: num, Minor: minor}
			if musiclib.KeyRelation(k, o) != musiclib.RelNone {
				sel[keyLabel(o)] = true
			}
		}
	}
	return sel
}

// setCollKeyFilter replaces the Collection key filter and jumps to the section.
func (sv *studioView) setCollKeyFilter(sel map[string]bool) {
	clear(sv.collKeySel)
	maps.Copy(sv.collKeySel, sel)
	sv.showSection("Collection") // rebuilds the head chip with the fresh count
	sv.applyCollFilter()
	if sv.collList != nil {
		sv.collList.Refresh()
	}
}

// harmonicPanel renders the HARMONIC KEYS block of the track detail: the track's
// key, the 24-position Camelot wheel colored by relation (dissonant = uncolored),
// and a one-tap "harmonic tracks only" Collection filter. Tapping any wheel key
// filters the Collection to exactly that key.
func (sv *studioView) harmonicPanel(t musiclib.Track) fyne.CanvasObject {
	if strings.TrimSpace(t.Key) == "" {
		return nil
	}
	k, ok := musiclib.ParseKey(t.Key)
	if !ok {
		note := mutedLabel(fmt.Sprintf("Key %q not recognized - no harmonic info.", t.Key))
		note.Wrapping = fyne.TextWrapWord
		return container.NewVBox(note)
	}
	head := container.NewHBox(
		newPill(keyLabel(k), colBrandBase, colBackground, nil),
		mutedInline("tap a key to filter the Collection"),
	)
	filterBtn := widget.NewButtonWithIcon("Show harmonic tracks", theme.SearchIcon(), func() {
		sv.setCollKeyFilter(harmonicSet(k))
	})
	filterBtn.Importance = widget.HighImportance
	wheel := newKeyWheel(280, &k, nil, presentKeys(sv.tracks), func(picked musiclib.Key) {
		sv.setCollKeyFilter(map[string]bool{keyLabel(picked): true})
	})
	return container.NewVBox(
		head,
		container.NewCenter(wheel),
		harmonicLegend(),
		filterBtn,
	)
}
