package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/vrdll"
	"rave.page/mate/internal/vroverlay"
)

// vrOverlayCard configures VR overlays (OpenVR/SteamVR): chat/alert panels rendered into the
// headset, fed by the event bus (so a VR PC shows another instance's Twitch chat).
func (u *UI) vrOverlayCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.VROverlay
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case u.svc.VROverlay != nil && u.svc.VROverlay.Available():
			s.set(colBrandMint, "active - SteamVR connected")
		case !vroverlay.BuiltWithVR():
			s.set(colBrandAmber, "non-vr build - reinstall from the installer")
		case u.svc.VROverlay != nil:
			s.set(colBrandAmber, "enabled - waiting for SteamVR (or openvr_api.dll missing)")
		default:
			s.set(colBrandAmber, "enabled")
		}
	})
	toggle := u.moduleToggle("vroverlay", &f.Enabled)

	count := widget.NewLabel("")
	refreshCount := func() { count.SetText(fmt.Sprintf("%d overlay(s) configured", len(f.Overlays))) }
	refreshCount()
	manage := widget.NewButtonWithIcon("Overlays…", theme.SettingsIcon(), func() {
		u.vrManageOverlaysDialog(refreshCount)
	})

	handSel := widget.NewSelect([]string{"left", "right"}, func(s string) { f.EditHand = s; u.saveCfg() })
	handSel.SetSelected(f.ResolvedEditHand())

	// One clear "how do I open the editor" control. Summon = a face button read via SteamVR Input
	// (works on Index/Touch, unlike the old legacy button poll). Hold to open; long-hold means a quick
	// in-game press won't trigger it. "Custom" leaves it unbound so you assign it in SteamVR.
	const optAX, optBY, optCustom = "A / X button", "B / Y button", "Custom (set in SteamVR)"
	openSel := widget.NewSelect([]string{optAX, optBY, optCustom}, func(s string) {
		switch s {
		case optBY:
			f.SummonButton = "by"
		case optCustom:
			f.SummonButton = "custom"
		default:
			f.SummonButton = "ax"
		}
		f.SummonOn = true
		u.saveCfg()
	})
	switch f.ResolvedSummonButton() {
	case "by":
		openSel.SetSelected(optBY)
	case "custom":
		openSel.SetSelected(optCustom)
	default:
		openSel.SetSelected(optAX)
	}
	tapHides := widget.NewCheck("Short tap of that button shows / hides the overlays", func(v bool) { f.SummonTapHides = v; u.saveCfg() })
	tapHides.SetChecked(f.SummonTapHides)

	autoStart := widget.NewCheck("Start automatically with SteamVR", func(v bool) {
		f.AutoStart = v
		u.saveCfg()
	})
	autoStart.SetChecked(f.AutoStart)

	// Opens SteamVR's controller-binding editor for rave-mate (the same screen OVRAS uses) so any input
	// can be rebound. Needs SteamVR running + the vr build (input loaded).
	openBindings := widget.NewButton("Open rave-mate controller bindings (in headset)", func() {
		if u.svc.VROverlay == nil {
			return
		}
		if err := u.svc.VROverlay.OpenBindingUI(); err != nil {
			u.Notify("VR bindings", "Couldn't open bindings - start SteamVR + the VR build first.")
		} else {
			u.Notify("VR bindings", "Opened in the headset (SteamVR controller bindings).")
		}
	})

	// Binding-health warning: the action manifest loaded but SteamVR has NO bindings for our action set
	// (a stale custom binding overriding our defaults) → summon/pointer/grab silently do nothing. Amber
	// row (WarningImportance → brand-amber theme token) + a one-click fix, shown only when Unbound.
	bindWarn := widget.NewLabel("SteamVR has no controller bindings for rave-mate - open bindings and pick 'rave-mate default'.")
	bindWarn.Importance = widget.WarningImportance
	bindWarn.Wrapping = fyne.TextWrapWord
	bindWarnBtn := widget.NewButtonWithIcon("Fix bindings (open in headset)", theme.WarningIcon(), func() {
		if u.svc.VROverlay == nil {
			return
		}
		if err := u.svc.VROverlay.OpenBindingUI(); err != nil {
			u.Notify("VR bindings", "Couldn't open bindings - start SteamVR + the VR build first.")
		} else {
			u.Notify("VR bindings", "Opened in the headset - pick the 'rave-mate default' binding.")
		}
	})
	bindWarnRow := container.NewVBox(bindWarn, container.NewHBox(bindWarnBtn))
	bindWarnRow.Hide()
	refreshBindWarn := func() {
		if u.svc.VROverlay != nil && u.svc.VROverlay.BindingStatus() == vroverlay.BindingUnbound {
			bindWarnRow.Show()
		} else {
			bindWarnRow.Hide()
		}
	}
	refreshBindWarn()

	// Dedicated bindable buttons for the two default-UNBOUND actions (open/close editor, show/hide
	// overlays). Each row shows its live SteamVR bind + a one-click Bind. SteamVR can't deep-link a
	// single action, so both buttons open the same binding screen (all actions listed) - expected.
	openBindingUI := func() {
		if u.svc.VROverlay == nil {
			return
		}
		if err := u.svc.VROverlay.OpenBindingUI(); err != nil {
			u.Notify("VR bindings", "Couldn't open bindings - start SteamVR + the VR build first.")
		} else {
			u.Notify("VR bindings", "Opened in the headset (SteamVR controller bindings).")
		}
	}
	editorBind := widget.NewLabel("")
	editorBind.Wrapping = fyne.TextWrapWord
	overlaysBind := widget.NewLabel("")
	overlaysBind.Wrapping = fyne.TextWrapWord
	setActionBind := func(l *widget.Label, action string) {
		var b string
		if u.svc.VROverlay != nil {
			b = u.svc.VROverlay.ActionBinding(action)
		}
		if b != "" {
			l.SetText("Bound: " + b)
			l.Importance = widget.SuccessImportance
		} else {
			l.SetText("Not bound - press Bind")
			l.Importance = widget.LowImportance
		}
		l.Refresh()
	}
	refreshActionBinds := func() {
		setActionBind(editorBind, vroverlay.ActionToggleEditor)
		setActionBind(overlaysBind, vroverlay.ActionToggleOverlays)
	}
	refreshActionBinds()
	editorBindRow := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Open / close editor"),
			widget.NewButtonWithIcon("Bind in SteamVR", theme.SettingsIcon(), openBindingUI), nil),
		editorBind)
	overlaysBindRow := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Show / hide overlays"),
			widget.NewButtonWithIcon("Bind in SteamVR", theme.SettingsIcon(), openBindingUI), nil),
		overlaysBind)

	// VR-View capture (opt-in): allow `ctl screenshot-vr` (local + from a paired peer) to grab the
	// SteamVR VR-View mirror window - so a desk instance / support can see what the headset shows.
	vrViewCapture := widget.NewCheck("Allow VR-View screenshots (capture the SteamVR VR-View window)", func(v bool) {
		f.VRViewCapture = v
		u.saveCfg()
	})
	vrViewCapture.SetChecked(f.VRViewCapture)

	// Motion capture → VRChat OSC target (P3). Record SteamVR tracker/HMD motion in VR, replay to
	// VRChat over OSC. Controls live in the in-VR menu's MOTION page; this just sets the OSC address.
	oscEntry := widget.NewEntry()
	oscEntry.SetPlaceHolder("127.0.0.1:9000")
	oscEntry.SetText(f.OSCAddr)
	oscEntry.OnChanged = func(s string) { f.OSCAddr = s; u.saveCfg() }

	// VMC (VTuber): stream HMD+controller+tracker devices to a VMC receiver; it does the IK and
	// animates the avatar. The real motion-playback path (unlike VRChat OSC trackers).
	vmcEntry := widget.NewEntry()
	vmcEntry.SetPlaceHolder("127.0.0.1:39539")
	vmcEntry.SetText(f.VMCAddr)
	vmcEntry.OnChanged = func(s string) { f.VMCAddr = s; u.saveCfg() }
	vmcLive := widget.NewCheck("Stream live VR motion to VMC (VTuber)", func(v bool) {
		f.VMCLive = v
		u.saveCfg()
	})
	vmcLive.SetChecked(f.VMCLive)

	// Live VR performance monitor (this PC + any peer publishing vr.perf over the bus).
	perf := widget.NewLabel("VR performance: (no data yet)")
	perf.Wrapping = fyne.TextWrapWord
	if u.svc.VRStats != nil {
		stop := make(chan struct{})
		refresh := func() {
			refreshBindWarn()    // re-evaluate binding health on the same cadence (SteamVR may connect later)
			refreshActionBinds() // live-update the two action bind labels (updates after the user binds in SteamVR)
			insts := u.svc.VRStats.Snapshot()
			if len(insts) == 0 {
				perf.SetText("VR performance: no instance reporting (start the `vr` build with SteamVR, or pair the instance running VR)")
				return
			}
			var b strings.Builder
			for _, in := range insts {
				who := in.Origin
				if in.Local {
					who = "this PC"
				}
				if !in.Connected {
					fmt.Fprintf(&b, "%s: SteamVR not connected\n", who)
					continue
				}
				fmt.Fprintf(&b, "%s [%s] %.0f/%.0f fps · frame %.1fms · gpu %.1fms · drop %d · reproj %d%s · %d overlay(s)\n",
					who, in.HMDModel, in.FPS, in.DisplayHz, in.FrameMs, in.GpuMs, in.Dropped, in.Reprojected,
					reproSuffix(in.Reprojecting), in.Overlays)
			}
			perf.SetText(strings.TrimRight(b.String(), "\n"))
		}
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					fyne.Do(refresh)
				}
			}
		}()
		u.closers = append(u.closers, func() { close(stop) })
	}

	body := container.NewVBox(
		mutedLabel("Floating Twitch chat / alerts, an OBS stream + recording cockpit, and live viewer count in your headset. Content rides the event bus, so this PC can show another instance's Twitch/OBS - no local Twitch or OBS needed."),
		mutedLabel("Needs SteamVR running + the `vr` build. Restart SteamVR after changing the open button below so it reloads the bindings."),
		u.vrRuntimeControls(),

		widget.NewSeparator(),
		smallCaps("In-headset controls"),
		bindWarnRow,
		container.NewBorder(nil, nil, widget.NewLabel("Open the editor:"), nil, openSel),
		mutedLabel("Hold the button to open/close the editor. A quick press still works in-game - only a long hold opens rave-mate. Or open it from the SteamVR dashboard → rave-mate tab (no button needed)."),
		tapHides,
		container.NewBorder(nil, nil, widget.NewLabel("Edit badge on wrist:"), nil, handSel),
		autoStart,
		openBindings,
		mutedLabel("Optional: bind dedicated buttons to these actions (unbound by default - the summon button above already opens the editor). Press Bind, then in SteamVR assign any input/combo to 'Open / close editor' or 'Show / hide overlays'."),
		editorBindRow,
		overlaysBindRow,
		mutedLabel("In the editor: grip = grab/move a panel, thumbstick = push/pull. These only act while editing, so they never disturb the game. Rebind anything (grab, push/pull, open, show/hide) in SteamVR → Controllers → rave-mate, or via the button above."),

		widget.NewSeparator(),
		smallCaps("Motion & capture"),
		vrViewCapture,
		mutedLabel("Lets `ctl screenshot-vr` (and a paired peer) capture SteamVR's VR-View window - for support/debugging the headset view. Open SteamVR → Display VR View first."),
		container.NewBorder(nil, nil, widget.NewLabel("VRChat OSC:"), nil, oscEntry),
		mutedLabel("VRChat's OSC in-port (default 127.0.0.1:9000). Used to replay recorded motion as FBT trackers + apply camera-path presets. Enable OSC in VRChat."),
		container.NewBorder(nil, nil, widget.NewLabel("VMC (VTuber):"), nil, vmcEntry),
		vmcLive,
		mutedLabel("Streams your real VR motion to a VTuber renderer (VSeeFace / Warudo / VNyan) - it does the IK and animates your avatar. 'Stream live' performs straight in; the Motion Studio can also stream a recorded take. Default port 127.0.0.1:39539."),

		widget.NewSeparator(),
		perf,
		mutedLabel("Live VR frame timing (this PC + any paired instance running VR, over the bus). Also `rave-mate ctl vrperf`."),
		container.NewBorder(nil, nil, count, container.NewHBox(
			widget.NewButtonWithIcon("Motion studio…", theme.MediaPlayIcon(), func() { u.motionStudioDialog() }),
			widget.NewButtonWithIcon("Keybinds…", theme.SettingsIcon(), func() { u.keybindsDialog() }),
			widget.NewButtonWithIcon("Layouts…", theme.StorageIcon(), func() { u.vrLayoutsDialog() }), manage),
			widget.NewLabel("")),
		container.NewHBox(
			widget.NewButtonWithIcon("Wrist buttons…", theme.GridIcon(), func() { u.vrQuickButtonsDialog() }),
			widget.NewButtonWithIcon("World layouts…", theme.HomeIcon(), func() { u.vrWorldLayoutsDialog() })),
		mutedLabel("Wrist buttons: extra quick actions on the in-headset wrist strip. World layouts: bind a saved layout to a VRChat world and auto-apply (or get a notice) when you join it."),
	)
	return featureCard("VR overlays", "Twitch chat + alerts in your headset (OpenVR).", toggle, st, body)
}

