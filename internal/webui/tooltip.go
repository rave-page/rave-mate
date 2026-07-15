package webui

// Tooltip primitive - long-form, educational hover/click help. Design rule (owner,
// 2026-07-05): the app teaches while it's used; rich explanations of technical terms
// live in tooltips (never trimmed), ideally with links to authoritative sources.
//
// Markup is a pure-CSS "checkbox pin": hover/focus previews the card, clicking the
// ⓘ pins it open (touch-friendly), clicking again unpins. The hidden checkbox also
// makes tooltips ctl-drivable - `ctl set tt-<id> true` pins a card for screenshots
// (data-label carries the id). Links open in the OS browser via the open-url act.
//
// Shown cards portal into the body-level #__ttlayer (shell.go) so ancestor overflow
// panels / stacking contexts can't clip them; hidden cards return to the trigger.
//
// Avoid anchoring inside a live-ticked region (live_ticks.go): a PINNED card survives
// the 1 Hz innerHTML patch (the layer sweep re-pins the replacement trigger by
// data-label), but an unpinned hover preview closes on every tick. Prefer the static
// wrapper (e.g. section titles around live-net/live-tim).

import (
	"html"
	"strings"

	"rave.page/mate/internal/i18n"
)

// ttLink is one authoritative-source reference at the card's foot.
type ttLink struct {
	Label string
	URL   string
}

// kbRow is one keybind→action row of a topic's key grid (rendered atop the card).
// Combo entries are key-cap chips; "+" and "/" render as separators, an "@<i18n key>"
// entry resolves at render time (localized mouse-gesture words like "Right-click"),
// anything else is a literal, locale-independent key label (←, Shift, Del, T).
type kbRow struct {
	Group string // i18n key of the section header, shown when it changes ("" = same section)
	Combo []string
	Act   string // i18n key of the action description (resolved at render)
}

// helpTopic is a reusable explanation of one technical term/feature.
type helpTopic struct {
	Title string
	// Body paragraphs, split on "\n\n". Plain text (escaped at render).
	Body  string
	Keys  []kbRow // optional keybind grid, shown between title and body
	Links []ttLink
}

