package ui

import (
	"context"
	"fmt"
	"image/color"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/obs"
	"rave.page/mate/internal/service"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/aggregator"
	"rave.page/mate/internal/shared/auth"
	"rave.page/mate/internal/vrchat"
)

// ── live status indicators ───────────────────────────────────────────────────

// cardStatus is a feature card's live indicator: colored dot + terse state line.
// The settings ticker re-runs update on the UI thread; rank feeds the section nav
// dot: live(3) > warn(2) > ok(1) > off(0).
type cardStatus struct {
	dot    *canvas.Text
	lbl    *widget.Label
	rank   int
	update func(*cardStatus) // nil = state pushed by the card's own async refresh
}

func (s *cardStatus) set(c color.Color, text string) {
	if s.dot.Color != c {
		s.dot.Color = c
		s.dot.Refresh()
	}
	if s.lbl.Text != text {
		s.lbl.SetText(text)
	}
	switch c {
	case colBrandBase:
		s.rank = 3
	case colBrandAmber:
		s.rank = 2
	case colBrandMint:
		s.rank = 1
	default:
		s.rank = 0
	}
}

// newStatus registers a card indicator with the settings ticker. update runs once now +
// every tick; pass nil when the card pushes state itself (async probes: service, QML, …).
func (u *UI) newStatus(update func(*cardStatus)) *cardStatus {
	dot := canvas.NewText("●", colMuted)
	dot.TextSize = 12
	s := &cardStatus{dot: dot, lbl: mutedInline("-"), update: update}
	if update != nil {
		update(s)
	}
	u.settingsStats = append(u.settingsStats, s)
	return s
}

// onOffStatus is the trivial enabled/off indicator.
func (u *UI) onOffStatus(field *bool, onText string) *cardStatus {
	return u.newStatus(func(s *cardStatus) {
		if *field {
			s.set(colBrandMint, onText)
		} else {
			s.set(colMuted, "off")
		}
	})
}

// sourceInfo returns the aggregation-hub state for the given source ids, preferring a
// receiving source, then a running one.
func (u *UI) sourceInfo(ids ...string) (aggregator.SourceInfo, bool) {
	if u.svc.Session == nil {
		return aggregator.SourceInfo{}, false
	}
	var best aggregator.SourceInfo
	found := false
	for _, si := range u.svc.Session.Sources() {
		if !slices.Contains(ids, si.ID) {
			continue
		}
		if !found || si.Receiving || (si.Running && !best.Receiving) {
			best = si
		}
		found = true
	}
	return best, found
}

// ── sectioned settings ───────────────────────────────────────────────────────

// settingsSection groups related feature cards behind one nav entry; the nav dot
// aggregates the member cards' indicator ranks.
type settingsSection struct {
	name  string
	btn   *widget.Button
	dot   *canvas.Text
	stats []*cardStatus
	view  fyne.CanvasObject
}