// vrRuntimeControls renders the openvr_api.dll detect + download/install UI (mirrors
// spoutRuntimeControls): the in-app updater historically swapped only the exe, so a self-updated
// install can end up vr-capable but DLL-less ("waiting for vr build"). One click fetches the DLL
// from this build's feed (SHA-verified) and drops it beside the exe; a restart picks it up.
func (u *UI) vrRuntimeControls() fyne.CanvasObject {
	status := mutedLabel("")
	prog := widget.NewProgressBar()
	prog.Hide()
	link := widget.NewHyperlink("Open the OpenVR download page", mustURL(vrdll.HomePage))
	var installBtn *widget.Button

	apply := func() {
		if !vroverlay.BuiltWithVR() {
			status.SetText("This is a non-vr build - reinstall from the installer to get VR support.")
			installBtn.Disable()
			link.Show()
			return
		}
		if st := vrdll.Probe(); st.Installed {
			status.SetText("Installed: " + st.Path)
			installBtn.SetText("Re-download & install")
			link.Hide()
		} else {
			status.SetText("openvr_api.dll not found beside the app - install it to enable VR (the app runs fine without it).")
			installBtn.SetText("Install VR runtime")
			link.Show()
		}
	}

	installBtn = widget.NewButtonWithIcon("Install VR runtime", theme.DownloadIcon(), func() {
		installBtn.Disable()
		prog.SetValue(0)
		prog.Show()
		go func() {
			defer debuglog.Recover(u.svc.Log, "vrdll-install", false)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()
			last := -1
			err := vrdll.Install(ctx, version.FeedURL, func(done, total int64) {
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
				u.Notify("rave-mate", "VR runtime install failed: "+err.Error())
			} else {
				u.Notify("rave-mate", "VR runtime installed - restart rave-mate to enable VR.")
			}
		}()
	})
	if !vrdll.CanInstall() {
		installBtn.Disable()
	}
	apply()
	return container.NewVBox(
		mutedLabel("VR needs openvr_api.dll beside the app. Bundled with the installer; if it's missing (e.g. after a self-update) install it here - downloads the pinned DLL from this build's feed, SHA-256-verified. Restart afterwards."),
		status,
		container.NewHBox(installBtn),
		prog,
		link,
	)
}

// vrLayoutsDialog manages named overlay layouts: save current, load, rename, delete, import/export.
func (u *UI) vrLayoutsDialog() {
	f := &u.svc.Cfg.Features.VROverlay
	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.Layouts {
			idx := i
			L := &f.Layouts[i]
			load := widget.NewButtonWithIcon("", theme.MediaPlayIcon(), func() { u.vrLoadLayout(idx) })
			ren := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
				e := newEntry()
				e.SetText(f.Layouts[idx].Name)
				dialog.NewCustomConfirm("Rename layout", "Save", "Cancel", e, func(ok bool) {
					if ok && e.Text != "" {
						f.Layouts[idx].Name = e.Text
						u.saveCfg()
						rebuild()
					}
				}, u.win).Show()
			})
			exp := widget.NewButtonWithIcon("", theme.UploadIcon(), func() { u.vrExportLayout(f.Layouts[idx]) })
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.Layouts = append(f.Layouts[:idx], f.Layouts[idx+1:]...)
				u.saveCfg()
				rebuild()
			})
			row := container.NewBorder(nil, nil, nil, container.NewHBox(load, ren, exp, del),
				widget.NewLabel(fmt.Sprintf("%s - %d overlay(s)", L.Name, len(L.Overlays))))
			list.Add(row)
		}
		if len(f.Layouts) == 0 {
			list.Add(mutedLabel("No layouts saved. Save the current arrangement below or import one."))
		}
		list.Refresh()
	}
	rebuild()
	saveCur := widget.NewButtonWithIcon("Save current as…", theme.ContentAddIcon(), func() {
		e := newEntry()
		e.SetPlaceHolder("Layout name")
		dialog.NewCustomConfirm("Save layout", "Save", "Cancel", e, func(ok bool) {
			if !ok {
				return
			}
			name := e.Text
			if name == "" {
				name = fmt.Sprintf("Layout %d", len(f.Layouts)+1)
			}
			f.Layouts = append(f.Layouts, u.vrSnapshotLayout(name))
			u.saveCfg()
			rebuild()
		}, u.win).Show()
	})
	imp := widget.NewButtonWithIcon("Import…", theme.DownloadIcon(), func() { u.vrImportLayout(rebuild) })
	content := container.NewBorder(nil, container.NewHBox(saveCur, imp), nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("VR overlay layouts", "Done", content, u.win)
	d.Resize(fyne.NewSize(600, 480))
	d.Show()
}

