package webui

import (
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/twitch"
	"rave.page/mate/internal/zigui"
)

// Twitch is a Zig-rendered tab (native/zigui/src/twitch.zig): Go resolves the feed +
// presets + viewer state + i18n into twState, the Zig lib renders HTML byte-identical to
// the Go renderers below (fallback + golden reference, zigui_golden_twitch_test.go).
// The rolling feed buffer holds RESOLVED ROW STATE (twRow), not markup, so both
// renderers build the same rows from one source.
//
// Exception: the streaming-cockpit block inside #twitch-obs is render_live.go's
// cockpitHTML - that renderer belongs to the Live tab, so it rides through the state as
// pre-rendered trusted markup and is emitted raw by both renderers.

// Rolling viewer state (published on the bus by whichever instance owns the Twitch session - local
// OR a paired peer). Package-scoped because the single UI's struct fields live in ui.go (not owned
// here); guarded by its own mutex.
var (
	twViewMu sync.Mutex
	twViewer twitch.ViewerInfo
	twViewOK bool
)

const twFeedCap = 250 // rolling feed rows; drop-oldest

// twTag is one badge beside a chat name.
type twTag struct {
	Text    string `json:"text"`
	Variant string `json:"variant"`
}

// twRow is one resolved feed row. Kind ∈ day|chat|alert; the other fields are the ones
// that kind renders.
type twRow struct {
	Kind string `json:"kind"`

	Date string `json:"date"` // day

	Name      string  `json:"name"` // chat
	NameStyle string  `json:"nameStyle"`
	Tags      []twTag `json:"tags"`
	Mod       bool    `json:"mod"`      // moderation button present
	ModVal    string  `json:"modVal"`   // messageID|userID|name
	ModTitle  string  `json:"modTitle"` // button title

	Text    string `json:"text"`    // chat message / alert line
	Variant string `json:"variant"` // alert accent (follow|sub|cheer)
}

// twViewerState is the viewer-count chip.
type twViewerState struct {
	Cls  string `json:"cls"` // trusted class literal
	Text string `json:"text"`
}

// twObsState is the #twitch-obs fragment: viewer chip + the Live-tab cockpit markup.
type twObsState struct {
	Viewers twViewerState `json:"viewers"`
	Cockpit string        `json:"cockpit"` // RAW: render_live.go cockpitHTML output
}

// twPresetsState is the #twitch-presets fragment (one chip per title preset).
type twPresetsState struct {
	Chips  []uiBtn `json:"chips"`
	Empty  string  `json:"empty"` // shown instead of chips when there are none
	Manage string  `json:"manage"`
	Add    string  `json:"add"`
}

// twFeedState is the #twitch-feed fragment.
type twFeedState struct {
	Empty string  `json:"empty"`
	Rows  []twRow `json:"rows"`
}

// twState is the resolved render state for the Twitch view (JSON → Zig).
type twState struct {
	Title       string `json:"title"`
	Sub         string `json:"sub"`
	Available   bool   `json:"available"` // manager or bus present
	Unavailable string `json:"unavailable"`

	ShowObs  bool       `json:"showObs"` // OBSControl present
	ObsTitle string     `json:"obsTitle"`
	Obs      twObsState `json:"obs"`

	ShowPresets  bool           `json:"showPresets"`
	PresetsTitle string         `json:"presetsTitle"`
	Presets      twPresetsState `json:"presets"`

	Feed twFeedState `json:"feed"`

	ShowSend bool   `json:"showSend"`
	SendPH   string `json:"sendPh"`
	SendLbl  string `json:"sendLbl"`
}

