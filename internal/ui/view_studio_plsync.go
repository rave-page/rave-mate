package ui

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/playsync"
)

// Playlist cloud sync UI: status chips on the playlist list, a per-playlist sync panel
// (Push / Pull / Diff / Undo / Unlink), and toolbar actions (refresh, Sync all, remote-only
// import). Pull only rewrites rave-mate's own copy - the DJ software's playlist files are
// never written (the panel copy says so).

const plSyncOpTimeout = 2 * time.Minute

func (sv *studioView) plSyncAvailable() bool {
	return sv.u.svc.Syncer != nil && sv.u.svc.Lib != nil
}

func (sv *studioView) plSignedIn() bool {
	return sv.u.svc.Auth != nil && sv.u.svc.Auth.SignedIn()
}

// plSyncStatusLabel renders a pair status as a short chip text ("" = status unknown).
func plSyncStatusLabel(s playsync.PlaylistStatus) string {
	switch s {
	case playsync.PlaylistInSync:
		return "✓ in sync"
	case playsync.PlaylistLocalAhead:
		return "↑ local ahead"
	case playsync.PlaylistRemoteAhead:
		return "↓ remote ahead"
	case playsync.PlaylistDiverged:
		return "⚠ diverged"
	case playsync.PlaylistLocalOnly:
		return "- not synced"
	case playsync.PlaylistRemoteOnly:
		return "☁ remote only"
	default:
		return ""
	}
}

// plSyncGo runs one engine call off the UI thread, then refreshes statuses. Errors toast.
func (sv *studioView) plSyncGo(what string, fn func(ctx context.Context) error) {
	if sv.plSyncBusy {
		sv.u.Notify("rave-mate", "Playlist sync already running.")
		return
	}
	sv.plSyncBusy = true
	sv.setPlSyncInfo(what + "…")
	debuglog.Go(sv.u.svc.Log, "plsync-ui", func() {
		ctx, cancel := context.WithTimeout(context.Background(), plSyncOpTimeout)
		defer cancel()
		err := fn(ctx)
		fyne.Do(func() {
			sv.plSyncBusy = false
			if err != nil {
				sv.setPlSyncInfo("")
				sv.u.Notify("rave-mate", what+" failed: "+err.Error())
				return
			}
			sv.refreshPlaylistSync()
		})
	})
}

// refreshPlaylistSync recomputes all pair statuses (async). UI thread entry.
func (sv *studioView) refreshPlaylistSync() {
	if !sv.plSyncAvailable() {
		return
	}
	if !sv.plSignedIn() {
		sv.setPlSyncInfo("Sign in to sync playlists with rave.page.")
		return
	}
	if sv.plSyncBusy {
		return
	}
	sv.plSyncBusy = true
	sv.setPlSyncInfo("Checking sync status…")
	debuglog.Go(sv.u.svc.Log, "plsync-refresh", func() {
		ctx, cancel := context.WithTimeout(context.Background(), plSyncOpTimeout)
		defer cancel()
		ov, err := sv.u.svc.Syncer.PlaylistOverviewCtx(ctx)
		fyne.Do(func() {
			sv.plSyncBusy = false
			if err != nil {
				sv.setPlSyncInfo("")
				sv.u.Notify("rave-mate", "Sync status: "+err.Error())
				return
			}
			sv.plSyncPairs = make(map[int64]playsync.PlaylistPair, len(ov.Pairs))
			for _, p := range ov.Pairs {
				sv.plSyncPairs[p.LocalID] = p
			}
			sv.plRemoteOnly = ov.RemoteOnly
			counts := map[playsync.PlaylistStatus]int{}
			for _, p := range ov.Pairs {
				counts[p.Status]++
			}
			info := fmt.Sprintf("☁ %d synced · %d ahead · %d behind · %d diverged · %d unsynced",
				counts[playsync.PlaylistInSync],
				counts[playsync.PlaylistLocalAhead], counts[playsync.PlaylistRemoteAhead],
				counts[playsync.PlaylistDiverged], counts[playsync.PlaylistLocalOnly])
			if n := len(ov.RemoteOnly); n > 0 {
				info += fmt.Sprintf(" · %d remote-only", n)
			}
			sv.setPlSyncInfo(info)
			sv.refreshPlaylists() // repaint rows + open head with fresh chips
		})
	})
}

func (sv *studioView) setPlSyncInfo(s string) {
	if sv.plSyncInfo != nil {
		sv.plSyncInfo.SetText(s)
	}
}