// buildSettings is the per-feature control panel: section nav (left, with aggregate
// status dots) + that section's cards in a masonry (right). Every capability is an
// independent toggle; module-backed features start/stop live, the rest gate
// availability. See internal/config (schema) + internal/module (lifecycle).
func (u *UI) buildSettings() fyne.CanvasObject {
	u.settingsStats = nil

	mark := 0
	var sections []*settingsSection
	// section captures the cardStatuses registered while its cards were built (the args
	// evaluate immediately before the call), so the nav dot can aggregate them.
	section := func(name, desc string, cards ...fyne.CanvasObject) {
		s := &settingsSection{name: name, stats: u.settingsStats[mark:]}
		mark = len(u.settingsStats)
		s.dot = canvas.NewText("●", colMuted)
		s.dot.TextSize = 12
		head := container.NewVBox(smallCaps(name), mutedLabel(desc), widget.NewSeparator())
		s.view = container.NewBorder(head, nil, nil, nil,
			container.NewVScroll(container.New(newMasonry(), cards...)))
		sections = append(sections, s)
	}

	section("Account & API", "Sign-in + the endpoint this instance talks to.",
		u.accountCard(), u.apiCard(u.svc.Cfg.APIBaseURL))
	section("DJ sources", "Live deck/mixer data - every enabled source fuses on the Live tab (Decks card). Controller mappings (Traktor/Rekordbox) live here too.",
		u.traktorCard(), u.traktorQmlCard(), u.traktorMappingCard(), u.midiCard(), u.nmlCard(), u.proDjLinkCard(),
		u.seratoCard(), u.virtualdjCard(), u.rekordboxLiveCard(), u.rekordboxKeyCard(), u.rekordboxMidiCard())
	section("Recording", "Tracklists, set capture, OBS recording + fingerprinting (Publish tab).",
		u.recorderCard(), u.setCaptureCard(), u.audioRecordCard(), u.obsCard(), u.obsSyncCard(), u.fingerprintCard())
	section("Streaming & remote", "Publish live sets; let the web app or paired peers drive this box. House timecode for external gear.",
		u.streamBridgeCard(), u.studioCard(), u.peersCard(), u.webcamCard(), u.mediaLinkCard(), u.timecodeCard())
	section("Library & media", "Local library, poster editor + media workers.",
		u.libraryCard(), u.mediaEditorCard(), u.transcodeCard())
	section("Integrations", "Twitch (chat/alerts/title/moderation) + VRChat + VR overlays + DMX lighting.",
		u.twitchCard(), u.sttCard(), u.vrchatCard(), u.vrcToolsCard(), u.worldSyncCard(), u.vrOverlayCard(), u.dmxCard(), u.dmxMidiCard(), u.rtspCard(), u.unityCard())
	section("System", "Notifications, background service + updates.",
		u.appGroupsCard(), u.notificationsCard(), u.guardianCard(), u.serviceCard(), u.updatesCard())

	content := container.NewStack(sections[0].view)
	var selectSec func(i int)
	nav := container.NewVBox()
	for i, s := range sections {
		s.btn = widget.NewButton(s.name, func() { selectSec(i) })
		s.btn.Alignment = widget.ButtonAlignLeading
		nav.Add(container.NewBorder(nil, nil, container.NewCenter(s.dot), nil, s.btn))
	}
	selectSec = func(i int) {
		for j, s := range sections {
			imp := widget.LowImportance
			if j == i {
				imp = widget.HighImportance
			}
			if s.btn.Importance != imp {
				s.btn.Importance = imp
				s.btn.Refresh()
			}
		}
		content.Objects = []fyne.CanvasObject{sections[i].view}
		content.Refresh()
	}
	selectSec(0)

	// Nav dot = highest-priority member card color.
	refreshNavDots := func() {
		for _, s := range sections {
			best, rank := color.Color(colMuted), -1
			for _, st := range s.stats {
				if st.rank > rank {
					rank, best = st.rank, st.dot.Color
				}
			}
			if s.dot.Color != best {
				s.dot.Color = best
				s.dot.Refresh()
			}
		}
	}
	refreshNavDots()

	stats := u.settingsStats
	stop := make(chan struct{})
	u.closers = append(u.closers, func() { close(stop) })
	goUI("settings-status", func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				fyne.Do(func() {
					for _, s := range stats {
						if s.update != nil {
							s.update(s)
						}
					}
					refreshNavDots()
				})
			}
		}
	})

	head := container.NewVBox(
		widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		mutedLabel("Toggle what you use - disabled features own no ports, goroutines, or subprocesses."),
		widget.NewSeparator(),
	)
	// Both panes get shrinkable min widths so a wide card (set capture's form) can't force
	// the split to stack - the masonry reflows to whatever width the trailing pane has.
	split := container.New(newAdaptiveSplit(0.24), shrinkWidth(210, nav), shrinkWidth(380, content))
	return container.NewBorder(head, nil, nil, nil, split)
}

// featureCard renders a titled card: header (title + optional toggle top-right), optional
// live status row (dot + state line), optional description, body.
func featureCard(title, desc string, toggle *widget.Check, st *cardStatus, body ...fyne.CanvasObject) *widget.Card {
	titleLbl := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	var right fyne.CanvasObject
	if toggle != nil {
		right = toggle
	}
	items := []fyne.CanvasObject{container.NewBorder(nil, nil, titleLbl, right, nil)}
	if st != nil {
		// right-click → Copy on the live status value (ports, addresses, error text)
		items = append(items, container.NewHBox(container.NewCenter(st.dot), newKitCopyableLabel("status", st.lbl)))
	}
	if desc != "" {
		items = append(items, mutedLabel(desc))
	}
	items = append(items, body...)
	return widget.NewCard("", "", container.NewVBox(items...))
}

// ── Account (browser sign-in) ────────────────────────────────────────────────

func (u *UI) accountCard() fyne.CanvasObject {
	detail := widget.NewLabel("")
	detail.Wrapping = fyne.TextWrapWord
	signIn := widget.NewButton("Sign in with browser", func() {
		if u.svc.Auth != nil {
			if err := u.svc.Auth.Login(); err != nil {
				u.Notify("rave-mate", "Couldn't open the browser: "+err.Error())
			}
		}
	})
	signIn.Importance = widget.HighImportance
	signOut := widget.NewButton("Sign out", func() {
		if u.svc.Auth != nil {
			u.svc.Auth.SignOut()
		}
	})
	apply := func(s auth.State) {
		if s.SignedIn {
			detail.SetText("Signed in.")
			signIn.SetText("Re-authenticate")
			signIn.Importance = widget.MediumImportance
			signOut.Enable()
		} else {
			detail.SetText("Not signed in. Sign in via your browser to publish sets + open the Studio channel.")
			signIn.SetText("Sign in with browser")
			signIn.Importance = widget.HighImportance
			signOut.Disable()
		}
		signIn.Refresh()
	}
	if u.svc.Auth != nil {
		apply(auth.State{SignedIn: u.svc.Auth.SignedIn()})
		u.svc.Auth.OnChange(func(s auth.State) { fyne.Do(func() { apply(s) }) })
	}
	st := u.newStatus(func(s *cardStatus) {
		if u.svc.Auth != nil && u.svc.Auth.SignedIn() {
			s.set(colBrandMint, "signed in")
		} else {
			s.set(colBrandAmber, "not signed in")
		}
	})
	return featureCard("Account", "Sign-in happens in your browser - no password is entered here.", nil, st,
		detail, container.NewHBox(signIn, signOut))
}

