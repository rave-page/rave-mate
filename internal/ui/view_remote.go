package ui

import (
	"context"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/remotectl"
)

// remotePeer is a connected peer that can be remote-controlled (the switcher options).
type remotePeer struct {
	NodeID string
	Name   string
}

// controllablePeers returns connected peers - the "control target" choices. Empty when the peer
// link is off or nothing is connected, so the switcher hides itself.
func (u *UI) controllablePeers() []remotePeer {
	if u.svc.Peers == nil || u.svc.RemoteCtl == nil {
		return nil
	}
	var out []remotePeer
	for _, c := range u.svc.Peers.Connections() {
		if c.Status == peerlink.StatusConnected {
			out = append(out, remotePeer{NodeID: c.NodeID, Name: peerName(c.Nickname, c.NodeID)})
		}
	}
	return out
}

// remoteClient binds a typed peer-control client to a node id (nil if unavailable).
func (u *UI) remoteClient(nodeID string) *remotectl.Client {
	if u.svc.RemoteCtl == nil || nodeID == "" {
		return nil
	}
	return remotectl.NewClient(u.svc.RemoteCtl, nodeID)
}

const localTargetLabel = "This computer"

// targetSwitcher builds the "Controlling: [This computer ▾]" header for a manager. onSelect
// fires with "" for local or a peer node id. Returns ok=false when there's no peer to control
// (caller omits the row). current = the currently-selected node id ("" = local).
func (u *UI) targetSwitcher(current string, onSelect func(nodeID string)) (fyne.CanvasObject, bool) {
	peers := u.controllablePeers()
	if len(peers) == 0 {
		return nil, false
	}
	labels := []string{localTargetLabel}
	byLabel := map[string]string{localTargetLabel: ""}
	curLabel := localTargetLabel
	for _, p := range peers {
		lbl := "▸ " + p.Name
		labels = append(labels, lbl)
		byLabel[lbl] = p.NodeID
		if p.NodeID == current {
			curLabel = lbl
		}
	}
	sel := widget.NewSelect(labels, func(s string) { onSelect(byLabel[s]) })
	sel.SetSelected(curLabel)
	// mutedInline (no wrap) - a wrapping label as a Border side-cell collapses to ~1 char wide
	// and stacks vertically, ballooning the row height.
	return container.NewBorder(nil, nil, mutedInline("Controlling"), nil, sel), true
}

// ── streamed remote file browser ─────────────────────────────────────────────

// remoteBrowser is an in-app directory browser backed by a peer's localMedia.listDirectory.
// The controlled machine streams its filesystem here; it never pops a native dialog (the bug
// this fixes). Same browse contract as the web LocalMediaBrowser.
type remoteBrowser struct {
	u       *UI
	client  *remotectl.Client
	onPick  func(path string)
	dirMode bool // pick the current directory instead of a file

	path     string
	parent   *string
	entries  []localmedia.Entry
	selected string

	pathEntry *widget.Entry
	list      *widget.List
	upBtn     *widget.Button
	useBtn    *widget.Button
	status    *widget.Label
	dlg       dialog.Dialog
}

// showRemoteFileBrowser opens the browser dialog; onPick(path) fires when the user chooses a
// file. Starts at the peer's Music folder (falling back to Home), fetched remotely.
func (u *UI) showRemoteFileBrowser(client *remotectl.Client, title string, onPick func(path string)) {
	u.showRemoteBrowser(client, title, false, onPick)
}

// showRemoteDirPicker opens the same browser in folder-pick mode ("Use this folder"
// picks the directory currently shown) - the remote counterpart of showFolderOpen.
func (u *UI) showRemoteDirPicker(client *remotectl.Client, title string, onPick func(dir string)) {
	u.showRemoteBrowser(client, title, true, onPick)
}

