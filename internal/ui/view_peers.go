package ui

import (
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/peerlink"
)

// peersCard is the Settings entry: the discovery on/off button (the "press to discover"
// toggle) + the advertised nickname. Toggling also shows/hides the Peers tab.
func (u *UI) peersCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Peers
	nick := newEntry()
	nick.SetText(f.Nickname)
	nick.SetPlaceHolder("this computer's name")
	nick.OnChanged = func(s string) { f.Nickname = s; u.saveCfg() }

	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case u.svc.Modules == nil || !u.svc.Modules.IsRunning("peers"):
			s.set(colBrandAmber, "not running")
		default:
			n := 0
			if u.svc.Peers != nil {
				n = len(u.svc.Peers.Connections())
			}
			if n > 0 {
				s.set(colBrandMint, fmt.Sprintf("%d peer(s) connected", n))
			} else {
				s.set(colBrandMint, "discovering")
			}
		}
	})
	toggle := u.moduleTabToggle("peers", &f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Name"), nil, nick),
		mutedLabel("Discover and securely link other rave-mate instances on your network. "+
			"Pairing shows a 6-digit code on both screens - confirm they match. Restart applies a name change."),
	)
	return featureCard("LAN peers",
		"Find + link other rave-mate instances on this network.", toggle, st, body)
}

