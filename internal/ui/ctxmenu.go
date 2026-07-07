package ui

// Right-click context menu for Library file/dir rows (List AND Grid, local AND remote).
// File ops go through mediaOps so a remote row acts on the paired instance's filesystem
// with identical semantics. Items that can't work for a row are disabled, not hidden
// (except "Send to paired instance…", which is hidden when no FileXfer backend exists).

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// ctxRow wraps a list-row's content and catches ONLY secondary taps, so primary taps still
// reach widget.List selection (the driver matches the tap interface per event).
type ctxRow struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onMenu  func(pos fyne.Position, self fyne.CanvasObject)
}

var _ fyne.SecondaryTappable = (*ctxRow)(nil)

func newCtxRow(content fyne.CanvasObject) *ctxRow {
	r := &ctxRow{content: content}
	r.ExtendBaseWidget(r)
	return r
}

func (r *ctxRow) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(r.content) }

func (r *ctxRow) TappedSecondary(ev *fyne.PointEvent) {
	if r.onMenu != nil {
		r.onMenu(ev.AbsolutePosition, r)
	}
}

// setRowMenu binds a row's context-menu handler (no-op on non-wrapped rows).
func setRowMenu(o fyne.CanvasObject, fn func(pos fyne.Position, self fyne.CanvasObject)) {
	if r, ok := o.(*ctxRow); ok {
		r.onMenu = fn
	}
}

// fileMenuCtx is everything the shared menu needs for one row.
type fileMenuCtx struct {
	ops     mediaOps
	entry   fileEntry
	refresh func()                        // re-list after a mutation (UI thread)
	pickDir func(onPick func(dir string)) // backend-matched folder picker (Move to…)
	xfer    FileXfer                      // nil → "Send to paired instance…" hidden
	marked  bool                          // this exact path has an ID mark
	onMark  func(mark bool)               // toggle ID mark; nil → item disabled (remote rows)
}

// showFileMenu builds + pops the row menu at pos.
func showFileMenu(u *UI, c fileMenuCtx, pos fyne.Position, anchor fyne.CanvasObject) {
	cv := fyne.CurrentApp().Driver().CanvasForObject(anchor)
	if cv == nil {
		return
	}
	e := c.entry
	items := []*fyne.MenuItem{
		fyne.NewMenuItem("Rename…", func() { fileMenuRename(u, c) }),
	}
	dup := fyne.NewMenuItem("Duplicate", func() {
		fileMenuOp(u, c, "Duplicate", func(ctx context.Context) error { _, err := c.ops.Duplicate(ctx, e.path); return err })
	})
	dup.Disabled = e.isDir // files only (implicit deep copies are too risky)
	items = append(items,
		dup,
		fyne.NewMenuItem("Move to…", func() { fileMenuMove(u, c) }),
		fyne.NewMenuItem("Delete…", func() { fileMenuDelete(u, c) }),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Copy path", func() { fyne.CurrentApp().Clipboard().SetContent(e.path) }),
	)
	reveal := fyne.NewMenuItem("Reveal in file manager", func() { revealFile(e.path) })
	reveal.Disabled = c.ops.Remote() // the file lives on the paired instance
	items = append(items, reveal, fyne.NewMenuItemSeparator())

	markLabel := "Mark as ID"
	if c.marked {
		markLabel = "Unmark ID"
	}
	mark := fyne.NewMenuItem(markLabel, func() {
		if c.onMark != nil {
			c.onMark(!c.marked)
		}
	})
	mark.Disabled = c.onMark == nil // ID marks apply to this computer's session output only
	items = append(items, mark)

	if c.xfer != nil && !c.ops.Remote() {
		items = append(items, fyne.NewMenuItemSeparator(), sendToPeerItem(u, c.xfer, e.path))
	}
	widget.ShowPopUpMenuAtPosition(fyne.NewMenu("", items...), cv, pos)
}

