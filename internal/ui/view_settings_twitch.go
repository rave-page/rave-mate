package ui

import (
	"context"
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"net/url"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/twitch"
)

// twitchCard is the Twitch integration settings card: Device-Code sign-in, connection status, and
// stream-title presets with {variables}. Chat + alerts + moderation live on the Twitch tab.
func (u *UI) twitchCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Twitch
	mgr := u.svc.Twitch
	if mgr == nil {
		st := u.newStatus(func(s *cardStatus) { s.set(colMuted, "unavailable") })
		return featureCard("Twitch", "Chat, alerts, stream title + moderation.", u.simpleToggle(&f.Enabled), st)
	}

	detail := widget.NewLabel("")
	detail.Wrapping = fyne.TextWrapWord

	// Device-flow rows.
	codeLabel := widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true, Monospace: true})
	var verifyURL string
	openBtn := widget.NewButtonWithIcon("Open Twitch activation page", theme.ComputerIcon(), func() {
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
	var signIn, logout *widget.Button
	signIn = widget.NewButtonWithIcon("Sign in to Twitch", theme.LoginIcon(), nil)
	signIn.Importance = widget.HighImportance
	logout = widget.NewButtonWithIcon("Sign out", theme.LogoutIcon(), func() {
		mgr.Auth().Logout()
		applyState()
	})

	// signIn handler: Device Code Flow.
	signIn.OnTapped = func() {
		signIn.Disable()
		detail.SetText("Starting sign-in…")
		goUI("twitch-auth", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			da, err := mgr.Auth().StartDevice(ctx)
			if err != nil {
				fyne.Do(func() { signIn.Enable(); detail.SetText("Sign-in failed: " + err.Error()) })
				return
			}
			fyne.Do(func() {
				verifyURL = da.VerificationURI
				codeLabel.SetText(da.UserCode)
				detail.SetText("Waiting for you to approve in the browser…")
				signIn.Hide()
				pendingRow.Show()
				if uri, e := url.Parse(da.VerificationURI); e == nil {
					_ = u.app.OpenURL(uri)
				}
			})
			err = mgr.Auth().PollDevice(ctx, da)
			fyne.Do(func() {
				signIn.Enable()
				if err != nil {
					detail.SetText("Sign-in failed: " + err.Error())
					applyState()
					return
				}
				mgr.Kick() // connect EventSub now
				u.Notify("Twitch", "Signed in")
				applyState()
			})
		})
	}

	// applyState shows the right rows for the current sign-in state (status dot updates via ticker).
	applyState = func() {
		pendingRow.Hide()
		if mgr.SignedIn() {
			self := mgr.Self()
			if self.Login != "" {
				detail.SetText("Signed in as " + self.DisplayName + ". Chat, alerts, title presets + moderation live on the Twitch tab.")
			} else {
				detail.SetText("Signed in. Connecting…")
			}
			signIn.Hide()
			logout.Show()
		} else {
			detail.SetText("Sign in with the Rave-Mate Twitch app (Device Code - no password, no secret). Scopes: title, chat r/w, follows, subs, bits, moderation.")
			signIn.Show()
			logout.Hide()
		}
	}
	applyState()

	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case mgr.SignedIn() && mgr.Self().Login != "":
			s.set(colBrandMint, "live as "+mgr.Self().Login)
		case mgr.SignedIn():
			s.set(colBrandAmber, "connecting…")
		default:
			s.set(colBrandAmber, "not signed in")
		}
	})
	toggle := u.moduleTabToggle("twitch", &f.Enabled)
	return featureCard("Twitch",
		"Live chat, follow/sub/bit alerts, one-click stream-title presets + moderation. Cross-PC: another rave-mate instance can use this connection over the peer link.",
		toggle, st, detail, container.NewHBox(signIn, logout), pendingRow)
}