// helpTopics - the shared glossary. One entry per term; every surface that mentions
// the term points here so the explanation stays consistent. Write for a newcomer,
// stay precise for the expert.
var helpTopics = map[string]helpTopic{
	"network-graph": {
		Title: i18n.T("help.network-graph.title"),
		Body:  i18n.T("help.network-graph.body"),
	},
	"timing-graph": {
		Title: i18n.T("help.timing-graph.title"),
		Body:  i18n.T("help.timing-graph.body"),
		Links: []ttLink{{"RTT explained (Wikipedia)", "https://en.wikipedia.org/wiki/Round-trip_delay"}},
	},
	"perf-graph": {
		Title: i18n.T("help.perf-graph.title"),
		Body:  i18n.T("help.perf-graph.body"),
	},
	"icecast": {
		Title: i18n.T("help.icecast.title"),
		Body:  i18n.T("help.icecast.body"),
		Links: []ttLink{
			{"Icecast project", "https://icecast.org/"},
			{"Traktor broadcasting manual", "https://support.native-instruments.com/hc/en-us/articles/209590569"},
		},
	},
	"session-recorder": {
		Title: i18n.T("help.session-recorder.title"),
		Body:  i18n.T("help.session-recorder.body"),
	},
	"remote-cache": {
		Title: i18n.T("help.remote-cache.title"),
		Body:  i18n.T("help.remote-cache.body"),
	},
	"fingerprinting": {
		Title: i18n.T("help.fingerprinting.title"),
		Body:  i18n.T("help.fingerprinting.body"),
		Links: []ttLink{
			{"AcoustID / Chromaprint", "https://acoustid.org/chromaprint"},
		},
	},
	"led-feedback": {
		Title: i18n.T("help.led-feedback.title"),
		Body:  i18n.T("help.led-feedback.body"),
		Links: []ttLink{
			{"MIDI 1.0 message summary (midi.org)", "https://midi.org/summary-of-midi-1-0-messages"},
		},
	},
	"vrchat-announcement": {
		Title: i18n.T("help.vrchat-announcement.title"),
		Body:  i18n.T("help.vrchat-announcement.body"),
		Links: []ttLink{
			{"VRChat groups docs", "https://creators.vrchat.com/groups/"},
		},
	},
	"camera-paths": {
		Title: i18n.T("help.camera-paths.title"),
		Body:  i18n.T("help.camera-paths.body"),
		Links: []ttLink{
			{"VRChat camera docs", "https://docs.vrchat.com/docs/vrchat-camera"},
			{"VRChat OSC overview", "https://docs.vrchat.com/docs/osc-overview"},
		},
	},
	"motion-studio": {
		Title: i18n.T("help.motion-studio.title"),
		Body:  i18n.T("help.motion-studio.body"),
		Links: []ttLink{
			{"VMC protocol", "https://protocol.vmc.info/english"},
			{"VRChat OSC trackers", "https://docs.vrchat.com/docs/osc-trackers"},
		},
	},
	"embedded-video": {
		Title: i18n.T("help.embedded-video.title"),
		Body:  i18n.T("help.embedded-video.body"),
		Links: []ttLink{
			{"HTTP Range requests (MDN)", "https://developer.mozilla.org/en-US/docs/Web/HTTP/Range_requests"},
		},
	},
	"wave-nav": {
		Title: i18n.T("help.wave-nav.title"),
		Body:  i18n.T("help.wave-nav.body"),
		Keys:  waveNavKeys,
	},
	"cue-edit": {
		Title: i18n.T("help.cue-edit.title"),
		Body:  i18n.T("help.cue-edit.body"),
		Keys:  cueEditKeys,
	},
	"midi-mapping": {
		Title: i18n.T("help.midi-mapping.title"),
		Body:  i18n.T("help.midi-mapping.body"),
		Links: []ttLink{
			{"MIDI 1.0 CC + relative encoder conventions (midi.org)", "https://midi.org/midi-1-0-control-change-messages"},
		},
	},
	"trim-editor": {
		Title: i18n.T("help.trim-editor.title"),
		Body:  i18n.T("help.trim-editor.body"),
		Links: []ttLink{
			{"ffmpeg silencedetect", "https://ffmpeg.org/ffmpeg-filters.html#silencedetect"},
		},
	},
	"dual-alignment": {
		Title: i18n.T("help.dual-alignment.title"),
		Body:  i18n.T("help.dual-alignment.body"),
		Links: []ttLink{
			{"Cross-correlation (Wikipedia)", "https://en.wikipedia.org/wiki/Cross-correlation"},
		},
	},
	"vrchat-presence": {
		Title: i18n.T("help.vrchat-presence.title"),
		Body:  i18n.T("help.vrchat-presence.body"),
		Links: []ttLink{
			{"VRChat status docs", "https://docs.vrchat.com/docs/vrchat-safety-and-trust-system"},
		},
	},
	// ── Transcode / encoding builder (Library) ──
	"enc-container": {
		Title: i18n.T("help.enc-container.title"),
		Body:  i18n.T("help.enc-container.body"),
		Links: []ttLink{
			{"FFmpeg formats/muxers", "https://ffmpeg.org/ffmpeg-formats.html"},
		},
	},
	"enc-video-codec": {
		Title: i18n.T("help.enc-video-codec.title"),
		Body:  i18n.T("help.enc-video-codec.body"),
		Links: []ttLink{
			{"H.264 / AVC (Wikipedia)", "https://en.wikipedia.org/wiki/Advanced_Video_Coding"},
			{"H.265 / HEVC (Wikipedia)", "https://en.wikipedia.org/wiki/High_Efficiency_Video_Coding"},
			{"AV1 (AOMedia)", "https://aomedia.org/av1/"},
		},
	},
	"enc-rate": {
		Title: i18n.T("help.enc-rate.title"),
		Body:  i18n.T("help.enc-rate.body"),
		Links: []ttLink{
			{"FFmpeg CRF guide", "https://trac.ffmpeg.org/wiki/Encode/H.264#crf"},
		},
	},
	"enc-audio-codec": {
		Title: i18n.T("help.enc-audio-codec.title"),
		Body:  i18n.T("help.enc-audio-codec.body"),
		Links: []ttLink{
			{"Opus codec", "https://opus-codec.org/"},
			{"AAC (Wikipedia)", "https://en.wikipedia.org/wiki/Advanced_Audio_Coding"},
		},
	},
	"enc-loudness": {
		Title: i18n.T("help.enc-loudness.title"),
		Body:  i18n.T("help.enc-loudness.body"),
		Links: []ttLink{
			{"EBU R128 loudness", "https://tech.ebu.ch/publications/r128"},
			{"ITU-R BS.1770 (true peak)", "https://www.itu.int/rec/R-REC-BS.1770"},
		},
	},
	// ── Settings: Timecode card (SMPTE / LTC / MTC / Art-Net) ──
	"tc-timecode": {
		Title: i18n.T("help.tc-timecode.title"),
		Body:  i18n.T("help.tc-timecode.body"),
		Links: []ttLink{{"SMPTE timecode (Wikipedia)", "https://en.wikipedia.org/wiki/SMPTE_timecode"}},
	},
	"tc-rate": {
		Title: i18n.T("help.tc-rate.title"),
		Body:  i18n.T("help.tc-rate.body"),
	},
	"tc-start": {
		Title: i18n.T("help.tc-start.title"),
		Body:  i18n.T("help.tc-start.body"),
	},
	"tc-ltc": {
		Title: i18n.T("help.tc-ltc.title"),
		Body:  i18n.T("help.tc-ltc.body"),
		Links: []ttLink{{"LTC (Wikipedia)", "https://en.wikipedia.org/wiki/Linear_timecode"}},
	},
	"tc-ltc-level": {
		Title: i18n.T("help.tc-ltc-level.title"),
		Body:  i18n.T("help.tc-ltc-level.body"),
	},
	"tc-mtc": {
		Title: i18n.T("help.tc-mtc.title"),
		Body:  i18n.T("help.tc-mtc.body"),
		Links: []ttLink{{"MIDI Time Code (Wikipedia)", "https://en.wikipedia.org/wiki/MIDI_timecode"}},
	},
	"tc-artnet": {
		Title: i18n.T("help.tc-artnet.title"),
		Body:  i18n.T("help.tc-artnet.body"),
		Links: []ttLink{{"Art-Net (Wikipedia)", "https://en.wikipedia.org/wiki/Art-Net"}},
	},
	// ── Settings: OBS media-sync card ──
	"obssync-mediasync": {
		Title: i18n.T("help.obssync-mediasync.title"),
		Body:  i18n.T("help.obssync-mediasync.body"),
	},
	"obssync-fps": {
		Title: i18n.T("help.obssync-fps.title"),
		Body:  i18n.T("help.obssync-fps.body"),
	},
	"obssync-deadband": {
		Title: i18n.T("help.obssync-deadband.title"),
		Body:  i18n.T("help.obssync-deadband.body"),
	},
	"obssync-restart": {
		Title: i18n.T("help.obssync-restart.title"),
		Body:  i18n.T("help.obssync-restart.body"),
	},
	// ── Settings: DMX / VRSL card ──
	"account-bridge": {
		Title: i18n.T("help.account-bridge.title"),
		Body:  i18n.T("help.account-bridge.body"),
		Links: []ttLink{
			{"Time-Based One-Time Password (RFC 6238)", "https://datatracker.ietf.org/doc/html/rfc6238"},
			{"End-to-end encryption (Wikipedia)", "https://en.wikipedia.org/wiki/End-to-end_encryption"},
			{"Authenticated key exchange (Wikipedia)", "https://en.wikipedia.org/wiki/Authenticated_Key_Exchange"},
		},
	},
	"bridge-local-studio": {
		Title: i18n.T("help.bridge-local-studio.title"),
		Body:  i18n.T("help.bridge-local-studio.body"),
		Links: []ttLink{
			{"AES-GCM (Wikipedia)", "https://en.wikipedia.org/wiki/Galois/Counter_Mode"},
		},
	},
	"dmx-connect": {
		Title: i18n.T("help.dmx-connect.title"),
		Body:  i18n.T("help.dmx-connect.body"),
		Links: []ttLink{
			{"DMX512 (Wikipedia)", "https://en.wikipedia.org/wiki/DMX512"},
			{"Art-Net (Wikipedia)", "https://en.wikipedia.org/wiki/Art-Net"},
		},
	},
	"dmx-vrsl": {
		Title: i18n.T("help.dmx-vrsl.title"),
		Body:  i18n.T("help.dmx-vrsl.body"),
		Links: []ttLink{{"VR Stage Lighting (GitHub)", "https://github.com/AcChosen/VR-Stage-Lighting"}},
	},
	"dmx-reemit": {
		Title: i18n.T("help.dmx-reemit.title"),
		Body:  i18n.T("help.dmx-reemit.body"),
	},
	// ── Settings: RTSP card ──
	"rtsp-why": {
		Title: i18n.T("help.rtsp-why.title"),
		Body:  i18n.T("help.rtsp-why.body"),
		Links: []ttLink{{"RTSP (Wikipedia)", "https://en.wikipedia.org/wiki/Real_Time_Streaming_Protocol"}},
	},
	"rtsp-passthrough": {
		Title: i18n.T("help.rtsp-passthrough.title"),
		Body:  i18n.T("help.rtsp-passthrough.body"),
	},
	"stream-why": {
		Title: i18n.T("help.stream-why.title"),
		Body:  i18n.T("help.stream-why.body"),
		Links: []ttLink{{"VRSL (VR Stage Lighting)", "https://github.com/AcChosen/VR-Stage-Lighting"}},
	},
	// ── Settings: Media-link card ──
	"ml-accel": {
		Title: i18n.T("help.ml-accel.title"),
		Body:  i18n.T("help.ml-accel.body"),
	},
	"ml-budget": {
		Title: i18n.T("help.ml-budget.title"),
		Body:  i18n.T("help.ml-budget.body"),
	},
	// ── MIDI tab: native MIDI-learn + DJ bridge ──
	"midi-learn-controllers": {
		Title: i18n.T("help.midi-learn-controllers.title"),
		Body:  i18n.T("help.midi-learn-controllers.body"),
		Links: append(virtualMIDILinks(), ttLink{"Rekordbox MIDI LEARN guide", "https://rekordbox.com/en/support/faq/mapping-6/"}),
	},
	"midi-in-port": {
		Title: i18n.T("help.midi-in-port.title"),
		Body:  i18n.T("help.midi-in-port.body"),
		Links: virtualMIDILinks(),
	},
	"midi-thru": {
		Title: i18n.T("help.midi-thru.title"),
		Body:  i18n.T("help.midi-thru.body"),
		Links: append(virtualMIDILinks(), ttLink{"MIDI-OX (router/splitter)", "http://www.midiox.com/"}),
	},
	"midi-learn-grid": {
		Title: i18n.T("help.midi-learn-grid.title"),
		Body:  i18n.T("help.midi-learn-grid.body"),
	},
	"midi-drv-filter": {
		Title: i18n.T("help.midi-drv-filter.title"),
		Body:  i18n.T("help.midi-drv-filter.body"),
	},
	"midi-bridge": {
		Title: i18n.T("help.midi-bridge.title"),
		Body:  i18n.T("help.midi-bridge.body"),
		Links: virtualMIDILinks(),
	},
	// ── Remote library (mirror banner, library_mirror.go) ──
	"remote-library": {
		Title: i18n.T("help.remote-library.title"),
		Body:  i18n.T("help.remote-library.body"),
	},
	// ── Self-update (nav-rail block + settings Updates card) ──
	"app-updates": {
		Title: i18n.T("help.app-updates.title"),
		Body:  i18n.T("help.app-updates.body"),
		Links: []ttLink{
			{"rave-mate releases (the update feed)", "https://github.com/rave-page/rave-mate/releases"},
			{"Ed25519 signatures (Wikipedia)", "https://en.wikipedia.org/wiki/EdDSA"},
			{"Authenticode code signing (Microsoft)", "https://learn.microsoft.com/en-us/windows-hardware/drivers/install/authenticode"},
		},
	},
}

