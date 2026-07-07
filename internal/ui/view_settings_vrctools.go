package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// vrcToolsCard configures the VRChat screenshot organizer + camera-path manager, and opens the
// camera-path browser/preview.
func (u *UI) vrcToolsCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.VRCTools

	st := u.newStatus(func(s *cardStatus) {
		if u.svc.VRCTools == nil {
			s.set(colMuted, "unavailable")
			return
		}
		if loc, ok := u.svc.VRCTools.CurrentWorld(); ok && loc.WorldName != "" {
			s.set(colBrandMint, "in "+loc.WorldName)
			return
		}
		if f.Enabled {
			s.set(colMuted, "watching")
		} else {
			s.set(colMuted, "off")
		}
	})

	orgPhotos := widget.NewCheck("Organize screenshots by world/event", func(v bool) { f.OrganizePhotos = v; u.saveCfg() })
	orgPhotos.SetChecked(f.OrganizePhotos)
	byEvent := widget.NewCheck("Prefer rave.page event over world", func(v bool) { f.OrganizeByEvent = v; u.saveCfg() })
	byEvent.SetChecked(f.OrganizeByEvent)
	photoMove := widget.NewCheck("Move (instead of copy)", func(v bool) { f.PhotoMove = v; u.saveCfg() })
	photoMove.SetChecked(f.PhotoMove)

	orgPaths := widget.NewCheck("Organize camera paths by world", func(v bool) { f.OrganizeCamPaths = v; u.saveCfg() })
	orgPaths.SetChecked(f.OrganizeCamPaths)
	pathMove := widget.NewCheck("Move (instead of copy)", func(v bool) { f.CamPathMove = v; u.saveCfg() })
	pathMove.SetChecked(f.CamPathMove)

	autoBackup := widget.NewCheck("Back up camera paths on play", func(v bool) { f.AutoBackupCamPaths = v; u.saveCfg() })
	autoBackup.SetChecked(f.AutoBackupCamPaths)
	autoRestore := widget.NewCheck("Auto-restore path on world join (while live)", func(v bool) { f.AutoRestoreCamPaths = v; u.saveCfg() })
	autoRestore.SetChecked(f.AutoRestoreCamPaths)

	osc := widget.NewEntry()
	osc.SetPlaceHolder("127.0.0.1:9000")
	osc.SetText(f.OSCAddr)
	osc.OnChanged = func(s string) { f.OSCAddr = s; u.saveCfg() }

	camPathsBtn := widget.NewButtonWithIcon("Camera paths…", theme.MediaPlayIcon(), func() { u.camPathsDialog() })
	photosBtn := widget.NewButtonWithIcon("Photos…", theme.FolderOpenIcon(), func() { u.vrcPhotosDialog() })
	organizeNow := widget.NewButton("Organize now", func() {
		if u.svc.VRCTools == nil {
			return
		}
		p, c := u.svc.VRCTools.OrganizeNow()
		u.Notify("VRChat tools", fmt.Sprintf("Organized %d photo(s), %d path(s).", p, c))
	})

	// Camera look preset auto-applied (over /usercamera OSC) after a dolly path loads.
	presetNames := []string{"(none)"}
	for _, p := range f.AllCamPresets() {
		presetNames = append(presetNames, p.Name)
	}
	presetSel := widget.NewSelect(presetNames, func(s string) {
		if s == "(none)" {
			f.DefaultCamPreset = ""
		} else {
			f.DefaultCamPreset = s
		}
		u.saveCfg()
	})
	if f.DefaultCamPreset == "" {
		presetSel.SetSelected("(none)")
	} else {
		presetSel.SetSelected(f.DefaultCamPreset)
	}
	applyPresetBtn := widget.NewButton("Apply now", func() {
		if u.svc.VRCTools == nil || f.DefaultCamPreset == "" {
			return
		}
		if err := u.svc.VRCTools.ApplyCamPreset(f.DefaultCamPreset); err != nil {
			dialog.ShowError(err, u.win)
		}
	})
	installPathsBtn := widget.NewButtonWithIcon("Install DJ paths", theme.DownloadIcon(), func() {
		if u.svc.VRCTools == nil {
			return
		}
		n, dst, err := u.svc.VRCTools.InstallBuiltinPaths()
		if err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		dialog.ShowInformation("DJ paths", fmt.Sprintf("Installed %d pro camera paths to:\n%s\n\nThey appear in Camera paths… and load over OSC. Set loop/path-type once in VRChat (those aren't saved in the file).", n, dst), u.win)
	})

	body := container.NewVBox(
		mutedLabel("Watches the VRChat log to know which world/instance you're in, then sorts screenshots + camera paths accordingly. Camera paths preview in 3D and load into VRChat over OSC."),
		orgPhotos, byEvent, photoMove,
		orgPaths, pathMove,
		autoBackup, autoRestore,
		mutedLabel("Crash-resilience: playing a path saves it per world; rejoining that world during a live set (OBS/Twitch) reloads it after a few seconds. Only paths rave-mate plays are captured - not paths triggered inside VRChat's own dolly UI."),
		labeled("OSC target", osc),
		labeled("Camera preset on load", container.NewBorder(nil, nil, nil, applyPresetBtn, presetSel)),
		mutedLabel("Applies a camera look (focal distance, aperture, grade, fly/turn/smoothing) over /usercamera OSC right after a path loads. VRChat exposes camera LOOK over OSC - not camera MODE: Stream/Spout + the Path Type/Easing/Looping/Capture dropdowns can't be set this way."),
		container.NewHBox(camPathsBtn, photosBtn, organizeNow, installPathsBtn),
	)
	return featureCard("VRChat tools", "Organize screenshots + camera paths by world.", u.simpleToggle(&f.Enabled), st, body)
}
