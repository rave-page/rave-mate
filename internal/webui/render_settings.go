package webui

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/serato"
	"rave.page/mate/internal/stt"
	"rave.page/mate/internal/timecode"
	"rave.page/mate/internal/traktormap"
	"rave.page/mate/internal/unityproj"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/vrdll"
	"rave.page/mate/internal/vroverlay"
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
		{id: "webcam", label: i18n.T("settings.toggle.webcam"), module: "webcam", get: func() bool { return f.Webcam.Enabled }, set: func(b bool) { f.Webcam.Enabled = b }},
		{id: "medialink", label: i18n.T("settings.toggle.medialink"), get: func() bool { return f.MediaLink.ShareVideo }, set: func(b bool) { f.MediaLink.ShareVideo = b }},
		{id: "timecode", label: i18n.T("settings.toggle.timecode"), module: "timecode", get: func() bool { return f.Timecode.Enabled }, set: func(b bool) { f.Timecode.Enabled = b }},
		{id: "ablelink", label: i18n.T("settings.toggle.ablelink"), module: "abletonlink", get: func() bool { return f.AbletonLink.Enabled }, set: func(b bool) { f.AbletonLink.Enabled = b }},
		// Library & media
		{id: "library", label: i18n.T("settings.toggle.library"), retab: true, get: func() bool { return f.Library.Enabled }, set: func(b bool) { f.Library.Enabled = b }},
		{id: "mediaeditor", label: i18n.T("settings.toggle.mediaeditor"), retab: true, get: func() bool { return f.MediaEditor.Enabled }, set: func(b bool) { f.MediaEditor.Enabled = b }},
		{id: "transcode", label: i18n.T("settings.toggle.transcode"), get: func() bool { return f.Transcode.Enabled }, set: func(b bool) { f.Transcode.Enabled = b }},
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

// renderStatus renders the dot + line fragment patched by the settings tick.
func renderStatus(s stv) string {
	return `<span class="dot dot--` + s.v + `"></span><span data-value="` + html.EscapeString(s.t) + `">` + html.EscapeString(s.t) + `</span>`
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
		{"streaming", st("streaming"), sd("streaming"), []string{"streambridge", "studio", "peers", "webcam", "medialink", "timecode", "ablelink"}},
		{"libmedia", st("libmedia"), sd("libmedia"), []string{"library", "mediaeditor", "transcode"}},
		{"integrations", st("integrations"), sd("integrations"), []string{"twitch", "stt", "vrchat", "vrctools", "worldsync", "vroverlay", "dmx", "dmxmidi", "rtsp", "unity"}},
		{"system", st("system"), sd("system"), []string{"appgroups", "notifications", "guardian", "service", "updates"}},
	}
}

// renderSettings: header + a global search box + a patchable content pane. Only the ACTIVE
// sub-tab's cards are in the DOM (the old render built all ~40 cards at once - the main reason
// opening Settings froze the app); a non-empty query switches the pane to cross-section results.
func (u *UI) renderSettings() string {
	if u.svc.Cfg == nil {
		return panel(i18n.T("settings.title"), "") + emptyState(i18n.T("settings.body.configUnavailable"))
	}
	u.maybeRefreshProbes() // async fs/PATH/device probe refresh - render below reads cache only (instant)
	q := u.settingsQueryText()
	return `<div id=settings-body>` + panel(i18n.T("settings.title"), i18n.T("settings.subtitle")) +
		`<div class=set-search data-label="settings-search"><input id=set-q class=field-input type=search value=` + attrQ(q) +
		` placeholder=` + attrQ(i18n.T("settings.search.placeholder")) +
		` data-actinput=settings-search autocomplete=off spellcheck=false></div>` +
		`<div id=set-content>` + u.renderSettingsContent() + `</div></div>`
}

// renderSettingsContent renders the pane below the search box: sub-tab pills + the active
// section's cards, or (query non-empty) matching cards across ALL sections grouped by section.
// Patched on its own (#set-content) so the search input's DOM - and its focus - survive.
func (u *UI) renderSettingsContent() string {
	secs := settingsSections()
	stats := u.settingsStatus()
	visible := map[string]bool{}
	var b strings.Builder

	if rawQ := strings.TrimSpace(u.settingsQueryText()); rawQ != "" {
		terms := strings.Fields(foldSearch(rawQ))
		total := 0
		for _, s := range secs {
			var cards strings.Builder
			for _, id := range s.cards {
				h := u.settingsCard(id, stats[id])
				// match against the card's visible text (title + labels + help notes) - the
				// registry stays single-sourced, every label is searchable for free
				if !matchAllTerms(foldSearch(stripTags(h)), terms) {
					continue
				}
				cards.WriteString(h)
				visible[id] = true
				total++
			}
			if cards.Len() > 0 {
				b.WriteString(section(s.title, `<div class=set-sec>`+cards.String()+`</div>`))
			}
		}
		if total == 0 {
			b.WriteString(emptyState(i18n.T("settings.search.noResults", i18n.A{"query": rawQ})))
		}
		u.setViewState(visible, true)
		return b.String()
	}

	active := u.settingsActiveSec(secs)
	b.WriteString(`<nav class=set-nav>`)
	for _, s := range secs {
		agg := "off"
		for _, id := range s.cards {
			if st, ok := stats[id]; ok && stRank(st.v) > stRank(agg) {
				agg = st.v
			}
		}
		cls := "set-navpill"
		if s.id == active {
			cls += " active"
		}
		b.WriteString(`<button class="` + cls + `" data-act=settings-sec data-val="` + s.id + `">` +
			`<span id=stnav-` + s.id + `><span class="dot dot--` + agg + `"></span></span>` +
			html.EscapeString(s.title) + `</button>`)
	}
	b.WriteString(`</nav>`)
	for _, s := range secs {
		if s.id != active {
			continue
		}
		var body strings.Builder
		body.WriteString(`<p class=page-sub>` + html.EscapeString(s.desc) + `</p>`)
		body.WriteString(`<div id=set-` + s.id + ` class=set-sec>`)
		for _, id := range s.cards {
			body.WriteString(u.settingsCard(id, stats[id]))
			visible[id] = true
		}
		body.WriteString(`</div>`)
		b.WriteString(body.String())
	}
	u.setViewState(visible, false)
	return b.String()
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

// stripTags reduces card HTML to its visible text (titles, labels, help notes).
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
	"recorder":    "session-recorder",
	"setcapture":  "icecast",
	"fingerprint": "fingerprinting",
	"timecode":    "tc-timecode",
	"obssync":     "obssync-mediasync",
	"dmx":         "dmx-connect",
	"rtsp":        "rtsp-why",
}

func (u *UI) settingsCard(id string, st stv) string {
	m := u.toggleMap()
	title, desc, body := u.cardContent(id)
	head := `<span class=set-title>` + html.EscapeString(title) + `</span>`
	if topic := settingsCardTips[id]; topic != "" {
		head += tipTopic(topic)
	}
	if t, ok := m[id]; ok {
		checked := ""
		if t.get() {
			checked = " checked"
		}
		head += `<label class=switch title="` + html.EscapeString(t.label) + `"><input type=checkbox` + checked +
			` data-act="toggle:` + id + `" data-value="` + boolStr(t.get()) + `"><span class=switch-track></span></label>`
	}
	descHTML := ""
	if desc != "" {
		descHTML = `<div class=set-note>` + html.EscapeString(desc) + `</div>`
	}
	return `<div class="rp-card"><div class=set-cardhead>` + head + `</div>` +
		`<div class=set-st id=stset-` + id + `>` + renderStatus(st) + `</div>` +
		descHTML + body + `</div>`
}