// sendToPeerItem builds the "Send to paired instance…" submenu from the FileXfer backend.
func sendToPeerItem(u *UI, xfer FileXfer, path string) *fyne.MenuItem {
	it := fyne.NewMenuItem("Send to paired instance…", nil)
	peers := xfer.Peers()
	if len(peers) == 0 {
		none := fyne.NewMenuItem("No paired instance connected", nil)
		none.Disabled = true
		it.ChildMenu = fyne.NewMenu("", none)
		return it
	}
	subs := make([]*fyne.MenuItem, 0, len(peers))
	for _, p := range peers {
		peer := p
		subs = append(subs, fyne.NewMenuItem(peer.Name, func() {
			goUI("file-xfer", func() {
				// SendToPeer queues + returns; progress lives in the Peers tab.
				if _, err := xfer.SendToPeer(peer.NodeID, path); err != nil {
					u.Notify("rave-mate", "Send failed: "+err.Error())
					return
				}
				u.Notify("rave-mate", "Sending "+baseName(path)+" to "+peer.Name+" - progress in the Peers tab.")
			})
		}))
	}
	it.ChildMenu = fyne.NewMenu("", subs...)
	return it
}

// fileMenuRename asks for a new base name and renames through the backend.
func fileMenuRename(u *UI, c fileMenuCtx) {
	win := currentWindow()
	if win == nil {
		return
	}
	entry := newEntry()
	entry.SetText(c.entry.name)
	form := dialog.NewForm("Rename", "Rename", "Cancel",
		[]*widget.FormItem{widget.NewFormItem("New name", entry)},
		func(ok bool) {
			if !ok || entry.Text == "" || entry.Text == c.entry.name {
				return
			}
			newName := entry.Text
			fileMenuOp(u, c, "Rename", func(ctx context.Context) error {
				_, err := c.ops.Rename(ctx, c.entry.path, newName)
				return err
			})
		}, win)
	form.Resize(fyne.NewSize(420, 160))
	form.Show()
}

// fileMenuMove picks a destination folder (backend-matched picker) and moves.
func fileMenuMove(u *UI, c fileMenuCtx) {
	pick := c.pickDir
	if pick == nil { // local default: native folder browser
		pick = func(onPick func(dir string)) {
			win := currentWindow()
			if win == nil {
				return
			}
			showFolderOpen(win, func(lu fyne.ListableURI, _ error) {
				if lu != nil {
					onPick(lu.Path())
				}
			})
		}
	}
	pick(func(dir string) {
		if dir == "" {
			return
		}
		fileMenuOp(u, c, "Move", func(ctx context.Context) error {
			_, err := c.ops.Move(ctx, c.entry.path, dir)
			return err
		})
	})
}

// fileMenuDelete confirms, then deletes through the backend.
func fileMenuDelete(u *UI, c fileMenuCtx) {
	win := currentWindow()
	if win == nil {
		return
	}
	what := "file"
	if c.entry.isDir {
		what = "folder (and everything in it)"
	}
	where := ""
	if c.ops.Remote() {
		where = " on the paired instance"
	}
	dialog.ShowConfirm("Delete "+c.entry.name,
		fmt.Sprintf("Permanently delete this %s%s?\n\n%s", what, where, c.entry.path),
		func(ok bool) {
			if !ok {
				return
			}
			fileMenuOp(u, c, "Delete", func(ctx context.Context) error {
				return c.ops.Delete(ctx, c.entry.path)
			})
		}, win)
}

// fileMenuOp runs one backend mutation off-thread, toasts the outcome, and refreshes.
// Budget is generous: duplicate/cross-volume moves of big media files far exceed the
// default RPC round-trip timeout.
func fileMenuOp(u *UI, c fileMenuCtx, verb string, op func(ctx context.Context) error) {
	goUI("file-op", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := op(ctx); err != nil {
			u.Notify("rave-mate", verb+" failed: "+err.Error())
			return
		}
		u.Notify("rave-mate", verb+": "+c.entry.name+" - done.")
		if c.refresh != nil {
			fyne.Do(c.refresh)
		}
	})
}
