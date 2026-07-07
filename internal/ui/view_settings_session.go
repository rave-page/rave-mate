package ui

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/overlaystyle"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/spoutdll"
	"rave.page/mate/internal/traktormap"
	"rave.page/mate/internal/videoshare"
)

func (u *UI) midiCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.MIDI
	names, _ := midi.Ports()

	picker := func(cur string, set func(string)) *widget.Select {
		sel := widget.NewSelect(append([]string{}, names...), func(s string) {
			set(s)
			u.saveCfg()
			if u.svc.Session != nil {
				u.svc.Session.Reconcile()
			}
		})
		sel.PlaceHolder = "(select MIDI input port)"
		if cur != "" && hasStr(names, cur) {
			sel.SetSelected(cur)
		}
		return sel
	}
	customSel := picker(f.CustomPort, func(s string) { f.CustomPort = s })
	denonSel := picker(f.DenonPort, func(s string) { f.DenonPort = s })

	hint := mutedLabel("")
	link := widget.NewHyperlink("Install loopMIDI (free virtual MIDI port)",
		mustURL("https://www.tobias-erichsen.de/software/loopmidi.html"))
	applyPorts := func(n []string) {
		customSel.Options = append([]string{}, n...)
		denonSel.Options = append([]string{}, n...)
		customSel.Refresh()
		denonSel.Refresh()
		if len(n) == 0 {
			hint.SetText("No MIDI input ports found. Install a virtual MIDI port (loopMIDI or LoopBe1), then refresh.")
			link.Show()
		} else {
			hint.SetText("")
			link.Hide()
		}
	}
	applyPorts(names)
	refresh := widget.NewButtonWithIcon("Refresh ports", theme.ViewRefreshIcon(), func() {
		n, _ := midi.Ports()
		applyPorts(n)
	})

	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case f.CustomPort == "" && f.DenonPort == "":
			s.set(colBrandAmber, "no MIDI port selected")
		default:
			src, ok := u.sourceInfo(session.SourceMIDI)
			switch {
			case ok && src.Receiving:
				s.set(colBrandMint, "receiving")
			case ok && src.Running:
				s.set(colBrandMint, "port open")
			default:
				s.set(colBrandAmber, "not running")
			}
		}
	})
	toggle := u.sessionToggle(&f.Enabled)
	// Mesh mirror: live-applies to the peer bridge (broadcast to all paired instances).
	mesh := u.chk("Mirror MIDI to all paired instances", &f.MeshForward, func() {
		if u.svc.PeerBridge != nil {
			u.svc.PeerBridge.SetMIDIMesh(f.MeshForward)
		}
	})
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Custom-map port"), nil, customSel),
		container.NewBorder(nil, nil, widget.NewLabel("Denon-map port"), nil, denonSel),
		container.NewHBox(refresh),
		hint, link,
		mutedLabel("Pick the virtual MIDI port your Traktor mapping outputs to. Toggle off/on to apply a port change."),
		widget.NewSeparator(),
		mesh,
		mutedLabel("A controller plugged into either PC drives both - MIDI input fans out to every connected peer. Loop-safe: peer-received MIDI is never re-forwarded."),
	)
	return featureCard("MIDI controller", "Read deck/mixer state + A/B titles from a Traktor MIDI-out mapping.", toggle, st, body)
}