// ── feature cards ────────────────────────────────────────────────────────────

func (u *UI) traktorCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Traktor

	portEntry := newEntry()
	portEntry.SetText(strconv.Itoa(f.ResolvedPort()))
	portEntry.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65536 {
			f.Port = n
			u.saveCfg()
		}
	}
	logChk := widget.NewCheck("Log raw payloads (jsonl, for schema discovery)", func(b bool) {
		f.LogPayloads = b
		if u.svc.Traktor != nil {
			u.svc.Traktor.SetLogging(b)
		}
		u.saveCfg()
	})
	logChk.SetChecked(f.LogPayloads)

	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled {
			s.set(colMuted, "off")
			return
		}
		src, ok := u.sourceInfo(session.SourceTraktor, session.SourceQML)
		switch {
		case ok && src.Receiving:
			s.set(colBrandMint, "receiving deck data")
		case u.svc.Traktor != nil && u.svc.Traktor.Listening():
			s.set(colBrandMint, fmt.Sprintf("listening :%d", f.ResolvedPort()))
		default:
			s.set(colBrandAmber, "not listening")
		}
	})
	toggle := u.moduleTabToggle("traktor", &f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Listen port"), nil, portEntry),
		mutedLabel("Default 8080. Toggle off/on to apply a port change. The Electron client also uses 8080 - only one can bind."),
		logChk,
	)
	return featureCard("Traktor / DJ bridge", "Receive Traktor Pro 4 deck + mixer metadata for live sets.", toggle, st, body)
}

func (u *UI) streamBridgeCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.StreamBridge
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case u.svc.Stream != nil && u.svc.Stream.Status().IsLive:
			s.set(colBrandBase, "LIVE - publishing")
		case u.svc.Auth == nil || !u.svc.Auth.SignedIn():
			s.set(colBrandAmber, "sign-in required")
		case !u.svc.Cfg.Features.Traktor.Enabled:
			s.set(colBrandAmber, "needs the Traktor bridge")
		default:
			s.set(colBrandMint, "ready")
		}
	})
	toggle := u.simpleToggle(&f.Enabled)
	return featureCard("Live stream bridge", "Publish your live set (from Traktor) to rave.page. Needs Traktor + sign-in.", toggle, st)
}