// twitchApplyPresetDialog prompts for the preset's {variables} (live preview) then sets the title.
func (u *UI) twitchApplyPresetDialog(mgr *twitch.Manager, p *config.TitlePreset) {
	vars := twitch.TemplateVars(p.Template)
	if p.Vars == nil {
		p.Vars = map[string]string{}
	}
	entries := map[string]*widget.Entry{}
	var items []*widget.FormItem
	preview := widget.NewLabel("")
	preview.Wrapping = fyne.TextWrapWord
	refresh := func() {
		tmp := map[string]string{}
		for k, e := range entries {
			tmp[k] = e.Text
		}
		preview.SetText(twitch.ResolveTemplate(p.Template, tmp))
	}
	for _, v := range vars {
		e := newEntry()
		e.SetText(p.Vars[v])
		e.OnChanged = func(string) { refresh() }
		entries[v] = e
		items = append(items, widget.NewFormItem(v, e))
	}
	gameEntry := newEntry()
	gameEntry.SetText(p.GameName)
	gameEntry.SetPlaceHolder("Twitch category (optional)")
	items = append(items, widget.NewFormItem("Category", gameEntry))
	refresh()
	content := container.NewVBox(widget.NewForm(items...), widget.NewSeparator(), mutedLabel("Preview:"), preview)
	d := dialog.NewCustomConfirm("Apply: "+p.Name, "Set title", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		for k, e := range entries {
			p.Vars[k] = e.Text
		}
		p.GameName = gameEntry.Text
		u.saveCfg()
		goUI("twitch-title", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := mgr.ApplyTitlePreset(ctx, *p); err != nil {
				u.Notify("Twitch", "Title update failed: "+err.Error())
			} else {
				u.Notify("Twitch", "Stream title updated")
			}
		})
	}, u.win)
	d.Resize(fyne.NewSize(480, 360))
	d.Show()
}

// twitchManagePresetsDialog lists presets with add/edit/remove. onChange refreshes the caller.
func (u *UI) twitchManagePresetsDialog(onChange func()) {
	f := &u.svc.Cfg.Features.Twitch
	list := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		list.RemoveAll()
		for i := range f.Presets {
			p := &f.Presets[i]
			edit := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
				u.twitchEditPresetDialog(p, func() { rebuild(); onChange() })
			})
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.Presets = append(f.Presets[:i], f.Presets[i+1:]...)
				u.saveCfg()
				rebuild()
				onChange()
			})
			row := container.NewBorder(nil, nil, nil, container.NewHBox(edit, del),
				widget.NewLabel(fmt.Sprintf("%s - %s", p.Name, p.Template)))
			list.Add(row)
		}
		if len(f.Presets) == 0 {
			list.Add(mutedLabel("No presets yet. Add one below."))
		}
		list.Refresh()
	}
	rebuild()
	addBtn := widget.NewButtonWithIcon("Add preset", theme.ContentAddIcon(), func() {
		f.Presets = append(f.Presets, config.TitlePreset{Name: "New preset", Template: "{genre} set @ {club}"})
		u.saveCfg()
		rebuild()
		onChange()
	})
	content := container.NewBorder(nil, addBtn, nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("Title presets", "Done", content, u.win)
	d.Resize(fyne.NewSize(560, 460))
	d.Show()
}

// twitchEditPresetDialog edits one preset's name/template/category.
func (u *UI) twitchEditPresetDialog(p *config.TitlePreset, onSave func()) {
	name := newEntry()
	name.SetText(p.Name)
	tmpl := newEntry()
	tmpl.SetText(p.Template)
	tmpl.SetPlaceHolder("{genre} set @ {club} - {event}")
	game := newEntry()
	game.SetText(p.GameName)
	game.SetPlaceHolder("Twitch category (optional)")
	content := container.NewVBox(widget.NewForm(
		widget.NewFormItem("Name", name),
		widget.NewFormItem("Template", tmpl),
		widget.NewFormItem("Category", game),
	), mutedLabel("Use {placeholders} for values you fill in on apply (e.g. {genre}, {club}, {event})."))
	d := dialog.NewCustomConfirm("Edit preset", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		p.Name, p.Template, p.GameName = name.Text, tmpl.Text, game.Text
		u.saveCfg()
		onSave()
	}, u.win)
	d.Resize(fyne.NewSize(480, 300))
	d.Show()
}

func presetNames(ps []config.TitlePreset) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func findPreset(ps []config.TitlePreset, name string) *config.TitlePreset {
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i]
		}
	}
	return nil
}
