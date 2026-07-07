package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/vrbind"
)

// Settings for the in-headset wrist strip (quick buttons) + per-world layout bindings.

// quickActionOptions is the quick-button action catalog: every bindable app action + the two
// editor-local loads (layout / camera path). Parallel label/id slices for the Select.
func quickActionOptions() (labels, ids []string) {
	for _, a := range vrbind.Actions() {
		labels = append(labels, a.Label)
		ids = append(ids, string(a.ID))
	}
	labels = append(labels, "Load overlay layout", "Load camera path")
	ids = append(ids, "layout.load", "campath.load")
	return
}

func quickActionLabel(id string) string {
	labels, ids := quickActionOptions()
	for i, v := range ids {
		if v == id {
			return labels[i]
		}
	}
	return id
}

// vrQuickButtonsDialog manages the user wrist-strip quick buttons (extra icon buttons after the
// built-in set, each firing an app action).
func (u *UI) vrQuickButtonsDialog() {
	f := &u.svc.Cfg.Features.VROverlay
	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.QuickButtons {
			idx := i
			q := f.QuickButtons[i]
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.QuickButtons = append(f.QuickButtons[:idx], f.QuickButtons[idx+1:]...)
				u.saveCfg()
				rebuild()
			})
			sum := quickActionLabel(q.Action)
			if q.Target != "" {
				sum += " [" + q.Target + "]"
			}
			label := q.Label
			if label == "" {
				label = sum
			}
			list.Add(container.NewBorder(nil, nil, nil, del,
				widget.NewLabel(fmt.Sprintf("%s - %s", label, sum))))
		}
		if len(f.QuickButtons) == 0 {
			list.Add(mutedLabel("No quick buttons yet - add one below. They appear on the in-headset wrist strip after the built-in buttons."))
		}
		list.Refresh()
	}
	rebuild()

	// ── add form ──
	actLabels, actIDs := quickActionOptions()
	actionSel := widget.NewSelect(actLabels, nil)
	labelEntry := newEntry()
	labelEntry.SetPlaceHolder("Button label (hover tooltip)")
	glyphEntry := newEntry()
	glyphEntry.SetPlaceHolder("Glyph, 1-3 chars (empty = from label)")
	targetEntry := newEntry()
	targetEntry.SetPlaceHolder("target")
	targetPick := widget.NewSelect(nil, nil)
	targetPick.Hide()
	targetRow := container.NewVBox(targetPick, targetEntry)
	targetRow.Hide()

	selAction := func() string {
		for i, l := range actLabels {
			if l == actionSel.Selected {
				return actIDs[i]
			}
		}
		return ""
	}
	// camFiles mirrors targetPick.Options for campath.load (display name → file path).
	var camFiles []string
	targetPick.OnChanged = func(s string) {
		if camFiles != nil {
			for i, o := range targetPick.Options {
				if o == s && i < len(camFiles) {
					targetEntry.SetText(camFiles[i])
					return
				}
			}
		}
		targetEntry.SetText(s)
	}
	actionSel.OnChanged = func(string) {
		id := selAction()
		camFiles = nil
		targetEntry.SetText("")
		show := true
		switch id {
		case "layout.load":
			var opts []string
			for _, l := range f.Layouts {
				opts = append(opts, l.Name)
			}
			targetPick.Options = opts
			targetPick.Show()
			targetEntry.SetPlaceHolder("layout name")
		case "campath.load":
			var opts []string
			if u.svc.VRCTools != nil {
				for _, p := range u.svc.VRCTools.CamPaths() {
					opts = append(opts, p.Name)
					camFiles = append(camFiles, p.File)
				}
			}
			targetPick.Options = opts
			targetPick.Show()
			targetEntry.SetPlaceHolder("camera-path file")
		case string(vrbind.ActOverlayToggle), string(vrbind.ActOverlayShow), string(vrbind.ActOverlayHide):
			var opts []string
			for _, o := range f.Overlays {
				opts = append(opts, o.ID)
			}
			targetPick.Options = opts
			targetPick.Show()
			targetEntry.SetPlaceHolder("overlay id")
		case string(vrbind.ActOBSRecord), string(vrbind.ActOBSStream):
			opts := []string{""}
			if u.svc.OBSControl != nil {
				for _, in := range u.svc.OBSControl.Statuses() {
					opts = append(opts, in.ID)
				}
			}
			targetPick.Options = opts
			targetPick.Show()
			targetEntry.SetPlaceHolder("OBS instance id (empty = local)")
		case string(vrbind.ActOBSMic):
			targetPick.Hide()
			targetEntry.SetPlaceHolder("OBS input/source name")
		case string(vrbind.ActAppGroupLaunch):
			var opts []string
			for _, g := range u.svc.Cfg.Features.AppGroups.Groups {
				opts = append(opts, g.ID)
			}
			targetPick.Options = opts
			targetPick.Show()
			targetEntry.SetPlaceHolder("application-group id")
		default:
			show = false
		}
		if show {
			targetRow.Show()
		} else {
			targetRow.Hide()
		}
		targetPick.Refresh()
		targetRow.Refresh()
	}
	add := widget.NewButtonWithIcon("Add quick button", theme.ContentAddIcon(), func() {
		id := selAction()
		if id == "" {
			dialog.ShowInformation("Quick button", "Pick an action first.", u.win)
			return
		}
		f.QuickButtons = append(f.QuickButtons, config.VRQuickButton{
			Label: labelEntry.Text, Glyph: glyphEntry.Text, Action: id, Target: targetEntry.Text,
		})
		u.saveCfg()
		labelEntry.SetText("")
		glyphEntry.SetText("")
		targetEntry.SetText("")
		rebuild()
	})
	form := container.NewVBox(
		labelWithHelp("Add a quick button", "Quick buttons are extra icon buttons on the in-headset wrist strip (open it via the wrist badge or the summon button). Each fires one action - toggle an overlay, load a layout or camera path, OBS record/stream, and more."),
		actionSel, targetRow, labelEntry, glyphEntry, add,
	)
	body := container.NewBorder(nil, form, nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("Wrist quick buttons", "Done", body, u.win)
	d.Resize(fyne.NewSize(560, 540))
	d.Show()
}