func (u *UI) showRemoteBrowser(client *remotectl.Client, title string, dirMode bool, onPick func(path string)) {
	if client == nil {
		u.Notify("rave-mate", "No connected peer to browse.")
		return
	}
	b := &remoteBrowser{u: u, client: client, onPick: onPick, dirMode: dirMode}

	b.pathEntry = newEntry()
	b.pathEntry.SetPlaceHolder("path on the controlled computer")
	b.pathEntry.OnSubmitted = func(s string) { b.navigate(s) }

	b.upBtn = widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		if b.parent != nil {
			b.navigate(*b.parent)
		}
	})
	b.status = mutedLabel("Loading…")

	b.list = widget.NewList(
		func() int { return len(b.entries) },
		func() fyne.CanvasObject {
			icon := widget.NewIcon(theme.FileIcon())
			name := widget.NewLabel("")
			name.Truncation = fyne.TextTruncateEllipsis
			return container.NewBorder(nil, nil, icon, mutedLabel(""), name)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < 0 || id >= len(b.entries) {
				return
			}
			e := b.entries[id]
			c := o.(*fyne.Container)
			c.Objects[1].(*widget.Icon).SetResource(entryIcon(e))
			c.Objects[0].(*widget.Label).SetText(e.Name)
			meta := ""
			if !e.IsDirectory {
				meta = humanBytes(e.SizeBytes)
			}
			c.Objects[2].(*widget.Label).SetText(meta)
		},
	)
	b.list.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(b.entries) {
			return
		}
		e := b.entries[id]
		if e.IsDirectory {
			b.list.UnselectAll()
			b.navigate(e.Path)
			return
		}
		if b.dirMode {
			b.list.UnselectAll() // folder mode - files aren't pickable
			return
		}
		b.selected = e.Path
		b.useBtn.Enable()
		b.useBtn.SetText("Use " + e.Name)
	}

	useLabel := "Use file"
	if dirMode {
		useLabel = "Use this folder"
	}
	b.useBtn = widget.NewButtonWithIcon(useLabel, theme.ConfirmIcon(), func() {
		pick := b.selected
		if b.dirMode {
			pick = b.path
		}
		if pick == "" {
			return
		}
		b.onPick(pick)
		if b.dlg != nil {
			b.dlg.Hide()
		}
	})
	b.useBtn.Importance = widget.HighImportance
	b.useBtn.Disable()

	head := container.NewBorder(nil, nil, b.upBtn, nil, b.pathEntry)
	content := container.NewBorder(
		container.NewVBox(head, b.status),
		b.useBtn, nil, nil,
		b.list,
	)
	wrap := container.NewVScroll(content)
	wrap.SetMinSize(fyne.NewSize(560, 460))

	b.dlg = dialog.NewCustom(title, "Cancel", wrap, u.win)
	b.dlg.Resize(fyne.NewSize(600, 520))
	b.dlg.Show()

	// Pick a sensible start dir from the peer's defaults, off-thread.
	goUI("remote", func() {
		ctx, cancel := context.WithTimeout(context.Background(), remotectl.DefaultCallTimeout)
		defer cancel()
		def, err := client.GetDefaults(ctx)
		start := ""
		if err == nil {
			switch {
			case def.Music != "":
				start = def.Music
			case def.Home != "":
				start = def.Home
			}
		}
		b.navigate(start)
	})
}

// navigate loads a remote directory and repaints (off the UI thread; UI writes via fyne.Do).
func (b *remoteBrowser) navigate(path string) {
	goUI("remote", func() {
		ctx, cancel := context.WithTimeout(context.Background(), remotectl.DefaultCallTimeout)
		defer cancel()
		listing, err := b.client.ListDirectory(ctx, path, false)
		fyne.Do(func() {
			b.selected = ""
			if b.dirMode {
				b.useBtn.SetText("Use this folder")
			} else {
				b.useBtn.Disable()
				b.useBtn.SetText("Use file")
			}
			if err != nil {
				b.status.SetText("Error: " + err.Error())
				return
			}
			if listing.Error != "" {
				b.status.SetText(listing.Error)
			} else {
				b.status.SetText(fmt.Sprintf("%d item(s)", len(listing.Entries)))
			}
			b.path = listing.Path
			b.parent = listing.Parent
			b.pathEntry.SetText(listing.Path)
			b.entries = listing.Entries
			if b.dirMode && b.path != "" {
				b.useBtn.Enable()
			}
			if b.parent == nil {
				b.upBtn.Disable()
			} else {
				b.upBtn.Enable()
			}
			b.list.UnselectAll()
			b.list.Refresh()
		})
	})
}

func entryIcon(e localmedia.Entry) fyne.Resource {
	if e.IsDirectory {
		return theme.FolderIcon()
	}
	switch e.Kind {
	case "audio":
		return theme.MediaMusicIcon()
	case "video":
		return theme.MediaVideoIcon()
	default:
		return theme.FileIcon()
	}
}
