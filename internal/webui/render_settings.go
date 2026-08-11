package webui

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/remotecache"
	"rave.page/mate/internal/serato"
	"rave.page/mate/internal/stt"
	"rave.page/mate/internal/traktormap"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/vrdll"
	"rave.page/mate/internal/vroverlay"
	"rave.page/mate/internal/zigui"
)

// setToggle describes one feature toggle: how to read/write the config bool + how to apply it.
// module != "" → also Modules.SetEnabled(module, on); retab → nav may gain/lose a tab.
type setToggle struct {
	id, label, module string
	retab             bool
	get               func() bool
	set               func(bool)
}

// toggleRegistry is the full Settings feature-toggle set (mirrors the Fyne cards' toggle semantics:
// simple/module/session/tab/moduleTab). All persist via saveCfgBG (async saveCfg off the actWorker).
func (u *UI) toggleRegistry() []setToggle {
	f := &u.svc.Cfg.Features
	c := u.svc.Cfg
	return []setToggle{
		// DJ sources
		{id: "traktor", label: i18n.T("settings.toggle.traktor"), module: "traktor", retab: true, get: func() bool { return f.Traktor.Enabled }, set: func(b bool) { f.Traktor.Enabled = b }},
		{id: "midi", label: i18n.T("settings.toggle.midi"), get: func() bool { return f.MIDI.Enabled }, set: func(b bool) { f.MIDI.Enabled = b }},
		{id: "nml", label: i18n.T("settings.toggle.nml"), get: func() bool { return f.NML.Enabled }, set: func(b bool) { f.NML.Enabled = b }},
		{id: "prodjlink", label: i18n.T("settings.toggle.prodjlink"), get: func() bool { return f.ProDJLink.Enabled }, set: func(b bool) { f.ProDJLink.Enabled = b }},
		{id: "serato", label: i18n.T("settings.toggle.serato"), get: func() bool { return f.Serato.Enabled }, set: func(b bool) { f.Serato.Enabled = b }},
		{id: "virtualdj", label: i18n.T("settings.toggle.virtualdj"), get: func() bool { return f.VirtualDJ.Enabled }, set: func(b bool) { f.VirtualDJ.Enabled = b }},
		{id: "rekordbox", label: i18n.T("settings.toggle.rekordbox"), get: func() bool { return f.Rekordbox.Enabled }, set: func(b bool) { f.Rekordbox.Enabled = b }},
		// Recording
		{id: "recorder", label: i18n.T("settings.toggle.recorder"), get: func() bool { return f.Recorder.Enabled }, set: func(b bool) { f.Recorder.Enabled = b }},
		{id: "setcapture", label: i18n.T("settings.toggle.setcapture"), module: "setcapture", get: func() bool { return f.SetCapture.Enabled }, set: func(b bool) { f.SetCapture.Enabled = b }},
		{id: "audiorecord", label: i18n.T("settings.toggle.audiorecord"), module: "audiorecord", get: func() bool { return f.AudioRecord.Enabled }, set: func(b bool) { f.AudioRecord.Enabled = b }},
		{id: "obs", label: i18n.T("settings.toggle.obs"), module: "obs", get: func() bool { return f.OBS.Enabled }, set: func(b bool) { f.OBS.Enabled = b }},
		{id: "obssync", label: i18n.T("settings.toggle.obssync"), get: func() bool { return f.OBS.Sync.Enabled }, set: func(b bool) { f.OBS.Sync.Enabled = b }},
		{id: "fingerprint", label: i18n.T("settings.toggle.fingerprint"), get: func() bool { return f.Fingerprint.Enabled }, set: func(b bool) { f.Fingerprint.Enabled = b }},
		// Streaming & remote
		{id: "streambridge", label: i18n.T("settings.toggle.streambridge"), get: func() bool { return f.StreamBridge.Enabled }, set: func(b bool) { f.StreamBridge.Enabled = b }},
		{id: "studio", label: i18n.T("settings.toggle.studio"), module: "studio", get: func() bool { return f.StudioChannel.Enabled }, set: func(b bool) { f.StudioChannel.Enabled = b }},
		{id: "peers", label: i18n.T("settings.toggle.peers"), module: "peers", retab: true, get: func() bool { return f.Peers.Enabled }, set: func(b bool) { f.Peers.Enabled = b }},
		{id: "accountbridge", label: i18n.T("settings.toggle.accountbridge"), module: "accountbridge", get: func() bool { return f.AccountBridge.Enabled }, set: func(b bool) { f.AccountBridge.Enabled = b }},
		{id: "webcam", label: i18n.T("settings.toggle.webcam"), module: "webcam", get: func() bool { return f.Webcam.Enabled }, set: func(b bool) { f.Webcam.Enabled = b }},
		{id: "medialink", label: i18n.T("settings.toggle.medialink"), get: func() bool { return f.MediaLink.ShareVideo }, set: func(b bool) { f.MediaLink.ShareVideo = b }},
		{id: "timecode", label: i18n.T("settings.toggle.timecode"), module: "timecode", get: func() bool { return f.Timecode.Enabled }, set: func(b bool) { f.Timecode.Enabled = b }},
		{id: "ablelink", label: i18n.T("settings.toggle.ablelink"), module: "abletonlink", get: func() bool { return f.AbletonLink.Enabled }, set: func(b bool) { f.AbletonLink.Enabled = b }},
		// Library & media
		{id: "library", label: i18n.T("settings.toggle.library"), retab: true, get: func() bool { return f.Library.Enabled }, set: func(b bool) { f.Library.Enabled = b }},
		{id: "mediaeditor", label: i18n.T("settings.toggle.mediaeditor"), retab: true, get: func() bool { return f.MediaEditor.Enabled }, set: func(b bool) { f.MediaEditor.Enabled = b }},
		{id: "transcode", label: i18n.T("settings.toggle.transcode"), get: func() bool { return f.Transcode.Enabled }, set: func(b bool) { f.Transcode.Enabled = b }},
		{id: "gridfix", label: i18n.T("settings.toggle.gridfix"), get: func() bool { return f.GridFix.Enabled }, set: func(b bool) { f.GridFix.Enabled = b }},
		// Integrations
		{id: "twitch", label: i18n.T("settings.toggle.twitch"), module: "twitch", retab: true, get: func() bool { return f.Twitch.Enabled }, set: func(b bool) { f.Twitch.Enabled = b }},
		{id: "stt", label: i18n.T("settings.toggle.stt"), get: func() bool { return f.STT.Enabled }, set: func(b bool) { f.STT.Enabled = b }},
		{id: "vrchat", label: i18n.T("settings.toggle.vrchat"), module: "vrchat", retab: true, get: func() bool { return f.VRChat.Enabled }, set: func(b bool) { f.VRChat.Enabled = b }},
		{id: "vrctools", label: i18n.T("settings.toggle.vrctools"), get: func() bool { return f.VRCTools.Enabled }, set: func(b bool) { f.VRCTools.Enabled = b }},
		{id: "worldsync", label: i18n.T("settings.toggle.worldsync"), module: "worldsync", retab: true, get: func() bool { return f.WorldSync.Enabled }, set: func(b bool) { f.WorldSync.Enabled = b }},
		{id: "vroverlay", label: i18n.T("settings.toggle.vroverlay"), module: "vroverlay", get: func() bool { return f.VROverlay.Enabled }, set: func(b bool) { f.VROverlay.Enabled = b }},
		{id: "dmx", label: i18n.T("settings.toggle.dmx"), module: "dmx", get: func() bool { return f.DMX.Enabled }, set: func(b bool) { f.DMX.Enabled = b }},
		{id: "dmxmidi", label: i18n.T("settings.toggle.dmxmidi"), module: "dmxmidi", get: func() bool { return f.DMXMIDI.Enabled }, set: func(b bool) { f.DMXMIDI.Enabled = b }},
		{id: "rtsp", label: i18n.T("settings.toggle.rtsp"), module: "rtspserve", get: func() bool { return f.RTSPServe.Enabled }, set: func(b bool) { f.RTSPServe.Enabled = b }},
		{id: "vrslstream", label: i18n.T("settings.toggle.vrslstream"), module: "vrslstream", get: func() bool { return f.Stream.Enabled }, set: func(b bool) { f.Stream.Enabled = b }},
		{id: "mocap", label: i18n.T("settings.toggle.mocap"), module: "mocap", get: func() bool { return f.Mocap.Enabled }, set: func(b bool) { f.Mocap.Enabled = b }},
		{id: "crew", label: i18n.T("settings.toggle.crew"), module: "crew", get: func() bool { return f.Crew.Enabled }, set: func(b bool) { f.Crew.Enabled = b }},
		{id: "unity", label: i18n.T("settings.toggle.unity"), get: func() bool { return f.Unity.Enabled }, set: func(b bool) { f.Unity.Enabled = b }},
		// System
		{id: "appgroups", label: i18n.T("settings.toggle.appgroups"), retab: true, get: func() bool { return f.AppGroups.Enabled }, set: func(b bool) { f.AppGroups.Enabled = b }},
		{id: "notifications", label: i18n.T("settings.toggle.notifications"), get: func() bool { return f.Notifications.Enabled }, set: func(b bool) { f.Notifications.Enabled = b }},
		{id: "guardian", label: i18n.T("settings.toggle.guardian"), get: func() bool { return !c.DisableCrashGuardian }, set: func(b bool) { c.DisableCrashGuardian = !b }},
	}
}

// toggleMap builds an id→setToggle lookup over the registry.
func (u *UI) toggleMap() map[string]setToggle {
	m := map[string]setToggle{}
	for _, t := range u.toggleRegistry() {
		m[t.id] = t
	}
	return m
}

// ── status model ──

// stv is a card status: variant (live > warn > ok > off) + a terse state line.
type stv struct{ v, t string }

func stOff(t string) stv  { return stv{"off", or(t, "off")} }
func stOk(t string) stv   { return stv{"ok", t} }
func stWarn(t string) stv { return stv{"warn", t} }
func stLive(t string) stv { return stv{"live", t} }

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func stRank(v string) int {
	switch v {
	case "live":
		return 3
	case "warn":
		return 2
	case "ok":
		return 1
	}
	return 0
}

// ── sections ──

type setSection struct {
	id, title, desc string
	cards           []string // card ids in order
}

func settingsSections() []setSection {
	// title/desc are localized (i18n.T("settings.section.<id>.{title,desc}")); ids stable.
	st := func(id string) string { return i18n.T("settings.section." + id + ".title") }
	sd := func(id string) string { return i18n.T("settings.section." + id + ".desc") }
	return []setSection{
		{"account", st("account"), sd("account"), []string{"uilang", "account", "api"}},
		{"djsources", st("djsources"), sd("djsources"), []string{"traktor", "traktorqml", "traktormap", "midi", "nml", "prodjlink", "serato", "virtualdj", "rekordbox", "rekordboxkey", "rekordboxmidi"}},
		{"recording", st("recording"), sd("recording"), []string{"recorder", "setcapture", "audiorecord", "obs", "obssync", "fingerprint"}},
		{"streaming", st("streaming"), sd("streaming"), []string{"streambridge", "studio", "peers", "accountbridge", "webcam", "medialink", "timecode", "ablelink"}},
		{"libmedia", st("libmedia"), sd("libmedia"), []string{"library", "mediaeditor", "transcode", "gridfix", "gridfixmodel"}},
		{"integrations", st("integrations"), sd("integrations"), []string{"twitch", "stt", "vrchat", "vrctools", "worldsync", "vroverlay", "dmx", "dmxmidi", "rtsp", "vrslstream", "mocap", "crew", "unity"}},
		{"system", st("system"), sd("system"), []string{"appgroups", "notifications", "guardian", "service", "updates"}},
	}
}

