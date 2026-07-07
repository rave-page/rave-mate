package ui

import (
	"runtime"

	"fyne.io/fyne/v2"

	"rave.page/mate/internal/giokit/playerwin"
	"rave.page/mate/internal/mpvplayer"
)

// useGioPlayer: the Gio player window is the DEFAULT pop-out (tri-state config
// features.player.gioWindow: unset/true = Gio, explicit false = legacy Fyne/mpv-popout).
// darwin always legacy - Gio aux windows need the main thread Fyne owns (GIO_MIGRATION.md).
func (u *UI) useGioPlayer() bool {
	if runtime.GOOS == "darwin" {
		return false
	}
	return u.svc.Cfg == nil || u.svc.Cfg.Features.Player.UseGioWindow()
}

// openGioPlayerWindow opens file in the Gio player window (mpv embedded into the Gio
// window's HWND on Windows; popout elsewhere). Export-cut reuses the existing Fyne
// preset dialog + transcode pipeline via fyne.Do. Errors only when mpv is missing -
// the caller falls back to the legacy player.
func (u *UI) openGioPlayerWindow(title, file string, tgt trimTarget) error {
	var opts mpvplayer.Options
	if u.svc.Cfg != nil {
		pf := u.svc.Cfg.Features.Player
		opts = mpvplayer.Options{VO: pf.ResolvedVO(), HWDec: pf.ResolvedHWDec(), Profile: pf.Profile, Extra: pf.ExtraArgs}
	}
	_, err := playerwin.Open(playerwin.Config{
		File:      file,
		Title:     title,
		Mpv:       opts,
		Log:       u.svc.Log,
		LoadPeaks: u.peaksLoader(file),
		OnExportCut: func(inSec, outSec float64) {
			fyne.Do(func() { u.exportTrim(file, tgt, inSec, outSec) })
		},
	})
	return err
}