// mediaToolControls renders the detect + "Download & install" UI for an external media tool
// (ffmpeg / fpcalc), mirroring the loopMIDI "install a missing dependency" pattern: it shows
// where the binary was found (the app-managed copy or one on PATH), a button that downloads,
// SHA-256-verifies, and unpacks the official Windows build into the managed bin dir (the
// workers then resolve it automatically), a progress bar while it runs, and a fallback
// hyperlink to the manual download page (the only option on non-Windows).
func (u *UI) mediaToolControls(tool mediatools.Tool) fyne.CanvasObject {
	status := mutedLabel("")
	prog := widget.NewProgressBar()
	prog.Hide()
	link := widget.NewHyperlink("Open the "+tool.Display+" download page", mustURL(tool.HomePage))
	var installBtn *kitButton

	// apply sets the widgets to the current detection state. Called directly during build
	// (we're on the UI thread) and via fyne.Do after an install finishes.
	apply := func() {
		st := tool.Status()
		switch {
		case st.Installed && st.Managed:
			status.SetText("Installed (managed copy): " + st.Path)
			installBtn.SetText("Re-download & install")
			link.Hide()
		case st.Installed:
			status.SetText("Found on PATH: " + st.Path)
			installBtn.SetText("Download a managed copy")
			link.Hide()
		default:
			status.SetText("Not found - download & install it, or add it to your PATH.")
			installBtn.SetText("Download & install")
			link.Show()
		}
	}

	installBtn = newKitButtonWithIcon("Download & install", theme.DownloadIcon(), func() {
		installBtn.Disable()
		prog.SetValue(0)
		prog.Show()
		go func() {
			defer debuglog.Recover(u.svc.Log, "mediatool-install-"+tool.Key, false)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			last := -1
			err := tool.Install(ctx, func(done, total int64) {
				if total <= 0 {
					return
				}
				pct := int(float64(done) / float64(total) * 100)
				if pct == last { // throttle UI updates to whole-percent changes
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
				u.Notify("rave-mate", tool.Display+" install failed: "+err.Error())
			} else {
				u.Notify("rave-mate", tool.Display+" installed.")
			}
		}()
	})
	if !mediatools.CanInstall() {
		installBtn.Disable() // auto-install ships only the Windows builds; elsewhere use the link
	}
	apply()
	return container.NewVBox(status, container.NewHBox(installBtn), prog, link)
}

func (u *UI) transcodeCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Transcode

	ffEntry := newEntry()
	ffEntry.SetPlaceHolder("auto-detect on PATH")
	ffEntry.SetText(f.FfmpegPath)
	ffEntry.OnChanged = func(s string) { f.FfmpegPath = s; u.saveCfg() }

	concEntry := newEntry()
	conc := f.MaxConcurrent
	if conc < 1 {
		conc = 2
	}
	concEntry.SetText(strconv.Itoa(conc))
	concEntry.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 16 {
			f.MaxConcurrent = n
			if u.svc.Workers != nil {
				u.svc.Workers.Configure("transcode", n)
			}
			u.saveCfg()
		}
	}
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case !mediatools.FFmpeg.Status().Installed:
			s.set(colBrandAmber, "ffmpeg missing")
		default:
			s.set(colBrandMint, "ffmpeg ready")
		}
	})
	toggle := u.simpleToggle(&f.Enabled)
	body := container.NewVBox(
		formGrid(fieldLabel("ffmpeg path"), filePickerRow(ffEntry), fieldLabel("Max concurrent jobs"), concEntry),
		mutedLabel("Transcoding runs in worker subprocesses, so a crash can't take down the app."),
		mutedLabel("Loudness normalization (per preset, Library → Transcode) is two-pass: measure the whole track (EBU R128), then ONE constant gain - never compression. Target LUFS + true-peak ceiling are fully configurable."),
		widget.NewSeparator(),
		mutedLabel("FFmpeg also powers media analysis (waveforms, probe metadata) + fingerprint segmenting. Download a managed copy and the workers use it automatically - no PATH setup."),
		u.mediaToolControls(mediatools.FFmpeg),
	)
	return featureCard("Transcode", "ffmpeg-backed media conversion workers (for Local Studio).", toggle, st, body)
}

func (u *UI) studioCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.StudioChannel
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case u.svc.Modules != nil && u.svc.Modules.IsRunning("studio"):
			s.set(colBrandMint, "listening on loopback")
		default:
			s.set(colBrandAmber, "not running")
		}
	})
	toggle := u.moduleToggle("studio", &f.Enabled)
	return featureCard("Local Studio channel",
		"Let the rave.page web app drive this desktop over a secure loopback connection (127.0.0.1).", toggle, st)
}

