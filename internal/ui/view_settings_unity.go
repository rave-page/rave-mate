package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/unityproj"
)

// unityCard manages Unity-project integration: discover projects (from VRChat Creator Companion
// or a manual folder), install the rave.page editor plugin, and see plugin status per project.
func (u *UI) unityCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Unity

	st := u.newStatus(func(s *cardStatus) {
		if n := len(f.Projects); n == 0 {
			s.set(colMuted, "no projects")
		} else {
			s.set(colBrandMint, fmt.Sprintf("%d project(s)", n))
		}
	})

	has := func(p string) bool {
		for _, q := range f.Projects {
			if q == p {
				return true
			}
		}
		return false
	}

	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.Projects {
			idx, dir := i, f.Projects[i]
			info := unityproj.Inspect(dir)
			status := "✓ plugin installed"
			switch {
			case !info.Valid:
				status = "⚠ not a Unity project"
			case !info.HasPlugin:
				status = "plugin not installed"
			}
			installBtn := widget.NewButton("Install plugin", func() {
				if err := unityproj.InstallPlugin(dir); err != nil {
					dialog.ShowError(err, u.win)
					return
				}
				u.Notify("Unity", "Installed rave.page plugin → "+info.Name)
				rebuild()
			})
			if !info.Valid {
				installBtn.Disable()
			}
			if info.HasPlugin {
				installBtn.SetText("Reinstall plugin")
			}
			removeBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.Projects = append(f.Projects[:idx], f.Projects[idx+1:]...)
				u.saveCfg()
				rebuild()
			})
			label := container.NewVBox(
				widget.NewLabelWithStyle(info.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
				mutedLabel(dir+" - "+status),
			)
			list.Add(container.NewBorder(nil, nil, nil, container.NewHBox(installBtn, removeBtn), label))
		}
		list.Refresh()
	}
	rebuild()

	discoverBtn := widget.NewButtonWithIcon("Add from VRChat Creator Companion", theme.SearchIcon(), func() {
		added := 0
		for _, p := range unityproj.DiscoverVCCProjects() {
			if unityproj.IsUnityProject(p) && !has(p) {
				f.Projects = append(f.Projects, p)
				added++
			}
		}
		u.saveCfg()
		rebuild()
		u.Notify("Unity", fmt.Sprintf("Added %d project(s) from VCC", added))
	})
	addBtn := widget.NewButtonWithIcon("Add folder…", theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			p := uri.Path()
			if !unityproj.IsUnityProject(p) {
				dialog.ShowInformation("Unity", "Not a Unity project (needs Assets/ + ProjectSettings/).", u.win)
				return
			}
			if !has(p) {
				f.Projects = append(f.Projects, p)
				u.saveCfg()
				rebuild()
			}
		}, u.win)
	})

	body := container.NewVBox(
		mutedLabel("Install the rave.page editor plugin into your Unity avatar projects. In Unity it adds Tools → rave.page → Motion: import the motion takes you record in VR, preview them on the REAL avatar model (Unity's renderer), and set up which animations get added to the avatar. The plugin also exports the avatar to VRM for the in-app preview."),
		container.NewHBox(discoverBtn, addBtn),
		list,
		mutedLabel("After installing, open the project in Unity (2022.3) → Tools → rave.page → Motion. The plugin exposes a local control port (127.0.0.1:47625) so rave-mate can drive it. The avatar-model preview lives in Unity; rave-mate's motion studio keeps its skeleton preview."),
	)
	return featureCard("Unity", "Install the rave.page editor plugin into your avatar projects.", u.simpleToggle(&f.Enabled), st, body)
}
