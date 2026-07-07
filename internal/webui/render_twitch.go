package webui

import (
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/twitch"
)

// Rolling viewer state (published on the bus by whichever instance owns the Twitch session - local
// OR a paired peer). Package-scoped because the single UI's struct fields live in ui.go (not owned
// here); guarded by its own mutex.
var (
	twViewMu sync.Mutex
	twViewer twitch.ViewerInfo
	twViewOK bool
)

// subscribeTwitch buffers chat + alert events + viewer counts from the mesh bus (works for local AND
// paired-peer Twitch, like the Fyne tab). Called once from onReady.
func (u *UI) subscribeTwitch() {
	if u.svc.EventBus == nil {
		return
	}
	canMod := u.svc.Twitch != nil // manager present ⇒ moderation routes (locally or to the owning peer)
	push := func(row string) {
		u.twMu.Lock()
		u.twitchRows = append(u.twitchRows, row)
		if len(u.twitchRows) > 250 {
			u.twitchRows = u.twitchRows[len(u.twitchRows)-250:]
		}
		u.twMu.Unlock()
	}
	u.svc.EventBus.Subscribe(twitch.TopicChat, func(ev eventbus.Event) {
		var e twitch.Event
		if json.Unmarshal(ev.Data, &e) == nil {
			push(twitchChatRow(e, canMod))
		}
	})
	u.svc.EventBus.Subscribe(twitch.TopicEvent, func(ev eventbus.Event) {
		var e twitch.Event
		if json.Unmarshal(ev.Data, &e) == nil {
			push(twitchAlertRow(e))
		}
	})
	u.svc.EventBus.Subscribe(twitch.TopicViewers, func(ev eventbus.Event) {
		var vi twitch.ViewerInfo
		if json.Unmarshal(ev.Data, &vi) != nil {
			return
		}
		twViewMu.Lock()
		twViewer, twViewOK = vi, true
		twViewMu.Unlock()
	})
}