// vrSnapshotLayout captures the current overlays + menu placement as a named layout.
func (u *UI) vrSnapshotLayout(name string) config.VRLayout {
	f := u.svc.Cfg.Features.VROverlay
	return config.VRLayout{
		Name: name, Overlays: append([]config.VROverlay(nil), f.Overlays...),
		MenuSnap: f.MenuSnap, MenuX: f.MenuX, MenuY: f.MenuY, MenuZ: f.MenuZ,
		MenuYaw: f.MenuYaw, MenuPitch: f.MenuPitch, MenuWidth: f.MenuWidth, MenuBg: f.MenuBg,
	}
}

// vrLoadLayout applies a saved layout to the live config.
func (u *UI) vrLoadLayout(idx int) {
	f := &u.svc.Cfg.Features.VROverlay
	if idx < 0 || idx >= len(f.Layouts) {
		return
	}
	L := f.Layouts[idx]
	f.Overlays = append([]config.VROverlay(nil), L.Overlays...)
	f.MenuSnap, f.MenuX, f.MenuY, f.MenuZ = L.MenuSnap, L.MenuX, L.MenuY, L.MenuZ
	f.MenuYaw, f.MenuPitch, f.MenuWidth, f.MenuBg = L.MenuYaw, L.MenuPitch, L.MenuWidth, L.MenuBg
	u.saveCfg()
	u.Notify("VR overlays", "Loaded layout "+L.Name)
}