func (u *UI) vrchatCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.VRChat
	mgr := u.svc.Vrchat
	if mgr == nil {
		st := u.newStatus(func(s *cardStatus) { s.set(colMuted, "unavailable") })
		return featureCard("VRChat link", "Client-side VRChat account link.", u.simpleToggle(&f.Enabled), st)
	}

	detail := widget.NewLabel("Sign in with your VRChat account. Credentials go only to VRChat - never to rave.page.")
	detail.Wrapping = fyne.TextWrapWord

	userEntry := newEntry()
	userEntry.SetPlaceHolder("VRChat username / email")
	passEntry := newPasswordEntry()
	passEntry.SetPlaceHolder("password")
	codeEntry := newEntry()
	codeEntry.SetPlaceHolder("2FA code")

	var signIn, verify, unlink *widget.Button
	signIn = widget.NewButton("Sign in", func() {
		user, pass := userEntry.Text, passEntry.Text
		if user == "" || pass == "" {
			return
		}
		signIn.Disable()
		goUI("vrchat-login", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = mgr.Login(ctx, user, pass) // outcome lands via OnChange
			fyne.Do(func() {
				passEntry.SetText("") // never keep the password around
				signIn.Enable()
			})
		})
	})
	signIn.Importance = widget.HighImportance
	verify = widget.NewButton("Verify", func() {
		code := codeEntry.Text
		if code == "" {
			return
		}
		// Prefer TOTP when offered; otherwise the first advertised method.
		method := ""
		if ms := mgr.State().Methods; len(ms) > 0 && !hasStr(ms, "totp") {
			method = ms[0]
		}
		verify.Disable()
		goUI("vrchat-2fa", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = mgr.Verify2FA(ctx, method, code)
			fyne.Do(func() {
				codeEntry.SetText("")
				verify.Enable()
			})
		})
	})
	verify.Importance = widget.HighImportance
	unlink = widget.NewButton("Unlink", func() {
		goUI("vrchat-unlink", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			mgr.Unlink(ctx)
		})
	})

	rememberChk := widget.NewCheck("Stay signed in (session sealed at rest - Windows DPAPI)", func(b bool) {
		f.RememberSession = b
		u.saveCfg()
	})
	rememberChk.SetChecked(f.RememberSession)
	uplinkChk := widget.NewCheck("Share session with rave.page (server-side group/event features)", func(b bool) {
		f.Uplink = b
		u.saveCfg()
		if u.svc.VrchatUplink != nil {
			goUI("vrchat-uplink", func() { u.svc.VrchatUplink(b) }) // apply now: store / delete the server vault
		}
	})
	uplinkChk.SetChecked(f.Uplink)

	loginRow := container.NewVBox(userEntry, passEntry, container.NewHBox(signIn))
	tfaRow := container.NewVBox(codeEntry, container.NewHBox(verify))
	linkedRow := container.NewHBox(unlink)

	apply := func(s vrchat.State) {
		loginRow.Hide()
		tfaRow.Hide()
		linkedRow.Hide()
		switch {
		case s.LoggedIn:
			detail.SetText("Linked as " + s.DisplayName + ".")
			linkedRow.Show()
		case s.Awaiting2FA:
			detail.SetText("Two-factor required (" + strings.Join(s.Methods, ", ") + ") - enter your code.")
			tfaRow.Show()
		default:
			txt := "Sign in with your VRChat account. Credentials go only to VRChat - never to rave.page."
			if s.Message != "" {
				txt += " (" + s.Message + ")"
			}
			detail.SetText(txt)
			loginRow.Show()
		}
	}
	apply(mgr.State())
	mgr.OnChange(func(s vrchat.State) { fyne.Do(func() { apply(s) }) })

	st := u.newStatus(func(s *cardStatus) {
		ms := mgr.State()
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case ms.LoggedIn && u.svc.VrchatPipe != nil && u.svc.VrchatPipe.Status().Connected:
			s.set(colBrandMint, "live - pipeline connected")
		case ms.LoggedIn:
			s.set(colBrandMint, "signed in as "+ms.DisplayName)
		case ms.Awaiting2FA:
			s.set(colBrandAmber, "2FA pending")
		default:
			s.set(colBrandAmber, "not signed in")
		}
	})
	toggle := u.moduleTabToggle("vrchat", &f.Enabled) // also gates the VRChat tab (status/bio + emoji)
	return featureCard("VRChat link",
		"Link your VRChat account: realtime presence via the pipeline socket + a VRChat tab (status/bio editing, animated-emoji generator). Password is used once for sign-in; only the session cookie is kept.",
		toggle, st, detail, loginRow, tfaRow, linkedRow, rememberChk, uplinkChk)
}

func (u *UI) notificationsCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Notifications
	st := u.onOffStatus(&f.Enabled, "on")
	toggle := u.simpleToggle(&f.Enabled)
	return featureCard("Notifications", "Native desktop notifications for stream + job events.", toggle, st)
}

// guardianCard toggles the crash guardian (inverted config flag: zero value = on).
func (u *UI) guardianCard() fyne.CanvasObject {
	cfg := u.svc.Cfg
	st := u.newStatus(func(s *cardStatus) {
		if cfg.DisableCrashGuardian {
			s.set(colMuted, "off")
		} else {
			s.set(colBrandMint, "armed")
		}
	})
	sw := widget.NewCheck("", func(on bool) {
		cfg.DisableCrashGuardian = !on
		u.saveCfg()
	})
	sw.SetChecked(!cfg.DisableCrashGuardian)
	body := container.NewVBox(
		mutedLabel("A tiny supervisor process relaunches the app if it dies without a clean shutdown (driver/VR runtime faults can take the whole process down). Deliberate quits never trigger it, and a crash loop stops after 4 restarts in 10 minutes."),
		mutedLabel("Changing this takes effect on the next launch."),
	)
	return featureCard("Crash auto-restart", "Relaunch automatically after a hard crash.", sw, st, body)
}