// cueEditKeys - every cue-editor binding (keyboard, then mouse gestures), the grid
// atop the cue-edit tooltip. Order mirrors the workflow: navigate → mark → select →
// delete → audition → grid alignment.
var cueEditKeys = []kbRow{
	// grouped by use context; the first row of each group carries the section header
	{"help.cue-edit.g.nav", []string{"←", "/", "→"}, "help.cue-edit.k.step"},
	{"", []string{"Shift", "+", "←", "/", "→"}, "help.cue-edit.k.jump"},
	{"", []string{"Shift", "+", "↑", "/", "↓"}, "help.cue-edit.k.jumpSize"},
	{"", []string{"↑", "/", "↓"}, "help.cue-edit.k.listNav"},
	{"help.cue-edit.g.wave", []string{"@help.kb.drag"}, "help.cue-edit.k.pan"},
	{"", []string{"@help.kb.wheel"}, "help.cue-edit.k.zoom"},
	{"", []string{"@help.kb.click"}, "help.cue-edit.k.click"},
	{"help.cue-edit.g.drops", []string{"T", "/", "Enter"}, "help.cue-edit.k.addDrop"},
	{"", []string{"Shift", "+", "T", "/", "Enter"}, "help.cue-edit.k.removeDrop"},
	{"", []string{"Shift", "+", "@help.kb.rclick"}, "help.cue-edit.k.srclick"},
	{"help.cue-edit.g.cues", []string{"@help.kb.rclick"}, "help.cue-edit.k.rclick"},
	{"", []string{"Shift", "+", "Space"}, "help.cue-edit.k.addCue"},
	{"", []string{"Ctrl", "+", "@help.kb.rclick"}, "help.cue-edit.k.crclick"},
	{"help.cue-edit.g.select", []string{"Shift", "+", "@help.kb.drag"}, "help.cue-edit.k.drag"},
	{"", []string{"Ctrl", "+", "@help.kb.click"}, "help.cue-edit.k.ctrlClick"},
	{"", []string{"Del", "/", "Backspace"}, "help.cue-edit.k.deleteSel"},
	{"", []string{"Ctrl", "+", "Z"}, "help.cue-edit.k.undo"},
	{"help.cue-edit.g.grid", []string{"Ctrl", "+", "←", "/", "→"}, "help.cue-edit.k.nudge"},
	{"", []string{"Ctrl", "+", "Shift", "+", "←", "/", "→"}, "help.cue-edit.k.nudgeFine"},
	{"help.cue-edit.g.audition", []string{"Space"}, "help.cue-edit.k.audition"},
}