// vrExportLayout writes a layout to a JSON file the user picks.
func (u *UI) vrExportLayout(L config.VRLayout) {
	dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil || w == nil {
			return
		}
		defer func() { _ = w.Close() }()
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(L); err != nil {
			u.Notify("VR overlays", "Export failed: "+err.Error())
		}
	}, u.win).Show()
}

// vrImportLayout reads a layout JSON file the user picks and appends it.
func (u *UI) vrImportLayout(onChange func()) {
	dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		defer func() { _ = r.Close() }()
		var L config.VRLayout
		if err := json.NewDecoder(r).Decode(&L); err != nil {
			u.Notify("VR overlays", "Import failed: "+err.Error())
			return
		}
		if L.Name == "" {
			L.Name = "Imported"
		}
		f := &u.svc.Cfg.Features.VROverlay
		f.Layouts = append(f.Layouts, L)
		u.saveCfg()
		onChange()
		u.Notify("VR overlays", "Imported layout "+L.Name)
	}, u.win).Show()
}

// vrManageOverlaysDialog lists VR overlays with add/edit/remove.
func (u *UI) vrManageOverlaysDialog(onChange func()) {
	f := &u.svc.Cfg.Features.VROverlay
	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.Overlays {
			o := &f.Overlays[i]
			edit := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
				u.vrEditOverlayDialog(o, func() { rebuild(); onChange() })
			})
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.Overlays = append(f.Overlays[:i], f.Overlays[i+1:]...)
				u.saveCfg()
				rebuild()
				onChange()
			})
			state := "on"
			if !o.Enabled {
				state = "off"
			}
			row := container.NewBorder(nil, nil, nil, container.NewHBox(edit, del),
				widget.NewLabel(fmt.Sprintf("%s [%s] - %s, %.2fm", o.ID, o.Type, snapLabel(o.SnapTo), o.ResolvedWidthM())+" ("+state+")"))
			list.Add(row)
		}
		if len(f.Overlays) == 0 {
			list.Add(mutedLabel("No overlays yet. Add a chat or alerts panel below."))
		}
		list.Refresh()
	}
	rebuild()
	add := widget.NewButtonWithIcon("Add overlay", theme.ContentAddIcon(), func() {
		f.Overlays = append(f.Overlays, config.VROverlay{
			ID: fmt.Sprintf("ov%d", time.Now().Unix()), Type: "chat", Enabled: true,
			Y: 1.4, Z: -1.0, WidthM: 0.5, Opacity: 0.9, MaxMessages: 8,
		})
		u.saveCfg()
		rebuild()
		onChange()
	})
	content := container.NewBorder(nil, add, nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("VR overlays", "Done", content, u.win)
	d.Resize(fyne.NewSize(600, 480))
	d.Show()
}