// playlistSyncBar is the toolbar block under the playlist list header.
func (sv *studioView) playlistSyncBar() fyne.CanvasObject {
	if !sv.plSyncAvailable() {
		return container.NewVBox()
	}
	if sv.plSyncInfo == nil {
		sv.plSyncInfo = mutedLabel("")
		sv.plSyncInfo.Wrapping = fyne.TextWrapWord
	}
	low := func(label string, icon fyne.Resource, fn func()) *widget.Button {
		b := widget.NewButtonWithIcon(label, icon, fn)
		b.Importance = widget.LowImportance
		return b
	}
	refresh := low("Cloud status", theme.ViewRefreshIcon(), sv.refreshPlaylistSync)
	syncAll := low("Sync all", theme.MediaReplayIcon(), sv.plSyncAllDialog)
	remote := low("Remote playlists…", theme.DownloadIcon(), sv.plRemoteDialog)
	return container.NewVBox(WrapActions(refresh, syncAll, remote), sv.plSyncInfo)
}

// plSyncAllDialog confirms + runs the bulk sync (push local-ahead, pull remote-ahead).
func (sv *studioView) plSyncAllDialog() {
	win := currentWindow()
	if win == nil || !sv.plSignedIn() {
		sv.u.Notify("rave-mate", "Sign in to sync playlists.")
		return
	}
	dialog.ShowConfirm("Sync all playlists",
		"Pushes every locally-changed playlist to rave.page and pulls every remotely-changed one "+
			"into rave-mate. Diverged playlists are skipped (resolve them per playlist). "+
			"Each overwritten side is snapshotted for Undo.",
		func(ok bool) {
			if !ok {
				return
			}
			sv.plSyncGo("Sync all", func(ctx context.Context) error {
				res, err := sv.u.svc.Syncer.SyncAllPlaylists(ctx)
				if err != nil {
					return err
				}
				sv.u.Notify("rave-mate", fmt.Sprintf(
					"Playlists synced: %d pushed, %d pulled, %d in sync, %d diverged, %d failed.",
					res.Pushed, res.Pulled, res.InSync, res.Diverged, res.Failed))
				return nil
			})
		}, win)
}

// plRemoteDialog lists owned remote playlists with no local mapping; Import materializes one.
func (sv *studioView) plRemoteDialog() {
	win := currentWindow()
	if win == nil {
		return
	}
	if len(sv.plRemoteOnly) == 0 {
		dialog.ShowInformation("Remote playlists",
			"No unsynced rave.page playlists. Use “Cloud status” to refresh.", win)
		return
	}
	box := container.NewVBox(mutedLabel("rave.page playlists not yet in rave-mate. Import creates a local manual playlist linked for sync."))
	var d dialog.Dialog
	for _, rp := range sv.plRemoteOnly {
		rp := rp
		imp := widget.NewButtonWithIcon("Import", theme.DownloadIcon(), func() {
			d.Hide()
			sv.plSyncGo("Import "+rp.RemoteTitle, func(ctx context.Context) error {
				_, err := sv.u.svc.Syncer.ImportRemotePlaylist(ctx, rp.RemoteID)
				return err
			})
		})
		imp.Importance = widget.HighImportance
		del := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() {
			d.Hide()
			dialog.ShowConfirm("Delete remote playlist",
				fmt.Sprintf("Delete “%s” on rave.page? This cannot be undone.", rp.RemoteTitle),
				func(ok bool) {
					if !ok {
						return
					}
					sv.plSyncGo("Delete "+rp.RemoteTitle, func(ctx context.Context) error {
						return sv.u.svc.Syncer.DeleteRemotePlaylist(ctx, rp.RemoteID)
					})
				}, win)
		})
		del.Importance = widget.LowImportance
		box.Add(container.NewBorder(nil, nil, nil, container.NewHBox(imp, del), widget.NewLabel(rp.RemoteTitle)))
	}
	scroll := container.NewVScroll(box)
	scroll.SetMinSize(fyne.NewSize(380, 280))
	d = dialog.NewCustom("Remote playlists", "Close", scroll, win)
	d.Show()
}

