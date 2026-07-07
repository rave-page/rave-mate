package ui

import (
	"context"
	"errors"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// fileDialogSize returns a generous size for a file/folder browser: ~85% of the window, floored so
// it's usable even on a small window. Fyne dialogs are canvas overlays (no OS drag-resize), so we
// open them large rather than at Fyne's cramped default.
func fileDialogSize(win fyne.Window) fyne.Size {
	s := win.Canvas().Size()
	w, h := s.Width*0.85, s.Height*0.85
	if w < 720 {
		w = 720
	}
	if h < 520 {
		h = 520
	}
	return fyne.NewSize(w, h)
}

// showFolderOpen opens a large folder browser.
func showFolderOpen(win fyne.Window, cb func(fyne.ListableURI, error)) {
	d := dialog.NewFolderOpen(cb, win)
	d.Resize(fileDialogSize(win))
	d.Show()
}

// showFileOpen opens a large file browser (optional extension filter).
func showFileOpen(win fyne.Window, cb func(fyne.URIReadCloser, error), exts ...string) {
	d := dialog.NewFileOpen(cb, win)
	if len(exts) > 0 {
		d.SetFilter(storage.NewExtensionFileFilter(exts))
	}
	d.Resize(fileDialogSize(win))
	d.Show()
}

// folderPickerRow wraps a directory Entry with a Browse… button that opens the app's native
// folder picker and writes the chosen path back into the entry. SetText fires the entry's
// OnChanged, so the existing persist/validate wiring runs exactly as if the path were typed.
// Use this for every directory field instead of a bare Entry.
func folderPickerRow(e *widget.Entry) fyne.CanvasObject {
	browse := widget.NewButtonWithIcon("Browse…", theme.FolderOpenIcon(), func() {
		win := currentWindow()
		if win == nil {
			return
		}
		showFolderOpen(win, func(lu fyne.ListableURI, err error) {
			if err != nil || lu == nil {
				return
			}
			e.SetText(lu.Path())
		})
	})
	return container.NewVBox(e, browse)
}

// filePickerRow is folderPickerRow for a single file. exts (e.g. ".nml") filter the dialog;
// pass none to allow any file.
func filePickerRow(e *widget.Entry, exts ...string) fyne.CanvasObject {
	browse := widget.NewButtonWithIcon("Browse…", theme.FileIcon(), func() {
		win := currentWindow()
		if win == nil {
			return
		}
		showFileOpen(win, func(rc fyne.URIReadCloser, err error) {
			if err != nil || rc == nil {
				return
			}
			p := rc.URI().Path()
			_ = rc.Close()
			e.SetText(p)
		}, exts...)
	})
	return container.NewVBox(e, browse)
}

// Native file dialogs for the studio channel (studio.Picker). The web Local Studio client
// calls these to pick paths on THIS desktop; we surface a Fyne dialog on the main thread
// and bridge its async callback back to the (goroutine) caller. Cancel → "" (no error).

type pathResult struct {
	path string
	err  error
}

// runPathDialog shows a dialog on the UI thread and waits for the user (or ctx timeout).
func (u *UI) runPathDialog(ctx context.Context, show func(win fyne.Window, done func(string, error))) (string, error) {
	win := currentWindow()
	if win == nil {
		return "", errors.New("no desktop window")
	}
	ch := make(chan pathResult, 1)
	deliver := func(p string, e error) {
		select {
		case ch <- pathResult{p, e}:
		default:
		}
	}
	fyne.Do(func() {
		win.Show() // un-hide if closed-to-tray so the dialog has a visible parent
		win.RequestFocus()
		show(win, deliver)
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.path, r.err
	}
}

// PickDirectory implements studio.Picker.
func (u *UI) PickDirectory(ctx context.Context) (string, error) {
	return u.runPathDialog(ctx, func(win fyne.Window, done func(string, error)) {
		showFolderOpen(win, func(lu fyne.ListableURI, err error) {
			if err != nil || lu == nil {
				done("", err)
				return
			}
			done(lu.Path(), nil)
		})
	})
}

// PickFile implements studio.Picker.
func (u *UI) PickFile(ctx context.Context) (string, error) {
	return u.runPathDialog(ctx, func(win fyne.Window, done func(string, error)) {
		showFileOpen(win, func(rc fyne.URIReadCloser, err error) {
			if err != nil || rc == nil {
				done("", err)
				return
			}
			p := rc.URI().Path()
			_ = rc.Close()
			done(p, nil)
		})
	})
}

// ChooseSavePath implements studio.Picker. We only need the chosen path; the transcode
// worker writes the file (ffmpeg -y), so we close the handle Fyne opens immediately.
func (u *UI) ChooseSavePath(ctx context.Context, defaultPath, container string) (string, error) {
	return u.runPathDialog(ctx, func(win fyne.Window, done func(string, error)) {
		d := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
			if err != nil || wc == nil {
				done("", err)
				return
			}
			p := wc.URI().Path()
			_ = wc.Close()
			done(p, nil)
		}, win)
		if name := defaultSaveName(defaultPath, container); name != "" {
			d.SetFileName(name)
		}
		d.Resize(fileDialogSize(win))
		d.Show()
	})
}

// defaultSaveName suggests "<source-stem>.<container>" for the save dialog.
func defaultSaveName(defaultPath, container string) string {
	if defaultPath == "" {
		return ""
	}
	base := filepath.Base(defaultPath)
	stem := base[:len(base)-len(filepath.Ext(base))]
	if stem == "" {
		return ""
	}
	if container != "" {
		return stem + "." + container
	}
	return base
}