// waveNavKeys - the waveform pointer gestures (wave-nav tooltip grid).
var waveNavKeys = []kbRow{
	{"", []string{"@help.kb.click"}, "help.wave-nav.k.click"},
	{"", []string{"@help.kb.wheel"}, "help.wave-nav.k.wheel"},
	{"", []string{"@help.kb.drag"}, "help.wave-nav.k.drag"},
}

// virtualMIDILinks is the shared list of virtual-MIDI-port options. loopMIDI is the recommended
// default (no admin, no registry, unlimited ports, works on every Windows). LoopBe1 is a simple
// single-port option. Windows MIDI Services is listed INFORMATIONALLY only: it's the open-source
// (MIT) future stack with native multi-client (both apps read one controller directly - no
// loopback needed), and rave-mate uses it AUTOMATICALLY once Windows enables it. We deliberately
// do NOT link its enablement/SDK page or mention the winmm registry fix (midifixreg): the service
// ships in-box, but the classic-app (winmm) handoff is staged-rollout-gated - Windows reverts a
// forced Drivers32 rewiring on the next boot while the device-transfer flag stays set, a
// half-state where EVERY winmm MIDI port vanishes (verified 2026-07: winmm=0 ports, WinRT fine;
// recovery = registry restore + reboot). So: point at the info page, no call to action.
func virtualMIDILinks() []ttLink {
	return []ttLink{
		{"loopMIDI - unlimited named ports, no admin (freeware, recommended)", "https://www.tobias-erichsen.de/software/loopmidi.html"},
		{"LoopBe1 - single port (freeware)", "https://www.nerds.de/en/loopbe1.html"},
		{"Windows MIDI Services - future built-in option; used automatically once Windows ships it", "https://microsoft.github.io/MIDI/"},
	}
}

