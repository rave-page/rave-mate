package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
)

// navRecord pushes a visited tab title onto the back/forward history (browser-style). No-op while
// applying a back/forward navigation or when the title already matches the current entry.
func (u *UI) navRecord(title string) {
	if u.navApplying || title == "" {
		return
	}
	if u.navPos >= 0 && u.navPos < len(u.navHist) && u.navHist[u.navPos] == title {
		return
	}
	u.navHist = append(u.navHist[:u.navPos+1], title) // drop any forward entries, then append
	u.navPos = len(u.navHist) - 1
}

// navBack steps to the previously-visited tab.
func (u *UI) navBack() {
	if u.navPos > 0 {
		u.navPos--
		u.navApply(u.navHist[u.navPos])
	}
}

// navForward re-advances after a navBack.
func (u *UI) navForward() {
	if u.navPos < len(u.navHist)-1 {
		u.navPos++
		u.navApply(u.navHist[u.navPos])
	}
}

// navApply selects the tab with title without recording it as a new history entry.
func (u *UI) navApply(title string) {
	if u.tabs == nil {
		return
	}
	for _, it := range u.tabs.Items {
		if it.Text == title {
			u.navApplying = true
			u.tabs.Select(it)
			u.navApplying = false
			return
		}
	}
}

// installNavShortcuts binds Alt+Left / Alt+Right to back/forward (the cross-platform trigger;
// the mouse X1/X2 buttons need a native hook - see installMouseNav / nav_windows.go).
func (u *UI) installNavShortcuts() {
	c := u.win.Canvas()
	c.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyLeft, Modifier: fyne.KeyModifierAlt},
		func(fyne.Shortcut) { u.navBack() })
	c.AddShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyRight, Modifier: fyne.KeyModifierAlt},
		func(fyne.Shortcut) { u.navForward() })
}