// renderSettings is the bridge: header + a global search box + a patchable content pane. Only
// the ACTIVE sub-tab's cards are in the DOM (the old render built all ~40 cards at once - the
// main reason opening Settings froze the app); a non-empty query switches the pane to
// cross-section results. Zig-rendered (native/zigui/src/settings.zig) with the pure Go
// renderers in render_settings_html.go as fallback + golden reference.
func (u *UI) renderSettings() string {
	st := u.settingsState()
	if zigui.Available() {
		if h, ok := zigWire("RenderSettingsV2", wireSetState(st), zigui.RenderSettingsV2,
			zigui.RenderSettings, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return settingsHTML(st)
}

// renderSettingsContent renders the pane below the search box (#set-content) - patched on its
// own so the search input's DOM (and its focus) survive.
func (u *UI) renderSettingsContent() string {
	st := u.settingsContentState()
	if zigui.Available() {
		if h, ok := zigWire("RenderSettingsContentV2", wireSetContent(st), zigui.RenderSettingsContentV2,
			zigui.RenderSettingsContent, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return setContentHTML(st)
}

// renderStatus renders the dot + line fragment patched by the settings tick (#stset-<id>).
func renderStatus(s stv) string {
	st := setStatusSt{V: s.v, T: s.t}
	if zigui.Available() {
		if h, ok := zigWire("RenderSettingsStatusV2", wireSetStatus(st), zigui.RenderSettingsStatusV2,
			zigui.RenderSettingsStatus, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return setStatusHTML(st)
}

// ── state builders (impure: config + services + probes + i18n) ──

// settingsState resolves the whole view. Cfg nil = the unavailable placeholder.
func (u *UI) settingsState() setState {
	st := setState{
		Title:       i18n.T("settings.title"),
		Available:   u.svc.Cfg != nil,
		Unavailable: i18n.T("settings.body.configUnavailable"),
	}
	if !st.Available {
		return st
	}
	u.kickProbes() // concurrent off-lane probe refresh; the state below reads retained slots only (instant)
	st.Sub = i18n.T("settings.subtitle")
	st.Query = u.settingsQueryText()
	st.Placeholder = i18n.T("settings.search.placeholder")
	st.Content = u.settingsContentState()
	return st
}

// settingsContentState resolves the content pane: sub-tab pills + the active section's cards,
// or (query non-empty) matching cards across ALL sections grouped by section. Records which
// cards are in the DOM (setViewState) so the ~1 Hz status tick patches only those.
func (u *UI) settingsContentState() setContentSt {
	secs := settingsSections()
	stats := u.settingsStatus()
	visible := map[string]bool{}
	c := setContentSt{Nav: []setNavSt{}, Secs: []setSecSt{}}

	if rawQ := strings.TrimSpace(u.settingsQueryText()); rawQ != "" {
		c.Searching = true
		c.NoResults = i18n.T("settings.search.noResults", i18n.A{"query": rawQ})
		terms := strings.Fields(foldSearch(rawQ))
		for _, s := range secs {
			cards := []setCardSt{}
			for _, id := range s.cards {
				card := u.settingsCardState(id, stats[id])
				// match against the card's STRUCTURED state (render_settings_search.go): same visible
				// text the rendered card would carry (title + labels + status + help notes), without
				// rendering ~40 cards per keystroke on the handler lane
				if !setCardMatches(card, terms) {
					continue
				}
				cards = append(cards, card)
				visible[id] = true
			}
			if len(cards) > 0 {
				c.Secs = append(c.Secs, setSecSt{ID: s.id, Title: s.title, Cards: cards})
			}
		}
		u.setViewState(visible, true)
		return c
	}

	active := u.settingsActiveSec(secs)
	for _, s := range secs {
		agg := "off"
		for _, id := range s.cards {
			if st, ok := stats[id]; ok && stRank(st.v) > stRank(agg) {
				agg = st.v
			}
		}
		c.Nav = append(c.Nav, setNavSt{ID: s.id, Title: s.title, Agg: agg, Active: s.id == active})
	}
	for _, s := range secs {
		if s.id != active {
			continue
		}
		cards := make([]setCardSt, 0, len(s.cards))
		for _, id := range s.cards {
			cards = append(cards, u.settingsCardState(id, stats[id]))
			visible[id] = true
		}
		c.Secs = append(c.Secs, setSecSt{ID: s.id, Desc: s.desc, Cards: cards})
	}
	u.setViewState(visible, false)
	return c
}

// ── settings view state (guarded by setMu) ──

func (u *UI) settingsQueryText() string {
	u.setMu.Lock()
	defer u.setMu.Unlock()
	return u.setQuery
}

func (u *UI) settingsActiveSec(secs []setSection) string {
	u.setMu.Lock()
	cur := u.setSec
	u.setMu.Unlock()
	for _, s := range secs {
		if s.id == cur {
			return cur
		}
	}
	return secs[0].id
}

func (u *UI) setViewState(visible map[string]bool, searching bool) {
	u.setMu.Lock()
	u.setVisible, u.setSearch = visible, searching
	u.setMu.Unlock()
}

func (u *UI) settingsVisible() (map[string]bool, bool) {
	u.setMu.Lock()
	defer u.setMu.Unlock()
	return u.setVisible, u.setSearch
}

// ── search helpers ──

// stripTags reduces markup to its visible text: every text node, joined by the space each '<'
// leaves behind, entities decoded. Since B4d the matcher walks structured state instead, so this is
// only for the RAW seams a card body can still carry (raw/noteRaw/region blocks + the sub-view
// renderers + a legacy pre-rendered tooltip) - and it stays the reference the differential gate
// measures the structured walk against.
func stripTags(h string) string {
	var b strings.Builder
	in := false
	for _, r := range h {
		switch {
		case r == '<':
			in = true
			b.WriteByte(' ')
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return html.UnescapeString(b.String())
}

// foldSearch lowercases + strips diacritics ("Résolume" matches "resolume").
func foldSearch(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func matchAllTerms(hay string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// settingsCard renders one feature card: header (title + toggle) + status row + body.
// settingsCardTips maps a card id to its helpTopics glossary entry (tooltip.go).
var settingsCardTips = map[string]string{
	"accountbridge": "account-bridge",
	"recorder":      "session-recorder",
	"setcapture":    "icecast",
	"fingerprint":   "fingerprinting",
	"timecode":      "tc-timecode",
	"obssync":       "obssync-mediasync",
	"dmx":           "dmx-connect",
	"rtsp":          "rtsp-why",
	"vrslstream":    "stream-why",
}

// settingsCardState resolves one feature card: header (title + tooltip + switch), live status,
// body blocks.
func (u *UI) settingsCardState(id string, st stv) setCardSt {
	c := setCardSt{ID: id, St: setStatusSt{V: st.v, T: st.t}}
	c.Title, c.Desc, c.Blocks = u.cardBlocks(id)
	if topic := settingsCardTips[id]; topic != "" {
		c.TipS = tipTopicSt(topic)
	}
	if t, ok := u.toggleMap()[id]; ok {
		sw := setSwitchSt{Label: t.label, On: t.get()}
		// dependency missing: grey the enable switch + name what to install
		// (an already-on feature is never gated - it must stay turn-off-able)
		if gate := u.cardGate(id); gate != "" && !sw.On {
			sw.Gate = gate
		}
		c.Tgl = &sw
	}
	return c
}

// cardGate names the missing component that blocks ENABLING card id ("" = not gated).
// Reads cached probes only (never the filesystem beyond the cheap stt/vr checks the
// status map already makes) - gated controls render greyed with this hint, never hidden.
func (u *UI) cardGate(id string) string {
	switch id {
	case "fingerprint":
		if u.probeDone(pkTools) && !u.toolStatusCached("fpcalc").Installed {
			return i18n.T("settings.gate.fpcalc")
		}
	case "transcode":
		if u.probeDone(pkTools) && !u.toolStatusCached("ffmpeg").Installed &&
			strings.TrimSpace(u.svc.Cfg.Features.Transcode.FfmpegPath) == "" {
			return i18n.T("settings.gate.ffmpeg")
		}
	case "stt":
		if !stt.BinInstalled() {
			return i18n.T("settings.gate.whisper")
		}
	case "vroverlay":
		if !vroverlay.BuiltWithVR() {
			return i18n.T("settings.gate.vrBuild")
		}
		if u.probeDone(pkVR) && !u.vrStatusCached().Installed {
			return i18n.T("settings.gate.vrRuntime")
		}
	case "gridfix":
		if st, ok := u.gridfixStatusCached(); ok && !st.CPU.EngineOK && !st.CUDA.EngineOK {
			return i18n.T("settings.gate.gridfix")
		}
	}
	return ""
}

// ── block constructors (every one resolves into a components.go primitive at render time) ──

func nbtn(label, variant, act, val string) uiBtn {
	return uiBtn{Label: label, Variant: variant, Act: act, Val: val}
}

func sbNote(text string) setBlock        { return setBlock{K: "note", Text: text} }
func sbNoteRaw(h string) setBlock        { return setBlock{K: "noteRaw", HTML: h} }
func sbHint(tone, text string) setBlock  { return setBlock{K: "hint", Tone: tone, Text: text} }
func sbEmpty(text string) setBlock       { return setBlock{K: "empty", Text: text} }
func sbRaw(h string) setBlock            { return setBlock{K: "raw", HTML: h} }
func sbRegion(id, h string) setBlock     { return setBlock{K: "region", ID: id, HTML: h} }
func sbInstallNote(note string) setBlock { return setBlock{K: "installNote", Text: note} }
func sbBtnOf(b uiBtn) setBlock           { return setBlock{K: "btn", Btn: &b} }

func sbKV(label, value string) setBlock {
	k := newKV(label, value)
	return setBlock{K: "kv", KV: &k}
}

func sbField(label, act, value, inputType string) setBlock {
	f := newField(label, act, value, inputType)
	return setBlock{K: "field", Fld: &f}
}

func sbFieldPH(label, act, value, inputType, placeholder string) setBlock {
	b := sbField(label, act, value, inputType)
	b.Fld.PH = placeholder
	return b
}

func sbFieldTip(label, act, value, inputType string, tp *tipSt) setBlock {
	b := sbField(label, act, value, inputType)
	b.TipS = tp
	return b
}

func sbToggle(label, act string, on bool) setBlock {
	t := newToggle(label, act, on)
	return setBlock{K: "toggle", Tgl: &t}
}

func sbToggleTip(label, act string, on bool, tp *tipSt) setBlock {
	b := sbToggle(label, act, on)
	b.TipS = tp
	return b
}

// sbToggleGated is the disabled switch + warn hint naming the missing dependency (Go
// toggleRowGated): gated controls stay visible, greyed, explained.
func sbToggleGated(label string, on bool, gateHint string) setBlock {
	b := sbToggle(label, "", on)
	b.Gate = gateHint
	return b
}

func sbSelect(label, act string, options [][2]string, current string) setBlock {
	s := resolveSelectBox(label, act, options, current)
	return setBlock{K: "select", Sel: &s}
}

func sbSelectTip(label, act string, options [][2]string, current, topic string) setBlock {
	s, lbl := resolveSelectBoxTip(label, act, options, current, topic)
	return setBlock{K: "select", Sel: &s, SelLblS: &lbl}
}

// sbSmartSel is a rich smart select (opts fn) with a plain label (Go smartSelect).
func sbSmartSel(id, label, act, cur string, opts func() []ssOpt) setBlock {
	s := resolveSmartSelect(id, act, cur, opts)
	s.Label = label
	return setBlock{K: "select", Sel: &s}
}

// sbAMenu mirrors actionMenu: a label-as-current smart select in an amenu span, each option's
// value being the act to dispatch (menugo:).
func sbAMenu(id, label string, items []ssOpt) setBlock {
	opts := make([]ssOpt, 0, len(items)+1)
	opts = append(opts, ssOpt{Val: "", Label: label})
	opts = append(opts, items...)
	s := resolveSmartSelect(id, "menugo:", "", func() []ssOpt { return opts })
	return setBlock{K: "amenu", Sel: &s}
}

func sbFpair(a, b setBlock) setBlock {
	return setBlock{K: "fpair", Kids: []setKid{blockKid(a), blockKid(b)}}
}

func sbBtnRow(bs ...uiBtn) setBlock {
	ks := make([]setKid, 0, len(bs))
	for _, b := range bs {
		ks = append(ks, setKid{K: "btn", Btn: &b})
	}
	return setBlock{K: "btnrow", Kids: ks}
}

// sbBtnRowMix is a btn-row holding heterogeneous children (an action menu beside buttons).
func sbBtnRowMix(items ...setBlock) setBlock {
	ks := make([]setKid, 0, len(items))
	for _, it := range items {
		ks = append(ks, blockKid(it))
	}
	return setBlock{K: "btnrow", Kids: ks}
}

// sbPathRow renders a path input + native Browse… button (picker contract: pick-<kind>:<act>,
// kind ∈ dir|file). The pick handler re-dispatches act with the chosen path.
func sbPathRow(label, act, value, kind string) setBlock {
	return sbPathRowPH(label, act, value, kind, "")
}

// sbPathRowPH is sbPathRow pre-filled with a placeholder (detected default path). placeholder is
// only meaningful when value is empty - shows the user the auto-detected dir so they needn't browse.
func sbPathRowPH(label, act, value, kind, placeholder string) setBlock {
	b := sbFieldPH(label, act, value, "text", placeholder)
	b.K = "pathrow"
	browse := nbtn("Browse…", "ghost", "pick-"+kind+":"+act, "")
	b.Btn = &browse
	return b
}

func sbItemRow(title, sub string, trailing ...uiBtn) setBlock {
	b := sbBtnRow(trailing...)
	b.K, b.Title, b.Sub = "itemrow", title, sub
	return b
}

// sbInstall is the detect + download block whose progress region (#inst-<key>) the install
// handlers patch.
func sbInstall(key, line string, bs ...uiBtn) setBlock {
	b := sbBtnRow(bs...)
	b.K, b.ID, b.Text = "install", key, line
	return b
}

// sbForm is a set-dlgform with raw named inputs (field() emits no name attribute, so parseForm
// would see nothing), optional action buttons and an optional literal type=submit button.
func sbForm(act string, inputs []setInput, submit, submitVariant string, bs ...uiBtn) setBlock {
	b := sbBtnRow(bs...)
	b.K, b.ID, b.Inputs, b.Submit, b.SubVar = "form", act, inputs, submit, submitVariant
	return b
}

// ── sub-view blocks (bodies owned by other files, crossing as structured state) ──

func sbGridfix(s gfCardSt) setBlock       { return setBlock{K: "gridfix", GF: &s} }
func sbGridfixModel(s gfModelSt) setBlock { return setBlock{K: "gridfixmodel", GFM: &s} }
func sbBridge(s bridgeSt) setBlock        { return setBlock{K: "bridge", Brg: &s} }

// sbUpdRegion is sbRegion whose inner markup is the update flow's state (#inst-update).
func sbUpdRegion(id string, s updFlowSt) setBlock {
	return setBlock{K: "updregion", ID: id, Upd: &s}
}

// blockKid narrows a control block to a composite child (fpair / btn-row / item-row trailing).
func blockKid(b setBlock) setKid {
	switch b.K {
	case "field":
		return setKid{K: "field", Fld: b.Fld, Tip: b.Tip, TipS: b.TipS}
	case "select":
		return setKid{K: "select", Sel: b.Sel, SelLbl: b.SelLbl, SelLblS: b.SelLblS}
	case "amenu":
		return setKid{K: "amenu", Sel: b.Sel}
	case "btn":
		return setKid{K: "btn", Btn: b.Btn}
	}
	return setKid{}
}

// ── account / api ──

func (u *UI) accountBlocks() []setBlock {
	signed := u.svc.Auth != nil && u.svc.Auth.SignedIn()
	status := i18n.T("settings.body.account.notSignedIn")
	row := []uiBtn{nbtn(i18n.T("settings.body.account.signInBrowser"), "primary", "auth-login", "")}
	if signed {
		status = i18n.T("settings.body.account.signedIn")
		row = []uiBtn{
			nbtn(i18n.T("settings.body.account.reauth"), "outline", "auth-login", ""),
			nbtn(i18n.T("common.signOut"), "destructive", "auth-logout", ""),
		}
	}
	return []setBlock{
		sbNote(status),
		sbField(i18n.T("settings.body.account.nodeName"), "set:peer-nick", u.svc.Cfg.Features.Peers.Nickname, "text"),
		sbBtnRow(row...),
	}
}

// cardBlocks returns (title, desc, body blocks) for a card id.
func (u *UI) cardBlocks(id string) (string, string, []setBlock) {
	f := &u.svc.Cfg.Features
	switch id {
	case "uilang":
		// picking a locale dispatches "ui-setlang:<code>" → persist + i18n.SetLocale + re-render
		opts := func() []ssOpt {
			locs := i18n.Available()
			out := make([]ssOpt, 0, len(locs))
			for _, l := range locs {
				out = append(out, ssOpt{Val: l.Code, Label: l.Name, Badge: strings.ToUpper(l.Code)})
			}
			return out
		}
		return i18n.T("settings.language.title"), i18n.T("settings.language.desc"), []setBlock{
			sbSmartSel("uilang", i18n.T("settings.language.label"), "ui-setlang:", i18n.Current(), opts)}
	case "account":
		return i18n.T("settings.card.account.title"), i18n.T("settings.card.account.desc"), u.accountBlocks()
	case "api":
		// base URL shows once - in the live status line (stset-api), not duplicated as a kv
		return i18n.T("settings.card.api.title"), "", []setBlock{sbNote(i18n.T("settings.body.api.note"))}

	// ── DJ sources ──
	case "traktor":
		t := &f.Traktor
		return i18n.T("settings.card.traktor.title"), i18n.T("settings.card.traktor.desc"), []setBlock{
			sbField(i18n.T("settings.body.traktor.listenPort"), "set:traktor-port", strconv.Itoa(t.ResolvedPort()), "number"),
			sbToggle(i18n.T("settings.body.traktor.logRaw"), "set:traktor-log", t.LogPayloads),
			sbNote(i18n.T("settings.body.traktor.note"))}
	case "traktorqml":
		return i18n.T("settings.card.traktorqml.title"), i18n.T("settings.card.traktorqml.desc"), []setBlock{
			sbBtnRow(nbtn(i18n.T("settings.body.traktorqml.install"), "primary", "settings-qml-apply", ""),
				nbtn(i18n.T("settings.body.common.remove"), "warn", "settings-qml-revert", ""),
				nbtn(i18n.T("common.refresh"), "ghost", "settings-refresh", ""))}
	case "traktormap":
		return i18n.T("settings.card.traktormap.title"), i18n.T("settings.card.traktormap.desc"), u.traktorMapBlocks()
	case "midi":
		mf := &f.MIDI
		names := u.devNamesCached("midi")
		portPlaceholder := i18n.T("settings.body.midi.selectPortPlaceholder")
		out := []setBlock{
			sbSelect(i18n.T("settings.body.midi.customPort"), "set:midi-custom", devOpts(names, portPlaceholder, mf.CustomPort), mf.CustomPort),
			sbSelect(i18n.T("settings.body.midi.denonPort"), "set:midi-denon", devOpts(names, portPlaceholder, mf.DenonPort), mf.DenonPort),
			sbToggle(i18n.T("settings.body.midi.mesh"), "set:midi-mesh", mf.MeshForward),
			sbBtnRow(nbtn(i18n.T("settings.body.midi.refreshPorts"), "ghost", "settings-refresh", ""),
				nbtn(i18n.T("settings.body.midi.installLoopMidi"), "ghost", "open-url", "https://www.tobias-erichsen.de/software/loopmidi.html"))}
		if len(names) == 0 {
			out = append(out, sbNote(i18n.T("settings.body.midi.noPorts")))
		}
		return i18n.T("settings.card.midi.title"), i18n.T("settings.card.midi.desc"), out
	case "nml":
		return i18n.T("settings.card.nml.title"), i18n.T("settings.card.nml.desc"), []setBlock{
			sbPathRow(i18n.T("settings.body.nml.pathLabel"), "set:nml-path", f.NML.CollectionPath, "file"),
			sbNote(i18n.T("settings.body.nml.note"))}
	case "prodjlink":
		return i18n.T("settings.card.prodjlink.title"), i18n.T("settings.card.prodjlink.desc"), nil
	case "serato":
		sf := &f.Serato
		// pre-fill the dir field with the redirection-aware detected _Serato_ dir (registry-resolved
		// Music known folder) so the user needn't browse. Only probe when unset.
		seratoPH := ""
		if sf.SeratoDir == "" {
			seratoPH = serato.SuggestedDir()
		}
		return i18n.T("settings.card.serato.title"), i18n.T("settings.card.serato.desc"), []setBlock{
			sbPathRowPH(i18n.T("settings.body.serato.folder"), "set:serato-dir", sf.SeratoDir, "dir", seratoPH),
			sbToggle(i18n.T("settings.body.serato.nowPlaying"), "set:serato-np", sf.NowPlaying),
			sbNote(i18n.T("settings.body.serato.note")),
			sbToggle(i18n.T("settings.body.serato.remote"), "set:serato-remote", sf.Remote),
			sbToggle(i18n.T("settings.body.serato.remoteDebug"), "set:serato-remotedebug", sf.RemoteDebug),
			sbNote(i18n.T("settings.body.serato.remoteNote")),
			sbToggle(i18n.T("settings.body.serato.livePlaylist"), "set:serato-live", sf.LivePlaylist),
			sbFieldPH(i18n.T("settings.body.serato.livePlaylistUrl"), "set:serato-liveurl", sf.LivePlaylistURL, "text", "https://serato.com/playlists/<username>/live"),
			sbFieldPH(i18n.T("settings.body.serato.livePlaylistInterval"), "set:serato-liveinterval", liveIntervalStr(sf.LivePlaylistInterval), "number", "10"),
			sbNote(i18n.T("settings.body.serato.livePlaylistNote"))}
	case "virtualdj":
		vf := &f.VirtualDJ
		vdjPH := ""
		if vf.DatabaseDir == "" {
			vdjPH = musiclib.DefaultVirtualDJDir()
		}
		return i18n.T("settings.card.virtualdj.title"), i18n.T("settings.card.virtualdj.desc"), []setBlock{
			sbPathRowPH(i18n.T("settings.body.virtualdj.folder"), "set:vdj-dir", vf.DatabaseDir, "dir", vdjPH),
			sbToggle(i18n.T("settings.body.virtualdj.netCtl"), "set:vdj-netctl", vf.NetCtl),
			sbField(i18n.T("settings.body.virtualdj.pluginUrl"), "set:vdj-netctlurl", vf.NetCtlURL, "text"),
			sbField(i18n.T("settings.body.virtualdj.pluginAuth"), "set:vdj-netctlauth", vf.NetCtlAuth, "password"),
			sbToggle(i18n.T("settings.body.virtualdj.os2l"), "set:vdj-os2l", vf.OS2L),
			sbToggle(i18n.T("settings.body.virtualdj.tracklist"), "set:vdj-tracklist", vf.Tracklist),
			sbBtnRow(nbtn(i18n.T("settings.body.virtualdj.howToEnable"), "ghost", "open-url", "https://virtualdj.com/wiki/NetworkControlPlugin.html"))}
	case "rekordbox":
		rf := &f.Rekordbox
		return i18n.T("settings.card.rekordbox.title"), i18n.T("settings.card.rekordbox.desc"), []setBlock{
			sbToggle(i18n.T("settings.body.rekordbox.dbPoll"), "set:rb-dbpoll", rf.DBPoll),
			sbToggle(i18n.T("settings.body.rekordbox.memRead"), "set:rb-memread", rf.MemoryRead)}
	case "rekordboxkey":
		return i18n.T("settings.card.rekordboxkey.title"), i18n.T("settings.card.rekordboxkey.desc"), []setBlock{
			sbForm("settings-rbkey-save",
				[]setInput{{Type: "password", Name: "key", PH: i18n.T("settings.body.rekordboxkey.placeholder")}},
				i18n.T("settings.body.rekordboxkey.save"), "primary"),
			sbBtnRow(nbtn(i18n.T("settings.body.rekordboxkey.test"), "outline", "settings-rbkey-test", "")),
			sbNote(i18n.T("settings.body.rekordboxkey.note"))}
	case "rekordboxmidi":
		return i18n.T("settings.card.rekordboxmidi.title"), i18n.T("settings.card.rekordboxmidi.desc"), []setBlock{
			sbBtnRow(nbtn(i18n.T("settings.body.rekordboxmidi.export"), "primary", "settings-rbmidi-export", ""),
				nbtn(i18n.T("settings.body.rekordboxmidi.openFolder"), "ghost", "settings-rbmidi-folder", ""))}

	// ── Recording ──
	case "recorder":
		return i18n.T("settings.card.recorder.title"), i18n.T("settings.card.recorder.desc"), []setBlock{
			sbField(i18n.T("settings.body.recorder.confirmAfter"), "set:rec-confirm", strconv.Itoa(f.Recorder.ResolvedConfirmSeconds()), "number"),
			sbNote(i18n.T("settings.body.recorder.note"))}
	case "setcapture":
		return i18n.T("settings.card.setcapture.title"), i18n.T("settings.card.setcapture.desc"), u.setCaptureBlocks()
	case "audiorecord":
		return i18n.T("settings.card.audiorecord.title"), i18n.T("settings.card.audiorecord.desc"), u.audioRecBlocks()
	case "obs":
		return i18n.T("settings.card.obs.title"), i18n.T("settings.card.obs.desc"), u.obsBlocks()
	case "obssync":
		return i18n.T("settings.card.obssync.title"), i18n.T("settings.card.obssync.desc"), u.obsSyncBlocks()
	case "fingerprint":
		return i18n.T("settings.card.fingerprint.title"), i18n.T("settings.card.fingerprint.desc"), append(
			[]setBlock{sbNote(i18n.T("settings.body.fingerprint.note"))},
			u.toolInstallBlock(mediatools.Fpcalc, "fpcalc"))

	// ── Streaming & remote ──
	case "streambridge":
		return i18n.T("settings.card.streambridge.title"), i18n.T("settings.card.streambridge.desc"), nil
	case "studio":
		return i18n.T("settings.card.studio.title"), i18n.T("settings.card.studio.desc"), nil
	case "peers":
		return i18n.T("settings.card.peers.title"), i18n.T("settings.card.peers.desc"), append(
			[]setBlock{sbField(i18n.T("settings.body.account.nodeName"), "set:peer-nick", f.Peers.Nickname, "text")},
			u.peersCacheBlocks()...)
	case "accountbridge":
		// bridge_actions.go owns this body's state (enrolment + trusted sessions).
		return i18n.T("settings.card.accountbridge.title"), i18n.T("settings.card.accountbridge.desc"),
			[]setBlock{sbBridge(u.bridgeCardState())}
	case "webcam":
		return i18n.T("settings.card.webcam.title"), i18n.T("settings.card.webcam.desc"), []setBlock{
			sbToggle(i18n.T("settings.body.webcam.autostart"), "set:webcam-autostart", f.Webcam.AutoStart),
			sbNote(i18n.T("settings.body.webcam.note"))}
	case "medialink":
		// settings_medialink.go owns this body (sender/receiver split + device & engine pickers).
		return i18n.T("settings.card.medialink.title"), i18n.T("settings.card.medialink.desc"),
			u.mediaLinkBlocks(&f.MediaLink)
	case "timecode":
		return i18n.T("settings.card.timecode.title"), i18n.T("settings.card.timecode.desc"), u.timecodeBlocks()
	case "ablelink":
		return i18n.T("settings.card.ablelink.title"), i18n.T("settings.card.ablelink.desc"), u.ableLinkBlocks()

	// ── Library & media ──
	case "library":
		embedRow := sbToggle(i18n.T("settings.body.library.embed"), "set:player-embed", f.Player.Embed)
		if u.probeDone(pkTools) && !u.toolStatusCached("mpv").Installed {
			embedRow = sbToggleGated(i18n.T("settings.body.library.embed"), f.Player.Embed, i18n.T("settings.gate.mpv"))
		}
		return i18n.T("settings.card.library.title"), i18n.T("settings.card.library.desc"), []setBlock{
			sbNote(i18n.T("settings.body.library.mpvNote")),
			u.toolInstallBlock(mediatools.MPV, "mpv"),
			embedRow}
	case "mediaeditor":
		return i18n.T("settings.card.mediaeditor.title"), i18n.T("settings.card.mediaeditor.desc"), nil
	case "transcode":
		tf := &f.Transcode
		conc := tf.MaxConcurrent
		if conc < 1 {
			conc = 2
		}
		return i18n.T("settings.card.transcode.title"), i18n.T("settings.card.transcode.desc"), []setBlock{
			sbPathRow(i18n.T("settings.body.transcode.ffmpegPath"), "set:trans-ffmpeg", tf.FfmpegPath, "file"),
			sbField(i18n.T("settings.body.transcode.maxJobs"), "set:trans-conc", strconv.Itoa(conc), "number"),
			sbNote(i18n.T("settings.body.transcode.note")),
			u.toolInstallBlock(mediatools.FFmpeg, "ffmpeg")}

	case "gridfix":
		// settings_gridfix.go owns this body's state (engine variants + install).
		return i18n.T("settings.card.gridfix.title"), i18n.T("settings.card.gridfix.desc"),
			[]setBlock{sbGridfix(u.gridfixCardState())}

	case "gridfixmodel":
		// settings_gridfix_model.go owns this body's state (checkpoints + fine-tuning).
		return i18n.T("settings.card.gridfixmodel.title"), i18n.T("settings.card.gridfixmodel.desc"),
			[]setBlock{sbGridfixModel(u.gridfixModelState())}

	// ── Integrations ──
	case "twitch":
		return i18n.T("settings.card.twitch.title"), i18n.T("settings.card.twitch.desc"), u.twitchBlocks()
	case "stt":
		return i18n.T("settings.card.stt.title"), i18n.T("settings.card.stt.desc"), u.sttBlocks()
	case "vrchat":
		return i18n.T("settings.card.vrchat.title"), i18n.T("settings.card.vrchat.desc"), u.vrchatBlocks()
	case "vrctools":
		return i18n.T("settings.card.vrctools.title"), i18n.T("settings.card.vrctools.desc"), u.vrcToolsBlocks()
	case "worldsync":
		return i18n.T("settings.card.worldsync.title"), i18n.T("settings.card.worldsync.desc"), u.worldSyncBlocks()
	case "vroverlay":
		return i18n.T("settings.card.vroverlay.title"), i18n.T("settings.card.vroverlay.desc"), u.vrOverlayBlocks()
	case "dmx":
		return i18n.T("settings.card.dmx.title"), i18n.T("settings.card.dmx.desc"), u.dmxBlocks()
	case "dmxmidi":
		return i18n.T("settings.card.dmxmidi.title"), i18n.T("settings.card.dmxmidi.desc"), u.dmxMidiBlocks()
	case "rtsp":
		return i18n.T("settings.card.rtsp.title"), i18n.T("settings.card.rtsp.desc"), u.rtspBlocks()
	case "vrslstream":
		return i18n.T("settings.card.vrslstream.title"), i18n.T("settings.card.vrslstream.desc"), u.streamBlocks()
	case "mocap":
		return i18n.T("settings.card.mocap.title"), i18n.T("settings.card.mocap.desc"), u.mocapBlocks()
	case "crew":
		return i18n.T("settings.card.crew.title"), i18n.T("settings.card.crew.desc"), u.crewBlocks()
	case "unity":
		return i18n.T("settings.card.unity.title"), i18n.T("settings.card.unity.desc"), u.unityBlocks()

	// ── System ──
	case "appgroups":
		return i18n.T("settings.card.appgroups.title"), i18n.T("settings.card.appgroups.desc"), nil
	case "notifications":
		return i18n.T("settings.card.notifications.title"), i18n.T("settings.card.notifications.desc"), nil
	case "guardian":
		// the supervisor only (dis)arms at process start - show a restart hint only while
		// the toggle differs from the state this process launched with (dirty tracking)
		guardianAtStart.once.Do(func() { guardianAtStart.disabled = u.svc.Cfg.DisableCrashGuardian })
		out := []setBlock{sbNote(i18n.T("settings.body.guardian.note"))}
		if u.svc.Cfg.DisableCrashGuardian != guardianAtStart.disabled {
			out = append(out, sbHint("warn", i18n.T("settings.hint.appRestart")))
		}
		return i18n.T("settings.card.guardian.title"), i18n.T("settings.card.guardian.desc"), out
	case "service":
		return i18n.T("settings.card.service.title"), i18n.T("settings.card.service.desc"), []setBlock{
			sbBtnRow(nbtn(i18n.T("settings.body.service.install"), "primary", "settings-svc-install", ""),
				nbtn(i18n.T("settings.body.service.uninstall"), "outline", "settings-svc-uninstall", ""),
				nbtn(i18n.T("common.refresh"), "ghost", "settings-refresh", "")),
			sbNote(i18n.T("settings.body.service.note"))}
	case "updates":
		return i18n.T("settings.card.updates.title"), i18n.T("settings.card.updates.desc"), u.updatesBlocks()
	}
	return "?", "", nil
}

// ── bodies that need more than one-liners ──

func (u *UI) traktorMapBlocks() []setBlock {
	tm := u.svc.TraktorMap
	out := []setBlock{sbSelect(i18n.T("settings.body.traktormap.version"), "set:traktor-mapver",
		[][2]string{{traktormap.AutoVersion, i18n.T("settings.body.traktormap.autoNewest")}}, u.svc.Cfg.Features.Traktor.MappingVersion)}
	if tm == nil {
		return append(out, sbNote(i18n.T("settings.body.traktormap.unavailable")))
	}
	for _, mp := range tm.Available() {
		out = append(out, sbItemRow(mp.Display, "",
			nbtn(i18n.T("settings.body.traktormap.activate"), "outline", "settings-tmap-on:"+mp.Key, ""),
			nbtn(i18n.T("settings.body.common.remove"), "ghost", "settings-tmap-off:"+mp.Key, "")))
	}
	return append(out,
		sbBtnRow(nbtn(i18n.T("common.refresh"), "ghost", "settings-refresh", "")),
		sbNote(i18n.T("settings.body.traktormap.note")))
}

// peersCacheBlocks - remote cue-edit content cache (internal/remotecache): live usage, MiB cap
// (0 in config = DefaultCap; the field always shows the effective value), clear + open-folder.
// Usage() is two ReadDir levels over a handful of audio copies - cheap enough for the render path.
func (u *UI) peersCacheBlocks() []setBlock {
	f := &u.svc.Cfg.Features.Peers
	out := []setBlock{sbNote(i18n.T("settings.body.peers.cacheNote"))}
	if c := u.rceCacheStore(); c != nil {
		bytes, files := c.Usage()
		out = append(out, sbKV(i18n.T("settings.body.peers.cacheUsage"),
			i18n.T("settings.body.peers.cacheUsageVal", i18n.A{"size": humanBytes(uint64(bytes)), "count": strconv.Itoa(files)})))
	}
	mb := f.RemoteCacheMaxMB
	if mb <= 0 {
		mb = int(remotecache.DefaultCap >> 20)
	}
	return append(out,
		sbFieldTip(i18n.T("settings.body.peers.cacheSize"), "set:peer-cachemb", strconv.Itoa(mb), "number", tipTopicSt("remote-cache")),
		sbBtnRow(nbtn(i18n.T("settings.body.peers.cacheClear"), "destructive", "settings-rcecache-clear", ""),
			nbtn(i18n.T("settings.body.peers.cacheOpen"), "ghost", "settings-open:remotecache", "")))
}

func (u *UI) setCaptureBlocks() []setBlock {
	f := &u.svc.Cfg.Features.SetCapture
	return []setBlock{
		sbFpair(sbField(i18n.T("settings.body.setcapture.port"), "set:sc-port", strconv.Itoa(f.ResolvedPort()), "number"),
			sbField(i18n.T("settings.body.setcapture.mount"), "set:sc-mount", f.Mount, "text")),
		sbFpair(sbField(i18n.T("settings.body.setcapture.sourceUser"), "set:sc-user", f.Username, "text"),
			sbField(i18n.T("settings.body.setcapture.password"), "set:sc-pass", f.Password, "password")),
		sbPathRow(i18n.T("settings.body.setcapture.setsFolder"), "set:sc-dir", f.SetsDir, "dir"),
		sbField(i18n.T("settings.body.setcapture.reconnectGrace"), "set:sc-grace", strconv.Itoa(int(f.ResolvedReconnectGrace().Seconds())), "number"),
		sbToggle(i18n.T("settings.body.setcapture.singleFile"), "set:sc-single", f.SingleFile),
		sbToggle(i18n.T("settings.body.setcapture.metaOnly"), "set:sc-metaonly", f.MetadataOnly),
		sbBtnRow(nbtn(i18n.T("settings.body.setcapture.openFolder"), "ghost", "settings-open:sets", "")),
		sbNote(i18n.T("settings.body.setcapture.note", i18n.A{"port": strconv.Itoa(f.ResolvedPort()), "mount": or(f.Mount, "/stream")}))}
}

func (u *UI) audioRecBlocks() []setBlock {
	f := &u.svc.Cfg.Features.AudioRecord
	devs := u.devNamesCached("audiorec")
	return []setBlock{
		sbSelect(i18n.T("settings.body.audiorec.device"), "set:ar-device", devOpts(devs, i18n.T("settings.body.audiorec.devicePlaceholder"), f.Device), f.Device),
		sbSelect(i18n.T("settings.body.audiorec.format"), "set:ar-format", [][2]string{{"flac", "flac"}, {"wav", "wav"}, {"mp3", "mp3"}, {"aac", "aac"}}, f.ResolvedFormat()),
		sbFpair(sbField(i18n.T("settings.body.audiorec.bitrate"), "set:ar-bitrate", strconv.Itoa(f.ResolvedBitrate()), "number"),
			sbField(i18n.T("settings.body.audiorec.sampleRate"), "set:ar-samplerate", strconv.Itoa(f.SampleRate), "number")),
		sbPathRow(i18n.T("settings.body.audiorec.folder"), "set:ar-dir", f.Dir, "dir"),
		sbToggle(i18n.T("settings.body.audiorec.followObs"), "set:ar-followobs", f.FollowOBS),
		sbToggle(i18n.T("settings.body.audiorec.writeTags"), "set:ar-writetags", f.WriteTags),
		sbBtnRow(nbtn(i18n.T("settings.body.audiorec.refreshDevices"), "ghost", "settings-refresh", ""),
			nbtn(i18n.T("settings.body.audiorec.openFolder"), "ghost", "settings-open:recordings", "")),
		sbNote(i18n.T("settings.body.audiorec.note"))}
}

func (u *UI) obsBlocks() []setBlock {
	f := &u.svc.Cfg.Features.OBS
	return []setBlock{
		sbFpair(sbField(i18n.T("settings.body.obs.host"), "set:obs-host", f.ResolvedHost(), "text"),
			sbField(i18n.T("settings.body.obs.port"), "set:obs-port", strconv.Itoa(f.ResolvedPort()), "number")),
		sbField(i18n.T("settings.body.obs.password"), "set:obs-pass", f.Password, "password"),
		sbBtnRow(nbtn(i18n.T("settings.body.obs.connectValidate"), "outline", "settings-obs-validate", ""),
			nbtn(i18n.T("settings.body.obs.remoteObs", i18n.A{"count": fmt.Sprint(len(f.Remotes))}), "outline", "settings-obsrem", "")),
		sbNote(i18n.T("settings.body.obs.note"))}
}

func (u *UI) obsSyncBlocks() []setBlock {
	f := &u.svc.Cfg.Features.OBS.Sync
	return []setBlock{
		sbFpair(sbFieldTip(i18n.T("settings.body.common.frameRate"), "set:obssync-fps", trimNum(orFloat(f.Fps, 30)), "number", tipTopicSt("obssync-fps")),
			sbFieldTip(i18n.T("settings.body.obssync.deadBand"), "set:obssync-deadband", trimNum(orFloat(f.DeadBandFrames, 2)), "number", tipTopicSt("obssync-deadband"))),
		sbFieldTip(i18n.T("settings.body.obssync.restartThreshold"), "set:obssync-restart", strconv.Itoa(orInt(f.RestartThresholdMs, 1500)), "number", tipTopicSt("obssync-restart")),
		sbBtnRow(nbtn(i18n.T("settings.body.obssync.mediaSources", i18n.A{"count": fmt.Sprint(len(f.Sources))}), "outline", "settings-obssync-src", "")),
		sbNote(i18n.T("settings.body.obssync.note"))}
}

func (u *UI) ableLinkBlocks() []setBlock {
	f := &u.svc.Cfg.Features.AbletonLink
	r := &f.Resolume
	out := []setBlock{
		sbFpair(sbSelect(i18n.T("settings.body.ablelink.quantum"), "set:ablelink-quantum",
			[][2]string{{"8", "8"}, {"16", "16"}, {"32", "32"}}, strconv.Itoa(f.ResolvedQuantum())),
			sbSelect(i18n.T("settings.body.ablelink.tempoOwner"), "set:ablelink-owner",
				[][2]string{{"auto", i18n.T("settings.body.ablelink.ownerAuto")}, {"always", i18n.T("settings.body.ablelink.ownerAlways")}, {"follow", i18n.T("settings.body.ablelink.ownerFollow")}}, f.ResolvedTempoOwner())),
		sbToggle(i18n.T("settings.body.ablelink.startStopSync"), "set:ablelink-sss", f.StartStopSync),
		sbToggle(i18n.T("settings.body.ablelink.resolumeOn"), "set:ablelink-res-on", r.Enabled)}
	if r.Enabled {
		out = append(out,
			sbField(i18n.T("settings.body.ablelink.resolumeHost"), "set:ablelink-res-host", r.ResolvedHost(), "text"),
			sbFpair(sbField(i18n.T("settings.body.ablelink.resolumeOscPort"), "set:ablelink-res-osc", strconv.Itoa(r.ResolvedOSCPort()), "number"),
				sbField(i18n.T("settings.body.ablelink.resolumeRestPort"), "set:ablelink-res-rest", strconv.Itoa(r.ResolvedRESTPort()), "number")),
			sbFpair(sbField(i18n.T("settings.body.ablelink.phraseClipLayer"), "set:ablelink-res-layer", strconv.Itoa(r.PhraseClipLayer), "number"),
				sbField(i18n.T("settings.body.ablelink.phraseClipClip"), "set:ablelink-res-clip", strconv.Itoa(r.PhraseClipClip), "number")))
	}
	return append(out,
		sbBtnRow(nbtn(i18n.T("settings.body.ablelink.resync"), "outline", "ablelink-resync", "")),
		sbNote(i18n.T("settings.body.ablelink.note")))
}

func (u *UI) timecodeBlocks() []setBlock {
	f := &u.svc.Cfg.Features.Timecode
	waveOut := u.devNamesCached("waveout")
	midiOut := u.devNamesCached("midiout")
	clock := f.StartAt == "clock"
	out := []setBlock{
		sbSelectTip(i18n.T("settings.body.common.frameRate"), "set:tc-rate", [][2]string{{"24", i18n.T("settings.body.timecode.rate24")}, {"25", i18n.T("settings.body.timecode.rate25")}, {"29.97", i18n.T("settings.body.timecode.rate2997")}, {"30", i18n.T("settings.body.timecode.rate30")}}, f.ResolvedRate(), "tc-rate"),
		sbToggle(i18n.T("settings.body.timecode.clockStart"), "set:tc-clock", clock)}
	if !clock {
		out = append(out, sbFieldTip(i18n.T("settings.body.timecode.startPosition"), "set:tc-startat", f.StartAt, "text", tipTopicSt("tc-start")))
	}
	return append(out,
		sbToggleTip(i18n.T("settings.body.timecode.ltcOn"), "set:tc-ltc-on", f.LTC.On, tipTopicSt("tc-ltc")),
		sbFpair(sbSelect(i18n.T("settings.body.timecode.ltcDevice"), "set:tc-ltc-dev", devOpts(waveOut, i18n.T("settings.body.common.systemDefault"), f.LTC.Device), f.LTC.Device),
			sbFieldTip(i18n.T("settings.body.timecode.ltcLevel"), "set:tc-ltc-gain", trimNum(f.LTC.ResolvedGainDb()), "number", tipTopicSt("tc-ltc-level"))),
		sbToggleTip(i18n.T("settings.body.timecode.mtcOn"), "set:tc-mtc-on", f.MTC.On, tipTopicSt("tc-mtc")),
		sbSelect(i18n.T("settings.body.timecode.mtcPort"), "set:tc-mtc-dev", devOpts(midiOut, i18n.T("settings.body.timecode.firstPort"), f.MTC.Device), f.MTC.Device),
		sbToggleTip(i18n.T("settings.body.timecode.artnetOn"), "set:tc-art-on", f.ArtNet.On, tipTopicSt("tc-artnet")),
		sbField(i18n.T("settings.body.timecode.artnetTarget"), "set:tc-art-addr", f.ArtNet.Addr, "text"),
		sbBtnRowMix(sbAMenu("tcextras", "⋯ "+i18n.T("settings.body.timecode.extraMenu"), []ssOpt{
			{Val: "settings-tcextra:ltc", Label: i18n.T("settings.body.timecode.extraLtc", i18n.A{"count": fmt.Sprint(len(f.LTCExtra))})},
			{Val: "settings-tcextra:mtc", Label: i18n.T("settings.body.timecode.extraMtc", i18n.A{"count": fmt.Sprint(len(f.MTCExtra))})},
			{Val: "settings-tcextra:art", Label: i18n.T("settings.body.timecode.extraArtnet", i18n.A{"count": fmt.Sprint(len(f.ArtNetExtra))})},
		}), sbBtnOf(nbtn(i18n.T("common.refresh"), "ghost", "settings-refresh", ""))),
		sbNote(i18n.T("settings.body.timecode.note")))
}

func (u *UI) twitchBlocks() []setBlock {
	if u.svc.Twitch == nil {
		return []setBlock{sbNote(i18n.T("settings.body.twitch.unavailable"))}
	}
	signed := u.svc.Twitch.SignedIn()
	line := i18n.T("settings.body.twitch.signInLine")
	row := []uiBtn{nbtn(i18n.T("settings.body.twitch.signIn"), "primary", "settings-twitch-signin", "")}
	if signed {
		if lg := u.svc.Twitch.Self().Login; lg != "" {
			line = i18n.T("settings.body.twitch.signedInAs", i18n.A{"name": lg})
		} else {
			line = i18n.T("settings.body.twitch.connecting")
		}
		row = []uiBtn{nbtn(i18n.T("common.signOut"), "destructive", "settings-twitch-signout", "")}
	}
	row = append(row, nbtn(i18n.T("settings.body.twitch.titlePresets", i18n.A{"count": fmt.Sprint(len(u.svc.Cfg.Features.Twitch.Presets))}), "outline", "settings-twpreset", ""))
	return []setBlock{sbNote(line), sbBtnRow(row...)}
}

func (u *UI) sttBlocks() []setBlock {
	f := &u.svc.Cfg.Features.STT
	mics := u.devNamesCached("sttmic")
	var modelOpts [][2]string
	for _, m := range stt.Models {
		modelOpts = append(modelOpts, [2]string{m.File, m.Display})
	}
	installLine := i18n.T("settings.body.stt.notInstalled")
	if stt.Installed(f.Model) {
		installLine = i18n.T("settings.body.stt.ready")
	} else if stt.BinInstalled() {
		installLine = i18n.T("settings.body.stt.modelNotDownloaded")
	}
	return []setBlock{
		sbFpair(sbSelect(i18n.T("settings.body.stt.microphone"), "set:stt-input", devOpts(mics, i18n.T("settings.body.common.systemDefault"), f.InputDevice), f.InputDevice),
			sbField(i18n.T("settings.body.stt.output"), "set:stt-output", f.OutputDevice, "text")),
		sbFpair(sbSelect(i18n.T("settings.body.stt.model"), "set:stt-model", modelOpts, stt.ResolvedModel(f.Model).File),
			sbField(i18n.T("settings.body.stt.silenceTimeout"), "set:stt-silence", strconv.Itoa(f.ResolvedSilenceMs()), "number")),
		sbToggle(i18n.T("settings.body.stt.autoSubmit"), "set:stt-autosubmit", f.AutoSubmit),
		sbNote(installLine),
		sbBtnRow(nbtn(i18n.T("settings.body.stt.refreshMics"), "ghost", "settings-refresh", ""),
			nbtn(i18n.T("settings.body.stt.installWhisper"), "primary", "settings-stt-install", ""))}
}

// vrcLoginForm is the username/password sign-in form (a LOCAL sign-in always overrides a
// federated session, so it stays reachable in both states - only the button variant differs).
func vrcLoginForm(variant string) setBlock {
	return sbForm("settings-vrc-login", []setInput{
		{Name: "user", PH: i18n.T("settings.body.vrchat.usernamePlaceholder")},
		{Type: "password", Name: "pass", PH: i18n.T("settings.body.vrchat.passwordPlaceholder")},
	}, "", "", nbtn(i18n.T("common.signIn"), variant, "", ""))
}

func (u *UI) vrchatBlocks() []setBlock {
	if u.svc.Vrchat == nil {
		return []setBlock{sbNote(i18n.T("settings.body.vrchat.unavailable"))}
	}
	f := &u.svc.Cfg.Features.VRChat
	s := u.svc.Vrchat.State()
	var out []setBlock
	switch {
	case s.LoggedIn && s.Via != "":
		// federated: a paired instance serves the session. No unlink here (nothing
		// local to unlink) - but the login form stays reachable conceptually via
		// the note; a LOCAL sign-in always overrides the federation.
		out = []setBlock{
			sbNote(i18n.T("settings.body.vrchat.linkedViaPeer", i18n.A{"name": s.DisplayName, "peer": s.Via})),
			vrcLoginForm("outline")}
	case s.LoggedIn:
		out = []setBlock{
			sbNote(i18n.T("settings.body.vrchat.linkedAs", i18n.A{"name": s.DisplayName})),
			sbBtnRow(nbtn(i18n.T("settings.body.common.unlink"), "destructive", "settings-vrc-unlink", ""))}
	case s.Awaiting2FA:
		out = []setBlock{
			sbNote(i18n.T("settings.body.vrchat.twoFactorRequired", i18n.A{"methods": strings.Join(s.Methods, ", ")})),
			sbForm("settings-vrc-2fa", []setInput{{Name: "code", PH: i18n.T("settings.body.vrchat.twoFactorCodePlaceholder")}},
				"", "", nbtn(i18n.T("settings.body.vrchat.verify"), "primary", "", ""))}
	default:
		msg := i18n.T("settings.body.vrchat.credsMsg")
		if s.Message != "" {
			msg += " (" + s.Message + ")"
		}
		out = []setBlock{sbNote(msg), vrcLoginForm("primary")}
	}
	return append(out,
		sbToggle(i18n.T("settings.body.vrchat.rememberSession"), "set:vrc-remember", f.RememberSession),
		sbToggle(i18n.T("settings.body.vrchat.uplink"), "set:vrc-uplink", f.Uplink))
}

func (u *UI) vrcToolsBlocks() []setBlock {
	f := &u.svc.Cfg.Features.VRCTools
	presetOpts := [][2]string{{"", i18n.T("settings.body.vrctools.noneOption")}}
	for _, p := range f.AllCamPresets() {
		presetOpts = append(presetOpts, [2]string{p.Name, p.Name})
	}
	return []setBlock{
		sbToggle(i18n.T("settings.body.vrctools.orgPhotos"), "set:vct-orgphotos", f.OrganizePhotos),
		sbToggle(i18n.T("settings.body.vrctools.preferEvent"), "set:vct-byevent", f.OrganizeByEvent),
		sbToggle(i18n.T("settings.body.vrctools.photoMove"), "set:vct-photomove", f.PhotoMove),
		sbToggle(i18n.T("settings.body.vrctools.orgCamPaths"), "set:vct-orgcampaths", f.OrganizeCamPaths),
		sbToggle(i18n.T("settings.body.vrctools.camPathMove"), "set:vct-campathmove", f.CamPathMove),
		sbToggle(i18n.T("settings.body.vrctools.autoBackup"), "set:vct-autobackup", f.AutoBackupCamPaths),
		sbToggle(i18n.T("settings.body.vrctools.autoRestore"), "set:vct-autorestore", f.AutoRestoreCamPaths),
		sbField(i18n.T("settings.body.vrctools.oscTarget"), "set:vct-osc", f.OSCAddr, "text"),
		sbSelect(i18n.T("settings.body.vrctools.camPreset"), "set:vct-preset", presetOpts, f.DefaultCamPreset),
		sbBtnRow(nbtn(i18n.T("settings.body.vrctools.organizeNow"), "outline", "settings-vct-organize", ""),
			nbtn(i18n.T("settings.body.vrctools.applyPresetNow"), "outline", "settings-vct-applypreset", ""),
			nbtn(i18n.T("settings.body.vrctools.installDjPaths"), "primary", "settings-vct-djpaths", ""))}
}

func (u *UI) worldSyncBlocks() []setBlock {
	gh := u.svc.GitHub
	if gh == nil {
		return []setBlock{sbNote(i18n.T("settings.body.worldsync.unavailable"))}
	}
	f := &u.svc.Cfg.Features.WorldSync
	line := i18n.T("settings.body.worldsync.linkLine")
	row := sbBtnRow(nbtn(i18n.T("settings.body.worldsync.linkDeviceCode"), "primary", "settings-gh-device", ""),
		nbtn(i18n.T("settings.body.worldsync.pasteToken"), "outline", "settings-gh-pat", ""))
	if gh.SignedIn() {
		line = i18n.T("settings.body.worldsync.linkedAs", i18n.A{"name": gh.Login()})
		row = sbBtnRow(nbtn(i18n.T("settings.body.common.unlink"), "destructive", "settings-gh-unlink", ""))
	}
	// Publish mode: "direct" (user's gist token) or "hosted" (rave.page worldlive API) - values
	// mirror config.WorldSyncMode*. Hosted needs a target world id (shown only in hosted mode).
	mode := f.ResolvedPublishMode()
	out := []setBlock{sbNote(line), row,
		sbSelect(i18n.T("settings.body.worldsync.publishVia"), "set:ws-mode",
			[][2]string{{"direct", i18n.T("settings.body.worldsync.modeDirect")}, {"hosted", i18n.T("settings.body.worldsync.modeHosted")}}, mode),
		sbNote(i18n.T("settings.body.worldsync.modeHelp"))}
	if mode == "hosted" {
		out = append(out, sbField(i18n.T("settings.body.worldsync.hostedWorldId"), "set:ws-worldid", f.HostedWorldID, "text"))
	}
	return out
}

func (u *UI) vrOverlayBlocks() []setBlock {
	f := &u.svc.Cfg.Features.VROverlay
	return []setBlock{
		sbFpair(sbSelect(i18n.T("settings.body.vroverlay.editBadge"), "set:vr-edithand", [][2]string{{"left", "left"}, {"right", "right"}}, f.ResolvedEditHand()),
			sbSelect(i18n.T("settings.body.vroverlay.openEditor"), "set:vr-summon", [][2]string{{"ax", i18n.T("settings.body.vroverlay.axButton")}, {"by", i18n.T("settings.body.vroverlay.byButton")}, {"custom", i18n.T("settings.body.vroverlay.customSteamvr")}}, f.ResolvedSummonButton())),
		sbToggle(i18n.T("settings.body.vroverlay.tapHides"), "set:vr-taphides", f.SummonTapHides),
		sbToggle(i18n.T("settings.body.vroverlay.autostart"), "set:vr-autostart", f.AutoStart),
		sbToggle(i18n.T("settings.body.vroverlay.viewCapture"), "set:vr-viewcapture", f.VRViewCapture),
		sbFpair(sbField(i18n.T("settings.body.vroverlay.vrchatOsc"), "set:vr-osc", f.OSCAddr, "text"),
			sbField(i18n.T("settings.body.vroverlay.vmc"), "set:vr-vmc", f.VMCAddr, "text")),
		sbToggle(i18n.T("settings.body.vroverlay.vmcLive"), "set:vr-vmclive", f.VMCLive),
		// 6-button wall → one manage menu (every entry keeps its live count)
		sbBtnRowMix(sbAMenu("vrovmanage", "⋯ "+i18n.T("settings.body.vroverlay.manageMenu"), []ssOpt{
			{Val: "settings-vrov", Label: i18n.T("settings.body.vroverlay.overlaysCount", i18n.A{"count": fmt.Sprint(len(f.Overlays))})},
			{Val: "settings-vr-bindings", Label: i18n.T("settings.body.vroverlay.openBindings")},
			{Val: "settings-vr-keybinds", Label: i18n.T("settings.body.vroverlay.keybindsCount", i18n.A{"count": fmt.Sprint(len(f.Binds))})},
			{Val: "settings-vr-layouts", Label: i18n.T("settings.body.vroverlay.layoutsCount", i18n.A{"count": fmt.Sprint(len(f.Layouts))})},
			{Val: "settings-vr-wrist", Label: i18n.T("settings.body.vroverlay.wristCount", i18n.A{"count": fmt.Sprint(len(f.QuickButtons))})},
			{Val: "settings-vr-worldlay", Label: i18n.T("settings.body.vroverlay.worldLayoutsCount", i18n.A{"count": fmt.Sprint(len(f.WorldLayouts))})},
		})),
		u.vrInstallBlock()}
}

func (u *UI) dmxBlocks() []setBlock {
	f := &u.svc.Cfg.Features.DMX
	return append([]setBlock{
		sbFpair(sbField(i18n.T("settings.body.common.listenAddr"), "set:dmx-listen", f.ListenAddr, "text"),
			sbField(i18n.T("settings.body.dmx.universes"), "set:dmx-universes", intsToCSVWeb(f.Universes), "text")),
		sbToggleTip(i18n.T("settings.body.dmx.renderGrid"), "set:dmx-grid", f.Grid.Enabled, tipTopicSt("dmx-vrsl")),
		sbFpair(sbSelect(i18n.T("settings.body.dmx.gridMode"), "set:dmx-mode", [][2]string{{"mono", "mono"}, {"rgb9", "rgb9"}}, or(f.Grid.Mode, "mono")),
			sbField(i18n.T("settings.body.dmx.maxFps"), "set:dmx-fpscap", strconv.Itoa(f.Grid.ResolvedFPSCap()), "number")),
		sbField(i18n.T("settings.body.dmx.senderName"), "set:dmx-spout", f.Grid.SpoutName, "text"),
		sbToggleTip(i18n.T("settings.body.dmx.reemit"), "set:dmx-reemit", f.ReEmit, tipTopicSt("dmx-reemit")),
		sbField(i18n.T("settings.body.dmx.reemitTarget"), "set:dmx-emittarget", f.EmitTarget, "text"),
	}, u.dmxLightCueBlocks()...)
}

// dmxLightCueBlocks renders the lighting-cue recorder controls + sACN/Hz settings, folded into the
// DMX settings card. Record/stop/play/loop/publish emit lc-* acts (lightcue_actions.go); the
// enable/Hz/sACN fields emit dmx-* sets so the dmx module restarts to apply them.
func (u *UI) dmxLightCueBlocks() []setBlock {
	lc := &u.svc.Cfg.Features.LightCue
	d := &u.svc.Cfg.Features.DMX
	out := []setBlock{
		sbNote(i18n.T("settings.body.lightcue.note")),
		sbToggle(i18n.T("settings.body.lightcue.enable"), "set:dmx-lc-enable", lc.Enabled),
		sbFpair(sbField(i18n.T("settings.body.lightcue.hz"), "set:dmx-lc-hz", strconv.Itoa(lc.ResolvedHz()), "number"),
			sbField(i18n.T("settings.body.lightcue.sacnUniverses"), "set:dmx-sacn-universes", intsToCSVWeb(d.SACNUniverses), "text")),
		sbToggle(i18n.T("settings.body.lightcue.sacn"), "set:dmx-sacn", d.SACN)}

	if u.svc.DMX == nil {
		return out
	}
	st := u.svc.DMX.RecordStatus()
	if st.Recording {
		out = append(out, sbItemRow(i18n.T("lightcue.status.recording"), fmt.Sprintf("%.0fs", st.Elapsed),
			nbtn(i18n.T("lightcue.btn.stopSave"), "primary", "lc-stop", "")))
	} else {
		out = append(out, sbItemRow(i18n.T("lightcue.label.recorder"), i18n.T("lightcue.label.recorderSub"),
			nbtn(i18n.T("lightcue.btn.record"), "primary", "lc-record", "")))
	}
	takes := u.svc.DMX.Takes()
	if len(takes) == 0 {
		return append(out, sbEmpty(i18n.T("lightcue.label.noTakes")))
	}
	opts := make([][2]string, 0, len(takes))
	for _, t := range takes {
		opts = append(opts, [2]string{t, t})
	}
	sel := u.lc().sel()
	if sel == "" {
		sel = takes[0]
	}
	out = append(out, sbSelect(i18n.T("lightcue.label.take"), "lc-select", opts, sel))
	playLbl, playAct := i18n.T("lightcue.btn.play"), "lc-play"
	if st.Playing {
		playLbl, playAct = i18n.T("lightcue.btn.stop"), "lc-stopplay"
	}
	loopVariant := "outline"
	if st.Loop {
		loopVariant = "primary"
	}
	out = append(out, sbItemRow(i18n.T("lightcue.label.playback"), i18n.T("lightcue.label.playbackSub"),
		nbtn(playLbl, "primary", playAct, ""),
		nbtn(i18n.T("lightcue.btn.loop"), loopVariant, "lc-loop", ""),
		nbtn(i18n.T("lightcue.btn.publish"), "outline", "lc-publish", "")))
	if st.Playing {
		out = append(out, sbNote(i18n.T("lightcue.status.playing")+" "+st.Name))
	}
	return out
}

func (u *UI) dmxMidiBlocks() []setBlock {
	f := &u.svc.Cfg.Features.DMXMIDI
	return []setBlock{
		sbField(i18n.T("settings.body.dmxmidi.port"), "set:dmxmidi-device", f.Device, "text"),
		sbFpair(sbField(i18n.T("settings.body.dmxmidi.universes"), "set:dmxmidi-universes", intsToCSVWeb(f.Universes), "text"),
			sbField(i18n.T("settings.body.dmxmidi.maxRate"), "set:dmxmidi-rate", strconv.Itoa(f.ResolvedRate()), "number"))}
}

func (u *UI) rtspBlocks() []setBlock {
	f := &u.svc.Cfg.Features.RTSPServe
	// the URL hint splices pre-escaped literals around the resolved addr/path (a source
	// literal carries the &lt;…&gt; placeholders) - it travels as trusted raw markup
	note := html.EscapeString(i18n.T("settings.body.rtsp.note")) + ` rtspt://&lt;this machine's IP&gt;` +
		html.EscapeString(f.ResolvedListenAddr()) + html.EscapeString(f.ResolvedPath())
	return []setBlock{
		sbFpair(sbField(i18n.T("settings.body.rtsp.videoSource"), "set:rtsp-source", f.Source, "text"),
			sbField(i18n.T("settings.body.rtsp.inputFormat"), "set:rtsp-format", f.InputFormat, "text")),
		sbToggleTip(i18n.T("settings.body.rtsp.passthrough"), "set:rtsp-passthrough", f.Passthrough, tipTopicSt("rtsp-passthrough")),
		sbFpair(sbField(i18n.T("settings.body.common.listenAddr"), "set:rtsp-listen", f.ListenAddr, "text"),
			sbField(i18n.T("settings.body.rtsp.streamPath"), "set:rtsp-path", f.Path, "text")),
		sbFpair(sbField(i18n.T("settings.body.common.frameRate"), "set:rtsp-fps", strconv.Itoa(f.ResolvedFPS()), "number"),
			sbField(i18n.T("settings.body.rtsp.bitrate"), "set:rtsp-bitrate", strconv.Itoa(f.ResolvedBitrate()), "number")),
		sbNoteRaw(note)}
}

func (u *UI) streamBlocks() []setBlock {
	f := &u.svc.Cfg.Features.Stream
	bitrate := ""
	if f.BitrateKbps > 0 {
		bitrate = strconv.Itoa(f.BitrateKbps)
	}
	return []setBlock{
		sbFpair(sbSelect(i18n.T("settings.body.vrslstream.transport"), "set:vrslstream-transport",
			[][2]string{{"rtmp", "RTMP"}, {"whip", "WHIP"}}, f.ResolvedTransport()),
			sbField(i18n.T("settings.body.vrslstream.url"), "set:vrslstream-url", f.URL, "text")),
		sbField(i18n.T("settings.body.vrslstream.streamKey"), "set:vrslstream-key", f.StreamKey, "text"),
		sbFpair(sbSelect(i18n.T("settings.body.vrslstream.mode"), "set:vrslstream-mode",
			[][2]string{{"standard", "standard"}, {"extended", "extended"}}, f.ResolvedMode()),
			sbSelect(i18n.T("settings.body.vrslstream.colorMode"), "set:vrslstream-color",
				[][2]string{{"mono", "mono"}, {"rgb9", "rgb9"}}, f.ResolvedColorMode())),
		sbField(i18n.T("settings.body.dmx.universes"), "set:vrslstream-universes", intsToCSVWeb(f.Universes), "text"),
		sbFpair(sbField(i18n.T("settings.body.common.frameRate"), "set:vrslstream-fps", strconv.Itoa(f.ResolvedFPS()), "number"),
			sbField(i18n.T("settings.body.vrslstream.bitrate"), "set:vrslstream-bitrate", bitrate, "number")),
		sbSelect(i18n.T("settings.body.vrslstream.encoder"), "set:vrslstream-encoder",
			[][2]string{{"x264", "x264 (CPU)"}, {"nvenc", "NVENC"}, {"qsv", "QSV"}, {"amf", "AMF"}, {"auto", "auto"}}, f.ResolvedEncoder()),
		sbNote(i18n.T("settings.body.vrslstream.note"))}
}

// mocapBlocks configures the mocap capture master (module "mocap"): capture source, panel
// geometry (bone slots) + the master-authoritative stage bounds. The region only reaches the
// world when the VRSL stream runs in extended mode.
func (u *UI) mocapBlocks() []setBlock {
	f := &u.svc.Cfg.Features.Mocap
	min, size := f.ResolvedStageMin(), f.ResolvedStageSize()
	ff := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	return []setBlock{
		sbFpair(sbSelect(i18n.T("settings.body.mocap.source"), "set:mocap-source",
			[][2]string{{"desktop", "desktop"}, {"spout", "Spout"}, {"dshow", "DirectShow"}}, f.ResolvedSource()),
			sbField(i18n.T("settings.body.mocap.device"), "set:mocap-device", f.Device, "text")),
		sbFpair(sbField(i18n.T("settings.body.mocap.monitor"), "set:mocap-monitor", strconv.Itoa(f.Monitor), "number"),
			sbField(i18n.T("settings.body.common.frameRate"), "set:mocap-fps", strconv.Itoa(f.ResolvedFPS()), "number")),
		sbField(i18n.T("settings.body.mocap.boneSlots"), "set:mocap-boneslots", strconv.Itoa(f.ResolvedBoneSlots()), "number"),
		sbFpair(sbField(i18n.T("settings.body.mocap.minX"), "set:mocap-min-x", ff(min[0]), "number"),
			sbField(i18n.T("settings.body.mocap.sizeX"), "set:mocap-size-x", ff(size[0]), "number")),
		sbFpair(sbField(i18n.T("settings.body.mocap.minY"), "set:mocap-min-y", ff(min[1]), "number"),
			sbField(i18n.T("settings.body.mocap.sizeY"), "set:mocap-size-y", ff(size[1]), "number")),
		sbFpair(sbField(i18n.T("settings.body.mocap.minZ"), "set:mocap-min-z", ff(min[2]), "number"),
			sbField(i18n.T("settings.body.mocap.sizeZ"), "set:mocap-size-z", ff(size[2]), "number")),
		sbNote(i18n.T("settings.body.mocap.note"))}
}

// crewBlocks configures the capture-crew relay (module "crew"): the event room + this rig's
// role. No token/URL fields - the signed-in account is the bearer (contract §6).
func (u *UI) crewBlocks() []setBlock {
	f := &u.svc.Cfg.Features.Crew
	return []setBlock{
		sbField(i18n.T("settings.body.crew.eventId"), "set:crew-eventid", f.EventID, "text"),
		sbFpair(sbSelect(i18n.T("settings.body.crew.role"), "set:crew-role",
			[][2]string{{"node", i18n.T("settings.body.crew.roleNode")}, {"master", i18n.T("settings.body.crew.roleMaster")}}, f.ResolvedRole()),
			sbField(i18n.T("settings.body.crew.label"), "set:crew-label", f.Label, "text")),
		sbNote(i18n.T("settings.body.crew.note"))}
}

func (u *UI) unityBlocks() []setBlock {
	f := &u.svc.Cfg.Features.Unity
	var out []setBlock
	if len(f.Projects) == 0 {
		out = append(out, sbEmpty(i18n.T("settings.body.unity.empty")))
	}
	for i, dir := range f.Projects {
		info := u.unityInfoCached(dir)
		sub := i18n.T("settings.body.unity.pluginNotInstalled")
		switch {
		case !info.Valid:
			sub = i18n.T("settings.body.unity.notUnityProject")
		case info.HasPlugin:
			sub = i18n.T("settings.body.unity.pluginInstalled")
		}
		name := dir
		if info.Name != "" {
			name = info.Name
		}
		instLabel := i18n.T("settings.body.unity.installPlugin")
		if info.HasPlugin {
			instLabel = i18n.T("settings.body.unity.reinstallPlugin")
		}
		out = append(out, sbItemRow(name, sub,
			nbtn(instLabel, "outline", "settings-unity-install:"+strconv.Itoa(i), ""),
			nbtn(i18n.T("settings.body.common.remove"), "ghost", "settings-unity-remove:"+strconv.Itoa(i), "")))
	}
	return append(out,
		sbBtnRow(nbtn(i18n.T("settings.body.unity.addFromVcc"), "outline", "settings-unity-vcc", ""),
			nbtn(i18n.T("settings.body.unity.addFolder"), "outline", "pick-dir:settings-unity-addpath", ""),
			nbtn(i18n.T("settings.body.unity.pastePath"), "ghost", "settings-unity-add", "")),
		sbNote(i18n.T("settings.body.unity.note")))
}

// updatesBlocks renders the version line + channel, and (on a stamped build with a feed baked in)
// a "Check for updates" control wired to the shared selfupdate flow. A true dev build (empty
// FeedURL) keeps the manual-updates note. #inst-update is the progress/result region the check +
// apply handlers patch (same pattern as toolInstallBlock's #inst-<key>) - update_actions.go owns
// its state (updateFlowState) + the standalone patch renderer.
func (u *UI) updatesBlocks() []setBlock {
	out := []setBlock{
		sbKV(i18n.T("settings.body.updates.version"), version.String()),
		sbKV(i18n.T("settings.body.updates.channel"), version.ResolvedChannel())}
	if version.FeedURL == "" {
		return append(out, sbNote(i18n.T("settings.body.updates.feed")))
	}
	return append(out,
		sbBtnRow(nbtn(i18n.T("settings.body.updates.check"), "primary", "settings-update-check", "")),
		sbUpdRegion("inst-update", u.updateFlowState()),
		sbNote(i18n.T("settings.body.updates.note")))
}

// ── install / progress blocks ──

// toolInstallBlock renders a media-tool detect + download UI (progress patched into #inst-<key>).
func (u *UI) toolInstallBlock(t mediatools.Tool, key string) setBlock {
	st := u.toolStatusCached(key) // cached - never probe PATH on the render goroutine
	line, label := i18n.T("settings.body.install.notFound"), i18n.T("settings.body.install.download")
	switch {
	case st.Installed && st.Managed:
		line, label = i18n.T("settings.body.install.installedManaged", i18n.A{"path": st.Path}), i18n.T("settings.body.install.reDownload")
	case st.Installed:
		line, label = i18n.T("settings.body.install.foundOnPath", i18n.A{"path": st.Path}), i18n.T("settings.body.install.downloadManaged")
	}
	installBtn := nbtn(label, "primary", "settings-install:"+key, "")
	if !mediatools.CanInstall() {
		installBtn = nbtn(i18n.T("settings.body.install.windowsOnly", i18n.A{"label": label}), "ghost", "open-url", t.HomePage)
	}
	return sbInstall(key, line, installBtn,
		nbtn(i18n.T("settings.body.install.downloadPage", i18n.A{"name": t.Display}), "ghost", "open-url", t.HomePage))
}

func (u *UI) vrInstallBlock() setBlock {
	if !vroverlay.BuiltWithVR() {
		return sbInstallNote(i18n.T("settings.body.vrinstall.nonVrBuild"))
	}
	st := u.vrStatusCached() // cached - never probe the DLL fs on the render goroutine
	line, label := i18n.T("settings.body.vrinstall.notFound"), i18n.T("settings.body.vrinstall.installRuntime")
	if st.Installed {
		line, label = i18n.T("settings.body.vrinstall.installed", i18n.A{"path": st.Path}), i18n.T("settings.body.install.reDownload")
	}
	installBtn := nbtn(label, "primary", "settings-install-vr", "")
	if !vrdll.CanInstall() {
		installBtn = nbtn(i18n.T("settings.body.install.windowsOnly", i18n.A{"label": label}), "ghost", "open-url", vrdll.HomePage)
	}
	return sbInstall("vr", line, installBtn,
		nbtn(i18n.T("settings.body.vrinstall.downloadPage"), "ghost", "open-url", vrdll.HomePage))
}

// ── status map (cheap; patched ~1 Hz by the settings tick) ──

func (u *UI) settingsStatus() map[string]stv {
	f := &u.svc.Cfg.Features
	m := map[string]stv{}
	set := func(id string, s stv) { m[id] = s }
	tr := i18n.T // local alias, keeps the set(...) lines below scannable

	// account
	locName := i18n.Current()
	for _, l := range i18n.Available() {
		if l.Code == i18n.Current() {
			locName = l.Name
		}
	}
	set("uilang", stOk(locName))
	if u.svc.Auth != nil && u.svc.Auth.SignedIn() {
		set("account", stOk(tr("settings.status.account.signedIn")))
	} else {
		set("account", stWarn(tr("settings.status.common.notSignedIn")))
	}
	set("api", stOk(u.svc.Cfg.APIBaseURL))

	srcRecv := func(ids ...string) (recv, run bool) {
		if u.svc.Session == nil {
			return
		}
		for _, si := range u.svc.Session.Sources() {
			for _, id := range ids {
				if si.ID != id {
					continue
				}
				if si.Receiving {
					recv = true
				}
				if si.Running {
					run = true
				}
			}
		}
		return
	}
	srcStat := func(on bool, ids ...string) stv {
		if !on {
			return stOff("")
		}
		r, run := srcRecv(ids...)
		switch {
		case r:
			return stOk(tr("settings.status.common.receiving"))
		case run:
			return stOk(tr("settings.status.common.running"))
		default:
			return stWarn(tr("settings.status.common.notRunning"))
		}
	}

	// DJ sources
	if !f.Traktor.Enabled {
		set("traktor", stOff(""))
	} else if u.svc.Traktor != nil && u.svc.Traktor.Listening() {
		set("traktor", stOk(tr("settings.status.traktor.listening", i18n.A{"port": strconv.Itoa(f.Traktor.ResolvedPort())})))
	} else {
		set("traktor", stWarn(tr("settings.status.common.notListening")))
	}
	set("traktorqml", stOff(tr("settings.status.traktorqml.checkInstall")))
	set("traktormap", stOff(tr("settings.status.traktormap.manageDevices")))
	if !f.MIDI.Enabled {
		set("midi", stOff(""))
	} else if f.MIDI.CustomPort == "" && f.MIDI.DenonPort == "" {
		set("midi", stWarn(tr("settings.status.midi.noPortSelected")))
	} else {
		set("midi", srcStat(true, "midi"))
	}
	set("nml", srcStat(f.NML.Enabled, "nml"))
	set("prodjlink", srcStat(f.ProDJLink.Enabled, "prodjlink"))
	set("serato", srcStat(f.Serato.Enabled, "serato"))
	set("virtualdj", srcStat(f.VirtualDJ.Enabled, "virtualdj"))
	set("rekordbox", srcStat(f.Rekordbox.Enabled, "rekordbox"))
	set("rekordboxkey", stOff(tr("settings.status.rekordboxkey.saveTest")))
	set("rekordboxmidi", stOff(tr("settings.status.rekordboxmidi.generateImport")))

	// Recording
	if !f.Recorder.Enabled {
		set("recorder", stOff(""))
	} else if r := u.svc.Recorder; r != nil && r.Active() != nil {
		set("recorder", stLive(tr("settings.status.recorder.recordingTracks", i18n.A{"count": strconv.Itoa(len(r.Active().Tracks))})))
	} else {
		set("recorder", stOk(tr("settings.status.common.armed")))
	}
	if !f.SetCapture.Enabled || u.svc.SetCapture == nil {
		set("setcapture", stOff(""))
	} else {
		c := u.svc.SetCapture.Snapshot()
		switch {
		case c.LastError != "":
			set("setcapture", stWarn(tr("settings.status.setcapture.receiverError")))
		case c.Connected:
			set("setcapture", stOk(tr("settings.status.setcapture.capturing", i18n.A{"format": strings.ToUpper(c.Format)})))
		case c.Reconnecting:
			set("setcapture", stWarn(tr("settings.status.setcapture.sourceDropped")))
		case c.Listening:
			set("setcapture", stOk(tr("settings.status.setcapture.listening", i18n.A{"addr": c.Addr})))
		default:
			set("setcapture", stWarn(tr("settings.status.common.notListening")))
		}
	}
	if !f.AudioRecord.Enabled || u.svc.AudioRec == nil {
		set("audiorecord", stOff(""))
	} else if u.svc.AudioRec.Status().Recording {
		set("audiorecord", stLive(tr("settings.status.common.recording")))
	} else if f.AudioRecord.Device == "" {
		set("audiorecord", stWarn(tr("settings.status.audiorecord.noDeviceSelected")))
	} else {
		set("audiorecord", stOk(tr("settings.status.common.armed")))
	}
	if !f.OBS.Enabled || u.svc.OBS == nil {
		set("obs", stOff(""))
	} else {
		o := u.svc.OBS.Status()
		switch {
		case o.Recording:
			set("obs", stLive(tr("settings.status.common.recording")))
		case o.Connected:
			set("obs", stOk(tr("settings.status.obs.connected")))
		default:
			set("obs", stWarn(tr("settings.status.obs.notConnected")))
		}
	}
	if u.svc.OBSControl == nil {
		set("obssync", stOff(tr("settings.status.common.unavailable")))
	} else if !f.OBS.Sync.Enabled {
		set("obssync", stOff(""))
	} else if u.svc.OBSControl.SyncRunning() {
		set("obssync", stOk(tr("settings.status.obssync.syncing", i18n.A{"count": strconv.Itoa(len(u.svc.OBSControl.SyncStatuses()))})))
	} else {
		set("obssync", stWarn(tr("settings.status.obssync.clockStopped")))
	}
	if !f.Fingerprint.Enabled {
		set("fingerprint", stOff(""))
	} else if !u.toolStatusCached("fpcalc").Installed {
		set("fingerprint", stWarn(tr("settings.status.fingerprint.missing")))
	} else {
		set("fingerprint", stOk(tr("settings.status.fingerprint.ready")))
	}

	// Streaming & remote
	switch {
	case !f.StreamBridge.Enabled:
		set("streambridge", stOff(""))
	case u.svc.Stream != nil && u.svc.Stream.Status().IsLive:
		set("streambridge", stLive(tr("settings.status.streambridge.live")))
	case u.svc.Auth == nil || !u.svc.Auth.SignedIn():
		set("streambridge", stWarn(tr("settings.status.streambridge.signInRequired")))
	case !f.Traktor.Enabled:
		set("streambridge", stWarn(tr("settings.status.streambridge.needsTraktor")))
	default:
		set("streambridge", stOk(tr("settings.status.common.ready")))
	}
	if !f.StudioChannel.Enabled {
		set("studio", stOff(""))
	} else if u.svc.Modules != nil && u.svc.Modules.IsRunning("studio") {
		set("studio", stOk(tr("settings.status.studio.listeningLoopback")))
	} else {
		set("studio", stWarn(tr("settings.status.common.notRunning")))
	}
	if !f.Peers.Enabled {
		set("peers", stOff(""))
	} else {
		set("peers", stOk(tr("settings.status.peers.discoveryOn")))
	}
	if !f.Webcam.Enabled || u.svc.Webcam == nil {
		set("webcam", stOff(""))
	} else {
		set("webcam", stOk(tr("settings.status.webcam.readyPeersTab")))
	}
	if u.svc.Media == nil {
		set("medialink", stOff(tr("settings.status.medialink.off")))
	} else if f.MediaLink.ShareVideo {
		set("medialink", stOk(tr("settings.status.medialink.sharing")))
	} else {
		set("medialink", stOff(tr("settings.status.medialink.receiveOnly")))
	}
	if !f.Timecode.Enabled {
		set("timecode", stOff(""))
	} else if u.svc.Timecode != nil && u.svc.Timecode.Running() {
		tc, _ := u.svc.Timecode.Now()
		set("timecode", stLive(tr("settings.status.timecode.running", i18n.A{"tc": tc.String()})))
	} else {
		set("timecode", stOk(tr("settings.status.timecode.armedPressStart")))
	}

	// Library & media
	if !f.Library.Enabled {
		set("library", stOff(""))
	} else if u.toolStatusCached("mpv").Installed {
		set("library", stOk(tr("settings.status.library.mpvReady")))
	} else {
		set("library", stWarn(tr("settings.status.library.mpvMissing")))
	}
	set("mediaeditor", boolStat(f.MediaEditor.Enabled, tr("settings.status.common.on")))
	if !f.Transcode.Enabled {
		set("transcode", stOff(""))
	} else if !u.toolStatusCached("ffmpeg").Installed {
		set("transcode", stWarn(tr("settings.status.transcode.ffmpegMissing")))
	} else {
		set("transcode", stOk(tr("settings.status.transcode.ffmpegReady")))
	}
	if gf, gfReady := u.gridfixStatusCached(); !f.GridFix.Enabled {
		set("gridfix", stOff(""))
	} else if gfReady && (gf.CPU.EngineOK || gf.CUDA.EngineOK) {
		set("gridfix", stOk(tr("settings.status.gridfix.ready")))
	} else {
		set("gridfix", stWarn(tr("settings.status.gridfix.engineMissing")))
	}

	// Integrations
	if u.svc.Twitch == nil {
		set("twitch", stOff(tr("settings.status.common.unavailable")))
	} else if !f.Twitch.Enabled {
		set("twitch", stOff(""))
	} else if u.svc.Twitch.SignedIn() && u.svc.Twitch.Self().Login != "" {
		set("twitch", stOk(tr("settings.status.twitch.liveAs", i18n.A{"name": u.svc.Twitch.Self().Login})))
	} else if u.svc.Twitch.SignedIn() {
		set("twitch", stWarn(tr("settings.status.twitch.connecting")))
	} else {
		set("twitch", stWarn(tr("settings.status.common.notSignedIn")))
	}
	if stt.Installed(f.STT.Model) {
		set("stt", stOk(tr("settings.status.common.ready")))
	} else if stt.BinInstalled() {
		set("stt", stOff(tr("settings.status.stt.modelNotDownloaded")))
	} else {
		set("stt", stOff(tr("settings.status.stt.notInstalled")))
	}
	if u.svc.Vrchat == nil {
		set("vrchat", stOff(tr("settings.status.common.unavailable")))
	} else if !f.VRChat.Enabled {
		set("vrchat", stOff(""))
	} else {
		vs := u.svc.Vrchat.State()
		switch {
		case vs.LoggedIn:
			set("vrchat", stOk(tr("settings.status.vrchat.signedInAs", i18n.A{"name": vs.DisplayName})))
		case vs.Awaiting2FA:
			set("vrchat", stWarn(tr("settings.status.vrchat.twoFaPending")))
		default:
			set("vrchat", stWarn(tr("settings.status.common.notSignedIn")))
		}
	}
	if u.svc.VRCTools == nil {
		set("vrctools", stOff(tr("settings.status.common.unavailable")))
	} else if f.VRCTools.Enabled {
		set("vrctools", stOk(tr("settings.status.vrctools.watching")))
	} else {
		set("vrctools", stOff(""))
	}
	if u.svc.GitHub == nil {
		set("worldsync", stOff(tr("settings.status.common.unavailable")))
	} else if !f.WorldSync.Enabled {
		set("worldsync", stOff(""))
	} else if u.svc.GitHub.SignedIn() {
		set("worldsync", stOk(tr("settings.status.worldsync.linkedAs", i18n.A{"name": u.svc.GitHub.Login()})))
	} else {
		set("worldsync", stWarn(tr("settings.status.worldsync.notLinked")))
	}
	switch {
	case !f.VROverlay.Enabled:
		set("vroverlay", stOff(""))
	case u.svc.VROverlay != nil && u.svc.VROverlay.Available():
		set("vroverlay", stOk(tr("settings.status.vroverlay.active")))
	case !vroverlay.BuiltWithVR():
		set("vroverlay", stWarn(tr("settings.status.vroverlay.nonVrReinstall")))
	default:
		set("vroverlay", stWarn(tr("settings.status.vroverlay.waitingSteamvr")))
	}
	if !f.DMX.Enabled {
		set("dmx", stOff(""))
	} else if u.svc.DMX == nil {
		set("dmx", stWarn(tr("settings.status.common.unavailable")))
	} else {
		snap := u.svc.DMX.Status()
		recv := false
		for _, un := range snap.Universes {
			if un.PPS > 0 {
				recv = true
			}
		}
		switch {
		case !snap.Running:
			set("dmx", stWarn(tr("settings.status.common.notRunningPortBusy")))
		case recv:
			set("dmx", stOk(tr("settings.status.dmx.receiving")))
		case len(snap.Universes) > 0:
			set("dmx", stOk(tr("settings.status.dmx.idleSeen", i18n.A{"count": strconv.Itoa(len(snap.Universes))})))
		default:
			set("dmx", stWarn(tr("settings.status.dmx.listeningNoData")))
		}
	}
	if !f.DMXMIDI.Enabled {
		set("dmxmidi", stOff(""))
	} else if u.svc.DMXMIDI == nil {
		set("dmxmidi", stWarn(tr("settings.status.common.unavailable")))
	} else {
		snap := u.svc.DMXMIDI.Status()
		if !snap.Running {
			set("dmxmidi", stWarn(tr("settings.status.dmxmidi.notRunning")))
		} else if snap.Sent > 0 {
			set("dmxmidi", stOk(tr("settings.status.dmxmidi.sentMsgs", i18n.A{"port": snap.Port, "count": strconv.FormatUint(snap.Sent, 10)})))
		} else {
			set("dmxmidi", stOk(tr("settings.status.dmxmidi.readyOn", i18n.A{"port": snap.Port})))
		}
	}
	if !f.RTSPServe.Enabled {
		set("rtsp", stOff(""))
	} else if u.svc.RTSP == nil {
		set("rtsp", stWarn(tr("settings.status.common.unavailable")))
	} else {
		snap := u.svc.RTSP.Status()
		switch {
		case !snap.Running:
			set("rtsp", stWarn(tr("settings.status.common.notRunningPortBusy")))
		case snap.LastErr != "":
			set("rtsp", stWarn(snap.LastErr)) // raw error text, not authored UI copy
		case snap.SourceUp && snap.Clients > 0:
			set("rtsp", stOk(tr("settings.status.rtsp.streamingClients", i18n.A{"count": strconv.Itoa(snap.Clients)})))
		case snap.SourceUp:
			set("rtsp", stOk(tr("settings.status.rtsp.encodingWaiting")))
		default:
			set("rtsp", stWarn(tr("settings.status.rtsp.startingSource")))
		}
	}
	if !f.Stream.Enabled {
		set("vrslstream", stOff(""))
	} else if u.svc.VRSLStream == nil {
		set("vrslstream", stWarn(tr("settings.status.common.unavailable")))
	} else {
		snap := u.svc.VRSLStream.Status()
		switch {
		case !snap.Running:
			set("vrslstream", stWarn(tr("settings.status.common.notRunningPortBusy")))
		case snap.LastErr != "":
			set("vrslstream", stWarn(snap.LastErr)) // raw error text, not authored UI copy
		case snap.SourceUp:
			set("vrslstream", stLive(tr("settings.status.vrslstream.pushing", i18n.A{"count": strconv.FormatUint(snap.Frames, 10)})))
		default:
			set("vrslstream", stWarn(tr("settings.status.vrslstream.starting")))
		}
	}
	if !f.Mocap.Enabled {
		set("mocap", stOff(""))
	} else if u.svc.Mocap == nil {
		set("mocap", stWarn(tr("settings.status.common.unavailable")))
	} else {
		snap := u.svc.Mocap.Status()
		switch {
		case !snap.Running:
			set("mocap", stWarn(tr("settings.status.mocap.notRunning")))
		case snap.LastErr != "":
			set("mocap", stWarn(snap.LastErr)) // raw error text, not authored UI copy
		case snap.Packets > 0:
			set("mocap", stLive(tr("settings.status.mocap.capturing", i18n.A{
				"packets": strconv.FormatUint(snap.Packets, 10), "dancers": strconv.Itoa(snap.Dancers)})))
		default:
			set("mocap", stWarn(tr("settings.status.mocap.starting")))
		}
	}
	if !f.Crew.Enabled {
		set("crew", stOff(""))
	} else if u.svc.Crew == nil {
		set("crew", stWarn(tr("settings.status.common.unavailable")))
	} else {
		snap := u.svc.Crew.Status()
		switch {
		case !snap.Running:
			set("crew", stWarn(tr("settings.status.crew.notRunning")))
		case snap.LastErr != "":
			set("crew", stWarn(snap.LastErr)) // raw error text, not authored UI copy
		case snap.SID == "":
			set("crew", stWarn(tr("settings.status.crew.connecting")))
		case snap.Role == "master":
			set("crew", stLive(tr("settings.status.crew.master", i18n.A{
				"frames": strconv.FormatUint(snap.Frames, 10), "nodes": strconv.Itoa(snap.Members)})))
		default:
			set("crew", stLive(tr("settings.status.crew.node", i18n.A{
				"frames": strconv.FormatUint(snap.Frames, 10), "masters": strconv.Itoa(snap.Members)})))
		}
	}
	if len(f.Unity.Projects) == 0 {
		set("unity", stOff(tr("settings.status.unity.noProjects")))
	} else {
		set("unity", stOk(tr("settings.status.unity.projectsCount", i18n.A{"count": strconv.Itoa(len(f.Unity.Projects))})))
	}

	// System
	set("appgroups", boolStat(f.AppGroups.Enabled, tr("settings.status.common.on")))
	set("notifications", boolStat(f.Notifications.Enabled, tr("settings.status.common.on")))
	if u.svc.Cfg.DisableCrashGuardian {
		set("guardian", stOff(""))
	} else {
		set("guardian", stOk(tr("settings.status.common.armed")))
	}
	set("service", stOff(tr("settings.status.service.notChecked")))
	// Reflect the stamped build identity: a real CI build has a feed baked in → updater ON;
	// only an unstamped dev build (no feed / Version "dev") shows the manual "dev build" state.
	if version.FeedURL == "" || version.Version == "" || version.Version == "dev" {
		set("updates", stOff(tr("settings.status.updates.devBuild")))
	} else {
		set("updates", stOk(tr("settings.status.updates.onChannel", i18n.A{"channel": version.ResolvedChannel(), "build": version.Build})))
	}
	return m
}

func boolStat(on bool, onText string) stv {
	if on {
		return stOk(onText)
	}
	return stOff("")
}

// guardianAtStart snapshots the crash-guardian state this process launched with (the
// supervisor only arms at startup; first settings render precedes any toggle).
var guardianAtStart struct {
	once     sync.Once
	disabled bool
}

// ── apply (from onAction) ──

// applyToggle flips a feature toggle: optimistic in-memory flip + instant re-render, then persist +
// module start/stop via saveCfgBG (disk write, Session.Reconcile and featurehost spawn/exit-wait
// would stall the serial actWorker). A failed save reverts the flip.
func (u *UI) applyToggle(id string, on bool) {
	for _, t := range u.toggleRegistry() {
		if t.id != id {
			continue
		}
		prev := t.get()
		t.set(on) // optimistic - the switch re-renders before any disk/module work
		refresh := func() {
			if t.retab {
				u.eval("window.__patch('nav-list'," + jsQuote(u.navListHTML()) + ")")
			}
			u.patchMain()
		}
		refresh()
		u.toast(t.label + onOff(on))
		u.saveCfgBG("toggle:"+id, func() {
			if t.module != "" && u.svc.Modules != nil {
				u.svc.Modules.SetEnabled(t.module, on)
			}
		}, func() {
			t.set(prev) // save failed - revert the optimistic flip
			refresh()
			u.toast(t.label + " - save failed")
		})
		return
	}
}

// applySet persists every scalar/bool settings field the inputs expose (mirrors the Fyne writes).
func (u *UI) applySet(id, val string) {
	cfg := u.svc.Cfg
	if cfg == nil {
		return
	}
	f := &cfg.Features
	v := strings.TrimSpace(val)
	b := val == "true"
	if id == "bridge-studio" {
		// Serving the Local Studio channel over the relay - a browser ANYWHERE can then drive
		// this machine (still gated by TOTP + the mutual identity match).
		f.AccountBridge.LocalStudio = b
		u.saveCfg()
		u.patchMain()
		return
	}
	if id == "ws-mode" {
		// Values mirror config.WorldSyncMode*. Re-render so the hosted world-id field toggles.
		if v == "direct" || v == "hosted" {
			f.WorldSync.PublishMode = v
		}
		u.saveCfg()
		u.patchMain()
		return
	}
	toInt := func(dst *int, min, max int) {
		if n, err := strconv.Atoi(v); err == nil && n >= min && n <= max {
			*dst = n
		}
	}
	toFloat := func(dst *float64, min float64) {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n >= min {
			*dst = n
		}
	}
	// toStage writes one axis of a mocap stage-bounds array, materializing the other two from
	// their resolved values (config keeps a plain JSON array; ResolvedStage* guard validity).
	toStage := func(dst *[]float64, cur [3]float64, axis int) {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			cur[axis] = n
			*dst = []float64{cur[0], cur[1], cur[2]}
		}
	}
	save := true
	switch id {
	case "peer-nick":
		f.Peers.Nickname = v
	case "peer-cachemb":
		toInt(&f.Peers.RemoteCacheMaxMB, 0, 1<<20) // 0 = default cap; ceiling 1 TiB
		if c := u.rceCacheStore(); c != nil {      // SetCap already applied by the store accessor
			u.bg(c.EvictNow) // a shrink deletes files - keep the disk sweep off the actWorker
		}
	// Beatgrid fixer
	case "gridfix-python":
		f.GridFix.PythonPath = v
		u.invalidateGridfixProbe()
	case "gridfix-device":
		if v == "auto" || v == "cpu" || v == "cuda" {
			f.GridFix.Device = v
		}
	case "gridfix-minq":
		toFloat(&f.GridFix.MinQuality, 0.5)
	case "gridfix-thresh":
		toFloat(&f.GridFix.ThresholdMS, 1)
	case "gridfix-lock":
		f.GridFix.LockFixed = b
	// Traktor
	case "traktor-port":
		toInt(&f.Traktor.Port, 1, 65535)
	case "traktor-log":
		f.Traktor.LogPayloads = b
		if u.svc.Traktor != nil {
			u.svc.Traktor.SetLogging(b) // live reconfigure - no listener restart needed
		}
	case "traktor-mapver":
		f.Traktor.MappingVersion = v
		if u.svc.TraktorMap != nil {
			u.svc.TraktorMap.SetVersion(v)
		}
	// MIDI
	case "midi-custom":
		f.MIDI.CustomPort = v
	case "midi-denon":
		f.MIDI.DenonPort = v
	case "midi-mesh":
		f.MIDI.MeshForward = b
		if u.svc.PeerBridge != nil {
			u.svc.PeerBridge.SetMIDIMesh(b)
		}
	case "nml-path":
		f.NML.CollectionPath = v
	// Serato / VirtualDJ / Rekordbox
	case "serato-dir":
		f.Serato.SeratoDir = v
	case "serato-np":
		f.Serato.NowPlaying = b
	case "serato-remote":
		f.Serato.Remote = b
	case "serato-remotedebug":
		f.Serato.RemoteDebug = b
	case "serato-live":
		f.Serato.LivePlaylist = b
	case "serato-liveurl":
		f.Serato.LivePlaylistURL = v
	case "serato-liveinterval":
		toInt(&f.Serato.LivePlaylistInterval, 3, 300)
	case "vdj-dir":
		f.VirtualDJ.DatabaseDir = v
	case "vdj-netctl":
		f.VirtualDJ.NetCtl = b
	case "vdj-netctlurl":
		f.VirtualDJ.NetCtlURL = v
	case "vdj-netctlauth":
		f.VirtualDJ.NetCtlAuth = v
	case "vdj-os2l":
		f.VirtualDJ.OS2L = b
	case "vdj-tracklist":
		f.VirtualDJ.Tracklist = b
	case "rb-dbpoll":
		f.Rekordbox.DBPoll = b
	case "rb-memread":
		f.Rekordbox.MemoryRead = b
	// Recorder / SetCapture / AudioRecord
	case "rec-confirm":
		toInt(&f.Recorder.ConfirmSeconds, 1, 100000)
	case "sc-port":
		toInt(&f.SetCapture.Port, 1, 65535)
	case "sc-mount":
		f.SetCapture.Mount = v
	case "sc-user":
		f.SetCapture.Username = v
	case "sc-pass":
		f.SetCapture.Password = val
	case "sc-dir":
		f.SetCapture.SetsDir = v
	case "sc-grace":
		toInt(&f.SetCapture.ReconnectGraceSeconds, 1, 100000)
	case "sc-single":
		f.SetCapture.SingleFile = b
	case "sc-metaonly":
		f.SetCapture.MetadataOnly = b
	case "ar-device":
		f.AudioRecord.Device = v
	case "ar-format":
		f.AudioRecord.Format = v
	case "ar-bitrate":
		toInt(&f.AudioRecord.Bitrate, 1, 100000)
	case "ar-samplerate":
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			f.AudioRecord.SampleRate = n
		}
	case "ar-dir":
		f.AudioRecord.Dir = v
	case "ar-followobs":
		f.AudioRecord.FollowOBS = b
	case "ar-writetags":
		f.AudioRecord.WriteTags = b
	// OBS
	case "obs-host":
		f.OBS.Host = v
	case "obs-port":
		toInt(&f.OBS.Port, 1, 65535)
	case "obs-pass":
		f.OBS.Password = val
	case "obssync-fps":
		toFloat(&f.OBS.Sync.Fps, 0.001)
	case "obssync-deadband":
		toFloat(&f.OBS.Sync.DeadBandFrames, 0)
	case "obssync-restart":
		toInt(&f.OBS.Sync.RestartThresholdMs, 1, 100000)
	// Ableton Link
	case "ablelink-quantum":
		toInt(&f.AbletonLink.Quantum, 1, 64)
	case "ablelink-owner":
		f.AbletonLink.TempoOwner = v
	case "ablelink-sss":
		f.AbletonLink.StartStopSync = b
	case "ablelink-res-on":
		f.AbletonLink.Resolume.Enabled = b
	case "ablelink-res-host":
		f.AbletonLink.Resolume.Host = v
	case "ablelink-res-osc":
		toInt(&f.AbletonLink.Resolume.OSCPort, 1, 65535)
	case "ablelink-res-rest":
		toInt(&f.AbletonLink.Resolume.RESTPort, 1, 65535)
	case "ablelink-res-layer":
		toInt(&f.AbletonLink.Resolume.PhraseClipLayer, 0, 1000)
	case "ablelink-res-clip":
		toInt(&f.AbletonLink.Resolume.PhraseClipClip, 0, 1000)
	// Webcam / MediaLink
	case "webcam-autostart":
		f.Webcam.AutoStart = b
	case "ml-codec":
		f.MediaLink.PreferCodec = v
	case "ml-bitrate":
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.MediaLink.BitrateKbps = n * 1000
		}
	case "ml-swonly":
		f.MediaLink.SWOnly = b
	case "ml-device":
		applyEncodeDevice(&f.MediaLink, v)
	case "ml-encoder":
		f.MediaLink.Encoder = strings.TrimSpace(v)
	case "ml-subprocess":
		f.MediaLink.SetSubprocess(b)
	case "ml-maxfps":
		if n, err := strconv.Atoi(v); err == nil && (n > 0 || n == -1) {
			f.MediaLink.MaxFPS = n
		}
	case "ml-maxheight":
		if n, err := strconv.Atoi(v); err == nil && n >= -1 {
			f.MediaLink.MaxHeight = n
		}
	// Timecode
	case "tc-rate":
		f.Timecode.Rate = v
	case "tc-clock":
		if b {
			f.Timecode.StartAt = "clock"
		} else {
			f.Timecode.StartAt = ""
		}
	case "tc-startat":
		f.Timecode.StartAt = v
	case "tc-ltc-on":
		f.Timecode.LTC.On = b
	case "tc-ltc-dev":
		f.Timecode.LTC.Device = v
	case "tc-ltc-gain":
		if n, err := strconv.ParseFloat(v, 64); err == nil && n <= 0 && n >= -40 {
			f.Timecode.LTC.GainDb = n
		}
	case "tc-mtc-on":
		f.Timecode.MTC.On = b
	case "tc-mtc-dev":
		f.Timecode.MTC.Device = v
	case "tc-art-on":
		f.Timecode.ArtNet.On = b
	case "tc-art-addr":
		f.Timecode.ArtNet.Addr = v
	// Library / Transcode
	case "player-embed":
		f.Player.Embed = b
	case "trans-ffmpeg":
		f.Transcode.FfmpegPath = v
	case "trans-conc":
		toInt(&f.Transcode.MaxConcurrent, 1, 16)
		if u.svc.Workers != nil {
			u.svc.Workers.Configure("transcode", f.Transcode.MaxConcurrent)
		}
	// STT
	case "stt-input":
		f.STT.InputDevice = v
	case "stt-output":
		f.STT.OutputDevice = v
	case "stt-model":
		f.STT.Model = v
	case "stt-autosubmit":
		f.STT.AutoSubmit = b
	case "stt-silence":
		toInt(&f.STT.SilenceMs, 1, 100000)
	// VRChat
	case "vrc-remember":
		f.VRChat.RememberSession = b
	case "vrc-uplink":
		f.VRChat.Uplink = b
		if u.svc.VrchatUplink != nil {
			on := b
			u.bg(func() { u.svc.VrchatUplink(on) })
		}
	// VRCTools
	case "vct-orgphotos":
		f.VRCTools.OrganizePhotos = b
	case "vct-byevent":
		f.VRCTools.OrganizeByEvent = b
	case "vct-photomove":
		f.VRCTools.PhotoMove = b
	case "vct-orgcampaths":
		f.VRCTools.OrganizeCamPaths = b
	case "vct-campathmove":
		f.VRCTools.CamPathMove = b
	case "vct-autobackup":
		f.VRCTools.AutoBackupCamPaths = b
	case "vct-autorestore":
		f.VRCTools.AutoRestoreCamPaths = b
	case "vct-osc":
		f.VRCTools.OSCAddr = v
	case "vct-preset":
		f.VRCTools.DefaultCamPreset = v
	// VR overlay
	case "vr-edithand":
		f.VROverlay.EditHand = v
	case "vr-summon":
		f.VROverlay.SummonButton = v
		f.VROverlay.SummonOn = true
	case "vr-taphides":
		f.VROverlay.SummonTapHides = b
	case "vr-autostart":
		f.VROverlay.AutoStart = b
	case "vr-viewcapture":
		f.VROverlay.VRViewCapture = b
	case "vr-osc":
		f.VROverlay.OSCAddr = v
	case "vr-vmc":
		f.VROverlay.VMCAddr = v
	case "vr-vmclive":
		f.VROverlay.VMCLive = b
	// DMX
	case "dmx-listen":
		f.DMX.ListenAddr = v
	case "dmx-universes":
		f.DMX.Universes = csvToIntsWeb(v)
	case "dmx-grid":
		f.DMX.Grid.Enabled = b
	case "dmx-mode":
		f.DMX.Grid.Mode = v
	case "dmx-spout":
		f.DMX.Grid.SpoutName = v
	case "dmx-fpscap":
		toInt(&f.DMX.Grid.FPSCap, 1, 60)
	case "dmx-reemit":
		f.DMX.ReEmit = b
	case "dmx-emittarget":
		f.DMX.EmitTarget = v
	case "dmx-sacn":
		f.DMX.SACN = b
	case "dmx-sacn-universes":
		f.DMX.SACNUniverses = csvToIntsWeb(v)
	// Lighting-cue recorder (folded into the DMX plane; dmx- prefix restarts the dmx module)
	case "dmx-lc-enable":
		f.LightCue.Enabled = b
	case "dmx-lc-hz":
		toInt(&f.LightCue.Hz, 1, 44)
	// DMX→MIDI
	case "dmxmidi-device":
		f.DMXMIDI.Device = v
	case "dmxmidi-universes":
		f.DMXMIDI.Universes = csvToIntsWeb(v)
	case "dmxmidi-rate":
		toInt(&f.DMXMIDI.MaxPerSecond, 50, 1000)
	// RTSP
	case "rtsp-source":
		f.RTSPServe.Source = v
	case "rtsp-format":
		f.RTSPServe.InputFormat = v
	case "rtsp-passthrough":
		f.RTSPServe.Passthrough = b
	case "rtsp-listen":
		f.RTSPServe.ListenAddr = v
	case "rtsp-path":
		f.RTSPServe.Path = v
	case "rtsp-fps":
		toInt(&f.RTSPServe.FPS, 1, 120)
	case "rtsp-bitrate":
		toInt(&f.RTSPServe.BitrateKbps, 250, 50000)
	// VRSL video stream
	case "vrslstream-transport":
		f.Stream.Transport = v
	case "vrslstream-url":
		f.Stream.URL = v
	case "vrslstream-key":
		f.Stream.StreamKey = v
	case "vrslstream-mode":
		f.Stream.Mode = v
	case "vrslstream-color":
		f.Stream.ColorMode = v
	case "vrslstream-universes":
		f.Stream.Universes = csvToIntsWeb(v)
	case "vrslstream-fps":
		toInt(&f.Stream.FPS, 1, 60)
	case "vrslstream-bitrate":
		if v == "" || v == "0" {
			f.Stream.BitrateKbps = 0 // auto (derive from frame size)
		} else {
			toInt(&f.Stream.BitrateKbps, 250, 50000)
		}
	case "vrslstream-encoder":
		f.Stream.Encoder = v
	// Mocap capture
	case "mocap-source":
		if v == "desktop" || v == "spout" || v == "dshow" {
			f.Mocap.Source = v
		}
	case "mocap-device":
		f.Mocap.Device = v
	case "mocap-monitor":
		toInt(&f.Mocap.Monitor, 0, 16)
	case "mocap-fps":
		toInt(&f.Mocap.FPS, 1, 60)
	case "mocap-boneslots":
		toInt(&f.Mocap.BoneSlots, 1, 32)
	case "mocap-min-x":
		toStage(&f.Mocap.StageMin, f.Mocap.ResolvedStageMin(), 0)
	case "mocap-min-y":
		toStage(&f.Mocap.StageMin, f.Mocap.ResolvedStageMin(), 1)
	case "mocap-min-z":
		toStage(&f.Mocap.StageMin, f.Mocap.ResolvedStageMin(), 2)
	case "mocap-size-x":
		toStage(&f.Mocap.StageSize, f.Mocap.ResolvedStageSize(), 0)
	case "mocap-size-y":
		toStage(&f.Mocap.StageSize, f.Mocap.ResolvedStageSize(), 1)
	case "mocap-size-z":
		toStage(&f.Mocap.StageSize, f.Mocap.ResolvedStageSize(), 2)
	// Capture crew relay
	case "crew-eventid":
		f.Crew.EventID = v
	case "crew-role":
		if v == "node" || v == "master" {
			f.Crew.Role = v
		}
	case "crew-label":
		f.Crew.Label = v
	// World Sync hosted-mode target world id (wrld_…); persisted, read at publish time.
	case "ws-worldid":
		f.WorldSync.HostedWorldID = v
	// Overlays (rendered on the Overlays tab; port is read at overlay-server start)
	case "overlay-port":
		toInt(&f.OverlayWeb.Port, 1, 65535)
	// key hold (not persisted here - Save button reads it via form)
	case "rb-key-hold":
		save = false
	default:
		save = false
	}
	if save {
		// module/source reads this field only at (re)start - restart it automatically
		// (debounced; deferred while capturing/recording) instead of making the user
		// toggle the feature off/on
		var apply func()
		if mod := settingModule(id); mod != "" {
			apply = func() { u.scheduleModuleRestart(mod) }
		} else if src := settingSource(id); src != "" {
			apply = func() { u.scheduleSourceRestart(src) }
		} else if snk := settingSink(id); snk != "" {
			apply = func() { u.scheduleSinkRestart(snk) }
		}
		u.saveCfgBG("set:"+id, apply, nil) // disk write + Reconcile off the actWorker
	}
}

func (u *UI) authLogin() {
	if u.svc.Auth == nil {
		return
	}
	u.toast("Opening browser sign-in…")
	go func() {
		if err := u.svc.Auth.Login(); err != nil && u.log != nil {
			u.log.Error("webui", "auth login", map[string]any{"error": err.Error()})
		}
	}()
}

// saveCfg persists config + reconciles the session (mirrors the Fyne u.saveCfg).
func (u *UI) saveCfg() {
	if u.svc.Cfg != nil {
		// never swallow: a silent failure means the change dies with the process
		// (2026-08-11: user presets lost exactly this way)
		if err := u.svc.Cfg.Save(); err != nil {
			u.log.Error("webui", "config save failed: "+err.Error(), nil)
			u.toast(i18n.T("library.toast.saveFailed") + err.Error())
		}
	}
	if u.svc.Session != nil {
		u.svc.Session.Reconcile()
	}
}

// ── async persist (keeps disk + module churn off the serial actWorker) ──

// cfgSaveMu serializes background config writes (full-config marshal - last writer wins).
var cfgSaveMu sync.Mutex

// cfgJobs coalesces per-control background persist jobs: one in flight per key; a repeat while busy
// queues ONE rerun with the latest callbacks (optimistic flip + reconcile, mpv tap-freeze
// precedent). Deliberately process-global across UIs (window + headless remote sessions): the
// config file is one shared resource, and a queued rerun always drains promptly after the
// in-flight save - one UI can never starve another.
var (
	cfgJobMu sync.Mutex
	cfgJobs  = map[string]*cfgJob{}
)

type cfgJob struct {
	rerun         bool
	apply, onFail func()
}

// saveCfgBG persists config + reconciles the session in a background job keyed by control key, then
// runs apply (module start/stop etc.); onFail runs instead when the write fails so the caller can
// revert its optimistic patch. Both callbacks may be nil and run off the actWorker.
func (u *UI) saveCfgBG(key string, apply, onFail func()) {
	cfgJobMu.Lock()
	if j, ok := cfgJobs[key]; ok {
		j.rerun, j.apply, j.onFail = true, apply, onFail
		cfgJobMu.Unlock()
		return
	}
	cfgJobs[key] = &cfgJob{apply: apply, onFail: onFail}
	cfgJobMu.Unlock()
	u.bg(func() {
		for {
			var err error
			if u.svc.Cfg != nil {
				cfgSaveMu.Lock()
				err = u.svc.Cfg.Save()
				cfgSaveMu.Unlock()
			}
			if err != nil {
				if u.log != nil {
					u.log.Error("webui", "config save", map[string]any{"key": key, "error": err.Error()})
				}
				if onFail != nil {
					onFail()
				}
			} else {
				if u.svc.Session != nil {
					u.svc.Session.Reconcile()
				}
				if apply != nil {
					apply()
				}
			}
			cfgJobMu.Lock()
			j := cfgJobs[key]
			if !j.rerun {
				delete(cfgJobs, key)
				cfgJobMu.Unlock()
				return
			}
			j.rerun, apply, onFail = false, j.apply, j.onFail
			cfgJobMu.Unlock()
		}
	})
}

func onOff(b bool) string {
	if b {
		return " - enabled"
	}
	return " - disabled"
}

// ── small helpers ──

// pathField renders a path input + native Browse… button (picker contract: pick-<kind>:<act>,
// kind ∈ dir|file). The pick handler re-dispatches act with the chosen path.
func pathField(label, act, value, kind string) string {
	return `<div class=set-pathrow>` + field(label, act, value, "text") +
		btn("Browse…", "ghost", "pick-"+kind+":"+act, "") + `</div>`
}

// pathFieldPH is pathField pre-filled with a placeholder (detected default path). placeholder is
// only meaningful when value is empty - shows the user the auto-detected dir so they needn't browse.
func pathFieldPH(label, act, value, kind, placeholder string) string {
	return `<div class=set-pathrow>` + fieldPH(label, act, value, "text", placeholder) +
		btn("Browse…", "ghost", "pick-"+kind+":"+act, "") + `</div>`
}

// liveIntervalStr renders a poll interval for the number field: 0 → "" (placeholder default).
func liveIntervalStr(sec int) string {
	if sec <= 0 {
		return ""
	}
	return strconv.Itoa(sec)
}

func devOpts(names []string, defLabel, cur string) [][2]string {
	opts := [][2]string{{"", defLabel}}
	seen := map[string]bool{"": true}
	for _, n := range names {
		opts = append(opts, [2]string{n, n})
		seen[n] = true
	}
	if cur != "" && !seen[cur] {
		opts = append(opts, [2]string{cur, cur + " (saved)"})
	}
	return opts
}

func noPortsHint(names []string) string {
	if len(names) == 0 {
		return `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.midi.noPorts")) + `</div>`
	}
	return ""
}

func orInt(v, d int) int {
	if v != 0 {
		return v
	}
	return d
}

func orFloat(v, d float64) float64 {
	if v != 0 {
		return v
	}
	return d
}

func intsToCSVWeb(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, ",")
}

func csvToIntsWeb(s string) []int {
	var out []int
	for p := range strings.SplitSeq(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n, err := strconv.Atoi(p); err == nil && n >= 0 && n <= 0x7FFF {
			out = append(out, n)
		}
	}
	return out
}