// traktorMappingCard activates/deactivates controller mappings inside Traktor for the user.
// The auto-backup + "Traktor must be closed" guard live in traktormap; this is just the
// control surface. Denon now; RavePage once authored.
func (u *UI) traktorMappingCard() fyne.CanvasObject {
	tm := u.svc.TraktorMap
	if tm == nil {
		return container.NewVBox()
	}
	status := widget.NewLabel("checking…")
	status.Wrapping = fyne.TextWrapWord
	prog := false
	st := u.newStatus(nil) // pushed by refresh (Traktor probe is async)

	var refresh func()

	// One checkbox per available controller mapping (Denon, Kontrol D2, …) - activate/deactivate
	// the device in Traktor's Controller Manager. D2 is what loads the QML feed surface.
	mappings := tm.Available()
	checks := map[string]*widget.Check{}
	disableAll := func() {
		for _, c := range checks {
			c.Disable()
		}
	}
	for _, mp := range mappings {
		c := widget.NewCheck(mp.Display, nil)
		c.OnChanged = func(on bool) {
			if prog {
				return
			}
			disableAll()
			status.SetText("Working… (don't start Traktor)")
			go func() {
				defer debuglog.Recover(u.svc.Log, "traktor-map", false)
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				var bak string
				var err error
				if on {
					bak, err = tm.Activate(ctx, mp)
				} else {
					bak, err = tm.Deactivate(mp)
				}
				fyne.Do(func() {
					if err != nil {
						u.Notify("rave-mate", err.Error())
					} else {
						verb := map[bool]string{true: "activated", false: "deactivated"}[on]
						msg := mp.Display + " " + verb + "."
						if bak != "" {
							msg += " Backup: " + filepath.Base(bak)
						}
						u.Notify("rave-mate", msg)
					}
					refresh()
				})
			}()
		}
		checks[mp.Key] = c
	}

	// Version picker: which Traktor install to edit. Default Auto = newest (multiple versions
	// coexist under Documents\Native Instruments; editing the wrong one's Settings.tsi is a no-op).
	verSel := widget.NewSelect(nil, nil)
	labelToVer := map[string]string{}
	rebuildVersions := func() {
		installs, _ := tm.Installs()
		autoLabel := "Auto (newest)"
		if len(installs) > 0 {
			autoLabel = "Auto (newest: " + installs[0].Version + ")"
		}
		opts := []string{autoLabel}
		labelToVer = map[string]string{autoLabel: traktormap.AutoVersion}
		for _, in := range installs {
			lbl := "Traktor " + in.Version
			if in.Settings == "" {
				lbl += " - no Settings.tsi"
			}
			opts = append(opts, lbl)
			labelToVer[lbl] = in.Version
		}
		cur := tm.Version()
		selLabel := autoLabel
		for lbl, v := range labelToVer {
			if v != traktormap.AutoVersion && v == cur {
				selLabel = lbl
			}
		}
		verSel.Options = opts
		verSel.OnChanged = nil // suppress the programmatic SetSelected callback
		verSel.SetSelected(selLabel)
		verSel.OnChanged = func(lbl string) {
			v := labelToVer[lbl]
			tm.SetVersion(v)
			u.svc.Cfg.Features.Traktor.MappingVersion = v
			u.saveCfg()
			refresh()
		}
	}
	rebuildVersions()

	refresh = func() {
		// Opportunistically snapshot a capture-mapping (D2) that's present but not yet cached,
		// so the user can re-enable after disabling even if they never exported it.
		for _, mp := range mappings {
			if mp.Capture {
				goUI("traktor-map", func() { tm.CaptureIfPresent(mp) })
			}
		}
		go func() {
			defer debuglog.Recover(u.svc.Log, "traktor-map-status", false)
			inst, installed, ok, err := tm.Status()
			fyne.Do(func() {
				prog = true
				defer func() { prog = false }()
				switch {
				case err != nil:
					status.SetText("Traktor: " + err.Error())
					st.set(colBrandAmber, "Traktor error")
					for _, c := range checks {
						c.Enable()
					}
				case !ok:
					status.SetText("No Traktor install found.")
					st.set(colMuted, "no Traktor install")
					disableAll()
				default:
					n := 0
					for k, c := range checks {
						c.Enable()
						c.SetChecked(installed[k])
						if installed[k] {
							n++
						}
					}
					if n > 0 {
						st.set(colBrandMint, fmt.Sprintf("%d mapping(s) active · Traktor %s", n, inst.Version))
					} else {
						st.set(colMuted, "none active · Traktor "+inst.Version)
					}
					status.SetText("Editing Traktor " + inst.Version + ". Quit Traktor before changing - Settings.tsi is backed up automatically.")
				}
			})
		}()
	}
	refresh()

	rebtn := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() { rebuildVersions(); refresh() })
	items := []fyne.CanvasObject{
		container.NewBorder(nil, nil, widget.NewLabel("Version"), nil, verSel),
	}
	for _, mp := range mappings { // stable order from Available()
		items = append(items, checks[mp.Key])
	}
	items = append(items, status, container.NewHBox(rebtn))
	return featureCard("Traktor mappings",
		"Add/remove controller devices in Traktor for you - auto-backs up Settings.tsi; quit Traktor first. The Kontrol D2 device is what loads the QML feed surface.",
		nil, st, items...)
}