// ── account / api ──

func (u *UI) accountCardHTML() string {
	signed := u.svc.Auth != nil && u.svc.Auth.SignedIn()
	status := i18n.T("settings.body.account.notSignedIn")
	row := btn(i18n.T("settings.body.account.signInBrowser"), "primary", "auth-login", "")
	if signed {
		status = i18n.T("settings.body.account.signedIn")
		row = btn(i18n.T("settings.body.account.reauth"), "outline", "auth-login", "") + btn(i18n.T("common.signOut"), "destructive", "auth-logout", "")
	}
	nick := u.svc.Cfg.Features.Peers.Nickname
	return `<div class=set-note>` + html.EscapeString(status) + `</div>` +
		field(i18n.T("settings.body.account.nodeName"), "set:peer-nick", nick, "text") +
		btnRow(row)
}

// cardContent returns (title, desc, bodyHTML) for a card id.
func (u *UI) cardContent(id string) (string, string, string) {
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
		return i18n.T("settings.language.title"), i18n.T("settings.language.desc"),
			smartSelect("uilang", i18n.T("settings.language.label"), "ui-setlang:", i18n.Current(), opts)
	case "account":
		return i18n.T("settings.card.account.title"), i18n.T("settings.card.account.desc"), u.accountCardHTML()
	case "api":
		// base URL shows once - in the live status line (stset-api), not duplicated as a kv
		return i18n.T("settings.card.api.title"), "", `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.api.note")) + `</div>`

	// ── DJ sources ──
	case "traktor":
		t := &f.Traktor
		return i18n.T("settings.card.traktor.title"), i18n.T("settings.card.traktor.desc"),
			field(i18n.T("settings.body.traktor.listenPort"), "set:traktor-port", strconv.Itoa(t.ResolvedPort()), "number") +
				toggleRow(i18n.T("settings.body.traktor.logRaw"), "set:traktor-log", t.LogPayloads) +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.traktor.note")) + `</div>`
	case "traktorqml":
		return i18n.T("settings.card.traktorqml.title"), i18n.T("settings.card.traktorqml.desc"),
			btnRow(btn(i18n.T("settings.body.traktorqml.install"), "primary", "settings-qml-apply", ""), btn(i18n.T("settings.body.common.remove"), "warn", "settings-qml-revert", ""), btn(i18n.T("common.refresh"), "ghost", "settings-refresh", ""))
	case "traktormap":
		return i18n.T("settings.card.traktormap.title"), i18n.T("settings.card.traktormap.desc"), u.traktorMapBody()
	case "midi":
		mf := &f.MIDI
		names := u.devNamesCached("midi")
		portPlaceholder := i18n.T("settings.body.midi.selectPortPlaceholder")
		return i18n.T("settings.card.midi.title"), i18n.T("settings.card.midi.desc"),
			selectBox(i18n.T("settings.body.midi.customPort"), "set:midi-custom", devOpts(names, portPlaceholder, mf.CustomPort), mf.CustomPort) +
				selectBox(i18n.T("settings.body.midi.denonPort"), "set:midi-denon", devOpts(names, portPlaceholder, mf.DenonPort), mf.DenonPort) +
				toggleRow(i18n.T("settings.body.midi.mesh"), "set:midi-mesh", mf.MeshForward) +
				btnRow(btn(i18n.T("settings.body.midi.refreshPorts"), "ghost", "settings-refresh", ""), btn(i18n.T("settings.body.midi.installLoopMidi"), "ghost", "open-url", "https://www.tobias-erichsen.de/software/loopmidi.html")) +
				noPortsHint(names)
	case "nml":
		return i18n.T("settings.card.nml.title"), i18n.T("settings.card.nml.desc"),
			pathField(i18n.T("settings.body.nml.pathLabel"), "set:nml-path", f.NML.CollectionPath, "file") +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.nml.note")) + `</div>`
	case "prodjlink":
		return i18n.T("settings.card.prodjlink.title"), i18n.T("settings.card.prodjlink.desc"), ""
	case "serato":
		sf := &f.Serato
		// pre-fill the dir field with the redirection-aware detected _Serato_ dir (registry-resolved
		// Music known folder) so the user needn't browse. Only probe when unset.
		seratoPH := ""
		if sf.SeratoDir == "" {
			seratoPH = serato.SuggestedDir()
		}
		return i18n.T("settings.card.serato.title"), i18n.T("settings.card.serato.desc"),
			pathFieldPH(i18n.T("settings.body.serato.folder"), "set:serato-dir", sf.SeratoDir, "dir", seratoPH) +
				toggleRow(i18n.T("settings.body.serato.nowPlaying"), "set:serato-np", sf.NowPlaying) +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.serato.note")) + `</div>` +
				toggleRow(i18n.T("settings.body.serato.remote"), "set:serato-remote", sf.Remote) +
				toggleRow(i18n.T("settings.body.serato.remoteDebug"), "set:serato-remotedebug", sf.RemoteDebug) +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.serato.remoteNote")) + `</div>` +
				toggleRow(i18n.T("settings.body.serato.livePlaylist"), "set:serato-live", sf.LivePlaylist) +
				fieldPH(i18n.T("settings.body.serato.livePlaylistUrl"), "set:serato-liveurl", sf.LivePlaylistURL, "text", "https://serato.com/playlists/<username>/live") +
				fieldPH(i18n.T("settings.body.serato.livePlaylistInterval"), "set:serato-liveinterval", liveIntervalStr(sf.LivePlaylistInterval), "number", "10") +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.serato.livePlaylistNote")) + `</div>`
	case "virtualdj":
		vf := &f.VirtualDJ
		vdjPH := ""
		if vf.DatabaseDir == "" {
			vdjPH = musiclib.DefaultVirtualDJDir()
		}
		return i18n.T("settings.card.virtualdj.title"), i18n.T("settings.card.virtualdj.desc"),
			pathFieldPH(i18n.T("settings.body.virtualdj.folder"), "set:vdj-dir", vf.DatabaseDir, "dir", vdjPH) +
				toggleRow(i18n.T("settings.body.virtualdj.netCtl"), "set:vdj-netctl", vf.NetCtl) +
				field(i18n.T("settings.body.virtualdj.pluginUrl"), "set:vdj-netctlurl", vf.NetCtlURL, "text") +
				field(i18n.T("settings.body.virtualdj.pluginAuth"), "set:vdj-netctlauth", vf.NetCtlAuth, "password") +
				toggleRow(i18n.T("settings.body.virtualdj.os2l"), "set:vdj-os2l", vf.OS2L) +
				toggleRow(i18n.T("settings.body.virtualdj.tracklist"), "set:vdj-tracklist", vf.Tracklist) +
				btnRow(btn(i18n.T("settings.body.virtualdj.howToEnable"), "ghost", "open-url", "https://virtualdj.com/wiki/NetworkControlPlugin.html"))
	case "rekordbox":
		rf := &f.Rekordbox
		return i18n.T("settings.card.rekordbox.title"), i18n.T("settings.card.rekordbox.desc"),
			toggleRow(i18n.T("settings.body.rekordbox.dbPoll"), "set:rb-dbpoll", rf.DBPoll) +
				toggleRow(i18n.T("settings.body.rekordbox.memRead"), "set:rb-memread", rf.MemoryRead)
	case "rekordboxkey":
		return i18n.T("settings.card.rekordboxkey.title"), i18n.T("settings.card.rekordboxkey.desc"),
			`<form class=set-dlgform data-act=settings-rbkey-save><input class=field-input type=password name=key placeholder=` + attrQ(i18n.T("settings.body.rekordboxkey.placeholder")) + `autocomplete=off>` +
				`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(i18n.T("settings.body.rekordboxkey.save")) + `</button></form>` +
				btnRow(btn(i18n.T("settings.body.rekordboxkey.test"), "outline", "settings-rbkey-test", "")) +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.rekordboxkey.note")) + `</div>`
	case "rekordboxmidi":
		return i18n.T("settings.card.rekordboxmidi.title"), i18n.T("settings.card.rekordboxmidi.desc"),
			btnRow(btn(i18n.T("settings.body.rekordboxmidi.export"), "primary", "settings-rbmidi-export", ""), btn(i18n.T("settings.body.rekordboxmidi.openFolder"), "ghost", "settings-rbmidi-folder", ""))

	// ── Recording ──
	case "recorder":
		return i18n.T("settings.card.recorder.title"), i18n.T("settings.card.recorder.desc"),
			field(i18n.T("settings.body.recorder.confirmAfter"), "set:rec-confirm", strconv.Itoa(f.Recorder.ResolvedConfirmSeconds()), "number") +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.recorder.note")) + `</div>`
	case "setcapture":
		return i18n.T("settings.card.setcapture.title"), i18n.T("settings.card.setcapture.desc"), u.setCaptureBody()
	case "audiorecord":
		return i18n.T("settings.card.audiorecord.title"), i18n.T("settings.card.audiorecord.desc"), u.audioRecBody()
	case "obs":
		return i18n.T("settings.card.obs.title"), i18n.T("settings.card.obs.desc"), u.obsBody()
	case "obssync":
		return i18n.T("settings.card.obssync.title"), i18n.T("settings.card.obssync.desc"), u.obsSyncBody()
	case "fingerprint":
		return i18n.T("settings.card.fingerprint.title"), i18n.T("settings.card.fingerprint.desc"),
			`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.fingerprint.note")) + `</div>` +
				u.toolInstallHTML(mediatools.Fpcalc, "fpcalc")

	// ── Streaming & remote ──
	case "streambridge":
		return i18n.T("settings.card.streambridge.title"), i18n.T("settings.card.streambridge.desc"), ""
	case "studio":
		return i18n.T("settings.card.studio.title"), i18n.T("settings.card.studio.desc"), ""
	case "peers":
		return i18n.T("settings.card.peers.title"), i18n.T("settings.card.peers.desc"),
			field(i18n.T("settings.body.account.nodeName"), "set:peer-nick", f.Peers.Nickname, "text")
	case "webcam":
		return i18n.T("settings.card.webcam.title"), i18n.T("settings.card.webcam.desc"),
			toggleRow(i18n.T("settings.body.webcam.autostart"), "set:webcam-autostart", f.Webcam.AutoStart) +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.webcam.note")) + `</div>`
	case "medialink":
		mf := &f.MediaLink
		return i18n.T("settings.card.medialink.title"), i18n.T("settings.card.medialink.desc"),
			selectBoxTip(i18n.T("settings.body.medialink.codec"), "set:ml-codec", [][2]string{{"", "auto"}, {"hevc", "hevc"}, {"h264", "h264"}, {"mjpeg", "mjpeg"}}, mf.PreferCodec, "ml-accel") +
				selectBoxTip(i18n.T("settings.body.medialink.bitrate"), "set:ml-bitrate", [][2]string{{"8", "8"}, {"12", "12"}, {"20", "20"}, {"30", "30"}, {"50", "50"}}, strconv.Itoa(mf.Bitrate()/1000), "ml-budget") +
				toggleRow(i18n.T("settings.body.medialink.swOnly"), "set:ml-swonly", mf.SWOnly) +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.medialink.note")) + `</div>`
	case "timecode":
		return i18n.T("settings.card.timecode.title"), i18n.T("settings.card.timecode.desc"), u.timecodeBody()
	case "ablelink":
		return i18n.T("settings.card.ablelink.title"), i18n.T("settings.card.ablelink.desc"), u.ableLinkBody()

	// ── Library & media ──
	case "library":
		return i18n.T("settings.card.library.title"), i18n.T("settings.card.library.desc"),
			`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.library.mpvNote")) + `</div>` +
				u.toolInstallHTML(mediatools.MPV, "mpv") +
				toggleRow(i18n.T("settings.body.library.embed"), "set:player-embed", f.Player.Embed)
	case "mediaeditor":
		return i18n.T("settings.card.mediaeditor.title"), i18n.T("settings.card.mediaeditor.desc"), ""
	case "transcode":
		tf := &f.Transcode
		conc := tf.MaxConcurrent
		if conc < 1 {
			conc = 2
		}
		return i18n.T("settings.card.transcode.title"), i18n.T("settings.card.transcode.desc"),
			pathField(i18n.T("settings.body.transcode.ffmpegPath"), "set:trans-ffmpeg", tf.FfmpegPath, "file") +
				field(i18n.T("settings.body.transcode.maxJobs"), "set:trans-conc", strconv.Itoa(conc), "number") +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.transcode.note")) + `</div>` +
				u.toolInstallHTML(mediatools.FFmpeg, "ffmpeg")

	// ── Integrations ──
	case "twitch":
		return i18n.T("settings.card.twitch.title"), i18n.T("settings.card.twitch.desc"), u.twitchBody()
	case "stt":
		return i18n.T("settings.card.stt.title"), i18n.T("settings.card.stt.desc"), u.sttBody()
	case "vrchat":
		return i18n.T("settings.card.vrchat.title"), i18n.T("settings.card.vrchat.desc"), u.vrchatBody()
	case "vrctools":
		return i18n.T("settings.card.vrctools.title"), i18n.T("settings.card.vrctools.desc"), u.vrcToolsBody()
	case "worldsync":
		return i18n.T("settings.card.worldsync.title"), i18n.T("settings.card.worldsync.desc"), u.worldSyncBody()
	case "vroverlay":
		return i18n.T("settings.card.vroverlay.title"), i18n.T("settings.card.vroverlay.desc"), u.vrOverlayBody()
	case "dmx":
		return i18n.T("settings.card.dmx.title"), i18n.T("settings.card.dmx.desc"), u.dmxBody()
	case "dmxmidi":
		return i18n.T("settings.card.dmxmidi.title"), i18n.T("settings.card.dmxmidi.desc"), u.dmxMidiBody()
	case "rtsp":
		return i18n.T("settings.card.rtsp.title"), i18n.T("settings.card.rtsp.desc"), u.rtspBody()
	case "unity":
		return i18n.T("settings.card.unity.title"), i18n.T("settings.card.unity.desc"), u.unityBody()

	// ── System ──
	case "appgroups":
		return i18n.T("settings.card.appgroups.title"), i18n.T("settings.card.appgroups.desc"), ""
	case "notifications":
		return i18n.T("settings.card.notifications.title"), i18n.T("settings.card.notifications.desc"), ""
	case "guardian":
		return i18n.T("settings.card.guardian.title"), i18n.T("settings.card.guardian.desc"),
			`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.guardian.note")) + `</div>`
	case "service":
		return i18n.T("settings.card.service.title"), i18n.T("settings.card.service.desc"),
			btnRow(btn(i18n.T("settings.body.service.install"), "primary", "settings-svc-install", ""), btn(i18n.T("settings.body.service.uninstall"), "outline", "settings-svc-uninstall", ""), btn(i18n.T("common.refresh"), "ghost", "settings-refresh", "")) +
				`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.service.note")) + `</div>`
	case "updates":
		return i18n.T("settings.card.updates.title"), i18n.T("settings.card.updates.desc"), u.updatesBody()
	}
	return "?", "", ""
}