// vrWorldLayoutsDialog manages per-world layout bindings + the auto-apply mode.
func (u *UI) vrWorldLayoutsDialog() {
	f := &u.svc.Cfg.Features.VROverlay
	layoutNames := func() []string {
		var out []string
		for _, l := range f.Layouts {
			out = append(out, l.Name)
		}
		return out
	}

	const optOff, optNotify, optAuto = "off", "notify (suggest in VR)", "auto (apply on join)"
	modeSel := widget.NewSelect([]string{optOff, optNotify, optAuto}, func(s string) {
		switch s {
		case optOff:
			f.WorldLayoutMode = "off"
		case optAuto:
			f.WorldLayoutMode = "auto"
		default:
			f.WorldLayoutMode = "notify"
		}
		u.saveCfg()
	})
	switch f.ResolvedWorldLayoutMode() {
	case "off":
		modeSel.SetSelected(optOff)
	case "auto":
		modeSel.SetSelected(optAuto)
	default:
		modeSel.SetSelected(optNotify)
	}

	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.WorldLayouts {
			idx := i
			b := &f.WorldLayouts[i]
			laySel := widget.NewSelect(layoutNames(), func(s string) { b.Layout = s; u.saveCfg() })
			laySel.SetSelected(b.Layout)
			en := widget.NewCheck("", func(v bool) { b.Enabled = v; u.saveCfg() })
			en.SetChecked(b.Enabled)
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.WorldLayouts = append(f.WorldLayouts[:idx], f.WorldLayouts[idx+1:]...)
				u.saveCfg()
				rebuild()
			})
			name := b.WorldName
			if name == "" {
				name = b.WorldID
			}
			list.Add(container.NewBorder(nil, nil, en, container.NewHBox(laySel, del), widget.NewLabel(name)))
		}
		if len(f.WorldLayouts) == 0 {
			list.Add(mutedLabel("No world bindings yet. Bind the world you're in below, or use 'Save layout for this world' in the in-headset LAYOUTS page."))
		}
		list.Refresh()
	}
	rebuild()

	bindCur := widget.NewButtonWithIcon("Bind current world…", theme.ContentAddIcon(), func() {
		if u.svc.VRCTools == nil {
			dialog.ShowInformation("World layouts", "VRChat tools are off - the current world is unknown.", u.win)
			return
		}
		loc, ok := u.svc.VRCTools.CurrentWorld()
		if !ok || loc.WorldID == "" {
			dialog.ShowInformation("World layouts", "Current world unknown - join a VRChat world first.", u.win)
			return
		}
		names := layoutNames()
		if len(names) == 0 {
			dialog.ShowInformation("World layouts", "No saved layouts - save one first (Layouts…).", u.win)
			return
		}
		laySel := widget.NewSelect(names, nil)
		laySel.SetSelected(names[0])
		msg := widget.NewLabel("World: " + loc.WorldName)
		dialog.NewCustomConfirm("Bind current world", "Bind", "Cancel", container.NewVBox(msg, laySel), func(okd bool) {
			if !okd || laySel.Selected == "" {
				return
			}
			for i := range f.WorldLayouts {
				if f.WorldLayouts[i].WorldID == loc.WorldID {
					f.WorldLayouts[i].Layout, f.WorldLayouts[i].WorldName, f.WorldLayouts[i].Enabled = laySel.Selected, loc.WorldName, true
					u.saveCfg()
					rebuild()
					return
				}
			}
			f.WorldLayouts = append(f.WorldLayouts, config.VRWorldLayout{
				WorldID: loc.WorldID, WorldName: loc.WorldName, Layout: laySel.Selected, Enabled: true,
			})
			u.saveCfg()
			rebuild()
		}, u.win).Show()
	})

	body := container.NewBorder(
		container.NewVBox(
			labelWithHelp("Auto-apply mode", "What happens when you join a VRChat world with a bound layout: off = nothing; notify = an in-headset notice + a one-tap apply row in the wrist menu; auto = the layout is applied immediately. Per-binding checkbox disables a single world."),
			modeSel,
		),
		bindCur, nil, nil,
		container.NewVScroll(list),
	)
	d := dialog.NewCustom("Per-world overlay layouts", "Done", body, u.win)
	d.Resize(fyne.NewSize(560, 500))
	d.Show()
}