func (u *UI) nmlCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.NML
	col := newEntry()
	col.SetPlaceHolder("(auto-detect newest Traktor install)")
	col.SetText(f.CollectionPath)
	col.OnChanged = func(s string) { f.CollectionPath = s; u.saveCfg() }
	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		src, ok := u.sourceInfo(session.SourceNML)
		switch {
		case ok && src.Receiving:
			s.set(colBrandMint, "enriching from collection")
		case ok && src.Running:
			s.set(colBrandMint, "watching collection")
		default:
			s.set(colBrandAmber, "not running")
		}
	})
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("collection.nml"), nil, filePickerRow(col, ".nml")),
		mutedLabel("Fills album/genre/key/BPM from your Traktor collection - including decks C/D the live feed misses. Toggle off/on to apply a path change."),
	)
	return featureCard("Traktor NML (collection)", "Enrich live tracks with collection-accurate metadata.", toggle, st, body)
}

func (u *UI) recorderCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Recorder
	confirm := newEntry()
	confirm.SetText(strconv.Itoa(f.ResolvedConfirmSeconds()))
	confirm.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			f.ConfirmSeconds = n
			u.saveCfg()
		}
	}
	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		if r := u.svc.Recorder; r != nil {
			if a := r.Active(); a != nil {
				s.set(colBrandBase, fmt.Sprintf("recording · %d tracks", len(a.Tracks)))
				return
			}
		}
		s.set(colBrandMint, "armed")
	})
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Confirm after (seconds)"), nil, confirm),
		mutedLabel("A track must play this long before it's logged (filters quick previews). Auto-records across a live stream. Toggle off/on to apply a threshold change."),
	)
	return featureCard("Session recorder", "Capture an automatic tracklist with per-track timestamps (Publish tab).", toggle, st, body)
}

// Settings cards for the DJ-data aggregation sources + sinks. Toggling any of these saves
// config and calls Session.Reconcile (in saveCfg) so the hub starts/stops the component
// live - no restart needed (except where noted, e.g. an output-dir change).

func (u *UI) nowPlayingFileCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.NowPlayingFile
	dir := newEntry()
	dir.SetPlaceHolder("(app config dir)")
	dir.SetText(f.Dir)
	dir.OnChanged = func(s string) {
		f.Dir = s
		u.saveCfg()
	}
	st := u.onOffStatus(&f.Enabled, "writing on track change")
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Output folder"), nil, folderPickerRow(dir)),
		mutedLabel("Writes now_playing.json + now_playing.txt on each track change. Point an OBS Text/Browser source at these. Toggle off/on to apply a folder change."),
	)
	return featureCard("Now-playing files (OBS)", "Export the current track to disk for overlays.", toggle, st, body)
}

