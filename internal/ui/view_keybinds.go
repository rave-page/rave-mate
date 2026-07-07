package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/vrbind"
)

// keybindsDialog manages VR/MIDI keybinds: a list of binds (action ← VR slot and/or MIDI) plus an
// add row with action/target pickers + a "Learn MIDI" capture. Binds persist in config and are
// dispatched by the app (MIDI now; VR action slots once SteamVR-bound).
func (u *UI) keybindsDialog() {
	f := &u.svc.Cfg.Features.VROverlay

	listBox := container.NewVBox()
	var refresh func()
	refresh = func() {
		listBox.Objects = nil
		if len(f.Binds) == 0 {
			listBox.Add(widget.NewLabelWithStyle("No keybinds yet - add one below.", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}))
		}
		for i := range f.Binds {
			idx := i
			b := f.Binds[idx]
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.Binds = append(f.Binds[:idx], f.Binds[idx+1:]...)
				u.saveCfg()
				refresh()
			})
			row := container.NewBorder(nil, nil, nil, del, widget.NewLabel(bindSummary(b)))
			listBox.Add(row)
		}
		listBox.Refresh()
	}
	refresh()

	// ── Add row ──
	actions := vrbind.Actions()
	actLabels := make([]string, len(actions))
	for i, a := range actions {
		actLabels[i] = a.Label
	}
	actionSel := widget.NewSelect(actLabels, nil)

	targetEntry := widget.NewEntry()
	targetEntry.SetPlaceHolder("target")
	targetPick := widget.NewSelect(nil, func(s string) { targetEntry.SetText(s) })
	targetPick.Hide()
	targetRow := container.NewVBox(targetPick, targetEntry)

	// curAction resolves the catalog entry for the selected label.
	curAction := func() (vrbind.Action, bool) {
		for _, a := range actions {
			if a.Label == actionSel.Selected {
				return a, true
			}
		}
		return vrbind.Action{}, false
	}

	actionSel.OnChanged = func(string) {
		a, ok := curAction()
		if !ok {
			return
		}
		switch a.Target {
		case vrbind.TargetNone:
			targetEntry.SetText("")
			targetRow.Hide()
		case vrbind.TargetInstance:
			opts := []string{""}
			if u.svc.OBSControl != nil {
				for _, in := range u.svc.OBSControl.Statuses() {
					opts = append(opts, in.ID)
				}
			}
			targetPick.Options = opts
			targetPick.Show()
			targetEntry.SetPlaceHolder("OBS instance id (empty = local)")
			targetRow.Show()
		case vrbind.TargetOverlay:
			opts := make([]string, 0, len(f.Overlays))
			for _, o := range f.Overlays {
				opts = append(opts, o.ID)
			}
			targetPick.Options = opts
			targetPick.Show()
			targetEntry.SetPlaceHolder("overlay id")
			targetRow.Show()
		case vrbind.TargetOBSInput:
			targetPick.Hide()
			targetEntry.SetPlaceHolder("OBS input/source name")
			targetRow.Show()
		case vrbind.TargetAppGroup:
			var opts []string
			for _, g := range u.svc.Cfg.Features.AppGroups.Groups {
				opts = append(opts, g.ID)
			}
			targetPick.Options = opts
			targetPick.Show()
			targetEntry.SetPlaceHolder("application-group id")
			targetRow.Show()
		}
		targetPick.Refresh()
		targetRow.Refresh()
	}

	// MIDI learn.
	var midi *vrbind.MIDIKey
	midiLbl := widget.NewLabel("MIDI: (none)")
	var cancelLearn func()
	learnBtn := widget.NewButton("Learn MIDI", nil)
	learnBtn.OnTapped = func() {
		if u.svc.MIDILearn == nil {
			dialog.ShowInformation("Learn MIDI", "MIDI input not available.", u.win)
			return
		}
		if cancelLearn != nil { // already listening → cancel
			cancelLearn()
			cancelLearn = nil
			learnBtn.SetText("Learn MIDI")
			return
		}
		learnBtn.SetText("Press a key… (cancel)")
		cancelLearn = u.svc.MIDILearn(func(status, data1 byte) {
			midi = &vrbind.MIDIKey{Status: status, Data1: data1}
			fyne.Do(func() {
				midiLbl.SetText("MIDI: " + midiKeyLabel(*midi))
				learnBtn.SetText("Learn MIDI")
			})
			cancelLearn = nil
		})
	}

	vrSlots := append([]string{"(none)"}, vrbind.VRActionSlots()...)
	vrSel := widget.NewSelect(vrSlots, nil)
	vrSel.SetSelected("(none)")

	addBtn := widget.NewButton("Add bind", func() {
		a, ok := curAction()
		if !ok {
			dialog.ShowInformation("Add bind", "Pick an action first.", u.win)
			return
		}
		b := vrbind.Bind{Action: a.ID, Target: targetEntry.Text}
		if midi != nil {
			b.MIDI = midi
		}
		if vrSel.Selected != "" && vrSel.Selected != "(none)" {
			b.VRAction = vrSel.Selected
		}
		if b.MIDI == nil && b.VRAction == "" {
			dialog.ShowInformation("Add bind", "Assign a MIDI key and/or a VR slot.", u.win)
			return
		}
		f.Binds = append(f.Binds, b)
		u.saveCfg()
		// reset the add row
		midi = nil
		midiLbl.SetText("MIDI: (none)")
		vrSel.SetSelected("(none)")
		refresh()
	})

	steamvrBtn := widget.NewButton("Edit binds in SteamVR…", func() {
		if u.svc.VROverlay == nil || !u.svc.VROverlay.Available() {
			dialog.ShowInformation("SteamVR binds", "Start SteamVR (and a `vr` build) first, then reopen this.", u.win)
			return
		}
		if err := u.svc.VROverlay.OpenBindingUI(); err != nil {
			dialog.ShowError(err, u.win)
		}
	})

	addForm := container.NewVBox(
		widget.NewLabelWithStyle("Add a keybind", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		actionSel,
		targetRow,
		container.NewHBox(learnBtn, midiLbl),
		container.NewHBox(widget.NewLabel("VR slot:"), vrSel),
		addBtn,
		widget.NewLabelWithStyle("VR slots are assigned to controller inputs (incl. combos) in SteamVR's binding UI; map the slot to an action here.", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		steamvrBtn,
	)

	body := container.NewBorder(
		widget.NewLabelWithStyle("Keybinds", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		addForm, nil, nil,
		container.NewVScroll(listBox),
	)
	d := dialog.NewCustom("Keybinds", "Done", body, u.win)
	d.Resize(fyne.NewSize(520, 560))
	d.SetOnClosed(func() {
		if cancelLearn != nil {
			cancelLearn()
		}
	})
	d.Show()
}

// bindSummary renders a bind as one line for the list.
func bindSummary(b vrbind.Bind) string {
	label := string(b.Action)
	if a, ok := vrbind.ActionByID(b.Action); ok {
		label = a.Label
	}
	if b.Target != "" {
		label += " [" + b.Target + "]"
	}
	var src string
	if b.VRAction != "" {
		src = "VR:" + b.VRAction
	}
	if b.MIDI != nil {
		if src != "" {
			src += " + "
		}
		src += midiKeyLabel(*b.MIDI)
	}
	return label + "  ←  " + src
}

// midiKeyLabel names a MIDI key by message type + number (channel from status low nibble).
func midiKeyLabel(k vrbind.MIDIKey) string {
	ch := int(k.Status&0x0F) + 1
	switch k.Status & 0xF0 {
	case 0x90, 0x80:
		return fmt.Sprintf("Note %d (ch%d)", k.Data1, ch)
	case 0xB0:
		return fmt.Sprintf("CC %d (ch%d)", k.Data1, ch)
	default:
		return fmt.Sprintf("MIDI %02X %d (ch%d)", k.Status&0xF0, k.Data1, ch)
	}
}
