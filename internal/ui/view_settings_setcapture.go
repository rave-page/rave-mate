package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/icecast"
)

// setCaptureCard configures the local Icecast-source receiver Traktor broadcasts a set to.
// It captures the broadcast audio to setsDir and surfaces a guided Traktor Broadcasting
// config + a live "connected" indicator (Traktor's broadcast prefs aren't safely
// file-patchable, so we show the exact values instead of auto-writing them).
func (u *UI) setCaptureCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.SetCapture

	portEntry := newEntry()
	portEntry.SetText(strconv.Itoa(f.ResolvedPort()))
	portEntry.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65536 {
			f.Port = n
			u.saveCfg()
		}
	}
	mountEntry := newEntry()
	mountEntry.SetPlaceHolder("/stream")
	mountEntry.SetText(f.Mount)
	mountEntry.OnChanged = func(s string) { f.Mount = s; u.saveCfg() }

	userEntry := newEntry()
	userEntry.SetPlaceHolder("source")
	userEntry.SetText(f.Username)
	userEntry.OnChanged = func(s string) { f.Username = s; u.saveCfg() }

	passEntry := newPasswordEntry()
	passEntry.SetText(f.Password)
	passEntry.OnChanged = func(s string) { f.Password = s; u.saveCfg() }

	dirEntry := newEntry()
	dirEntry.SetPlaceHolder("default: app data / sets")
	dirEntry.SetText(f.SetsDir)
	dirEntry.OnChanged = func(s string) { f.SetsDir = s; u.saveCfg() }

	// Single-file capture: keep the whole broadcast in one file, coalescing a brief
	// disconnect/reconnect within the grace window so a transient drop doesn't chop the set.
	graceEntry := newEntry()
	graceEntry.SetText(strconv.Itoa(int(f.ResolvedReconnectGrace().Seconds())))
	graceEntry.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			f.ReconnectGraceSeconds = n
			u.saveCfg()
		}
	}
	if !f.SingleFile {
		graceEntry.Disable()
	}
	singleFile := widget.NewCheck("Record to one file while connected (coalesce reconnects)", func(v bool) {
		f.SingleFile = v
		if v {
			graceEntry.Enable()
		} else {
			graceEntry.Disable()
		}
		u.saveCfg()
	})
	singleFile.SetChecked(f.SingleFile)

	// Metadata-only: keep parsing now-playing but write no audio (native AudioRecord is the
	// canonical recording). Read at receiver (re)spawn, so it needs a toggle off/on to apply.
	metaOnly := widget.NewCheck("Metadata only - parse now-playing, don't save Icecast audio (use native audio recording instead)", func(v bool) {
		f.MetadataOnly = v
		if v {
			dirEntry.Disable()
			graceEntry.Disable()
			singleFile.Disable()
		} else {
			dirEntry.Enable()
			singleFile.Enable()
			if f.SingleFile {
				graceEntry.Enable()
			}
		}
		u.saveCfg()
	})
	metaOnly.SetChecked(f.MetadataOnly)
	if f.MetadataOnly {
		dirEntry.Disable()
		graceEntry.Disable()
		singleFile.Disable()
	}
	openDir := widget.NewButton("Open sets folder", func() { openFile(f.ResolvedSetsDir()) })
	dirPath := mutedLabel("→ " + f.ResolvedSetsDir())
	dirPath.Wrapping = fyne.TextWrapWord

	// Guided Traktor config - recomputed live as the fields change so the values always match.
	guide := widget.NewRichText()
	guide.Wrapping = fyne.TextWrapWord
	refreshGuide := func() {
		mount := f.Mount
		if mount == "" {
			mount = "/stream"
		}
		guide.ParseMarkdown(fmt.Sprintf(
			"**Traktor → Preferences → Broadcasting**, then start broadcasting:\n\n"+
				"- Server / Address: `127.0.0.1`\n"+
				"- Port: `%d`\n"+
				"- Mount Path: `%s`\n"+
				"- Password: `%s`\n"+
				"- Format: **Ogg Vorbis** (recommended - carries track titles in-band) or MP3\n\n"+
				"User is `%s`. Audio is broadcast-quality lossy by design (Icecast streams encoded audio).",
			f.ResolvedPort(), mount, orPlaceholder(f.Password, "(set a password above)"), f.ResolvedUsername()))
	}
	refreshGuide()
	for _, e := range []*widget.Entry{portEntry, mountEntry, userEntry, passEntry} {
		prev := e.OnChanged
		e.OnChanged = func(s string) { prev(s); refreshGuide() }
	}

	// Live connection indicator. Capture start/end arrive as events; rejected/half-open
	// connections don't, so we also poll the snapshot so "N attempts · last: …" stays current
	// (this is what tells a user Traktor never connected - Attempts stays 0 until they click
	// Start Broadcasting).
	statusLbl := widget.NewLabel("")
	statusLbl.Wrapping = fyne.TextWrapWord
	statusLbl.TextStyle = fyne.TextStyle{Bold: true}
	applyStatus := func(st icecast.Status) {
		switch {
		case st.LastError != "":
			statusLbl.SetText("⚠ Receiver error - " + st.LastError + " (toggle off/on to retry; another app may hold the port)")
		case !st.Listening:
			statusLbl.SetText("○ Receiver off - enable it to listen for Traktor's broadcast")
		case st.Connected:
			statusLbl.SetText(fmt.Sprintf("● Traktor connected ✓ - capturing %s (%s)", st.Format, humanBytes(st.Bytes)))
		case st.Reconnecting:
			statusLbl.SetText(fmt.Sprintf("◌ Source dropped - holding %s open for a reconnect (%s captured so far)", st.Format, humanBytes(st.Bytes)))
		case st.Attempts == 0:
			statusLbl.SetText("◌ Listening - no connection yet. In Traktor: Preferences → Broadcasting, then press the broadcast/antenna button to Start Broadcasting.")
		default:
			msg := fmt.Sprintf("◌ Listening - %d connection attempt(s)", st.Attempts)
			if st.LastEvent != "" {
				msg += " · last: " + st.LastEvent
			}
			statusLbl.SetText(msg)
		}
	}
	if u.svc.SetCapture != nil {
		applyStatus(u.svc.SetCapture.Snapshot())
		capCh, unCap := u.svc.SetCapture.SubscribeCapture()
		stop := make(chan struct{})
		u.closers = append(u.closers, unCap, func() { close(stop) })
		goUI("setcapture", func() {
			for range capCh {
				if u.svc.SetCapture != nil {
					st := u.svc.SetCapture.Snapshot()
					fyne.Do(func() { applyStatus(st) })
				}
			}
		})
		goUI("setcapture", func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if u.svc.SetCapture != nil {
						st := u.svc.SetCapture.Snapshot()
						fyne.Do(func() { applyStatus(st) })
					}
				}
			}
		})
	} else {
		statusLbl.SetText("○ Receiver unavailable")
	}

	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled || u.svc.SetCapture == nil {
			s.set(colMuted, "off")
			return
		}
		c := u.svc.SetCapture.Snapshot()
		switch {
		case c.LastError != "":
			s.set(colBrandAmber, "receiver error")
		case c.Connected:
			s.set(colBrandMint, "capturing "+strings.ToUpper(c.Format))
		case c.Reconnecting:
			s.set(colBrandAmber, "source dropped - holding")
		case c.Listening:
			s.set(colBrandMint, "listening "+c.Addr)
		default:
			s.set(colBrandAmber, "not listening")
		}
	})
	toggle := u.moduleToggle("setcapture", &f.Enabled)
	body := container.NewVBox(
		formGrid(
			fieldLabel("Port"), portEntry,
			fieldLabel("Mount"), mountEntry,
			fieldLabel("Source user"), userEntry,
			fieldLabel("Password"), passEntry,
			fieldLabel("Sets folder"), folderPickerRow(dirEntry),
			fieldLabel("Reconnect grace (s)"), graceEntry,
		),
		singleFile,
		metaOnly,
		dirPath,
		container.NewHBox(openDir),
		mutedLabel("Toggle off/on to apply a port/mount/password change."),
		widget.NewSeparator(),
		statusLbl,
		guide,
	)
	return featureCard("Set capture (Icecast)",
		"Capture your live set to disk by broadcasting it from Traktor to this local Icecast endpoint. Time-linked to the recorder's tracklist.",
		toggle, st, body)
}

// orPlaceholder returns s, or the placeholder when s is empty.
func orPlaceholder(s, placeholder string) string {
	if s == "" {
		return placeholder
	}
	return s
}