// ── bodies that need more than one-liners ──

func (u *UI) traktorMapBody() string {
	tm := u.svc.TraktorMap
	verSel := selectBox(i18n.T("settings.body.traktormap.version"), "set:traktor-mapver", [][2]string{{traktormap.AutoVersion, i18n.T("settings.body.traktormap.autoNewest")}}, u.svc.Cfg.Features.Traktor.MappingVersion)
	if tm == nil {
		return verSel + `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.traktormap.unavailable")) + `</div>`
	}
	var rows strings.Builder
	for _, mp := range tm.Available() {
		rows.WriteString(itemRow(mp.Display, "",
			btn(i18n.T("settings.body.traktormap.activate"), "outline", "settings-tmap-on:"+mp.Key, ""),
			btn(i18n.T("settings.body.common.remove"), "ghost", "settings-tmap-off:"+mp.Key, "")))
	}
	return verSel + rows.String() +
		btnRow(btn(i18n.T("common.refresh"), "ghost", "settings-refresh", "")) +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.traktormap.note")) + `</div>`
}

func (u *UI) setCaptureBody() string {
	f := &u.svc.Cfg.Features.SetCapture
	return field(i18n.T("settings.body.setcapture.port"), "set:sc-port", strconv.Itoa(f.ResolvedPort()), "number") +
		field(i18n.T("settings.body.setcapture.mount"), "set:sc-mount", f.Mount, "text") +
		field(i18n.T("settings.body.setcapture.sourceUser"), "set:sc-user", f.Username, "text") +
		field(i18n.T("settings.body.setcapture.password"), "set:sc-pass", f.Password, "password") +
		pathField(i18n.T("settings.body.setcapture.setsFolder"), "set:sc-dir", f.SetsDir, "dir") +
		field(i18n.T("settings.body.setcapture.reconnectGrace"), "set:sc-grace", strconv.Itoa(int(f.ResolvedReconnectGrace().Seconds())), "number") +
		toggleRow(i18n.T("settings.body.setcapture.singleFile"), "set:sc-single", f.SingleFile) +
		toggleRow(i18n.T("settings.body.setcapture.metaOnly"), "set:sc-metaonly", f.MetadataOnly) +
		btnRow(btn(i18n.T("settings.body.setcapture.openFolder"), "ghost", "settings-open:sets", "")) +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.setcapture.note", i18n.A{"port": strconv.Itoa(f.ResolvedPort()), "mount": or(f.Mount, "/stream")})) + `</div>`
}

func (u *UI) audioRecBody() string {
	f := &u.svc.Cfg.Features.AudioRecord
	devs := u.devNamesCached("audiorec")
	return selectBox(i18n.T("settings.body.audiorec.device"), "set:ar-device", devOpts(devs, i18n.T("settings.body.audiorec.devicePlaceholder"), f.Device), f.Device) +
		selectBox(i18n.T("settings.body.audiorec.format"), "set:ar-format", [][2]string{{"flac", "flac"}, {"wav", "wav"}, {"mp3", "mp3"}, {"aac", "aac"}}, f.ResolvedFormat()) +
		field(i18n.T("settings.body.audiorec.bitrate"), "set:ar-bitrate", strconv.Itoa(f.ResolvedBitrate()), "number") +
		field(i18n.T("settings.body.audiorec.sampleRate"), "set:ar-samplerate", strconv.Itoa(f.SampleRate), "number") +
		pathField(i18n.T("settings.body.audiorec.folder"), "set:ar-dir", f.Dir, "dir") +
		toggleRow(i18n.T("settings.body.audiorec.followObs"), "set:ar-followobs", f.FollowOBS) +
		toggleRow(i18n.T("settings.body.audiorec.writeTags"), "set:ar-writetags", f.WriteTags) +
		btnRow(btn(i18n.T("settings.body.audiorec.refreshDevices"), "ghost", "settings-refresh", ""), btn(i18n.T("settings.body.audiorec.openFolder"), "ghost", "settings-open:recordings", "")) +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.audiorec.note")) + `</div>`
}

func (u *UI) obsBody() string {
	f := &u.svc.Cfg.Features.OBS
	return field(i18n.T("settings.body.obs.host"), "set:obs-host", f.ResolvedHost(), "text") +
		field(i18n.T("settings.body.obs.port"), "set:obs-port", strconv.Itoa(f.ResolvedPort()), "number") +
		field(i18n.T("settings.body.obs.password"), "set:obs-pass", f.Password, "password") +
		btnRow(btn(i18n.T("settings.body.obs.connectValidate"), "outline", "settings-obs-validate", ""),
			btn(i18n.T("settings.body.obs.remoteObs", i18n.A{"count": fmt.Sprint(len(f.Remotes))}), "outline", "settings-obsrem", "")) +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.obs.note")) + `</div>`
}

func (u *UI) obsSyncBody() string {
	f := &u.svc.Cfg.Features.OBS.Sync
	return fieldTip(i18n.T("settings.body.common.frameRate"), "set:obssync-fps", trimNum(orFloat(f.Fps, 30)), "number", tipTopic("obssync-fps")) +
		fieldTip(i18n.T("settings.body.obssync.deadBand"), "set:obssync-deadband", trimNum(orFloat(f.DeadBandFrames, 2)), "number", tipTopic("obssync-deadband")) +
		fieldTip(i18n.T("settings.body.obssync.restartThreshold"), "set:obssync-restart", strconv.Itoa(orInt(f.RestartThresholdMs, 1500)), "number", tipTopic("obssync-restart")) +
		btnRow(btn(i18n.T("settings.body.obssync.mediaSources", i18n.A{"count": fmt.Sprint(len(f.Sources))}), "outline", "settings-obssync-src", "")) +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.obssync.note")) + `</div>`
}

func (u *UI) ableLinkBody() string {
	f := &u.svc.Cfg.Features.AbletonLink
	r := &f.Resolume
	body := selectBox(i18n.T("settings.body.ablelink.quantum"), "set:ablelink-quantum",
		[][2]string{{"8", "8"}, {"16", "16"}, {"32", "32"}}, strconv.Itoa(f.ResolvedQuantum())) +
		selectBox(i18n.T("settings.body.ablelink.tempoOwner"), "set:ablelink-owner",
			[][2]string{{"auto", i18n.T("settings.body.ablelink.ownerAuto")}, {"always", i18n.T("settings.body.ablelink.ownerAlways")}, {"follow", i18n.T("settings.body.ablelink.ownerFollow")}}, f.ResolvedTempoOwner()) +
		toggleRow(i18n.T("settings.body.ablelink.startStopSync"), "set:ablelink-sss", f.StartStopSync) +
		toggleRow(i18n.T("settings.body.ablelink.resolumeOn"), "set:ablelink-res-on", r.Enabled)
	if r.Enabled {
		body += field(i18n.T("settings.body.ablelink.resolumeHost"), "set:ablelink-res-host", r.ResolvedHost(), "text") +
			field(i18n.T("settings.body.ablelink.resolumeOscPort"), "set:ablelink-res-osc", strconv.Itoa(r.ResolvedOSCPort()), "number") +
			field(i18n.T("settings.body.ablelink.resolumeRestPort"), "set:ablelink-res-rest", strconv.Itoa(r.ResolvedRESTPort()), "number") +
			field(i18n.T("settings.body.ablelink.phraseClipLayer"), "set:ablelink-res-layer", strconv.Itoa(r.PhraseClipLayer), "number") +
			field(i18n.T("settings.body.ablelink.phraseClipClip"), "set:ablelink-res-clip", strconv.Itoa(r.PhraseClipClip), "number")
	}
	body += btnRow(btn(i18n.T("settings.body.ablelink.resync"), "outline", "ablelink-resync", "")) +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.ablelink.note")) + `</div>`
	return body
}

func (u *UI) timecodeBody() string {
	f := &u.svc.Cfg.Features.Timecode
	waveOut := u.devNamesCached("waveout")
	midiOut := u.devNamesCached("midiout")
	clock := f.StartAt == "clock"
	body := selectBoxTip(i18n.T("settings.body.common.frameRate"), "set:tc-rate", [][2]string{{"24", i18n.T("settings.body.timecode.rate24")}, {"25", i18n.T("settings.body.timecode.rate25")}, {"29.97", i18n.T("settings.body.timecode.rate2997")}, {"30", i18n.T("settings.body.timecode.rate30")}}, f.ResolvedRate(), "tc-rate") +
		toggleRow(i18n.T("settings.body.timecode.clockStart"), "set:tc-clock", clock)
	if !clock {
		body += fieldTip(i18n.T("settings.body.timecode.startPosition"), "set:tc-startat", f.StartAt, "text", tipTopic("tc-start"))
	}
	body += toggleRowTip(i18n.T("settings.body.timecode.ltcOn"), "set:tc-ltc-on", f.LTC.On, tipTopic("tc-ltc")) +
		selectBox(i18n.T("settings.body.timecode.ltcDevice"), "set:tc-ltc-dev", devOpts(waveOut, i18n.T("settings.body.common.systemDefault"), f.LTC.Device), f.LTC.Device) +
		fieldTip(i18n.T("settings.body.timecode.ltcLevel"), "set:tc-ltc-gain", trimNum(f.LTC.ResolvedGainDb()), "number", tipTopic("tc-ltc-level")) +
		toggleRowTip(i18n.T("settings.body.timecode.mtcOn"), "set:tc-mtc-on", f.MTC.On, tipTopic("tc-mtc")) +
		selectBox(i18n.T("settings.body.timecode.mtcPort"), "set:tc-mtc-dev", devOpts(midiOut, i18n.T("settings.body.timecode.firstPort"), f.MTC.Device), f.MTC.Device) +
		toggleRowTip(i18n.T("settings.body.timecode.artnetOn"), "set:tc-art-on", f.ArtNet.On, tipTopic("tc-artnet")) +
		field(i18n.T("settings.body.timecode.artnetTarget"), "set:tc-art-addr", f.ArtNet.Addr, "text") +
		btnRow(btn(i18n.T("settings.body.timecode.extraLtc", i18n.A{"count": fmt.Sprint(len(f.LTCExtra))}), "outline", "settings-tcextra:ltc", ""),
			btn(i18n.T("settings.body.timecode.extraMtc", i18n.A{"count": fmt.Sprint(len(f.MTCExtra))}), "outline", "settings-tcextra:mtc", ""),
			btn(i18n.T("settings.body.timecode.extraArtnet", i18n.A{"count": fmt.Sprint(len(f.ArtNetExtra))}), "outline", "settings-tcextra:art", ""),
			btn(i18n.T("common.refresh"), "ghost", "settings-refresh", "")) +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.timecode.note")) + `</div>`
	return body
}

func (u *UI) twitchBody() string {
	if u.svc.Twitch == nil {
		return `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.twitch.unavailable")) + `</div>`
	}
	signed := u.svc.Twitch.SignedIn()
	line := i18n.T("settings.body.twitch.signInLine")
	row := btn(i18n.T("settings.body.twitch.signIn"), "primary", "settings-twitch-signin", "")
	if signed {
		if lg := u.svc.Twitch.Self().Login; lg != "" {
			line = i18n.T("settings.body.twitch.signedInAs", i18n.A{"name": lg})
		} else {
			line = i18n.T("settings.body.twitch.connecting")
		}
		row = btn(i18n.T("common.signOut"), "destructive", "settings-twitch-signout", "")
	}
	return `<div class=set-note>` + html.EscapeString(line) + `</div>` +
		btnRow(row, btn(i18n.T("settings.body.twitch.titlePresets", i18n.A{"count": fmt.Sprint(len(u.svc.Cfg.Features.Twitch.Presets))}), "outline", "settings-twpreset", ""))
}