func (u *UI) obsCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.OBS
	host := newEntry()
	host.SetText(f.ResolvedHost())
	host.OnChanged = func(s string) { f.Host = s; u.saveCfg() }
	port := newEntry()
	port.SetText(strconv.Itoa(f.ResolvedPort()))
	port.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65536 {
			f.Port = n
			u.saveCfg()
		}
	}
	pass := newPasswordEntry()
	pass.SetText(f.Password)
	pass.OnChanged = func(s string) { f.Password = s; u.saveCfg() }

	validate := widget.NewButton("Connect & validate stream settings", func() {
		host, portN, pw := f.ResolvedHost(), f.ResolvedPort(), f.Password
		go func() {
			defer debuglog.Recover(u.svc.Log, "obs-validate", false)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, err := obs.Connect(ctx, host, portN, pw)
			if err != nil {
				u.Notify("OBS", "connect failed: "+err.Error())
				return
			}
			defer func() { _ = c.Close() }()
			diffs, err := c.ValidateStreamSettings(obs.DefaultStreamRequirements())
			if err != nil {
				u.Notify("OBS", "validate failed: "+err.Error())
				return
			}
			if len(diffs) == 0 {
				u.Notify("OBS", "stream settings look good")
			} else {
				u.Notify("OBS", "check: "+strings.Join(diffs, "; "))
			}
		}()
	})
	st := u.newStatus(func(s *cardStatus) {
		if !f.Enabled || u.svc.OBS == nil {
			s.set(colMuted, "off")
			return
		}
		o := u.svc.OBS.Status()
		switch {
		case o.Recording:
			s.set(colBrandBase, "recording · "+time.Since(o.RecStartedAt).Truncate(time.Second).String())
		case o.Connected:
			s.set(colBrandMint, "connected")
		default:
			s.set(colBrandAmber, "not connected")
		}
	})
	toggle := u.moduleToggle("obs", &f.Enabled) // live module: bridge watches for finished recordings
	remoteCount := widget.NewLabel("")
	refreshRemotes := func() { remoteCount.SetText(fmt.Sprintf("%d remote OBS on the LAN", len(f.Remotes))) }
	refreshRemotes()
	remotes := widget.NewButtonWithIcon("Remote OBS…", theme.ComputerIcon(), func() { u.obsRemotesDialog(refreshRemotes) })
	body := container.NewVBox(
		formGrid(fieldLabel("Host"), host, fieldLabel("Port"), port, fieldLabel("Password"), pass),
		validate,
		mutedLabel("Enable obs-websocket in OBS (Tools → WebSocket Server Settings). Default port 4455."),
		mutedLabel("While enabled, finished OBS recordings are linked to the set recorded over the same span (Publish tab). Connection changes apply on toggle off/on."),
		container.NewBorder(nil, nil, remoteCount, remotes, widget.NewLabel("")),
		mutedLabel("Remote OBS: connect directly to OBS on another LAN PC (no rave-mate needed there). Each appears in the Streaming cockpit + VR with its own start/stop control + bitrate."),
	)
	return featureCard("OBS", "Recording capture + stream-settings validation via obs-websocket.", toggle, st, body)
}

// obsRemotesDialog manages the list of direct LAN OBS endpoints.
func (u *UI) obsRemotesDialog(onChange func()) {
	f := &u.svc.Cfg.Features.OBS
	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.Remotes {
			r := &f.Remotes[i]
			idx := i
			edit := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
				u.obsRemoteEditDialog(r, func() { rebuild(); onChange() })
			})
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.Remotes = append(f.Remotes[:idx], f.Remotes[idx+1:]...)
				u.saveCfg()
				rebuild()
				onChange()
			})
			state := "on"
			if !r.Enabled {
				state = "off"
			}
			row := container.NewBorder(nil, nil, nil, container.NewHBox(edit, del),
				widget.NewLabel(fmt.Sprintf("%s - %s:%d (%s)", r.ResolvedName(), r.Host, r.ResolvedPort(), state)))
			list.Add(row)
		}
		if len(f.Remotes) == 0 {
			list.Add(mutedLabel("No remote OBS yet. Add one below (the OBS PC needs obs-websocket enabled)."))
		}
		list.Refresh()
	}
	rebuild()
	add := widget.NewButtonWithIcon("Add remote OBS", theme.ContentAddIcon(), func() {
		f.Remotes = append(f.Remotes, config.OBSRemote{Port: 4455, Enabled: true})
		u.saveCfg()
		rebuild()
		onChange()
	})
	content := container.NewBorder(nil, add, nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("Remote OBS instances", "Done", content, u.win)
	d.Resize(fyne.NewSize(560, 460))
	d.Show()
}

// obsRemoteEditDialog edits one remote OBS endpoint.
func (u *UI) obsRemoteEditDialog(r *config.OBSRemote, onSave func()) {
	name := newEntry()
	name.SetText(r.Name)
	name.SetPlaceHolder("e.g. Stream PC")
	host := newEntry()
	host.SetText(r.Host)
	host.SetPlaceHolder("192.168.1.5")
	port := newEntry()
	port.SetText(strconv.Itoa(r.ResolvedPort()))
	pass := newPasswordEntry()
	pass.SetText(r.Password)
	enabled := widget.NewCheck("Enabled", nil)
	enabled.SetChecked(r.Enabled)
	form := widget.NewForm(
		widget.NewFormItem("Name", name),
		widget.NewFormItem("Host / IP", host),
		widget.NewFormItem("Port", port),
		widget.NewFormItem("Password", pass),
		widget.NewFormItem("", enabled),
	)
	d := dialog.NewCustomConfirm("Remote OBS", "Save", "Cancel", form, func(ok bool) {
		if !ok {
			return
		}
		r.Name, r.Host, r.Password, r.Enabled = name.Text, host.Text, pass.Text, enabled.Checked
		if n, err := strconv.Atoi(port.Text); err == nil && n > 0 && n < 65536 {
			r.Port = n
		}
		u.saveCfg()
		onSave()
	}, u.win)
	d.Resize(fyne.NewSize(420, 360))
	d.Show()
}