// buildPeers is the Peers tab: live connections, discovered instances, and remembered peers.
func (u *UI) buildPeers() fyne.CanvasObject {
	list := container.NewVBox()
	strip := newKitStatusStrip()
	if u.svc.Identity != nil {
		strip.SetRight("node " + shortID(u.svc.Identity.NodeID))
		nodeID := u.svc.Identity.NodeID
		strip.SetRightCopyText("node ID", func() string { return nodeID }) // full id, not the shortID shown
	}
	strip.SetCenter("pairing shows a 6-digit code on both screens - confirm they match")

	xferSettings := u.xferSettingsRow() // built once; entries survive the 2 s list rebuilds
	camPanel := u.newWebcamPanel()      // built once; selects/sliders survive the 2 s list rebuilds

	refresh := func() {
		objs := []fyne.CanvasObject{}

		// Live connections.
		conns := map[string]peerlink.ConnInfo{}
		if u.svc.Peers != nil {
			for _, c := range u.svc.Peers.Connections() {
				conns[c.NodeID] = c
			}
		}
		// Each connected peer's last-known now-playing (bridged over the link).
		remotes := map[string]peerbridge.RemoteState{}
		if u.svc.PeerBridge != nil {
			for _, s := range u.svc.PeerBridge.RemoteStates() {
				remotes[s.NodeID] = s
			}
		}

		// "Controlling the other PC" banner. Auto-clears if the target dropped offline.
		if u.svc.PeerBridge != nil {
			if on, target := u.svc.PeerBridge.Forwarding(); on {
				if _, live := conns[target]; live {
					objs = append(objs, u.controlBanner(target, conns))
				} else {
					u.svc.PeerBridge.SetMIDIForwarding(false)
					u.svc.PeerBridge.SetControlTarget("")
				}
			}
		}

		objs = append(objs, sectionLabel("Connections"))
		if len(conns) == 0 {
			objs = append(objs, mutedLabel("No active connections."))
		}
		for _, c := range conns {
			objs = append(objs, u.connRow(c, remotes[c.NodeID]))
		}

		resolve := func(id string) string {
			if c, ok := conns[id]; ok {
				return peerName(c.Nickname, id)
			}
			return shortID(id)
		}

		// Media plane (routes / clock sync / TC master) - shown once peers are linked.
		if u.svc.Media != nil && (len(conns) > 0 || len(u.svc.Media.Stats()) > 0) {
			objs = append(objs, u.mediaSection(resolve)...)
		}

		// Webcam: this instance's camera + every paired instance's (media.cam.* bus).
		objs = append(objs, camPanel.section(resolve)...)

		// File transfer: pending accepts + active transfers + receive settings.
		if u.svc.FileXfer != nil {
			objs = append(objs, u.xferSection(resolve, xferSettings)...)
		}

		// Discovered on the network (not already connected).
		objs = append(objs, widget.NewSeparator(), sectionLabel("On this network"))
		found := []discovery.Peer{}
		if u.svc.Discovery != nil {
			found = u.svc.Discovery.Peers()
		}
		anyFound := false
		for _, p := range found {
			if _, busy := conns[p.NodeID]; busy {
				continue
			}
			anyFound = true
			objs = append(objs, u.discoveredRow(p))
		}
		if !anyFound {
			objs = append(objs, mutedLabel("Searching… make sure LAN peers is on, on both computers."))
		}

		// Remembered (trusted) peers that are neither connected nor visible right now.
		if u.svc.Peers != nil {
			online := map[string]bool{}
			for id := range conns {
				online[id] = true
			}
			for _, p := range found {
				online[p.NodeID] = true
			}
			var offline []fyne.CanvasObject
			for _, p := range u.svc.Peers.Remembered() {
				if !online[p.NodeID] {
					offline = append(offline, u.rememberedRow(p.NodeID, p.Nickname))
				}
			}
			if len(offline) > 0 {
				objs = append(objs, widget.NewSeparator(), sectionLabel("Remembered (offline)"))
				objs = append(objs, offline...)
			}
		}

		nConn, nFound := len(conns), len(found)
		fyne.Do(func() {
			strip.SetLeft(fmt.Sprintf("%d connected · %d on this network", nConn, nFound))
			// Don't rebuild the list while a dropdown/popup is open: reparenting reflows the
			// container and Fyne re-homes any open PopUp (the webcam device/mode Select) to the
			// canvas origin - it "jumps to the top" and the pending click never lands on an
			// option. Skip the swap this tick; it resumes once the popup closes. Panels are
			// persistent, so no live data is lost - just deferred a couple seconds.
			if c := fyne.CurrentApp().Driver().CanvasForObject(list); c != nil && c.Overlays().Top() != nil {
				return
			}
			list.Objects = objs
			list.Refresh()
		})
	}

	// Refresh on a ticker (cheap; the data is small) and once immediately.
	u.peersRefresh = refresh
	t := time.NewTicker(2 * time.Second)
	u.closers = append(u.closers, t.Stop, func() { u.peersRefresh = nil })
	goUI("peers", func() {
		refresh()
		for range t.C {
			refresh()
		}
	})

	head := container.NewVBox(
		container.NewHBox(
			widget.NewLabelWithStyle("LAN peers", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			helpIcon("Securely link other rave-mate instances on this network: bridged now-playing, "+
				"MIDI mirroring, remote control + library sync. Discovery on/off + this machine's "+
				"advertised name: Settings ▸ Streaming & remote ▸ LAN peers."),
		),
		widget.NewSeparator(),
	)
	return container.NewBorder(head, strip.Object(), nil, nil, container.NewVScroll(list))
}

func (u *UI) connRow(c peerlink.ConnInfo, remote peerbridge.RemoteState) fyne.CanvasObject {
	status := string(c.Status)
	if c.Status == peerlink.StatusConnected && c.Trusted {
		status = "connected · paired"
	}
	// right-click → Copy yields the FULL node id (display shortens it); rows rebuild
	// every 2s, so drag-selection would reset mid-use - copy menu instead.
	line := peerName(c.Nickname, c.NodeID) + "  -  " + status
	label := newKitCopyable("peer info", widget.NewLabel(line),
		func() string { return line + "  -  node " + c.NodeID })
	forget := widget.NewButton("Forget", func() {
		if u.svc.Peers != nil {
			u.svc.Peers.Forget(c.NodeID)
		}
	})

	// Control toggle: route this machine's MIDI/control to the peer (the "control the other
	// PC" context). Only one peer is controlled at a time.
	right := fyne.CanvasObject(forget)
	if c.Status == peerlink.StatusConnected && u.svc.PeerBridge != nil {
		on, target := u.svc.PeerBridge.Forwarding()
		var ctrl *widget.Button
		if on && target == c.NodeID {
			ctrl = widget.NewButton("Stop control", func() { u.setControl(c.NodeID, false) })
			ctrl.Importance = widget.WarningImportance
		} else {
			ctrl = widget.NewButton("Control", func() { u.setControl(c.NodeID, true) })
		}
		right = container.NewHBox(ctrl, forget)
	}

	row := container.NewBorder(nil, nil, nil, right, label)
	// Show the peer's now-playing under the row when we have it.
	if np := fmtRemoteNowPlaying(remote.NowPlaying); np != "" {
		return container.NewVBox(row, mutedLabel("   ▶ "+np))
	}
	return row
}

// setControl starts/stops controlling a peer (routes local MIDI/control to it).
func (u *UI) setControl(nodeID string, on bool) {
	if u.svc.PeerBridge == nil {
		return
	}
	if on {
		u.svc.PeerBridge.SetControlTarget(nodeID)
		u.svc.PeerBridge.SetMIDIForwarding(true)
	} else {
		u.svc.PeerBridge.SetMIDIForwarding(false)
		u.svc.PeerBridge.SetControlTarget("")
	}
	if u.peersRefresh != nil {
		u.peersRefresh()
	}
}

// fmtRemoteNowPlaying renders a peer's bridged now-playing as "Artist - Title (128 BPM)".
func fmtRemoteNowPlaying(np peerbridge.NowPlaying) string {
	if !np.Playing || (np.Title == "" && np.Artist == "") {
		return ""
	}
	s := np.Title
	if np.Artist != "" {
		s = np.Artist + " - " + np.Title
	}
	if np.BPM > 0 {
		s += fmt.Sprintf("  (%.0f BPM)", np.BPM)
	}
	return s
}

func (u *UI) discoveredRow(p discovery.Peer) fyne.CanvasObject {
	trusted := u.svc.Peers != nil && u.svc.Peers.IsTrusted(p.NodeID)
	verb := "Pair"
	if trusted {
		verb = "Connect"
	}
	btn := widget.NewButton(verb, func() {
		if u.svc.Peers != nil {
			u.svc.Peers.Connect(p)
		}
	})
	if !trusted {
		btn.Importance = widget.HighImportance
	}
	line := peerName(p.Name, p.NodeID) + "  -  " + p.Address.String()
	label := newKitCopyable("peer info", widget.NewLabel(line),
		func() string { return line + "  -  node " + p.NodeID })
	return container.NewBorder(nil, nil, nil, btn, label)
}

// rememberedRow shows a trusted peer that's currently offline, with a Forget action.
func (u *UI) rememberedRow(nodeID, nick string) fyne.CanvasObject {
	forget := widget.NewButton("Forget", func() {
		if u.svc.Peers != nil {
			u.svc.Peers.Forget(nodeID)
		}
	})
	line := peerName(nick, nodeID) + "  -  offline"
	return container.NewBorder(nil, nil, nil, forget,
		newKitCopyable("peer info", widget.NewLabel(line),
			func() string { return line + "  -  node " + nodeID }))
}

// controlBanner is the prominent "you're controlling the other PC" context indicator.
func (u *UI) controlBanner(target string, conns map[string]peerlink.ConnInfo) fyne.CanvasObject {
	name := peerName(conns[target].Nickname, target)
	bg := canvas.NewRectangle(color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 0xFF}) // brand-base
	bg.CornerRadius = 8
	txt := canvas.NewText("🎛  Controlling "+name+" - your MIDI is routed to it", color.White)
	txt.TextStyle = fyne.TextStyle{Bold: true}
	txt.TextSize = 14
	stop := widget.NewButton("Stop controlling", func() { u.setControl(target, false) })
	stop.Importance = widget.HighImportance
	inner := container.NewBorder(nil, nil, nil, stop, container.NewPadded(txt))
	return container.NewStack(bg, container.NewPadded(inner))
}

