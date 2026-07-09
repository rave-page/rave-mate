package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/rekordboxmap"
	"rave.page/mate/internal/session"
)

// chk builds a checkbox wired AFTER the initial SetChecked so constructing it during
// buildSettings never fires onChange (mirrors newToggle). Sub-backend changes save config;
// the source re-reads them on the next enable toggle (a running source captures its config at
// start - same applies-on-toggle rule as the MIDI port pickers).
func (u *UI) chk(label string, field *bool, onSet func()) *widget.Check {
	c := widget.NewCheck(label, nil)
	c.SetChecked(*field)
	c.OnChanged = func(b bool) {
		*field = b
		u.saveCfg()
		if onSet != nil {
			onSet()
		}
	}
	return c
}

// seratoCard configures the Serato source: collection (database V2 + crates) + live now-playing
// from the active History session file. Fully local - no Serato account or internet.
func (u *UI) seratoCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Serato
	dir := newEntry()
	dir.SetPlaceHolder("(auto-detect Music\\_Serato_)")
	dir.SetText(f.SeratoDir)
	dir.OnChanged = func(s string) { f.SeratoDir = s; u.saveCfg() }
	nowPlaying := u.chk("Live now-playing (watch History sessions)", &f.NowPlaying, nil)
	remote := u.chk("Serato Remote - real-time deck stream (OSC-over-TCP, experimental)", &f.Remote, nil)
	remoteDebug := u.chk("  └ Log every Remote frame (handshake capture)", &f.RemoteDebug, nil)

	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		src, ok := u.sourceInfo(session.SourceSerato)
		switch {
		case ok && src.Receiving:
			s.set(colBrandMint, "receiving")
		case ok && src.Running:
			s.set(colBrandMint, "watching sessions")
		default:
			s.set(colBrandAmber, "not running")
		}
	})
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("_Serato_ folder"), nil, folderPickerRow(dir)),
		nowPlaying,
		remote,
		remoteDebug,
		mutedLabel("Reads your Serato library + crates, and tracks the live set from the active History session (~1–2s after a track plays - Serato logs it once it's been playing a moment). Fully local: no Serato account, no internet, no \"Start Live Playlist\" needed. Toggle off/on to apply a folder change."),
		mutedLabel("Serato Remote (experimental): advertises this PC to Serato DJ Pro over the LAN for a real-time per-deck stream. The pairing handshake is still being reverse-engineered - enable \"Log every Remote frame\" and share the Logs so it can be finished. Serato Remote may need enabling in Serato's Setup → Expansion Packs."),
	)
	return featureCard("Serato DJ", "Collection + live now-playing from Serato's local files.", toggle, st, body)
}

// virtualdjCard configures the VirtualDJ source: collection (database.xml) + up to three live
// now-playing channels with very different trade-offs (surfaced inline so the user can choose).
func (u *UI) virtualdjCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.VirtualDJ
	dir := newEntry()
	dir.SetPlaceHolder("(auto-detect Documents\\VirtualDJ)")
	dir.SetText(f.DatabaseDir)
	dir.OnChanged = func(s string) { f.DatabaseDir = s; u.saveCfg() }

	netCtl := u.chk("Network Control plugin - full metadata (needs VDJ Pro)", &f.NetCtl, nil)
	netURL := newEntry()
	netURL.SetPlaceHolder("http://127.0.0.1:80")
	netURL.SetText(f.NetCtlURL)
	netURL.OnChanged = func(s string) { f.NetCtlURL = s; u.saveCfg() }
	netAuth := newPasswordEntry()
	netAuth.SetPlaceHolder("(optional auth token)")
	netAuth.SetText(f.NetCtlAuth)
	netAuth.OnChanged = func(s string) { f.NetCtlAuth = s; u.saveCfg() }
	os2l := u.chk("OS2L server - live BPM/beat only, zero-config", &f.OS2L, nil)
	tracklist := u.chk("Tracklist file - title/artist fallback (laggy)", &f.Tracklist, nil)

	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		src, ok := u.sourceInfo(session.SourceVirtualDJ)
		switch {
		case ok && src.Receiving:
			s.set(colBrandMint, "receiving")
		case ok && src.Running:
			s.set(colBrandMint, "listening")
		default:
			s.set(colBrandAmber, "not running")
		}
	})
	link := widget.NewHyperlink("How to enable the Network Control plugin",
		mustURL("https://virtualdj.com/wiki/NetworkControlPlugin.html"))
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("VirtualDJ folder"), nil, folderPickerRow(dir)),
		widget.NewSeparator(),
		netCtl,
		container.NewBorder(nil, nil, widget.NewLabel("Plugin URL"), nil, netURL),
		container.NewBorder(nil, nil, widget.NewLabel("Plugin auth"), nil, netAuth),
		link,
		os2l,
		tracklist,
		mutedLabel("Three ways to read the live set, pick any:\n• Network Control = full track title/artist/BPM/key, but needs VirtualDJ Pro 2023+ and a one-time manual plugin install (link above).\n• OS2L = VirtualDJ auto-connects with zero setup, but only carries live BPM/beat - no track name.\n• Tracklist = reads VDJ's history file (title/artist only, delayed).\nCollection reading works regardless. Toggle off/on to apply changes."),
	)
	return featureCard("VirtualDJ", "Collection + live now-playing (Network Control / OS2L / tracklist).", toggle, st, body)
}

