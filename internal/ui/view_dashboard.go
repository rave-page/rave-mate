package ui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/shared/auth"
	"rave.page/mate/internal/stream"
)

// Live-card content builders that predate the cockpit (status rows, now-playing LCD).
// The card list itself renders in buildLive (view_live.go).

// buildStatusContent is the Status module: API / account / Traktor-listener / stream rows.
func (u *UI) buildStatusContent() fyne.CanvasObject {
	apiRow := newStatusRow("API", u.svc.API.BaseURL())
	trkRow := newStatusRow("Traktor listener", "-")
	refreshTrk := func() {
		port := config.TraktorPort
		if u.svc.Cfg != nil {
			port = u.svc.Cfg.Features.Traktor.ResolvedPort()
		}
		switch {
		case u.svc.Cfg != nil && !u.svc.Cfg.Features.Traktor.Enabled:
			trkRow.set("disabled")
		case u.svc.Traktor.Listening():
			trkRow.set(fmt.Sprintf("listening :%d", port))
		default:
			trkRow.set(fmt.Sprintf("not bound (:%d in use? Electron may own it)", port))
		}
	}
	refreshTrk()
	// Bind happens async; poll so the row reflects reality, not config intent.
	trkTick := time.NewTicker(2 * time.Second)
	u.closers = append(u.closers, trkTick.Stop)
	goUI("dashboard", func() {
		for range trkTick.C {
			fyne.Do(refreshTrk)
		}
	})

	streamRow := newStatusRow("Stream", "idle")
	applyStream := func(s stream.Status) {
		if s.IsLive {
			streamRow.set(fmt.Sprintf("LIVE · %s · %d queued · last flush %s", s.Title, s.PendingEventCount, flushLabel(s)))
		} else {
			msg := "idle"
			if s.LastError != "" {
				msg = "idle · last error: " + s.LastError
			}
			streamRow.set(msg)
		}
	}
	applyStream(u.svc.Stream.Status())
	stCh, unsub := u.svc.Stream.SubscribeStatus()
	u.closers = append(u.closers, unsub)
	goUI("dashboard", func() {
		for s := range stCh {
			fyne.Do(func() { applyStream(s) })
		}
	})

	acctRow := newStatusRow("Account", "not signed in")
	applyAuth := func(st auth.State) {
		if st.SignedIn {
			acctRow.set("signed in")
		} else {
			acctRow.set("not signed in - sign in via Settings")
		}
	}
	if u.svc.Auth != nil {
		applyAuth(auth.State{SignedIn: u.svc.Auth.SignedIn()})
		u.svc.Auth.OnChange(func(st auth.State) { fyne.Do(func() { applyAuth(st) }) })
	}

	// FormLayout aligns the key column to the widest label so all values start at the same x
	// (status: x / account: y / …), instead of each row's value butting against its own key.
	return container.New(layout.NewFormLayout(),
		apiRow.key, apiRow.obj,
		acctRow.key, acctRow.obj,
		trkRow.key, trkRow.obj,
		streamRow.key, streamRow.obj,
	)
}