// overlayWebCard configures the live browser overlay server (OBS Browser source). Shows every
// loaded deck with animated faders + cover art; the layout editor drag-positions each deck.
// overlayStyleHintCard points at the live browser editor - the single source of truth for
// overlay appearance (colours, multi-stop gradients, per-band EQ/FX colours, card border/radius).
// Edits there write overlay-style.json, which every output (browser, PNG, Spout/Syphon/PipeWire)
// reads live - so all outputs stay in sync without a second native editor.
func (u *UI) overlayStyleHintCard() fyne.CanvasObject {
	w := &u.svc.Cfg.Features.OverlayWeb
	editURL := func() string { return fmt.Sprintf("http://127.0.0.1:%d/?edit=1", w.ResolvedPort()) }
	editBtn := widget.NewButtonWithIcon("Edit colours & gradients", theme.ColorPaletteIcon(), func() {
		if uri, err := url.Parse(editURL()); err == nil {
			_ = u.app.OpenURL(uri)
		}
	})
	editBtn.Importance = widget.HighImportance
	copyBtn := widget.NewButtonWithIcon("Copy editor URL", theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(editURL())
		u.Notify("rave-mate", "Overlay editor URL copied")
	})
	// "Fade cards by fader" toggle - also editable in the browser overlay editor (same
	// overlay-style.json key). Surgical read/write so browser-owned keys are preserved.
	stylePath, _ := config.DataPath("overlay-style.json")
	faderChk := widget.NewCheck("Fade deck cards by channel fader level", func(v bool) {
		if err := overlaystyle.SetBool(stylePath, "cardFaderReact", v); err != nil {
			u.Notify("rave-mate", "Couldn't save: "+err.Error())
			return
		}
		// Push live to the running overlay server so the browser source updates without a refresh.
		if w.Enabled {
			go func() { _ = overlaystyle.Push(w.ResolvedPort(), stylePath) }()
		}
	})
	faderChk.SetChecked(overlaystyle.GetBool(stylePath, "cardFaderReact", false))
	body := container.NewVBox(
		mutedLabel("All appearance - colours, multi-stop gradients (waveform + background), per-band EQ + per-direction FX colours, and card border/radius - is edited live in the browser overlay's edit mode. Changes save to overlay-style.json and apply instantly to every output: the browser source, the PNG renderer, and Spout/Syphon/PipeWire. Presets are saved server-side, so they survive an OBS cache refresh."),
		container.NewHBox(editBtn, copyBtn),
		widget.NewSeparator(),
		faderChk,
		mutedLabel("Fade-by-fader: each deck card's opacity follows its channel fader, so a deck pulled down fades out (the audible deck stays brightest). Only applies when fader data is available (a controller / Traktor feed); with no fader source, cards stay full. Applies live to every output - PNG/Spout and the browser overlay. Also in the overlay editor → Card → \"fade by fader\"."),
		mutedLabel("The browser overlay feature below must be enabled for the editor to load."),
	)
	return featureCard("Appearance (gradients, colours, borders)", "One editor, every output.", nil, nil, body)
}

func (u *UI) overlayWebCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.OverlayWeb
	urlOf := func() string { return fmt.Sprintf("http://127.0.0.1:%d/", f.ResolvedPort()) }

	port := newEntry()
	port.SetText(strconv.Itoa(f.ResolvedPort()))
	port.OnChanged = func(s string) {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 && n < 65536 {
			f.Port = n
			u.saveCfg()
		}
	}
	openURL := func(suffix string) {
		if uri, err := url.Parse(urlOf() + suffix); err == nil {
			_ = u.app.OpenURL(uri)
		}
	}
	openBtn := widget.NewButtonWithIcon("Open overlay", theme.ComputerIcon(), func() { openURL("") })
	editBtn := widget.NewButtonWithIcon("Layout editor", theme.GridIcon(), func() { openURL("?edit=1") })
	copyBtn := widget.NewButtonWithIcon("Copy URL", theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(urlOf())
		u.Notify("rave-mate", "Overlay URL copied - add a Browser source in OBS")
	})

	st := u.onOffStatus(&f.Enabled, "serving overlay")
	toggle := u.sessionToggle(&f.Enabled)

	// OBS auto-manage: create + maintain the browser source over obs-websocket (no manual add/reload).
	src := &f.OBSSource
	sceneE := newEntry()
	sceneE.SetText(src.ResolvedScene())
	sceneE.OnChanged = func(s string) { src.Scene = strings.TrimSpace(s); u.saveCfg() }
	nestChk := widget.NewCheck("Also add that scene to the current program scene", func(b bool) { src.NestInProgram = b; u.saveCfg() })
	nestChk.SetChecked(src.NestInProgram)
	obsChk := widget.NewCheck("Auto-add & size the browser source in OBS", func(b bool) { src.Enabled = b; u.saveCfg() })
	obsChk.SetChecked(src.Enabled)

	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Port"), nil, port),
		container.NewHBox(openBtn, editBtn, copyBtn),
		mutedLabel("Add a Browser source in OBS at "+urlOf()+" (set its size to your canvas, e.g. 1920×1080), or let rave-mate manage it below. Every loaded deck shows with live faders + cover art. Layout + style edits now apply live (no source reload). Toggle off/on to apply a port change."),
		widget.NewSeparator(),
		obsChk,
		container.NewBorder(nil, nil, widget.NewLabel("OBS scene"), nil, sceneE),
		nestChk,
		mutedLabel("Auto-manage needs the OBS connection (below) enabled + OBS running. Creates a browser source sized to your OBS canvas in a dedicated scene; applies on the next OBS (re)connect - toggle the OBS connection off/on to apply now."),
	)
	return featureCard("Live overlay (browser)", "Animated multi-deck overlay for an OBS Browser source.", toggle, st, body)
}