func (u *UI) sttBody() string {
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
	return selectBox(i18n.T("settings.body.stt.microphone"), "set:stt-input", devOpts(mics, i18n.T("settings.body.common.systemDefault"), f.InputDevice), f.InputDevice) +
		field(i18n.T("settings.body.stt.output"), "set:stt-output", f.OutputDevice, "text") +
		selectBox(i18n.T("settings.body.stt.model"), "set:stt-model", modelOpts, stt.ResolvedModel(f.Model).File) +
		toggleRow(i18n.T("settings.body.stt.autoSubmit"), "set:stt-autosubmit", f.AutoSubmit) +
		field(i18n.T("settings.body.stt.silenceTimeout"), "set:stt-silence", strconv.Itoa(f.ResolvedSilenceMs()), "number") +
		`<div class=set-note>` + html.EscapeString(installLine) + `</div>` +
		btnRow(btn(i18n.T("settings.body.stt.refreshMics"), "ghost", "settings-refresh", ""), btn(i18n.T("settings.body.stt.installWhisper"), "primary", "settings-stt-install", ""))
}

func (u *UI) vrchatBody() string {
	if u.svc.Vrchat == nil {
		return `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.vrchat.unavailable")) + `</div>`
	}
	f := &u.svc.Cfg.Features.VRChat
	s := u.svc.Vrchat.State()
	var body string
	switch {
	case s.LoggedIn:
		body = `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.vrchat.linkedAs", i18n.A{"name": s.DisplayName})) + `</div>` +
			btnRow(btn(i18n.T("settings.body.common.unlink"), "destructive", "settings-vrc-unlink", ""))
	case s.Awaiting2FA:
		body = `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.vrchat.twoFactorRequired", i18n.A{"methods": strings.Join(s.Methods, ", ")})) + `</div>` +
			`<form class=set-dlgform data-act=settings-vrc-2fa>` +
			`<input class=field-input name=code placeholder=` + attrQ(i18n.T("settings.body.vrchat.twoFactorCodePlaceholder")) + `autocomplete=off>` +
			btn(i18n.T("settings.body.vrchat.verify"), "primary", "", "") + `</form>`
	default:
		msg := i18n.T("settings.body.vrchat.credsMsg")
		if s.Message != "" {
			msg += " (" + s.Message + ")"
		}
		body = `<div class=set-note>` + html.EscapeString(msg) + `</div>` +
			`<form class=set-dlgform data-act=settings-vrc-login>` +
			`<input class=field-input name=user placeholder=` + attrQ(i18n.T("settings.body.vrchat.usernamePlaceholder")) + `autocomplete=off>` +
			`<input class=field-input type=password name=pass placeholder=` + attrQ(i18n.T("settings.body.vrchat.passwordPlaceholder")) + `autocomplete=off>` +
			btn(i18n.T("common.signIn"), "primary", "", "") + `</form>`
	}
	return body +
		toggleRow(i18n.T("settings.body.vrchat.rememberSession"), "set:vrc-remember", f.RememberSession) +
		toggleRow(i18n.T("settings.body.vrchat.uplink"), "set:vrc-uplink", f.Uplink)
}

func (u *UI) vrcToolsBody() string {
	f := &u.svc.Cfg.Features.VRCTools
	presetOpts := [][2]string{{"", i18n.T("settings.body.vrctools.noneOption")}}
	for _, p := range f.AllCamPresets() {
		presetOpts = append(presetOpts, [2]string{p.Name, p.Name})
	}
	return toggleRow(i18n.T("settings.body.vrctools.orgPhotos"), "set:vct-orgphotos", f.OrganizePhotos) +
		toggleRow(i18n.T("settings.body.vrctools.preferEvent"), "set:vct-byevent", f.OrganizeByEvent) +
		toggleRow(i18n.T("settings.body.vrctools.photoMove"), "set:vct-photomove", f.PhotoMove) +
		toggleRow(i18n.T("settings.body.vrctools.orgCamPaths"), "set:vct-orgcampaths", f.OrganizeCamPaths) +
		toggleRow(i18n.T("settings.body.vrctools.camPathMove"), "set:vct-campathmove", f.CamPathMove) +
		toggleRow(i18n.T("settings.body.vrctools.autoBackup"), "set:vct-autobackup", f.AutoBackupCamPaths) +
		toggleRow(i18n.T("settings.body.vrctools.autoRestore"), "set:vct-autorestore", f.AutoRestoreCamPaths) +
		field(i18n.T("settings.body.vrctools.oscTarget"), "set:vct-osc", f.OSCAddr, "text") +
		selectBox(i18n.T("settings.body.vrctools.camPreset"), "set:vct-preset", presetOpts, f.DefaultCamPreset) +
		btnRow(btn(i18n.T("settings.body.vrctools.organizeNow"), "outline", "settings-vct-organize", ""), btn(i18n.T("settings.body.vrctools.applyPresetNow"), "outline", "settings-vct-applypreset", ""), btn(i18n.T("settings.body.vrctools.installDjPaths"), "primary", "settings-vct-djpaths", ""))
}

func (u *UI) worldSyncBody() string {
	gh := u.svc.GitHub
	if gh == nil {
		return `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.worldsync.unavailable")) + `</div>`
	}
	line := i18n.T("settings.body.worldsync.linkLine")
	row := btnRow(btn(i18n.T("settings.body.worldsync.linkDeviceCode"), "primary", "settings-gh-device", ""), btn(i18n.T("settings.body.worldsync.pasteToken"), "outline", "settings-gh-pat", ""))
	if gh.SignedIn() {
		line = i18n.T("settings.body.worldsync.linkedAs", i18n.A{"name": gh.Login()})
		row = btnRow(btn(i18n.T("settings.body.common.unlink"), "destructive", "settings-gh-unlink", ""))
	}
	return `<div class=set-note>` + html.EscapeString(line) + `</div>` + row
}

