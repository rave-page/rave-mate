package mediaeditor

import (
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// TestEditorInteract drives the real editor actions (render preview, export PNG)
// headlessly - these run on a button click in the GUI and weren't covered by tab-switch.
func TestEditorInteract(t *testing.T) {
	test.NewApp()
	v := &editorView{}
	view := v.build()
	w := test.NewWindow(view)
	defer w.Close()
	w.Resize(fyne.NewSize(1000, 700))

	v.titleEntry.SetText("Friday Night Techno")
	v.subtitleEntry.SetText("Main Room")
	v.djEntry.SetText("DJ Alpha\nDJ Beta")
	v.outEntry.SetText(filepath.Join(t.TempDir(), "poster.png"))

	v.renderPreview() // → buildPoster → Render
	v.exportPNG()     // → Render → Encode → dialog (indexes AllWindows()[0])
}