// overlayPngCard configures the native per-deck PNG renderer: one deck_X.png per loaded deck,
// for an OBS Image source per deck (no browser). Dir change applies live (no restart).
func (u *UI) overlayPngCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.OverlayPNG
	dir := newEntry()
	dir.SetPlaceHolder("(app config dir / overlay-png)")
	dir.SetText(f.Dir)
	dir.OnChanged = func(s string) {
		f.Dir = s
		u.saveCfg()
	}
	openBtn := widget.NewButtonWithIcon("Open folder", theme.FolderOpenIcon(), func() {
		p := f.Dir
		if p == "" {
			p, _ = config.DataPath("overlay-png")
		}
		openDir(p)
	})
	st := u.onOffStatus(&f.Enabled, "rendering deck cards")
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Output folder"), nil, folderPickerRow(dir)),
		container.NewHBox(openBtn),
		mutedLabel("Renders deck_A.png … deck_D.png on change (transparent background, cover art + faders/EQ). Add one OBS Image source per deck pointed at each file. A cued-but-not-yet-played deck stays hidden until faded in once."),
	)
	return featureCard("Per-deck PNG cards (OBS)", "Native per-deck overlay images - one OBS Image source per deck.", toggle, st, body)
}

// overlayObsCard configures the obs-websocket renderer: rave-mate creates/updates a text + image
// input per loaded deck directly in OBS's current scene. Reuses the OBS feature's connection.
func (u *UI) overlayObsCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.OverlayOBS
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case !u.svc.Cfg.Features.OBS.Enabled:
			s.set(colBrandAmber, "enable OBS connection first")
		case u.svc.OBS != nil && u.svc.OBS.Status().Connected:
			s.set(colBrandMint, "driving OBS inputs")
		default:
			s.set(colBrandAmber, "OBS not connected")
		}
	})
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		mutedLabel("Drives OBS directly over obs-websocket: creates \"RaveMate Deck X\" text + image inputs in the current scene per loaded deck, positioned in a stacked layout. Requires the OBS connection (above) enabled + OBS running. No browser/file source needed. Toggle off to leave the inputs (hidden) in OBS for you to delete."),
	)
	return featureCard("OBS direct (obs-websocket)", "Render deck info as native OBS inputs - no browser source.", toggle, st, body)
}