func (u *UI) vrOverlayBody() string {
	f := &u.svc.Cfg.Features.VROverlay
	return selectBox(i18n.T("settings.body.vroverlay.editBadge"), "set:vr-edithand", [][2]string{{"left", "left"}, {"right", "right"}}, f.ResolvedEditHand()) +
		selectBox(i18n.T("settings.body.vroverlay.openEditor"), "set:vr-summon", [][2]string{{"ax", i18n.T("settings.body.vroverlay.axButton")}, {"by", i18n.T("settings.body.vroverlay.byButton")}, {"custom", i18n.T("settings.body.vroverlay.customSteamvr")}}, f.ResolvedSummonButton()) +
		toggleRow(i18n.T("settings.body.vroverlay.tapHides"), "set:vr-taphides", f.SummonTapHides) +
		toggleRow(i18n.T("settings.body.vroverlay.autostart"), "set:vr-autostart", f.AutoStart) +
		toggleRow(i18n.T("settings.body.vroverlay.viewCapture"), "set:vr-viewcapture", f.VRViewCapture) +
		field(i18n.T("settings.body.vroverlay.vrchatOsc"), "set:vr-osc", f.OSCAddr, "text") +
		field(i18n.T("settings.body.vroverlay.vmc"), "set:vr-vmc", f.VMCAddr, "text") +
		toggleRow(i18n.T("settings.body.vroverlay.vmcLive"), "set:vr-vmclive", f.VMCLive) +
		btnRow(btn(i18n.T("settings.body.vroverlay.overlaysCount", i18n.A{"count": fmt.Sprint(len(f.Overlays))}), "outline", "settings-vrov", ""),
			btn(i18n.T("settings.body.vroverlay.openBindings"), "outline", "settings-vr-bindings", "")) +
		btnRow(btn(i18n.T("settings.body.vroverlay.keybindsCount", i18n.A{"count": fmt.Sprint(len(f.Binds))}), "outline", "settings-vr-keybinds", ""),
			btn(i18n.T("settings.body.vroverlay.layoutsCount", i18n.A{"count": fmt.Sprint(len(f.Layouts))}), "outline", "settings-vr-layouts", ""),
			btn(i18n.T("settings.body.vroverlay.wristCount", i18n.A{"count": fmt.Sprint(len(f.QuickButtons))}), "outline", "settings-vr-wrist", ""),
			btn(i18n.T("settings.body.vroverlay.worldLayoutsCount", i18n.A{"count": fmt.Sprint(len(f.WorldLayouts))}), "outline", "settings-vr-worldlay", "")) +
		u.vrInstallHTML()
}