// playlistSyncPanel is the per-playlist sync block in the right-pane head.
func (sv *studioView) playlistSyncPanel(r libdb.PlaylistRow) fyne.CanvasObject {
	if !sv.plSyncAvailable() || r.Kind == libdb.PlaylistSmart {
		return container.NewVBox()
	}
	pair, known := sv.plSyncPairs[r.ID]
	box := container.NewVBox(smallCaps("RAVE.PAGE SYNC"))
	if !sv.plSignedIn() {
		box.Add(mutedLabel("Sign in to sync this playlist with rave.page."))
		return box
	}

	status := "Status unknown - use “Cloud status”."
	if known {
		status = plSyncStatusLabel(pair.Status)
		if !pair.SyncedAt.IsZero() {
			status += " · last synced " + pair.SyncedAt.Local().Format("2006-01-02 15:04")
		}
	}
	statusLbl := mutedLabel(status)
	statusLbl.Wrapping = fyne.TextWrapWord
	box.Add(statusLbl)
	if r.PulledAt != "" {
		note := mutedLabel("Contains pulled changes - they live in rave-mate only; your DJ software's own playlist is not updated.")
		note.Wrapping = fyne.TextWrapWord
		box.Add(note)
	}

	low := func(label string, icon fyne.Resource, fn func()) *widget.Button {
		b := widget.NewButtonWithIcon(label, icon, fn)
		b.Importance = widget.LowImportance
		return b
	}
	linked := known && pair.RemoteID != "" && pair.Status != playsync.PlaylistLocalOnly
	actions := []fyne.CanvasObject{
		low("Push", theme.UploadIcon(), func() { sv.plPushDialog(r, pair) }),
	}
	if linked {
		actions = append(actions,
			low("Pull", theme.DownloadIcon(), func() { sv.plPullDialog(r) }),
			low("Diff", theme.ListIcon(), func() { sv.plDiffDialog(r) }),
			low("Unlink", theme.ContentClearIcon(), func() { sv.plUnlinkDialog(r) }),
		)
	}
	actions = append(actions, low("Undo…", theme.ContentUndoIcon(), func() { sv.plUndoDialog(r) }))
	box.Add(WrapActions(actions...))
	return box
}

func (sv *studioView) plPushDialog(r libdb.PlaylistRow, pair playsync.PlaylistPair) {
	win := currentWindow()
	if win == nil {
		return
	}
	msg := fmt.Sprintf("Upload “%s” (%d tracks) to rave.page as a private playlist?", r.Name, r.TrackCount)
	if pair.RemoteID != "" && pair.Status != playsync.PlaylistLocalOnly {
		msg = fmt.Sprintf("Replace the rave.page playlist “%s” with this local version? "+
			"The current remote state is snapshotted for Undo.", pair.RemoteTitle)
	}
	dialog.ShowConfirm("Push playlist", msg, func(ok bool) {
		if !ok {
			return
		}
		sv.plSyncGo("Push "+r.Name, func(ctx context.Context) error {
			return sv.u.svc.Syncer.PushPlaylist(ctx, r.ID)
		})
	}, win)
}

func (sv *studioView) plPullDialog(r libdb.PlaylistRow) {
	win := currentWindow()
	if win == nil {
		return
	}
	dialog.ShowConfirm("Pull playlist",
		fmt.Sprintf("Replace “%s” in rave-mate with the rave.page version? The current local "+
			"tracks are snapshotted for Undo. Your DJ software's own playlist file is NOT modified "+
			"(write-back is not supported).", r.Name),
		func(ok bool) {
			if !ok {
				return
			}
			sv.plSyncGo("Pull "+r.Name, func(ctx context.Context) error {
				return sv.u.svc.Syncer.PullPlaylist(ctx, r.ID)
			})
		}, win)
}

func (sv *studioView) plUnlinkDialog(r libdb.PlaylistRow) {
	win := currentWindow()
	if win == nil {
		return
	}
	dialog.ShowConfirm("Unlink playlist",
		fmt.Sprintf("Stop syncing “%s”? Both the local and the rave.page playlist keep their "+
			"current tracks; only the link is removed.", r.Name),
		func(ok bool) {
			if !ok {
				return
			}
			if err := sv.u.svc.Syncer.UnlinkPlaylist(r.ID); err != nil {
				sv.u.Notify("rave-mate", "Unlink failed: "+err.Error())
				return
			}
			sv.refreshPlaylistSync()
		}, win)
}