// subscribeTwitch seeds the feed from the persistent chat log (history survives restarts -
// a set streamed with the tab closed is readable after), then buffers live chat + alert
// events + viewer counts from the mesh bus (works for local AND paired-peer Twitch, like
// the Fyne tab). Called once from onReady.
func (u *UI) subscribeTwitch() {
	if u.svc.EventBus == nil {
		return
	}
	canMod := u.svc.Twitch != nil // manager present ⇒ moderation routes (locally or to the owning peer)
	if cl := u.svc.TwitchLog; cl != nil {
		if evs := cl.Recent(twFeedCap); len(evs) > 0 {
			rows := make([]twRow, 0, len(evs)+2)
			day := ""
			for _, e := range evs {
				if d := time.UnixMilli(e.TS).Format("2006-01-02"); d != day {
					day = d
					rows = append(rows, twDayRow(d))
				}
				if e.Kind == twitch.KindChat {
					rows = append(rows, twChatRow(e, canMod))
				} else {
					rows = append(rows, twAlertRow(e))
				}
			}
			u.twMu.Lock()
			u.twitchRows = rows
			u.twMu.Unlock()
		}
	}
	push := func(row twRow) {
		u.twMu.Lock()
		u.twitchRows = append(u.twitchRows, row)
		if len(u.twitchRows) > twFeedCap {
			u.twitchRows = u.twitchRows[len(u.twitchRows)-twFeedCap:]
		}
		u.twMu.Unlock()
	}
	u.svc.EventBus.Subscribe(twitch.TopicChat, func(ev eventbus.Event) {
		var e twitch.Event
		if json.Unmarshal(ev.Data, &e) == nil {
			push(twChatRow(e, canMod))
		}
	})
	u.svc.EventBus.Subscribe(twitch.TopicEvent, func(ev eventbus.Event) {
		var e twitch.Event
		if json.Unmarshal(ev.Data, &e) == nil {
			push(twAlertRow(e))
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

// ── state builders ──

// twitchState resolves availability + the three sections + i18n into render state.
func (u *UI) twitchState() twState {
	st := twState{
		Title:       i18n.T("twitch.title"),
		Sub:         i18n.T("twitch.subtitle"),
		Available:   u.svc.Twitch != nil || u.svc.EventBus != nil,
		Unavailable: i18n.T("twitch.unavailable"),
		Feed:        twFeedState{Empty: i18n.T("twitch.noMessagesYet"), Rows: []twRow{}},
		Presets:     twPresetsState{Chips: []uiBtn{}},
	}
	if !st.Available {
		return st
	}
	if u.svc.OBSControl != nil {
		st.ShowObs, st.ObsTitle, st.Obs = true, i18n.T("twitch.streamingCockpit"), u.twObsState()
	}
	if u.svc.Twitch != nil && u.svc.Cfg != nil {
		st.ShowPresets, st.PresetsTitle, st.Presets = true, i18n.T("twitch.streamTitle"), u.twPresetsState()
	}
	st.Feed = u.twFeedState()
	if u.svc.Twitch != nil {
		st.ShowSend = true
		st.SendPH, st.SendLbl = i18n.T("twitch.sendPlaceholder"), i18n.T("twitch.send")
	}
	return st
}

// twObsState resolves the viewer chip + the Live-tab cockpit markup.
func (u *UI) twObsState() twObsState {
	return twObsState{Viewers: twViewers(), Cockpit: u.cockpitHTML()}
}

// twViewers resolves the viewer-count chip from the bus-published viewer info.
func twViewers() twViewerState {
	twViewMu.Lock()
	vi, ok := twViewer, twViewOK
	twViewMu.Unlock()
	switch {
	case !ok:
		return twViewerState{Cls: "tw-vc tw-vc--off", Text: i18n.T("twitch.viewersUnknown")}
	case vi.Live:
		return twViewerState{Cls: "tw-vc tw-vc--live", Text: i18n.T("twitch.viewersLive", i18n.A{"count": twComma(vi.ViewerCount)})}
	default:
		return twViewerState{Cls: "tw-vc tw-vc--off", Text: i18n.T("twitch.offline")}
	}
}

// twPresetsState resolves the title-preset chips + tools.
func (u *UI) twPresetsState() twPresetsState {
	f := &u.svc.Cfg.Features.Twitch
	st := twPresetsState{
		Chips:  make([]uiBtn, 0, len(f.Presets)),
		Empty:  i18n.T("twitch.noPresetsYet"),
		Manage: i18n.T("twitch.managePresets"),
		Add:    i18n.T("twitch.addPreset"),
	}
	for i, p := range f.Presets {
		st.Chips = append(st.Chips, uiBtn{Label: p.Name, Variant: "outline", Act: "tw-apply:" + strconv.Itoa(i)})
	}
	return st
}

// twFeedState snapshots the rolling feed. Snapshot under twMu, render outside - the
// 250-row build must not block eventbus publishers appending in push (slice header copy;
// appends never write into the snapshot).
func (u *UI) twFeedState() twFeedState {
	u.twMu.Lock()
	rows := u.twitchRows
	u.twMu.Unlock()
	if rows == nil {
		rows = []twRow{}
	}
	return twFeedState{Empty: i18n.T("twitch.noMessagesYet"), Rows: rows}
}

// twDayRow is a date separator between persisted-history days. Tags is non-nil on every
// row: a nil slice marshals to JSON null, which fails the Zig slice parse.
func twDayRow(d string) twRow { return twRow{Kind: "day", Date: d, Tags: []twTag{}} }

func twitchName(e twitch.Event) string {
	if e.UserName != "" {
		return e.UserName
	}
	return e.UserLogin
}

// twChatRow: coloured name + subscriber/mod/host/vip/cheer badges + text + (when a manager
// exists) a moderation button. ModVal = messageID|userID|name for the moderation modal.
func twChatRow(e twitch.Event, canMod bool) twRow {
	r := twRow{Kind: "chat", Name: twitchName(e), NameStyle: twNameStyle(e.Color), Tags: []twTag{}, Text: e.Text}
	switch {
	case e.Broadcaster:
		r.Tags = append(r.Tags, twTag{Text: i18n.T("twitch.badgeHost"), Variant: "error"})
	case e.Mod:
		r.Tags = append(r.Tags, twTag{Text: i18n.T("twitch.badgeMod"), Variant: "success"})
	}
	if e.VIP {
		r.Tags = append(r.Tags, twTag{Text: i18n.T("twitch.badgeVip"), Variant: "info"})
	}
	if e.Subscriber {
		r.Tags = append(r.Tags, twTag{Text: i18n.T("twitch.badgeSub"), Variant: "secondary"})
	}
	if e.Bits > 0 {
		r.Tags = append(r.Tags, twTag{Text: i18n.T("twitch.badgeCheer"), Variant: "warning"})
		r.Text = i18n.T("twitch.bitsPrefix", i18n.A{"count": fmt.Sprint(e.Bits), "text": e.Text})
	}
	if canMod {
		r.Mod = true
		r.ModVal = e.MessageID + "|" + e.UserID + "|" + twitchName(e)
		r.ModTitle = i18n.T("twitch.moderate")
	}
	return r
}

// twAlertRow: kind-specific follow/sub/resub/gift/cheer line with a brand-coloured accent.
func twAlertRow(e twitch.Event) twRow {
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
	return twRow{Kind: "alert", Text: text, Variant: variant, Tags: []twTag{}}
}

// ── bridges ──

// renderTwitch: OBS control bar (viewer count + per-instance stream/rec) + title-preset strip + live
// chat/alert feed with per-message moderation + send box. Full parity with the Fyne Twitch tab.
func (u *UI) renderTwitch() string {
	st := u.twitchState()
	if zigui.Available() {
		if h, ok := zigui.RenderTwitch(stateJSON(st)); ok {
			return h
		}
	}
	return twitchHTML(st)
}

// twitchObsHTML is the #twitch-obs fragment.
func (u *UI) twitchObsHTML() string {
	st := u.twObsState()
	if zigui.Available() {
		if h, ok := zigui.RenderTwitchObs(stateJSON(st)); ok {
			return h
		}
	}
	return twObsHTML(st)
}

// twitchPresetsHTML is the #twitch-presets fragment.
func (u *UI) twitchPresetsHTML() string {
	st := u.twPresetsState()
	if zigui.Available() {
		if h, ok := zigui.RenderTwitchPresets(stateJSON(st)); ok {
			return h
		}
	}
	return twPresetsHTML(st)
}

// twitchFeedHTML is the #twitch-feed inner fragment (patched on every chat/alert event).
func (u *UI) twitchFeedHTML() string {
	st := u.twFeedState()
	if zigui.Available() {
		if h, ok := zigui.RenderTwitchFeed(stateJSON(st)); ok {
			return h
		}
	}
	return twFeedHTML(st)
}

// ── pure Go renderers (golden reference; byte-identical to Zig) ──

func twitchHTML(st twState) string {
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	if !st.Available {
		return b.String() + emptyState(st.Unavailable)
	}
	if st.ShowObs {
		b.WriteString(section(st.ObsTitle, `<div id=twitch-obs>`+twObsHTML(st.Obs)+`</div>`))
	}
	if st.ShowPresets {
		b.WriteString(section(st.PresetsTitle, `<div id=twitch-presets>`+twPresetsHTML(st.Presets)+`</div>`))
	}
	b.WriteString(`<div id=twitch-feed class=log-view>` + twFeedHTML(st.Feed) + `</div>`)
	if st.ShowSend {
		b.WriteString(`<form data-act=twitch-send class=tw-send>` +
			`<input class=field-input name=text placeholder=` + attrQ(st.SendPH) + ` style="flex:1" autocomplete=off>` +
			`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(st.SendLbl) + `</button></form>`)
	}
	return b.String()
}

func twObsHTML(st twObsState) string {
	return `<div class=tw-viewers>` + twViewerHTML(st.Viewers) + `</div>` + st.Cockpit
}

func twViewerHTML(st twViewerState) string {
	return `<span class="` + st.Cls + `">` + html.EscapeString(st.Text) + `</span>`
}

func twPresetsHTML(st twPresetsState) string {
	var chips strings.Builder
	for _, c := range st.Chips {
		chips.WriteString(c.html())
	}
	if len(st.Chips) == 0 {
		chips.WriteString(`<span class=tw-hint>` + html.EscapeString(st.Empty) + `</span>`)
	}
	tools := btnRow(btn(st.Manage, "secondary", "tw-presets", ""), btn(st.Add, "ghost", "tw-preset-add", ""))
	return `<div class=tw-presets>` + chips.String() + `</div>` + tools
}

func twFeedHTML(st twFeedState) string {
	if len(st.Rows) == 0 {
		return `<div class=log-line>` + html.EscapeString(st.Empty) + `</div>`
	}
	var b strings.Builder
	for _, r := range st.Rows {
		b.WriteString(twRowHTML(r))
	}
	return b.String()
}

func twRowHTML(r twRow) string {
	switch r.Kind {
	case "day":
		return `<div class="log-line tw-sep">— ` + html.EscapeString(r.Date) + ` —</div>`
	case "alert":
		return `<div class="log-line tw-alert tw-alert--` + r.Variant + `">` + html.EscapeString(r.Text) + `</div>`
	}
	mod := ""
	if r.Mod {
		mod = `<button class="rp-btn rp-btn--ghost tw-modbtn" data-act=tw-mod data-val="` + html.EscapeString(r.ModVal) + `" title=` + attrQ(r.ModTitle) + `>⋮</button>`
	}
	var tags strings.Builder
	for _, t := range r.Tags {
		tags.WriteString(badge(t.Text, t.Variant))
	}
	return `<div class="log-line tw-row">` + mod +
		`<span class=tw-name style="` + r.NameStyle + `">` + html.EscapeString(r.Name) + `</span>` +
		tags.String() + ` <span class=tw-msg>` + html.EscapeString(r.Text) + `</span></div>`
}

// ── small helpers ──

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
