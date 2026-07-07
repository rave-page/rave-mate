package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"

	"rave.page/mate/internal/mediaeditor"
)

// buildEditor mounts the Editor tab: the layered visual editor plus the legacy poster
// composer as a secondary sub-tab (kept reachable). The poster's API event source is a
// stub for now (manual composition works); see editor_source.go.
func (u *UI) buildEditor() fyne.CanvasObject {
	tabs := container.NewAppTabs(
		container.NewTabItemWithIcon("Visual", theme.ColorPaletteIcon(), u.newVisualEditor()),
		container.NewTabItemWithIcon("Poster", theme.DocumentCreateIcon(), mediaeditor.New(editorSource{})),
	)
	tabs.SetTabLocation(container.TabLocationTop)
	return tabs
}