// renderTwitch: OBS control bar (viewer count + per-instance stream/rec) + title-preset strip + live
// chat/alert feed with per-message moderation + send box. Full parity with the Fyne Twitch tab.
func (u *UI) renderTwitch() string {
	var b strings.Builder
	b.WriteString(panel(i18n.T("twitch.title"), i18n.T("twitch.subtitle")))
	if u.svc.Twitch == nil && u.svc.EventBus == nil {
		return b.String() + emptyState(i18n.T("twitch.unavailable"))
	}
	if u.svc.OBSControl != nil {
		b.WriteString(section(i18n.T("twitch.streamingCockpit"), `<div id=twitch-obs>`+u.twitchObsHTML()+`</div>`))
	}
	if u.svc.Twitch != nil && u.svc.Cfg != nil {
		b.WriteString(section(i18n.T("twitch.streamTitle"), `<div id=twitch-presets>`+u.twitchPresetsHTML()+`</div>`))
	}
	b.WriteString(`<div id=twitch-feed class=log-view>` + u.twitchFeedHTML() + `</div>`)
	if u.svc.Twitch != nil {
		b.WriteString(`<form data-act=twitch-send class=tw-send>` +
			`<input class=field-input name=text placeholder=` + attrQ(i18n.T("twitch.sendPlaceholder")) + ` style="flex:1" autocomplete=off>` +
			`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(i18n.T("twitch.send")) + `</button></form>`)
	}
	return b.String()
}

// ── OBS control bar (viewer count + per-instance stream/rec via reused obs-stream/obs-record) ──

func (u *UI) twitchObsHTML() string {
	return `<div class=tw-viewers>` + twitchViewerHTML() + `</div>` + u.cockpitHTML()
}

func twitchViewerHTML() string {
	twViewMu.Lock()
	vi, ok := twViewer, twViewOK
	twViewMu.Unlock()
	switch {
	case !ok:
		return `<span class="tw-vc tw-vc--off">` + html.EscapeString(i18n.T("twitch.viewersUnknown")) + `</span>`
	case vi.Live:
		return `<span class="tw-vc tw-vc--live">` + html.EscapeString(i18n.T("twitch.viewersLive", i18n.A{"count": twComma(vi.ViewerCount)})) + `</span>`
	default:
		return `<span class="tw-vc tw-vc--off">` + html.EscapeString(i18n.T("twitch.offline")) + `</span>`
	}
}

// ── title-preset strip (one chip per preset → apply dialog; manage + add) ──

func (u *UI) twitchPresetsHTML() string {
	f := &u.svc.Cfg.Features.Twitch
	var chips strings.Builder
	for i, p := range f.Presets {
		chips.WriteString(btn(p.Name, "outline", "tw-apply:"+strconv.Itoa(i), ""))
	}
	if len(f.Presets) == 0 {
		chips.WriteString(`<span class=tw-hint>` + html.EscapeString(i18n.T("twitch.noPresetsYet")) + `</span>`)
	}
	tools := btnRow(btn(i18n.T("twitch.managePresets"), "secondary", "tw-presets", ""), btn(i18n.T("twitch.addPreset"), "ghost", "tw-preset-add", ""))
	return `<div class=tw-presets>` + chips.String() + `</div>` + tools
}

// ── feed ──

func (u *UI) twitchFeedHTML() string {
	u.twMu.Lock()
	defer u.twMu.Unlock()
	if len(u.twitchRows) == 0 {
		return `<div class=log-line>` + html.EscapeString(i18n.T("twitch.noMessagesYet")) + `</div>`
	}
	return strings.Join(u.twitchRows, "")
}

func twitchName(e twitch.Event) string {
	if e.UserName != "" {
		return e.UserName
	}
	return e.UserLogin
}

// twitchChatRow: coloured name + subscriber/mod/host/vip/cheer badges + text + (when a manager
// exists) a moderation button. enc = messageID|userID|name for the moderation modal.
func twitchChatRow(e twitch.Event, canMod bool) string {
	var tags strings.Builder
	switch {
	case e.Broadcaster:
		tags.WriteString(badge(i18n.T("twitch.badgeHost"), "error"))
	case e.Mod:
		tags.WriteString(badge(i18n.T("twitch.badgeMod"), "success"))
	}
	if e.VIP {
		tags.WriteString(badge(i18n.T("twitch.badgeVip"), "info"))
	}
	if e.Subscriber {
		tags.WriteString(badge(i18n.T("twitch.badgeSub"), "secondary"))
	}
	if e.Bits > 0 {
		tags.WriteString(badge(i18n.T("twitch.badgeCheer"), "warning"))
	}
	text := e.Text
	if e.Bits > 0 {
		text = i18n.T("twitch.bitsPrefix", i18n.A{"count": fmt.Sprint(e.Bits), "text": e.Text})
	}
	mod := ""
	if canMod {
		enc := e.MessageID + "|" + e.UserID + "|" + twitchName(e)
		mod = `<button class="rp-btn rp-btn--ghost tw-modbtn" data-act=tw-mod data-val="` + html.EscapeString(enc) + `" title=` + attrQ(i18n.T("twitch.moderate")) + `>⋮</button>`
	}
	return `<div class="log-line tw-row">` + mod +
		`<span class=tw-name style="` + twNameStyle(e.Color) + `">` + html.EscapeString(twitchName(e)) + `</span>` +
		tags.String() + ` <span class=tw-msg>` + html.EscapeString(text) + `</span></div>`
}

// twitchAlertRow: kind-specific follow/sub/resub/gift/cheer line with a brand-coloured accent.
func twitchAlertRow(e twitch.Event) string {
	var text, variant string
	switch e.Kind {
	case twitch.KindFollow:
		text, variant = i18n.T("twitch.followed", i18n.A{"name": twitchName(e)}), "follow"
	case twitch.KindSub:
		text, variant = i18n.T("twitch.subscribed", i18n.A{"name": twitchName(e), "tier": tierSuffix(e.Tier)}), "sub"
	case twitch.KindResub:
		text = i18n.T("twitch.resubscribed", i18n.A{
			"name":   twitchName(e),
			"months": i18n.Tn("twitch.monthsCount", e.Total),
			"tier":   tierSuffix(e.Tier),
		})
		if e.Text != "" {
			text += " - " + e.Text
		}
		variant = "sub"
	case twitch.KindGiftSub:
		who := twitchName(e)
		if e.Anon {
			who = i18n.T("twitch.anonymous")
		}
		text, variant = i18n.T("twitch.giftedSubs", i18n.A{"who": who, "count": fmt.Sprint(e.Total), "tier": tierSuffix(e.Tier)}), "sub"
	case twitch.KindCheer:
		who := twitchName(e)
		if e.Anon {
			who = i18n.T("twitch.anonymous")
		}
		text, variant = i18n.T("twitch.cheered", i18n.A{"who": who, "bits": i18n.Tn("twitch.bitsCount", e.Bits)}), "cheer"
	default:
		text, variant = string(e.Kind), "follow"
	}
	return `<div class="log-line tw-alert tw-alert--` + variant + `">` + html.EscapeString(text) + `</div>`
}

func tierSuffix(tier string) string {
	switch tier {
	case "2000":
		return " " + i18n.T("twitch.tier2")
	case "3000":
		return " " + i18n.T("twitch.tier3")
	}
	return ""
}

// twNameStyle returns an inline colour for a validated #rrggbb chat colour, else the brand base.
func twNameStyle(hex string) string {
	if isHexColor(hex) {
		return "color:" + hex
	}
	return "color:var(--rp-base,#F70864)"
}

func isHexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// twComma formats n with thousands separators.
func twComma(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b []byte
	pre := len(s) % 3
	if pre > 0 {
		b = append(b, s[:pre]...)
	}
	for i := pre; i < len(s); i += 3 {
		if len(b) > 0 {
			b = append(b, ',')
		}
		b = append(b, s[i:i+3]...)
	}
	return string(b)
}

// twitchSend sends the chat message from the send box (locally if signed in, else routed to the
// Twitch-owning peer via the manager).
func (u *UI) twitchSend(form string) {
	if u.svc.Twitch == nil {
		return
	}
	text := strings.TrimSpace(parseForm(form)["text"])
	if text == "" {
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		u.logErr("twitch send", u.svc.Twitch.SendChat(ctx, text, ""))
	})
	u.eval("var f=document.querySelector('form[data-act=twitch-send] input');if(f)f.value='';")
}