func (u *UI) dmxBody() string {
	f := &u.svc.Cfg.Features.DMX
	return field(i18n.T("settings.body.common.listenAddr"), "set:dmx-listen", f.ListenAddr, "text") +
		field(i18n.T("settings.body.dmx.universes"), "set:dmx-universes", intsToCSVWeb(f.Universes), "text") +
		toggleRowTip(i18n.T("settings.body.dmx.renderGrid"), "set:dmx-grid", f.Grid.Enabled, tipTopic("dmx-vrsl")) +
		selectBox(i18n.T("settings.body.dmx.gridMode"), "set:dmx-mode", [][2]string{{"mono", "mono"}, {"rgb9", "rgb9"}}, or(f.Grid.Mode, "mono")) +
		field(i18n.T("settings.body.dmx.senderName"), "set:dmx-spout", f.Grid.SpoutName, "text") +
		field(i18n.T("settings.body.dmx.maxFps"), "set:dmx-fpscap", strconv.Itoa(f.Grid.ResolvedFPSCap()), "number") +
		toggleRowTip(i18n.T("settings.body.dmx.reemit"), "set:dmx-reemit", f.ReEmit, tipTopic("dmx-reemit")) +
		field(i18n.T("settings.body.dmx.reemitTarget"), "set:dmx-emittarget", f.EmitTarget, "text")
}