// rekordboxLiveCard configures live now-playing from rekordbox software. Three mechanisms with
// honest trade-offs; the master.db key + collection read/write live in the Rekordbox key card
// (Library & media) and Library sync. Pioneer-hardware now-playing is the Pro DJ Link card.
func (u *UI) rekordboxLiveCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Rekordbox
	dbPoll := u.chk("master.db poll - recently played (safe)", &f.DBPoll, nil)
	memRead := u.chk("Memory read - real-time (Windows, may break on rekordbox updates)", &f.MemoryRead, nil)

	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		src, ok := u.sourceInfo(session.SourceRekordbox)
		switch {
		case ok && src.Receiving:
			s.set(colBrandMint, "receiving")
		case ok && src.Running:
			s.set(colBrandMint, "polling")
		default:
			s.set(colBrandAmber, "not running")
		}
	})
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		dbPoll,
		memRead,
		mutedLabel("rekordbox has no official live feed, so each option is a trade-off:\n• master.db poll reuses your saved key to read the most-recently-played track - reliable, with a small lag.\n• Memory read pulls the deck state straight from the running rekordbox process for true real-time data, but depends on per-version memory offsets and can stop working after a rekordbox update (Windows only).\n• For Pioneer CDJ/XDJ hardware on the network, use the Pro DJ Link card instead.\nUnlock master.db in the Rekordbox key card (Library & media). Toggle off/on to apply changes."),
		widget.NewSeparator(),
		smallCaps("REKORDBOX SETUP - faster now-playing"),
		mutedLabel("In rekordbox: Preferences → Advanced → Browse → \"Playback time setting\" sets how long a track must play before rekordbox logs it to History - which is when the master.db poll can see it. Default is ~60s; drag it down to 1–10s for a near-live overlay. rave-mate polls every ~3s, so the total lag ≈ your Playback time setting."),
	)
	return featureCard("Rekordbox (live now-playing)", "Real-time or recently-played track from rekordbox software.", toggle, st, body)
}

// rekordboxMidiCard generates an importable rekordbox MIDI mapping CSV matching the same CC
// layout the app's own MIDI source decodes - so one controller mapping drives both rekordbox's
// decks and rave-mate. No silent install: the user imports it once via rekordbox MIDI LEARN.
func (u *UI) rekordboxMidiCard() fyne.CanvasObject {
	st := u.newStatus(func(s *cardStatus) { s.set(colMuted, "generate + import") })
	outPath := func() string {
		p, err := config.DataPath("RavePage-rekordbox.csv")
		if err != nil {
			return "RavePage-rekordbox.csv"
		}
		return p
	}
	export := widget.NewButtonWithIcon("Export mapping CSV", theme.DocumentSaveIcon(), func() {
		p := outPath()
		if err := rekordboxmap.Export(p); err != nil {
			u.Notify("rave-mate", "Export failed: "+err.Error())
			return
		}
		u.Notify("rave-mate", "Saved "+p+" - in rekordbox: Preferences → Controller → MIDI → select "+rekordboxmap.DefaultDevice+" → IMPORT.")
	})
	export.Importance = widget.HighImportance
	openBtn := widget.NewButtonWithIcon("Open folder", theme.FolderOpenIcon(), func() {
		p, _ := config.DataPath("")
		openDir(p)
	})
	body := container.NewVBox(
		container.NewHBox(export, openBtn),
		mutedLabel("Makes rekordbox OUTPUT its per-deck Play + Cue state to the "+rekordboxmap.DefaultDevice+" virtual MIDI port, which rave-mate reads (deck N → MIDI channel N). The overlay then shows which deck is live + cue - verified live. Needs the virtual port (LoopBe/loopMIDI) installed and the MIDI source enabled below.\nNOTE: rekordbox can only output button/LED state - EQ, fader and volume can't come from rekordbox over MIDI. For those, eavesdrop your controller directly via the MIDI controller card (it reads CC 20/23-28 per deck).\nImport once: Preferences → Controller → MIDI → select the port → IMPORT."),
	)
	return featureCard("Rekordbox MIDI mapping", "rekordbox → rave-mate play/cue output (one-click CSV).", nil, st, body)
}