func (u *UI) libraryCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Library
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case mediatools.MPV.Status().Installed:
			s.set(colBrandMint, "mpv ready")
		default:
			s.set(colBrandAmber, "mpv missing")
		}
	})
	toggle := u.tabToggle(&f.Enabled)
	body := container.NewVBox(
		mutedLabel("The in-app player uses mpv for smooth, hardware-accelerated video, controlled from rave-mate (transport, jump-to-track, trim). Without mpv, video falls back to a slower in-app decoder; audio + waveforms don't need it."),
		u.mediaToolControls(mediatools.MPV),
	)
	if runtime.GOOS == "windows" {
		embed := newToggle(&u.svc.Cfg.Features.Player.Embed, func(bool) { u.saveCfg() })
		body.Add(container.NewHBox(
			embed,
			labelWithHelp("Play video inside the app window",
				"Renders mpv's GPU video INTO the player pane instead of a separate popout window. Off = mpv opens its own window."),
		))
	}
	if runtime.GOOS != "darwin" { // Gio aux windows unsupported on darwin (GIO_MIGRATION.md)
		pf := &u.svc.Cfg.Features.Player
		gio := widget.NewCheck("", func(v bool) {
			pf.GioWindow = &v // tri-state: unset = Gio default; touching the toggle pins an explicit value
			u.saveCfg()
		})
		gio.Checked = pf.UseGioWindow()
		body.Add(container.NewHBox(
			gio,
			labelWithHelp("New player window (default)",
				"Pop-out video opens in the new dense player window (embedded mpv, waveform strip, trim/export). Off = the previous pop-out player."),
		))
	}
	return featureCard("Library", "Native file browser + media player + metadata viewer (Library tab).", toggle, st, body)
}

func (u *UI) mediaEditorCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.MediaEditor
	st := u.onOffStatus(&f.Enabled, "on")
	toggle := u.tabToggle(&f.Enabled)
	return featureCard("Media editor", "Poster + thumbnail composer for releases/socials (Editor tab).", toggle, st)
}

func (u *UI) appGroupsCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.AppGroups
	st := u.onOffStatus(&f.Enabled, "on")
	toggle := u.tabToggle(&f.Enabled)
	return featureCard("App groups", "Relaunch a set of DJ-rig apps after a crash (App Groups tab). Recovered apps outlive rave-mate.", toggle, st)
}

func (u *UI) fingerprintCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Fingerprint
	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case !mediatools.Fpcalc.Status().Installed:
			s.set(colBrandAmber, "fpcalc missing")
		default:
			s.set(colBrandMint, "fpcalc ready")
		}
	})
	toggle := u.simpleToggle(&f.Enabled)
	body := container.NewVBox(
		mutedLabel("Needs Chromaprint's fpcalc. Download a managed copy below and the fingerprint workers use it automatically - no PATH setup."),
		u.mediaToolControls(mediatools.Fpcalc),
	)
	return featureCard("Fingerprinting", "Chromaprint track fingerprinting to help the rave.page music database.", toggle, st, body)
}

// ── service install ──────────────────────────────────────────────────────────

func (u *UI) serviceCard() fyne.CanvasObject {
	status := widget.NewLabel("checking…")
	st := u.newStatus(nil) // pushed by refresh (SCM query is too heavy for the 2s tick)
	refresh := func() {
		stat, err := service.Status()
		if err != nil {
			stat = "error: " + err.Error()
		}
		fyne.Do(func() {
			status.SetText("Service: " + stat)
			switch {
			case stat == "running" || stat == "active" || stat == "loaded":
				st.set(colBrandMint, "service "+stat)
			case stat == "not installed":
				st.set(colMuted, "not installed")
			case strings.HasPrefix(stat, "error"):
				st.set(colBrandAmber, "status check failed")
			default:
				st.set(colBrandAmber, "service "+stat)
			}
		})
	}
	goUI("settings", refresh)

	run := func(label string, fn func() error) {
		go func() {
			defer debuglog.Recover(u.svc.Log, "service-"+label, false)
			if err := fn(); err != nil {
				u.Notify("rave-mate", label+" failed: "+err.Error())
			} else {
				u.Notify("rave-mate", label+" succeeded")
			}
			refresh()
		}()
	}
	install := newKitButton("Install service", func() { run("Install", service.InstallInteractive) })
	uninstall := newKitButton("Uninstall service", func() { run("Uninstall", service.UninstallInteractive) })
	refreshBtn := newKitButton("Refresh", func() { goUI("settings", refresh) })

	return featureCard("Run in background",
		"Install as an OS service so rave-mate runs without the window open.", nil, st,
		status,
		WrapActions(install, uninstall, refreshBtn),
		mutedLabel("Windows: install/uninstall need an elevated (admin) prompt. Linux/macOS install per-user."),
		mutedLabel("The service runs headless under the system account (own config, no signed-in session, no tray icon) - meant for dedicated headless boxes. Both share one instance slot; launching the desktop app takes over from a running service (it returns at next boot). On a desktop, use crash auto-restart above instead."))
}

