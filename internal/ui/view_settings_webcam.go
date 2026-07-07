package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// webcamCard is the Settings entry for the webcam/UVC source: feature toggle + auto-start.
// Device pick, start/stop and PTZ/exposure controls live on the Peers tab (local + every
// paired instance's camera).
func (u *UI) webcamCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Webcam
	autoStart := widget.NewCheck("Start the camera with the app (crash-recovery rigs)", func(v bool) {
		f.AutoStart = v
		u.saveCfg()
	})
	autoStart.SetChecked(f.AutoStart)

	st := u.newStatus(func(s *cardStatus) {
		w := u.svc.Webcam
		switch {
		case !f.Enabled || w == nil:
			s.set(colMuted, "off")
		default:
			ls := w.Instances()[0].Status // local first
			switch {
			case ls.Running:
				s.set(colBrandMint, "LIVE - "+ls.Sender)
			case ls.Err != "":
				s.set(colBrandAmber, ls.Err)
			case ls.Device == "":
				s.set(colBrandAmber, "no camera selected")
			default:
				s.set(colBrandMint, "ready - "+ls.Device)
			}
		}
	})
	toggle := u.moduleToggle("webcam", &f.Enabled)
	body := container.NewVBox(
		autoStart,
		mutedLabel("Publishes a camera as a local Spout sender (\"rave-mate cam <device>\") with "+
			"lens controls (pan/tilt/zoom/focus/exposure). Camera pick + live controls: Peers tab - "+
			"including cameras on a paired instance. Needs ffmpeg + SpoutLibrary.dll."),
	)
	return featureCard("Webcam",
		"Share a camera as a Spout source and drive its lens - from here or a paired instance.",
		toggle, st, body)
}