// onPeerSAS shows the pairing-code confirmation on the UI thread.
func (u *UI) onPeerSAS(req peerlink.SASRequest) {
	fyne.Do(func() {
		code := widget.NewLabelWithStyle(spaceSAS(req.SAS), fyne.TextAlignCenter, fyne.TextStyle{Bold: true, Monospace: true})
		content := container.NewVBox(
			mutedLabel("Confirm this code matches the one shown on "+peerName(req.Nickname, req.NodeID)+":"),
			code,
		)
		d := dialog.NewCustomConfirm("Pair LAN peer", "Matches", "Doesn't match", content, func(ok bool) {
			if u.svc.Peers != nil {
				u.svc.Peers.ConfirmSAS(req.NodeID, ok)
			}
		}, u.win)
		d.Show()
	})
}

// onPeerState refreshes the Peers tab + the Library "Controlling" switcher when a
// connection changes.
func (u *UI) onPeerState() {
	if u.peersRefresh != nil {
		u.peersRefresh()
	}
	if u.libraryPeersRefresh != nil {
		u.libraryPeersRefresh()
	}
}

func sectionLabel(s string) *widget.Label {
	return widget.NewLabelWithStyle(s, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

func peerName(nick, nodeID string) string {
	if nick != "" {
		return nick
	}
	return shortID(nodeID)
}

func shortID(id string) string {
	if len(id) > 10 {
		return id[:10] + "…"
	}
	return id
}

// spaceSAS formats "472915" as "472 915" for readability.
func spaceSAS(s string) string {
	if len(s) == 6 {
		return s[:3] + " " + s[3:]
	}
	return s
}