func (u *UI) apiCard(base string) fyne.CanvasObject {
	r := newStatusRow("Base URL", base)
	return featureCard("API", "", nil, nil,
		formGrid(r.key, r.obj),
		mutedLabel("Override with RAVE_API_BASE_URL; prod only via RAVE_ENV=production."))
}

// ── toggle helpers ───────────────────────────────────────────────────────────

// newToggle builds a checkbox whose handler is wired only AFTER the initial SetChecked, so
// constructing it (during buildSettings, which runs inside New() before u.tabs exists) never
// fires onChange - otherwise the initial state would spuriously save config, restart modules,
// or rebuild the tab bar (the last nil-derefs u.tabs at startup).
func newToggle(field *bool, onChange func(bool)) *widget.Check {
	c := widget.NewCheck("", nil)
	c.SetChecked(*field)
	c.OnChanged = func(b bool) {
		*field = b
		onChange(b)
	}
	return c
}

// simpleToggle flips a config bool + saves (no live module).
func (u *UI) simpleToggle(field *bool) *widget.Check {
	return newToggle(field, func(bool) { u.saveCfg() })
}

// moduleToggle flips a config bool, saves, and applies the change live via the module
// manager (start/stop the subsystem immediately).
func (u *UI) moduleToggle(name string, field *bool) *widget.Check {
	return newToggle(field, func(b bool) {
		u.saveCfg()
		if u.svc.Modules != nil {
			u.svc.Modules.SetEnabled(name, b)
		}
	})
}

// sessionToggle flips a DJ-data source/sink bool, saves, and live-reconciles the aggregation
// hub so the component starts/stops immediately - no restart. (Value changes like a path/port
// still need a toggle off/on to re-read config.)
func (u *UI) sessionToggle(field *bool) *widget.Check {
	return newToggle(field, func(bool) {
		u.saveCfg()
		if u.svc.Session != nil {
			u.svc.Session.Reconcile()
		}
	})
}

// tabToggle flips a tab-gated feature bool, saves, and rebuilds the tab bar live so the
// tab appears/disappears without a restart.
func (u *UI) tabToggle(field *bool) *widget.Check {
	return newToggle(field, func(bool) {
		u.saveCfg()
		u.rebuildTabs()
	})
}

// moduleTabToggle is tabToggle for a feature that's ALSO a live module (start/stop) - used
// by Traktor (a module that backs a tab).
func (u *UI) moduleTabToggle(name string, field *bool) *widget.Check {
	return newToggle(field, func(b bool) {
		u.saveCfg()
		if u.svc.Modules != nil {
			u.svc.Modules.SetEnabled(name, b)
		}
		u.rebuildTabs()
	})
}

// saveWindowSize persists the current window content size so the next launch restores it (instead
// of the 85%-of-screen first-run default). Ignores degenerate sizes (hidden/minimized window).
func (u *UI) saveWindowSize() {
	if u.win == nil || u.svc.Cfg == nil {
		return
	}
	s := u.win.Canvas().Size()
	if s.Width < 600 || s.Height < 400 {
		return
	}
	// Never persist a canvas larger than the screen (the OS clamps the window but the
	// canvas keeps the requested size) - restoring it desyncs clicks from visuals.
	if sw, sh, ok := screenSizeDIP(); ok {
		if s.Width > sw {
			s.Width = sw
		}
		if s.Height > sh*0.96 {
			s.Height = sh * 0.96
		}
	}
	if s.Width == u.svc.Cfg.WindowW && s.Height == u.svc.Cfg.WindowH {
		return
	}
	u.svc.Cfg.WindowW, u.svc.Cfg.WindowH = s.Width, s.Height
	if err := u.svc.Cfg.Save(); err != nil {
		u.svc.Log.Warn("app", "failed to save window size", map[string]any{"error": err.Error()})
	}
}

func (u *UI) saveCfg() {
	if u.svc.Cfg == nil {
		return
	}
	if err := u.svc.Cfg.Save(); err != nil {
		u.svc.Log.Warn("app", "failed to save config", map[string]any{"error": err.Error()})
	}
	// Apply source/sink enable changes to the running aggregation hub without a restart.
	if u.svc.Session != nil {
		u.svc.Session.Reconcile()
	}
}