// tipTopic renders the shared tooltip for a registry topic id ("" for unknown ids -
// callers may reference topics that land later).
func tipTopic(id string) string {
	t, ok := helpTopics[id]
	if !ok {
		return ""
	}
	return renderTip(id, t.Title, t.Body, t.Keys, t.Links)
}

// tip renders an ad-hoc tooltip (id must be a unique single token - it becomes the
// ctl data-label `tt-<id>`). Prefer tipTopic + a registry entry for anything two
// surfaces could share.
func tip(id, title, body string, links ...ttLink) string {
	return renderTip(id, title, body, nil, links)
}

// kbChips renders a combo spec as key-cap chips + separators (see kbRow).
func kbChips(combo []string) string {
	var b strings.Builder
	for _, tok := range combo {
		switch {
		case tok == "+" || tok == "/":
			b.WriteString(`<span class=tt-kb-sep>` + tok + `</span>`)
		case strings.HasPrefix(tok, "@"):
			b.WriteString(`<kbd class=tt-kbd>` + html.EscapeString(i18n.T(tok[1:])) + `</kbd>`)
		default:
			b.WriteString(`<kbd class=tt-kbd>` + html.EscapeString(tok) + `</kbd>`)
		}
	}
	return b.String()
}

// kbEmph emphasises the leading keyword (the verb) of an action label - language-agnostic, so
// no per-string markup is needed in the catalogs.
func kbEmph(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i > 0 {
		return `<b class=tt-kb-verb>` + html.EscapeString(s[:i]) + `</b>` + html.EscapeString(s[i:])
	}
	return `<b class=tt-kb-verb>` + html.EscapeString(s) + `</b>`
}

