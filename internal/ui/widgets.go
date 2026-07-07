package ui

import (
	"net/url"
	"slices"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// mustURL parses a known-good static URL (for hyperlinks); nil on parse error.
func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

// hasStr reports whether s is in list.
func hasStr(list []string, s string) bool { return slices.Contains(list, s) }

// newEntry is a single-line Entry whose internal text-scroller is disabled (Wrapping off +
// Scroll none), so the mouse wheel passes through to the enclosing page scroll. Fyne routes
// the wheel to the innermost Scrollable under the cursor; a normal Entry's inner scroller is
// that target, which froze page scrolling whenever the cursor was over an input (#4). We
// trade horizontal wheel-scroll of overflow text (still reachable via the keyboard caret)
// for a page scroll that doesn't stall over fields.
//
// Density: Entry min height is fully theme-driven (text + 2·innerPadding → 12+8 ≈ 24px
// under the dense metrics in theme.go, vs ~35px stock), so no compact-entry fork is
// needed - the theme shrink IS the dense entry. Guarded by TestDenseMetricsShrinkWidgets.
func newEntry() *widget.Entry {
	e := widget.NewEntry()
	e.Wrapping = fyne.TextWrapOff
	e.Scroll = fyne.ScrollNone
	return e
}

// newPasswordEntry is newEntry's password variant (same scroll fix).
func newPasswordEntry() *widget.Entry {
	e := widget.NewPasswordEntry()
	e.Wrapping = fyne.TextWrapOff
	e.Scroll = fyne.ScrollNone
	return e
}
