package ui

import (
	"context"
	"net/url"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
)

// worldSyncCard is the World Sync settings card: feature toggle + GitHub link
// (Device Code Flow or pasted PAT, gist scope). List/channel management lives on
// the Worlds tab.
func (u *UI) worldSyncCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.WorldSync
	gh := u.svc.GitHub
	if gh == nil {
		st := u.newStatus(func(s *cardStatus) { s.set(colMuted, "unavailable") })
		return featureCard("World Sync", "VRChat world gist feeds.", u.simpleToggle(&f.Enabled), st)
	}

	detail := widget.NewLabel("")
	detail.Wrapping = fyne.TextWrapWord

	// Device-flow rows (Twitch pattern).
	codeLabel := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true, Monospace: true})
	var verifyURL string
	openBtn := widget.NewButtonWithIcon("Open GitHub activation page", theme.ComputerIcon(), func() {
		if verifyURL == "" {
			return
		}
		if uri, err := url.Parse(verifyURL); err == nil {
			_ = u.app.OpenURL(uri)
		}
	})
	copyCodeBtn := widget.NewButtonWithIcon("Copy code", theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(codeLabel.Text)
	})
	pendingRow := container.NewVBox(
		mutedLabel("Open the activation page and enter this code, then approve in your browser:"),
		container.NewHBox(codeLabel, copyCodeBtn),
		container.NewHBox(openBtn),
	)

	var applyState func()
	signIn := widget.NewButtonWithIcon("Link GitHub (device code)", theme.LoginIcon(), nil)
	signIn.Importance = widget.HighImportance
	patBtn := widget.NewButtonWithIcon("Paste token…", theme.ContentPasteIcon(), func() {
		u.worldSyncPATDialog(func() { applyState() })
	})
	logout := widget.NewButtonWithIcon("Unlink", theme.LogoutIcon(), func() {
		gh.Logout()
		applyState()
	})

	signIn.OnTapped = func() {
		signIn.Disable()
		detail.SetText("Starting GitHub link…")
		goUI("github-auth", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			da, err := gh.StartDevice(ctx)
			if err != nil {
				fyne.Do(func() { signIn.Enable(); detail.SetText("Link failed: " + err.Error()) })
				return
			}
			fyne.Do(func() {
				verifyURL = da.VerificationURI
				codeLabel.SetText(da.UserCode)
				detail.SetText("Waiting for you to approve on GitHub…")
				signIn.Hide()
				pendingRow.Show()
				if uri, e := url.Parse(da.VerificationURI); e == nil {
					_ = u.app.OpenURL(uri)
				}
			})
			err = gh.PollDevice(ctx, da)
			fyne.Do(func() {
				signIn.Enable()
				if err != nil {
					detail.SetText("Link failed: " + err.Error())
				} else {
					u.Notify("World Sync", "GitHub linked")
				}
				applyState()
			})
		})
	}

	applyState = func() {
		pendingRow.Hide()
		if gh.SignedIn() {
			detail.SetText("Linked as " + gh.Login() + ". Manage permission lists + world displays on the Worlds tab.")
			signIn.Hide()
			patBtn.Hide()
			logout.Show()
		} else {
			detail.SetText("Link a GitHub account (gist scope only). Device code needs an OAuth app client id in config; pasting a classic PAT with 'gist' scope always works. Token is sealed at rest, never logged.")
			signIn.Show()
			patBtn.Show()
			logout.Hide()
		}
	}
	applyState()

	// Publish mode: direct (the user's own gist token) vs hosted (rave.page's worldlive API creates
	// the gists under its account - no token needed, but a target world id is required).
	modeSel := widget.NewSelect([]string{config.WorldSyncModeDirect, config.WorldSyncModeHosted}, nil)
	modeSel.SetSelected(f.ResolvedPublishMode())
	worldIDEntry := widget.NewEntry()
	worldIDEntry.SetPlaceHolder("wrld_… (target world id)")
	worldIDEntry.SetText(f.HostedWorldID)
	worldIDEntry.OnChanged = func(v string) { f.HostedWorldID = strings.TrimSpace(v); u.saveCfg() }
	worldIDRow := container.NewBorder(nil, nil, widget.NewLabel("World id"), nil, worldIDEntry)
	syncMode := func() {
		if f.ResolvedPublishMode() == config.WorldSyncModeHosted {
			worldIDRow.Show()
		} else {
			worldIDRow.Hide()
		}
	}
	modeSel.OnChanged = func(v string) { f.PublishMode = v; u.saveCfg(); syncMode() }
	syncMode()
	modeRow := container.NewBorder(nil, nil,
		container.NewHBox(widget.NewLabel("Publish via"),
			helpIcon("Direct: your own GitHub gist token writes the gists. Hosted: rave.page creates them under its account - no token needed; set the target world id.")),
		nil, modeSel)

	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case gh.SignedIn():
			s.set(colBrandMint, "linked as "+gh.Login())
		default:
			s.set(colBrandAmber, "GitHub not linked")
		}
	})
	toggle := u.moduleTabToggle("worldsync", &f.Enabled)
	return featureCard("World Sync",
		"Feed VRChat worlds from gists: permission lists (VideoTXL etc.), poster billboards, upcoming events + a now-playing card - updated live, no world rebuild.",
		toggle, st, detail, modeRow, worldIDRow, container.NewHBox(signIn, patBtn, logout), pendingRow)
}

// ghTokenNewURL opens GitHub's new-token page with the gist scope pre-checked + a description.
const ghTokenNewURL = "https://github.com/settings/tokens/new?scopes=gist&description=rave-mate%20gist%20publishing"

// worldSyncPATDialog prompts for a classic PAT (gist scope) and validates it.
func (u *UI) worldSyncPATDialog(onDone func()) {
	gh := u.svc.GitHub
	pat := widget.NewPasswordEntry()
	pat.SetPlaceHolder("ghp_… (classic token, 'gist' scope)")
	getTokenBtn := widget.NewButtonWithIcon("Get token…", theme.MailForwardIcon(), func() {
		if uri, err := url.Parse(ghTokenNewURL); err == nil {
			_ = u.app.OpenURL(uri)
		}
	})
	content := container.NewVBox(
		mutedLabel("Create one at github.com/settings/tokens → classic → only the 'gist' scope. Stored sealed (OS secret store), never logged."),
		container.NewHBox(getTokenBtn, helpIcon("Opens GitHub with the 'gist' scope pre-checked. Approve to create a classic token, then paste it here.")),
		pat,
	)
	d := dialog.NewCustomConfirm("Paste GitHub token", "Link", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		goUI("github-pat", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := gh.SetPAT(ctx, pat.Text)
			fyne.Do(func() {
				if err != nil {
					u.Notify("World Sync", "Token rejected: "+err.Error())
				} else {
					u.Notify("World Sync", "GitHub linked as "+gh.Login())
				}
				onDone()
			})
		})
	}, u.win)
	d.Resize(fyne.NewSize(460, 220))
	d.Show()
}