// vrEditOverlayDialog edits one overlay's type/placement/style.
func (u *UI) vrEditOverlayDialog(o *config.VROverlay, onSave func()) {
	typeSel := widget.NewSelect([]string{"chat", "alerts", "obs", "viewers", "viewerlist", "perf", "network", "timing"}, nil)
	typeSel.SetSelected(orDefault(o.Type, "chat"))
	typeHelp := helpIcon("Live-stats panels (summon them in-headset too): perf = app + system CPU/RAM and VR fps/frametime/reprojection; network = peer + rave.page byte rates; timing = round-trip ping to a paired instance. They update ~2x/sec and grab/move/resize like any overlay.")
	snapSel := widget.NewSelect([]string{"world", "left controller", "right controller", "head (visor)"}, nil)
	snapSel.SetSelected(snapLabel(o.SnapTo))
	enabled := widget.NewCheck("Enabled", nil)
	enabled.SetChecked(o.Enabled)
	alwaysShow := widget.NewCheck("Always visible (ignore global hide)", nil)
	alwaysShow.SetChecked(o.AlwaysShow)
	placeholder := widget.NewCheck("Show placeholder when empty (chat/alerts)", nil)
	placeholder.SetChecked(!o.HidePlaceholder)

	x := numEntry(o.X)
	y := numEntry(o.Y)
	z := numEntry(o.Z)
	yaw := numEntry(o.Yaw)
	width := numEntry(o.ResolvedWidthM())
	opacity := numEntry(o.ResolvedOpacity())
	bgOpacity := numEntry(o.ResolvedBgOpacity())
	maxMsg := numEntry(float64(o.ResolvedMaxMessages()))

	form := widget.NewForm(
		widget.NewFormItem("Type", container.NewBorder(nil, nil, nil, typeHelp, typeSel)),
		widget.NewFormItem("Enabled", enabled),
		widget.NewFormItem("Lock", alwaysShow),
		widget.NewFormItem("Placeholder", placeholder),
		widget.NewFormItem("Anchor", snapSel),
		widget.NewFormItem("X (m)", x),
		widget.NewFormItem("Y (m)", y),
		widget.NewFormItem("Z (m)", z),
		widget.NewFormItem("Yaw (°)", yaw),
		widget.NewFormItem("Width (m)", width),
		widget.NewFormItem("Opacity 0–1", opacity),
		widget.NewFormItem("Background 0–1", bgOpacity),
		widget.NewFormItem("Max messages", maxMsg),
	)
	content := container.NewVScroll(container.NewVBox(form,
		mutedLabel("Type: chat / alerts = Twitch chat + follow/sub/bit alerts; obs = stream/recording cockpit (bitrate + connection health); viewers = live viewer count; viewerlist = current chatters. OBS + viewer panels read from any connected instance over the bus."),
		mutedLabel("Live stats: perf = app + system CPU/RAM and VR fps/frametime/reprojection; network = peer + rave.page byte rates; timing = round-trip ping to a paired instance. All refresh ~2x/sec."),
		mutedLabel("Anchor: world = fixed in room space; left/right = follows that controller. Z is forward (negative = in front of you).")))
	d := dialog.NewCustomConfirm("Edit overlay", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		o.Type = typeSel.Selected
		o.Enabled = enabled.Checked
		o.AlwaysShow = alwaysShow.Checked
		o.HidePlaceholder = !placeholder.Checked
		o.SnapTo = snapValue(snapSel.Selected)
		o.X, o.Y, o.Z = parseF(x.Text, o.X), parseF(y.Text, o.Y), parseF(z.Text, o.Z)
		o.Yaw = parseF(yaw.Text, o.Yaw)
		o.WidthM = parseF(width.Text, o.WidthM)
		o.Opacity = parseF(opacity.Text, o.Opacity)
		o.BgOpacity = parseF(bgOpacity.Text, o.BgOpacity)
		o.MaxMessages = int(parseF(maxMsg.Text, float64(o.MaxMessages)))
		u.saveCfg()
		onSave()
	}, u.win)
	d.Resize(fyne.NewSize(420, 520))
	d.Show()
}

func reproSuffix(on bool) string {
	if on {
		return " (REPROJECTING)"
	}
	return ""
}

func snapLabel(s string) string {
	switch s {
	case "left":
		return "left controller"
	case "right":
		return "right controller"
	case "head":
		return "head (visor)"
	default:
		return "world"
	}
}

func snapValue(label string) string {
	switch label {
	case "left controller":
		return "left"
	case "right controller":
		return "right"
	case "head (visor)":
		return "head"
	default:
		return ""
	}
}

func numEntry(v float64) *widget.Entry {
	e := newEntry()
	e.SetText(strconv.FormatFloat(v, 'g', -1, 64))
	return e
}

func parseF(s string, def float64) float64 {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}
	return def
}