// overlayVideoShareCard configures the GPU/IPC video-share sink (Spout/Syphon/PipeWire). The
// transport is compiled in; the default build has no backend (publishes nothing).
func (u *UI) overlayVideoShareCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.VideoShare
	backend := videoshare.Backend()
	hasBackend := backend != "none"
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case !hasBackend:
			s.set(colBrandAmber, "no backend in this build")
		default:
			s.set(colBrandMint, "sharing via "+backend)
		}
	})
	toggle := u.sessionToggle(&f.Enabled)
	note := "Publishes each loaded deck's card as a live video frame named \"" + videoshare.SenderName("A") +
		"\" … over the OS-native sharing API, so OBS/Resolume/TouchDesigner/VRChat can pull deck visuals from memory - no file or window capture."
	if hasBackend {
		note += " This build shares via " + backend + "."
	} else {
		note += " This build has no video-share backend compiled in (Windows=Spout, macOS=Syphon, Linux=PipeWire) - toggling on runs the sink but publishes nothing. Rebuild with the platform tag + SDK to enable."
	}
	// Render scale: supersample so the card stays crisp when shown large (e.g. on a 4K canvas).
	scaleOpts := []string{"1× (360×120)", "2× (720×240)", "3× (1080×360)", "4× (1440×480)", "6× (2160×720)"}
	scaleVal := map[string]int{scaleOpts[0]: 1, scaleOpts[1]: 2, scaleOpts[2]: 3, scaleOpts[3]: 4, scaleOpts[4]: 6}
	scaleLbl := map[int]string{1: scaleOpts[0], 2: scaleOpts[1], 3: scaleOpts[2], 4: scaleOpts[3], 6: scaleOpts[4]}
	scaleSel := widget.NewSelect(scaleOpts, func(s string) {
		if v, ok := scaleVal[s]; ok {
			f.RenderScale = v
			u.saveCfg()
		}
	})
	if lbl, ok := scaleLbl[f.ResolvedRenderScale()]; ok {
		scaleSel.SetSelected(lbl)
	} else {
		scaleSel.SetSelected(scaleOpts[1])
	}

	body := container.NewVBox(
		mutedLabel(note),
		container.NewBorder(nil, nil, widget.NewLabel("Render scale"), nil, scaleSel),
		mutedLabel("Higher scale = sharper when the receiver displays the card large (e.g. full-screen on a 4K canvas), at more CPU. Toggle off/on to apply a scale change."),
	)
	// Spout runtime DLL: only the Spout backend needs it, and it's loaded at runtime so its
	// absence just disables this feature. Offer a one-click install (+ manual link) like ffmpeg.
	if backend == "Spout" {
		body.Add(widget.NewSeparator())
		body.Add(u.spoutRuntimeControls())
	}
	return featureCard("Video share (Spout/Syphon/PipeWire)", "Share deck visuals GPU-to-GPU for any compatible receiver.", toggle, st, body)
}

// spoutRuntimeControls renders the SpoutLibrary.dll detect + download/install UI (mirrors
// mediaToolControls): shows where the DLL was found, a one-click download+SHA-verify+install into
// the managed bin dir, a progress bar, and a manual-download fallback link.
func (u *UI) spoutRuntimeControls() fyne.CanvasObject {
	status := mutedLabel("")
	prog := widget.NewProgressBar()
	prog.Hide()
	link := widget.NewHyperlink("Open the Spout SDK download page", mustURL(spoutdll.HomePage))
	var installBtn *widget.Button

	apply := func() {
		if st := spoutdll.Probe(); st.Installed {
			status.SetText("Installed: " + st.Path)
			installBtn.SetText("Re-download & install")
			link.Hide()
		} else {
			status.SetText("SpoutLibrary.dll not found - install it to enable Spout output (the app runs fine without it).")
			installBtn.SetText("Download & install")
			link.Show()
		}
	}

	installBtn = widget.NewButtonWithIcon("Download & install", theme.DownloadIcon(), func() {
		installBtn.Disable()
		prog.SetValue(0)
		prog.Show()
		go func() {
			defer debuglog.Recover(u.svc.Log, "spoutdll-install", false)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()
			last := -1
			err := spoutdll.Install(ctx, func(done, total int64) {
				if total <= 0 {
					return
				}
				pct := int(float64(done) / float64(total) * 100)
				if pct == last {
					return
				}
				last = pct
				fyne.Do(func() { prog.SetValue(float64(pct) / 100) })
			})
			fyne.Do(func() {
				prog.Hide()
				installBtn.Enable()
				apply()
			})
			if err != nil {
				u.Notify("rave-mate", "Spout runtime install failed: "+err.Error())
			} else {
				u.Notify("rave-mate", "Spout runtime installed - toggle Video share off/on to use it.")
			}
		}()
	})
	if !spoutdll.CanInstall() {
		installBtn.Disable()
	}
	apply()
	return container.NewVBox(
		mutedLabel("Spout needs SpoutLibrary.dll. Bundled with the installer; if it's missing (e.g. after a self-update) install it here - downloads the pinned Spout2 SDK, SHA-256-verified, into the app's bin folder."),
		status,
		container.NewHBox(installBtn),
		prog,
		link,
	)
}