// buildNowPlayingLCD is the Winamp-style hero: a recessed LCD well (mint-on-dark mono
// readout of the audible deck) framed in raised brushed metal, with a live spectrum strip.
func (u *UI) buildNowPlayingLCD() fyne.CanvasObject {
	line1 := canvas.NewText("- NO TRACK ON AIR -", colBrandMint)
	line1.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	line1.TextSize = 15
	line2 := canvas.NewText("", withAlpha(colBrandMint, 0xcc))
	line2.TextStyle = fyne.TextStyle{Monospace: true}
	line2.TextSize = 13
	spec := newSpectrum(14)

	info := container.NewVBox(line1, line2)
	inner := container.NewBorder(nil, nil, nil, container.NewGridWrap(fyne.NewSize(74, 30), spec), info)
	well := newBeveledPanel(inner, colLCD, false, 10)   // recessed LCD
	frame := newBeveledPanel(well, colSurface, true, 4) // raised metal bezel
	var npFull string                                   // untruncated "Artist - Title" for right-click Copy

	mmss := func(s float64) string {
		if s <= 0 {
			return "0:00"
		}
		t := int(s)
		return fmt.Sprintf("%d:%02d", t/60, t%60)
	}
	update := func() {
		if u.svc.Session == nil {
			return
		}
		ov := u.svc.Session.Snapshot().BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
		var d *session.DeckSnapshot
		for i := range ov.Decks {
			if ov.Decks[i].Deck == ov.Master.Deck {
				d = &ov.Decks[i]
				break
			}
		}
		if d == nil {
			// No local deck on air → a paired instance's bridged now-playing (the DJ PC's set shows on
			// the VR PC's dashboard too; peerbridge already delivers it, only the Peers tab showed it).
			if r, ok := u.freshestRemoteNowPlaying(); ok {
				d = r
			}
		}
		if d == nil {
			line1.Text = "- NO TRACK ON AIR -"
			line2.Text = ""
			npFull = ""
			spec.SetActive(false)
		} else {
			t := d.Artist
			if t != "" && d.Title != "" {
				t += " - "
			}
			t += d.Title
			npFull = t
			if len(t) > 40 {
				t = t[:39] + "…"
			}
			line1.Text = "♪ " + strings.ToUpper(t)
			meta := fmt.Sprintf("DECK %s    %s / %s", d.Deck, mmss(d.ElapsedTime), mmss(d.TrackLength))
			if d.BPM > 0 {
				meta += fmt.Sprintf("    %.1f BPM", d.BPM)
			}
			if d.Key != "" {
				meta += "    " + d.Key
			}
			line2.Text = meta
			spec.SetActive(d.IsPlaying)
		}
		canvas.Refresh(line1)
		canvas.Refresh(line2)
	}
	update()
	tick := time.NewTicker(500 * time.Millisecond)
	u.closers = append(u.closers, tick.Stop)
	goUI("dashboard-lcd", func() {
		for range tick.C {
			fyne.Do(update)
		}
	})
	return newKitCopyable("track", frame, func() string { return npFull })
}

// freshestRemoteNowPlaying maps the most recent PLAYING bridged peer now-playing to a DeckSnapshot
// (the LCD's shape). ok=false when no peer has a live track (stale > NowPlayingStaleAfter dropped).
func (u *UI) freshestRemoteNowPlaying() (*session.DeckSnapshot, bool) {
	if u.svc.PeerBridge == nil {
		return nil, false
	}
	var best *peerbridge.RemoteState
	for _, s := range u.svc.PeerBridge.RemoteStates() {
		if !s.NowPlaying.Playing || time.Since(s.UpdatedAt) > session.NowPlayingStaleAfter {
			continue
		}
		if best == nil || s.UpdatedAt.After(best.UpdatedAt) {
			best = &s
		}
	}
	if best == nil {
		return nil, false
	}
	np := best.NowPlaying
	return &session.DeckSnapshot{
		Deck: np.Deck, Title: np.Title, Artist: np.Artist,
		BPM: np.BPM, Key: np.Key, ElapsedTime: np.Elapsed, TrackLength: np.Length,
		IsPlaying: np.Playing,
	}, true
}

func flushLabel(s stream.Status) string {
	if s.LastFlushAt == "" {
		return "-"
	}
	if !s.LastFlushOK {
		return "FAILED"
	}
	return "ok"
}

// statusRow is a label:value pair with a live-updatable value, laid out via a shared
// FormLayout (see buildStatusContent) so keys form an aligned column. obj (the layout
// cell) wraps val in a right-click → Copy menu - values are URLs/ports/errors users
// paste into terminals + bug reports (kitCopyable, not Selectable: val mutates live).
type statusRow struct {
	key *widget.Label
	val *widget.Label
	obj fyne.CanvasObject
}

func newStatusRow(label, value string) *statusRow {
	key := widget.NewLabelWithStyle(label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	val := widget.NewLabel(value)
	val.Wrapping = fyne.TextWrapWord // a long value (e.g. an error) wraps instead of widening the card
	return &statusRow{key: key, val: val, obj: newKitCopyableLabel("value", val)}
}

func (r *statusRow) set(v string) {
	r.val.SetText(v)
}
