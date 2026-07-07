package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// kitSearchField is a compact inline filter input: a leading search glyph, a borderless
// entry, and a trailing clear (✕) that appears only while non-empty. Replaces bare
// widget.Entry filter boxes across the Library (Browse filter, Collection search).
// onChanged fires on every keystroke and on clear.
type kitSearchField struct {
	entry   *widget.Entry
	clear   *kitIconButton
	obj     fyne.CanvasObject
	changed func(string)
}

// newKitSearchField builds a search field. placeholder seeds the empty-state hint;
// onChanged receives the live query (may be nil).
func newKitSearchField(placeholder string, onChanged func(string)) *kitSearchField {
	f := &kitSearchField{changed: onChanged}
	f.entry = newEntry() // wheel-through entry (see widgets.go)
	f.entry.SetPlaceHolder(placeholder)
	icon := widget.NewIcon(theme.SearchIcon())
	f.clear = newKitIconButton(theme.CancelIcon(), "Clear filter", func() { f.SetText("") })
	f.clear.Hide()
	f.entry.OnChanged = func(s string) {
		if s == "" {
			f.clear.Hide()
		} else {
			f.clear.Show()
		}
		if f.changed != nil {
			f.changed(s)
		}
	}
	f.obj = container.NewBorder(nil, nil, container.NewPadded(icon), f.clear, f.entry)
	return f
}

// Object returns the field's canvas object for placement.
func (f *kitSearchField) Object() fyne.CanvasObject { return f.obj }

// Text returns the current query.
func (f *kitSearchField) Text() string { return f.entry.Text }

// SetText sets the query (fires onChanged + toggles the clear button).
func (f *kitSearchField) SetText(s string) { f.entry.SetText(s) }