// overlayWaveformCard configures the scrolling-waveform + combined EQ/FX panel shared by all the
// overlay renderers (browser, PNG, video-share). Zoom + playhead + colours + opacities apply live.
func (u *UI) overlayWaveformCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.OverlayWaveform

	zoomOpts := []string{"8 s", "12 s", "16 s", "20 s", "30 s", "45 s", "60 s"}
	zoomVal := map[string]float64{"8 s": 8, "12 s": 12, "16 s": 16, "20 s": 20, "30 s": 30, "45 s": 45, "60 s": 60}
	zoomSel := widget.NewSelect(zoomOpts, func(s string) {
		if v, ok := zoomVal[s]; ok {
			f.ZoomSeconds = v
			u.saveCfg()
		}
	})
	zoomSel.SetSelected(fmt.Sprintf("%g s", f.ResolvedZoomSeconds()))

	phOpts := []string{"Left quarter (1/4)", "Left third (1/3)", "Center (1/2)", "Right quarter (3/4)"}
	phVal := map[string]float64{phOpts[0]: 0.25, phOpts[1]: 1.0 / 3.0, phOpts[2]: 0.5, phOpts[3]: 0.75}
	phSel := widget.NewSelect(phOpts, func(s string) {
		if v, ok := phVal[s]; ok {
			f.PlayheadPct = v
			u.saveCfg()
		}
	})
	switch cur := f.ResolvedPlayheadPct(); {
	case cur < 0.29:
		phSel.SetSelected(phOpts[0])
	case cur < 0.42:
		phSel.SetSelected(phOpts[1])
	case cur < 0.6:
		phSel.SetSelected(phOpts[2])
	default:
		phSel.SetSelected(phOpts[3])
	}

	hexEntry := func(get func() string, set func(string)) *widget.Entry {
		e := newEntry()
		e.SetText(get())
		e.OnChanged = func(s string) {
			s = strings.TrimSpace(s)
			if validHexColor(s) {
				set(s)
				u.saveCfg()
			}
		}
		return e
	}
	waveColor := hexEntry(f.ResolvedWaveColor, func(s string) { f.WaveColor = s })
	bgColor := hexEntry(f.ResolvedBgColor, func(s string) { f.BgColor = s })

	opacityRow := func(label string, get func() float64, set func(float64)) fyne.CanvasObject {
		val := widget.NewLabel(fmt.Sprintf("%d%%", int(get()*100+0.5)))
		sl := widget.NewSlider(0, 1)
		sl.Step = 0.05
		sl.SetValue(get())
		sl.OnChanged = func(v float64) {
			set(v)
			val.SetText(fmt.Sprintf("%d%%", int(v*100+0.5)))
			u.saveCfg()
		}
		return container.NewBorder(nil, nil, widget.NewLabel(label), val, sl)
	}

	st := u.onOffStatus(&f.Enabled, "waveform on")
	toggle := u.sessionToggle(&f.Enabled)
	body := container.NewVBox(
		mutedLabel("Adds a scrolling waveform behind the EQ curve on every deck card, with the filter shown as a cutoff curve (HP cuts the low end from the left, LP the high end from the right). Browser + video-share scroll smoothly; PNG updates on change. Peaks are generated on first play (ffmpeg) and cached."),
		container.NewBorder(nil, nil, widget.NewLabel("Zoom (visible)"), nil, zoomSel),
		container.NewBorder(nil, nil, widget.NewLabel("Playhead"), nil, phSel),
		container.NewBorder(nil, nil, widget.NewLabel("Waveform color"), nil, waveColor),
		opacityRow("Waveform opacity", f.ResolvedWaveOpacity, func(v float64) { f.WaveOpacity = v }),
		container.NewBorder(nil, nil, widget.NewLabel("Background color"), nil, bgColor),
		opacityRow("Background opacity", f.ResolvedBgOpacity, func(v float64) { f.BgOpacity = v }),
		mutedLabel("Colors are #rrggbb defaults - the browser editor (Appearance card above) overrides them for every output with live gradients + per-band colours. The EQ curve sits over a dark halo so it stays legible even when it matches the waveform colour. Smaller zoom = the waveform moves faster."),
	)
	return featureCard("Waveform panel", "Scrolling waveform + combined EQ/FX on the deck cards.", toggle, st, body)
}

// validHexColor reports whether s is a #rgb or #rrggbb colour.
func validHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