func (u *UI) dmxMidiBody() string {
	f := &u.svc.Cfg.Features.DMXMIDI
	return field(i18n.T("settings.body.dmxmidi.port"), "set:dmxmidi-device", f.Device, "text") +
		field(i18n.T("settings.body.dmxmidi.universes"), "set:dmxmidi-universes", intsToCSVWeb(f.Universes), "text") +
		field(i18n.T("settings.body.dmxmidi.maxRate"), "set:dmxmidi-rate", strconv.Itoa(f.ResolvedRate()), "number")
}

func (u *UI) rtspBody() string {
	f := &u.svc.Cfg.Features.RTSPServe
	return field(i18n.T("settings.body.rtsp.videoSource"), "set:rtsp-source", f.Source, "text") +
		field(i18n.T("settings.body.rtsp.inputFormat"), "set:rtsp-format", f.InputFormat, "text") +
		toggleRowTip(i18n.T("settings.body.rtsp.passthrough"), "set:rtsp-passthrough", f.Passthrough, tipTopic("rtsp-passthrough")) +
		field(i18n.T("settings.body.common.listenAddr"), "set:rtsp-listen", f.ListenAddr, "text") +
		field(i18n.T("settings.body.rtsp.streamPath"), "set:rtsp-path", f.Path, "text") +
		field(i18n.T("settings.body.common.frameRate"), "set:rtsp-fps", strconv.Itoa(f.ResolvedFPS()), "number") +
		field(i18n.T("settings.body.rtsp.bitrate"), "set:rtsp-bitrate", strconv.Itoa(f.ResolvedBitrate()), "number") +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.rtsp.note")) + ` rtspt://&lt;this machine's IP&gt;` + html.EscapeString(f.ResolvedListenAddr()) + html.EscapeString(f.ResolvedPath()) + `</div>`
}

func (u *UI) unityBody() string {
	f := &u.svc.Cfg.Features.Unity
	var rows strings.Builder
	if len(f.Projects) == 0 {
		rows.WriteString(emptyState(i18n.T("settings.body.unity.empty")))
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
		rows.WriteString(itemRow(name, sub,
			btn(instLabel, "outline", "settings-unity-install:"+strconv.Itoa(i), ""),
			btn(i18n.T("settings.body.common.remove"), "ghost", "settings-unity-remove:"+strconv.Itoa(i), "")))
	}
	return rows.String() +
		btnRow(btn(i18n.T("settings.body.unity.addFromVcc"), "outline", "settings-unity-vcc", ""),
			btn(i18n.T("settings.body.unity.addFolder"), "outline", "pick-dir:settings-unity-addpath", ""),
			btn(i18n.T("settings.body.unity.pastePath"), "ghost", "settings-unity-add", "")) +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.unity.note")) + `</div>`
}

// updatesBody renders the version line + channel, and (on a stamped build with a feed baked in)
// a "Check for updates" control wired to the shared selfupdate flow. A true dev build (empty
// FeedURL) keeps the manual-updates note. #inst-update is the progress/result region the check +
// apply handlers patch (same pattern as toolInstallHTML's #inst-<key>).
func (u *UI) updatesBody() string {
	head := kv(i18n.T("settings.body.updates.version"), version.String()) +
		kv(i18n.T("settings.body.updates.channel"), version.ResolvedChannel())
	if version.FeedURL == "" {
		return head + `<div class=set-note>` + html.EscapeString(i18n.T("settings.body.updates.feed")) + `</div>`
	}
	return head +
		btnRow(btn(i18n.T("settings.body.updates.check"), "primary", "settings-update-check", "")) +
		`<div id=inst-update></div>` +
		`<div class=set-note>` + html.EscapeString(i18n.T("settings.body.updates.note")) + `</div>`
}

// ── install / progress blocks ──

// toolInstallHTML renders a media-tool detect + download UI (progress patched into #inst-<key>).
func (u *UI) toolInstallHTML(t mediatools.Tool, key string) string {
	st := u.toolStatusCached(key) // cached - never probe PATH on the render goroutine
	line, label := i18n.T("settings.body.install.notFound"), i18n.T("settings.body.install.download")
	switch {
	case st.Installed && st.Managed:
		line, label = i18n.T("settings.body.install.installedManaged", i18n.A{"path": st.Path}), i18n.T("settings.body.install.reDownload")
	case st.Installed:
		line, label = i18n.T("settings.body.install.foundOnPath", i18n.A{"path": st.Path}), i18n.T("settings.body.install.downloadManaged")
	}
	installBtn := btn(label, "primary", "settings-install:"+key, "")
	if !mediatools.CanInstall() {
		installBtn = btn(i18n.T("settings.body.install.windowsOnly", i18n.A{"label": label}), "ghost", "open-url", t.HomePage)
	}
	return `<div class=set-install><div class=set-note>` + html.EscapeString(line) + `</div>` +
		btnRow(installBtn, btn(i18n.T("settings.body.install.downloadPage", i18n.A{"name": t.Display}), "ghost", "open-url", t.HomePage)) +
		`<div id=inst-` + key + `></div></div>`
}

func (u *UI) vrInstallHTML() string {
	if !vroverlay.BuiltWithVR() {
		return `<div class=set-install><div class=set-note>` + html.EscapeString(i18n.T("settings.body.vrinstall.nonVrBuild")) + `</div></div>`
	}
	st := u.vrStatusCached() // cached - never probe the DLL fs on the render goroutine
	line, label := i18n.T("settings.body.vrinstall.notFound"), i18n.T("settings.body.vrinstall.installRuntime")
	if st.Installed {
		line, label = i18n.T("settings.body.vrinstall.installed", i18n.A{"path": st.Path}), i18n.T("settings.body.install.reDownload")
	}
	installBtn := btn(label, "primary", "settings-install-vr", "")
	if !vrdll.CanInstall() {
		installBtn = btn(i18n.T("settings.body.install.windowsOnly", i18n.A{"label": label}), "ghost", "open-url", vrdll.HomePage)
	}
	return `<div class=set-install><div class=set-note>` + html.EscapeString(line) + `</div>` +
		btnRow(installBtn, btn(i18n.T("settings.body.vrinstall.downloadPage"), "ghost", "open-url", vrdll.HomePage)) +
		`<div id=inst-vr></div></div>`
}