func renderTip(id, title, body string, keys []kbRow, links []ttLink) string {
	var b strings.Builder
	b.WriteString(`<label class=tt data-label="tt-` + html.EscapeString(id) + `" aria-label="About: ` + html.EscapeString(title) + `" tabindex=0>`)
	b.WriteString(`<input type=checkbox class=tt-x tabindex=-1>`)
	// lucide-style info glyph; currentColor follows muted/hover/pinned states.
	b.WriteString(`<svg class=tt-ic viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` +
		`<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>`)
	// tt-card = transparent positioner (+hover bridge), portaled to #__ttlayer while
	// shown; tt-in = visual panel, scrolls internally when tall (~60vh cap). __ttplace
	// (shell.go) flips/clamps both.
	b.WriteString(`<span class=tt-card role=tooltip><span class=tt-in>`)
	b.WriteString(`<b class=tt-title>` + html.EscapeString(title) + `</b>`)
	if len(keys) > 0 { // keybind grid: section header + (combo chips → action) rows, two columns
		b.WriteString(`<span class=tt-kb>`)
		curGroup := ""
		for _, r := range keys {
			if r.Group != "" && r.Group != curGroup {
				curGroup = r.Group
				b.WriteString(`<span class=tt-kb-group>` + html.EscapeString(i18n.T(r.Group)) + `</span>`)
			}
			b.WriteString(`<span class=tt-kb-keys>` + kbChips(r.Combo) + `</span>` +
				`<span class=tt-kb-act>` + kbEmph(i18n.T(r.Act)) + `</span>`)
		}
		b.WriteString(`</span>`)
	}
	for _, p := range strings.Split(body, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			b.WriteString(`<span class=tt-p>` + html.EscapeString(p) + `</span>`)
		}
	}
	if len(links) > 0 {
		b.WriteString(`<span class=tt-links>`)
		for _, l := range links {
			b.WriteString(`<a class=tt-link data-act=open-url data-val="` + html.EscapeString(l.URL) + `">` +
				html.EscapeString(l.Label) + ` ↗</a>`)
		}
		b.WriteString(`</span>`)
	}
	b.WriteString(`</span></span></label>`)
	return b.String()
}

// sectionTip is section() with a tooltip beside the title (title still escaped).
func sectionTip(title, tipHTML, bodyHTML string) string {
	return `<section class=sec><h2 class=sec-title>` + html.EscapeString(title) + tipHTML + `</h2>` + bodyHTML + `</section>`
}
