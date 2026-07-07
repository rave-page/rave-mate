package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"image/color"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/twitch"
)

const twitchMaxRows = 250

// buildTwitch is the Twitch tab: live chat + follow/sub/bit alerts, a send box, and per-message
// moderation. It subscribes to the event BUS (not the manager directly), so it renders chat from a
// remote peer that owns the Twitch connection just as well as a local one.
func (u *UI) buildTwitch() fyne.CanvasObject {
	mgr := u.svc.Twitch
	rows := container.NewVBox()
	scroll := container.NewVScroll(rows)

	addRow := func(o fyne.CanvasObject) {
		rows.Add(o)
		if len(rows.Objects) > twitchMaxRows {
			rows.Objects = rows.Objects[len(rows.Objects)-twitchMaxRows:]
		}
		rows.Refresh()
		scroll.ScrollToBottom()
	}

	render := func(ev twitch.Event) {
		switch ev.Kind {
		case twitch.KindChat:
			addRow(u.twitchChatRow(mgr, ev))
		default:
			addRow(twitchAlertRow(ev))
		}
	}

	// Subscribe to the bus (local + remote events arrive identically). Fall back to the manager's
	// direct hook if there's no bus.
	if u.svc.EventBus != nil {
		handle := func(e eventbus.Event) {
			var ev twitch.Event
			if json.Unmarshal(e.Data, &ev) != nil {
				return
			}
			fyne.Do(func() { render(ev) })
		}
		unsubChat := u.svc.EventBus.Subscribe(twitch.TopicChat, handle)
		unsubEvent := u.svc.EventBus.Subscribe(twitch.TopicEvent, handle)
		u.closers = append(u.closers, unsubChat, unsubEvent)
	} else if mgr != nil {
		mgr.SetOnEvent(func(ev twitch.Event) { fyne.Do(func() { render(ev) }) })
		u.closers = append(u.closers, func() { mgr.SetOnEvent(nil) })
	}

	// Send box.
	input := newEntry()
	input.SetPlaceHolder("Send a message to chat…")
	send := func() {
		text := strings.TrimSpace(input.Text)
		if text == "" || mgr == nil {
			return
		}
		input.SetText("")
		goUI("twitch-send", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := mgr.SendChat(ctx, text, ""); err != nil {
				u.Notify("Twitch", "Send failed: "+err.Error())
			}
		})
	}
	input.OnSubmitted = func(string) { send() }
	sendBtn := widget.NewButtonWithIcon("Send", theme.MailSendIcon(), send)
	sendBtn.Importance = widget.HighImportance
	sendRow := container.NewBorder(nil, nil, nil, sendBtn, input)

	cockpit, cockpitStop := u.obsControlBar("Twitch", "Streaming cockpit")
	u.closers = append(u.closers, cockpitStop)
	head := container.NewVBox(cockpit)
	if strip := u.twitchTitleStrip(mgr); strip != nil {
		head.Add(strip)
	}
	head.Add(widget.NewLabelWithStyle("Twitch chat & alerts", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
	return container.NewBorder(head, sendRow, nil, nil, scroll)
}

// twitchTitleStrip is the stream-title preset strip (moved off the Settings Twitch card):
// pick a preset, fill its {placeholders}, apply. nil without a manager/config.
func (u *UI) twitchTitleStrip(mgr *twitch.Manager) fyne.CanvasObject {
	if mgr == nil || u.svc.Cfg == nil {
		return nil
	}
	f := &u.svc.Cfg.Features.Twitch
	presetSel := widget.NewSelect(presetNames(f.Presets), nil)
	presetSel.PlaceHolder = "(title preset)"
	applyBtn := widget.NewButtonWithIcon("Apply title", theme.ConfirmIcon(), func() {
		p := findPreset(f.Presets, presetSel.Selected)
		if p == nil {
			u.Notify("Twitch", "Pick a preset first")
			return
		}
		u.twitchApplyPresetDialog(mgr, p)
	})
	manageBtn := widget.NewButtonWithIcon("Presets…", theme.SettingsIcon(), func() {
		u.twitchManagePresetsDialog(func() { presetSel.SetOptions(presetNames(f.Presets)) })
	})
	return kitToolStrip(
		smallCaps("TITLE"),
		container.NewGridWrap(fyne.NewSize(200, presetSel.MinSize().Height), presetSel),
		applyBtn, manageBtn,
		helpIcon("Stream-title presets - one per genre/venue, with {placeholders} you fill in on apply (live preview). Also sets the Twitch category. Sign-in lives in Settings ▸ Integrations ▸ Twitch."),
	)
}

// twitchChatRow renders a chat line: colored name + message + a moderation menu button.
func (u *UI) twitchChatRow(mgr *twitch.Manager, ev twitch.Event) fyne.CanvasObject {
	name := canvas.NewText(badgePrefix(ev)+displayName(ev)+":", twitchNameColor(ev.Color))
	name.TextStyle = fyne.TextStyle{Bold: true}
	msg := widget.NewLabel(ev.Text)
	msg.Wrapping = fyne.TextWrapWord
	if ev.Bits > 0 {
		msg.SetText(fmt.Sprintf("(%d bits) %s", ev.Bits, ev.Text))
	}
	var mod fyne.CanvasObject
	if mgr != nil {
		btn := widget.NewButtonWithIcon("", theme.MoreVerticalIcon(), func() { u.twitchModerateDialog(mgr, ev) })
		btn.Importance = widget.LowImportance
		mod = btn
	}
	return container.NewBorder(nil, nil, name, mod, msg)
}

// twitchAlertRow renders a follow/sub/cheer alert as a single brand-colored line.
func twitchAlertRow(ev twitch.Event) fyne.CanvasObject {
	var text string
	col := colBrandViol
	switch ev.Kind {
	case twitch.KindFollow:
		text = "★ " + displayName(ev) + " followed"
	case twitch.KindSub:
		text = "💜 " + displayName(ev) + " subscribed" + tierSuffix(ev.Tier)
		col = colBrandHot
	case twitch.KindResub:
		text = fmt.Sprintf("💜 %s resubscribed (%d months)%s", displayName(ev), ev.Total, tierSuffix(ev.Tier))
		col = colBrandHot
		if ev.Text != "" {
			text += " - " + ev.Text
		}
	case twitch.KindGiftSub:
		who := displayName(ev)
		if ev.Anon {
			who = "Anonymous"
		}
		text = fmt.Sprintf("🎁 %s gifted %d sub(s)%s", who, ev.Total, tierSuffix(ev.Tier))
		col = colBrandHot
	case twitch.KindCheer:
		who := displayName(ev)
		if ev.Anon {
			who = "Anonymous"
		}
		text = fmt.Sprintf("✦ %s cheered %d bits", who, ev.Bits)
		col = colBrandMint
	default:
		text = string(ev.Kind)
	}
	t := canvas.NewText(text, col)
	t.TextStyle = fyne.TextStyle{Bold: true}
	return t
}

// twitchModerateDialog offers delete/timeout/ban for a chat message.
func (u *UI) twitchModerateDialog(mgr *twitch.Manager, ev twitch.Event) {
	run := func(cmd twitch.ModerateCmd) {
		goUI("twitch-mod", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := mgr.Moderate(ctx, cmd); err != nil {
				u.Notify("Twitch", "Moderation failed: "+err.Error())
			} else {
				u.Notify("Twitch", "Done")
			}
		})
	}
	del := widget.NewButtonWithIcon("Delete message", theme.DeleteIcon(), func() {
		run(twitch.ModerateCmd{Action: "delete", MessageID: ev.MessageID})
	})
	to := widget.NewButtonWithIcon("Timeout 10 min", theme.MediaPauseIcon(), func() {
		run(twitch.ModerateCmd{Action: "timeout", UserID: ev.UserID, Duration: 600, Reason: "timeout"})
	})
	ban := widget.NewButtonWithIcon("Ban user", theme.CancelIcon(), func() {
		run(twitch.ModerateCmd{Action: "ban", UserID: ev.UserID, Reason: "banned"})
	})
	ban.Importance = widget.DangerImportance
	content := container.NewVBox(
		mutedLabel("Moderate "+displayName(ev)+":"),
		del, to, ban,
	)
	dialog.NewCustom("Moderation", "Close", content, u.win).Show()
}

func displayName(ev twitch.Event) string {
	if ev.UserName != "" {
		return ev.UserName
	}
	return ev.UserLogin
}

func badgePrefix(ev twitch.Event) string {
	var b strings.Builder
	if ev.Broadcaster {
		b.WriteString("⬢ ")
	} else if ev.Mod {
		b.WriteString("⚔ ")
	}
	if ev.VIP {
		b.WriteString("◆ ")
	}
	if ev.Subscriber {
		b.WriteString("★ ")
	}
	return b.String()
}

func tierSuffix(tier string) string {
	switch tier {
	case "2000":
		return " (Tier 2)"
	case "3000":
		return " (Tier 3)"
	}
	return ""
}

// twitchNameColor parses a #rrggbb chat color, falling back to the brand base for empty/invalid.
func twitchNameColor(hex string) color.Color {
	if len(hex) == 7 && hex[0] == '#' {
		var r, g, bl uint8
		if _, err := fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &bl); err == nil {
			return color.NRGBA{R: r, G: g, B: bl, A: 255}
		}
	}
	return colBrandBase
}
