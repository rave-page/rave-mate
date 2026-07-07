package ui

import (
	"fmt"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// mediaLinkCard is the Settings entry for P4 video routes (MEDIALINK_DESIGN.md §3.2): opt-in
// Spout-sender sharing + the codec/bitrate budget applied to routes this instance requests.
// Route creation lives on the Peers tab ("Receive video").
func (u *UI) mediaLinkCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.MediaLink
	share := widget.NewCheck("Share local Spout senders with paired instances", func(v bool) {
		f.ShareVideo = v
		u.saveCfg()
	})
	share.SetChecked(f.ShareVideo)

	codecSel := widget.NewSelect([]string{"auto", "hevc", "h264", "mjpeg"}, func(s string) {
		if s == "auto" {
			s = ""
		}
		f.PreferCodec = s
		u.saveCfg()
	})
	if f.PreferCodec == "" {
		codecSel.SetSelected("auto")
	} else {
		codecSel.SetSelected(f.PreferCodec)
	}
	rateSel := widget.NewSelect([]string{"8", "12", "20", "30", "50"}, func(s string) {
		if kb, err := strconv.Atoi(s); err == nil {
			f.BitrateKbps = kb * 1000
			u.saveCfg()
		}
	})
	rateSel.SetSelected(strconv.Itoa(f.Bitrate() / 1000))
	swOnly := widget.NewCheck("Force software encode (diagnostic - high CPU)", func(v bool) {
		f.SWOnly = v
		u.saveCfg()
	})
	swOnly.SetChecked(f.SWOnly)

	st := u.newStatus(func(s *cardStatus) {
		m := u.svc.Media
		if m == nil {
			s.set(colMuted, "media plane off (enable LAN peers)")
			return
		}
		video := 0
		for _, r := range m.Stats() {
			if r.Encoder != "" || r.JB != nil {
				video++
			}
		}
		switch {
		case video > 0:
			s.set(colBrandMint, fmt.Sprintf("%d video route(s) live", video))
		case f.ShareVideo:
			s.set(colBrandMint, "sharing - receive from the Peers tab")
		default:
			s.set(colMuted, "receive-only (sharing off)")
		}
	})
	body := container.NewVBox(
		container.NewHBox(widget.NewLabel("Preferred codec"), codecSel,
			helpIcon("auto negotiates the best common hardware tier (AV1/HEVC/H.264 → software "+
				"fallback). Pinning a codec only applies when the sending instance can encode it."),
			widget.NewLabel("Bitrate (Mbps)"), rateSel,
			helpIcon("Per-route video budget. 20 Mbps fits 1080p60 HEVC with headroom on gigabit LAN.")),
		swOnly,
		mutedLabel("Video from OBS/Resolume/webcam on one instance lands as a Spout sender "+
			"(\"rave-mate link <source>\") on the other - encrypted, hardware-encoded, telemetry on "+
			"the Peers tab. Same-PC consumers should use the Spout sender directly."),
	)
	return featureCard("Media link video",
		"Send OBS/webcam/Spout video to a paired instance over the encrypted LAN link.",
		share, st, body)
}