// plDiffDialog shows the live local↔remote delta as grouped add/remove lists.
func (sv *studioView) plDiffDialog(r libdb.PlaylistRow) {
	win := currentWindow()
	if win == nil {
		return
	}
	sv.u.Notify("rave-mate", "Computing diff…")
	debuglog.Go(sv.u.svc.Log, "plsync-diff", func() {
		ctx, cancel := context.WithTimeout(context.Background(), plSyncOpTimeout)
		defer cancel()
		d, err := sv.u.svc.Syncer.PlaylistDiffFor(ctx, r.ID)
		fyne.Do(func() {
			if err != nil {
				sv.u.Notify("rave-mate", "Diff failed: "+err.Error())
				return
			}
			sv.showPlDiff(r, d)
		})
	})
}

func (sv *studioView) showPlDiff(r libdb.PlaylistRow, d playsync.PlaylistDiff) {
	win := currentWindow()
	if win == nil {
		return
	}
	itemLine := func(it playsync.PlaylistItemRef) fyne.CanvasObject {
		txt := it.Title
		if it.Artist != "" {
			txt = it.Artist + " - " + it.Title
		}
		l := widget.NewLabel(txt)
		l.Truncation = fyne.TextTruncateEllipsis
		return l
	}
	box := container.NewVBox()
	if d.TitleChanged {
		box.Add(mutedLabel(fmt.Sprintf("Title differs: “%s” (local) vs “%s” (rave.page).", d.LocalName, d.RemoteTitle)))
	}
	if len(d.AddedLocal) == 0 && len(d.AddedRemote) == 0 && d.Moved == 0 && !d.TitleChanged {
		box.Add(mutedLabel("Contents are identical."))
	}
	if len(d.AddedLocal) > 0 {
		box.Add(smallCaps(fmt.Sprintf("ONLY LOCAL (%d) - push uploads, pull removes", len(d.AddedLocal))))
		for _, it := range d.AddedLocal {
			box.Add(itemLine(it))
		}
	}
	if len(d.AddedRemote) > 0 {
		box.Add(smallCaps(fmt.Sprintf("ONLY ON RAVE.PAGE (%d) - pull downloads, push removes", len(d.AddedRemote))))
		for _, it := range d.AddedRemote {
			box.Add(itemLine(it))
		}
	}
	if d.Moved > 0 {
		box.Add(mutedLabel(fmt.Sprintf("%d shared track(s) at a different position.", d.Moved)))
	}
	scroll := container.NewVScroll(box)
	scroll.SetMinSize(fyne.NewSize(420, 320))
	dialog.NewCustom("Diff - "+r.Name, "Close", scroll, win).Show()
}

// plUndoDialog lists the playlist's restorable snapshots.
func (sv *studioView) plUndoDialog(r libdb.PlaylistRow) {
	win := currentWindow()
	if win == nil {
		return
	}
	undos, err := sv.u.svc.Syncer.PlaylistUndos(r.ID)
	if err != nil {
		sv.u.Notify("rave-mate", "Undo history: "+err.Error())
		return
	}
	if len(undos) == 0 {
		dialog.ShowInformation("Undo", "No sync snapshots for this playlist yet. One is taken before every Push overwrite and Pull.", win)
		return
	}
	box := container.NewVBox(mutedLabel("Each entry is the state a sync overwrote. Restore puts it back (pull-snapshots restore the local tracks; push-snapshots restore the rave.page playlist)."))
	var d dialog.Dialog
	for _, u := range undos {
		u := u
		what := "local tracks before pull"
		if u.Direction == "push" {
			what = "rave.page state before push"
		}
		restore := widget.NewButtonWithIcon("Restore", theme.ContentUndoIcon(), func() {
			d.Hide()
			dialog.ShowConfirm("Restore snapshot",
				fmt.Sprintf("Restore the %s from %s (%d tracks)?", what, u.CreatedAt.Local().Format("2006-01-02 15:04"), u.Items),
				func(ok bool) {
					if !ok {
						return
					}
					sv.plSyncGo("Undo "+r.Name, func(ctx context.Context) error {
						return sv.u.svc.Syncer.RestorePlaylistUndo(ctx, u.ID)
					})
				}, win)
		})
		box.Add(container.NewBorder(nil, nil, nil, restore,
			widget.NewLabel(fmt.Sprintf("%s · %d tracks · %s", u.CreatedAt.Local().Format("2006-01-02 15:04"), u.Items, what))))
	}
	scroll := container.NewVScroll(box)
	scroll.SetMinSize(fyne.NewSize(420, 280))
	d = dialog.NewCustom("Undo - "+r.Name, "Close", scroll, win)
	d.Show()
}