// ── probe cache (keep slow fs/PATH probes off the render goroutine) ──
//
// mediatools.Tool.Status() (os.Stat + exec.LookPath - a PATH scan) and vrdll.Probe() (DLL fs
// probe) are the only blocking calls the Settings render used to make on the UI thread. Called
// per-tool in both the status map AND the install-card bodies, a long PATH / slow mount made
// tab-open lag seconds. Cache the results + refresh off-thread on the ~1 Hz settings tick; the
// render + status map read cache only (never touch the filesystem). Service Status() calls
// (OBS/Stream/AudioRec/RTSP/DMX proxies) are in-memory mirrors, so they stay live.

const probeTTL = 10 * time.Second // fs/PATH state rarely changes; re-probe at most this often

// settingsProbes caches the slow media-tool + VR-DLL probes and the device enumerations
// (MIDI / waveOut / STT mics / capture devices / Unity project inspects). Device enumeration
// hits OS APIs (winmm, WASAPI) and the filesystem - synchronous calls in card bodies froze the
// Settings tab open for seconds.
type settingsProbes struct {
	mu    sync.Mutex
	tools map[string]mediatools.Status // key ("ffmpeg"|"fpcalc"|"mpv") → last status
	vr    vrdll.Status
	devs  map[string][]string          // kind ("midi"|"waveout"|"midiout"|"sttmic"|"audiorec") → names
	unity map[string]unityproj.Project // project dir → inspect result
	at    time.Time
	ready bool
	busy  bool // a background refresh is in flight (prevents stacking on the 1 Hz tick)
}

// toolStatusCached returns the last cached status for a media tool (zero/uninstalled until the
// first background probe lands). Never touches the filesystem - safe on the render goroutine.
func (u *UI) toolStatusCached(key string) mediatools.Status {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.tools[key]
}

// vrStatusCached returns the last cached vrdll probe (non-blocking).
func (u *UI) vrStatusCached() vrdll.Status {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.vr
}

// devNamesCached returns the last cached device enumeration for kind (empty until the first
// background probe lands). Never hits OS device APIs - safe on the render goroutine.
func (u *UI) devNamesCached(kind string) []string {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.devs[kind]
}

// unityInfoCached returns the last cached inspect for a Unity project dir.
func (u *UI) unityInfoCached(dir string) unityproj.Project {
	u.probes.mu.Lock()
	defer u.probes.mu.Unlock()
	return u.probes.unity[dir]
}

// invalidateProbes forces the next maybeRefreshProbes to re-probe now (Refresh buttons).
func (u *UI) invalidateProbes() {
	u.probes.mu.Lock()
	u.probes.at = time.Time{}
	u.probes.mu.Unlock()
}

// refreshProbes recomputes the slow fs/PATH probes and caches them. MUST run off the UI goroutine
// (called via u.bg). Cheap enough for the tick when stale.
func (u *UI) refreshProbes() {
	tools := map[string]mediatools.Status{
		"ffmpeg": mediatools.FFmpeg.Status(),
		"fpcalc": mediatools.Fpcalc.Status(),
		"mpv":    mediatools.MPV.Status(),
	}
	var vr vrdll.Status
	if vroverlay.BuiltWithVR() {
		vr = vrdll.Probe()
	}
	devs := map[string][]string{
		"midi":    mustNames(midi.Ports),
		"waveout": mustNames(timecode.WaveOutDevices),
		"midiout": mustNames(timecode.MidiOutDevices),
		"sttmic":  mustNames(stt.InputDevices),
	}
	if u.svc.AudioRec != nil {
		devs["audiorec"] = mustNames(u.svc.AudioRec.Devices)
	}
	unity := map[string]unityproj.Project{}
	if u.svc.Cfg != nil {
		for _, dir := range u.svc.Cfg.Features.Unity.Projects {
			unity[dir] = unityproj.Inspect(dir)
		}
	}
	u.probes.mu.Lock()
	// The install-card bodies (toolInstallHTML/vrInstallHTML) only re-render on a full patchMain,
	// not the 1 Hz status tick - so patch once when the probe first lands or its install-state
	// changes, to flip the card from the placeholder "not installed" to the real state.
	changed := !u.probes.ready || vr.Installed != u.probes.vr.Installed ||
		toolInstallChanged(u.probes.tools, tools) || devListsChanged(u.probes.devs, devs)
	u.probes.tools = tools
	u.probes.vr = vr
	u.probes.devs = devs
	u.probes.unity = unity
	u.probes.at = time.Now()
	u.probes.ready = true
	u.probes.busy = false
	u.probes.mu.Unlock()
	if changed && u.activeTab() == "settings" {
		u.patchMain()
	}
}

// devListsChanged reports whether any device enumeration differs between snapshots.
func devListsChanged(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return true
	}
	for k, bv := range b {
		av := a[k]
		if len(av) != len(bv) {
			return true
		}
		for i := range bv {
			if av[i] != bv[i] {
				return true
			}
		}
	}
	return false
}

// toolInstallChanged reports whether any tool's install-state (installed/path) differs between two
// probe snapshots - used to decide if the Settings body needs a one-shot re-render.
func toolInstallChanged(a, b map[string]mediatools.Status) bool {
	if len(a) != len(b) {
		return true
	}
	for k, bv := range b {
		av := a[k]
		if av.Installed != bv.Installed || av.Path != bv.Path {
			return true
		}
	}
	return false
}

// maybeRefreshProbes kicks a background probe refresh when the cache is stale (or never filled),
// at most one in flight. Non-blocking - safe to call from the render path or the tick.
func (u *UI) maybeRefreshProbes() {
	u.probes.mu.Lock()
	stale := !u.probes.ready || time.Since(u.probes.at) > probeTTL
	if !stale || u.probes.busy {
		u.probes.mu.Unlock()
		return
	}
	u.probes.busy = true
	u.probes.mu.Unlock()
	u.bg(u.refreshProbes)
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
	save := true
	switch id {
	case "peer-nick":
		f.Peers.Nickname = v
	// Traktor
	case "traktor-port":
		toInt(&f.Traktor.Port, 1, 65535)
	case "traktor-log":
		f.Traktor.LogPayloads = b
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
	// key hold (not persisted here - Save button reads it via form)
	case "rb-key-hold":
		save = false
	default:
		save = false
	}
	if save {
		u.saveCfgBG("set:"+id, nil, nil) // disk write + Reconcile off the actWorker
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
		_ = u.svc.Cfg.Save()
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
// precedent). Package-level (the UI struct lives in ui.go); one webview UI per process.
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

func mustNames(fn func() ([]string, error)) []string {
	n, _ := fn()
	return n
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
	for _, p := range strings.Split(s, ",") {
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
