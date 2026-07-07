package ui

import (
	"testing"

	"fyne.io/fyne/v2"
	fynetest "fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func TestKitSelectableLabelDragCopy(t *testing.T) {
	fynetest.NewTempApp(t)
	l := kitSelectableLabel("copy me please")
	if !l.Selectable {
		t.Fatal("not selectable")
	}
	w := fynetest.NewTempWindow(t, l)
	w.Resize(fyne.NewSize(400, 40))
	fynetest.Drag(w.Canvas(), fyne.NewPos(120, 12), 100, 0)
	if l.SelectedText() == "" {
		t.Error("drag produced no selection")
	}
}

func TestKitCopyableSupplier(t *testing.T) {
	fynetest.NewTempApp(t)
	lbl := widget.NewLabel("short…")
	c := newKitCopyable("path", lbl, func() string { return "C:/full/path/file.flac" })
	fynetest.NewTempWindow(t, c)
	c.copyToClipboard()
	if got := fyne.CurrentApp().Clipboard().Content(); got != "C:/full/path/file.flac" {
		t.Errorf("clipboard = %q", got)
	}
	// empty supplier must not clobber the clipboard
	c.SetCopyText(func() string { return "" })
	c.copyToClipboard()
	if got := fyne.CurrentApp().Clipboard().Content(); got != "C:/full/path/file.flac" {
		t.Errorf("empty copy clobbered clipboard: %q", got)
	}
}
