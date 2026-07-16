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

// helpTopic is a reusable explanation of one technical term/feature. Title and body are NOT held
// here: a topic's text is `help.<id>.title` / `help.<id>.body` and is resolved by tipTopic at
// RENDER time. Holding the resolved strings froze every tooltip to the locale that was active when
// this package's vars were initialized, so a language switch never reached them.
type helpTopic struct {
	Keys  []kbRow // optional keybind grid, shown between title and body
	Links []ttLink
}

// helpTopics - the shared glossary. One entry per term; every surface that mentions
// the term points here so the explanation stays consistent. Write for a newcomer,
// stay precise for the expert. The map is the topic REGISTRY (which ids exist, plus their keybind
// grid + links); the prose lives in the locale catalogs under help.<id>.*.
var helpTopics = map[string]helpTopic{
	"network-graph": {},
	"timing-graph": {
		Links: []ttLink{{"RTT explained (Wikipedia)", "https://en.wikipedia.org/wiki/Round-trip_delay"}},
	},
	"perf-graph": {},
	"icecast": {
		Links: []ttLink{
			{"Icecast project", "https://icecast.org/"},
			{"Traktor broadcasting manual", "https://support.native-instruments.com/hc/en-us/articles/209590569"},
		},
	},
	"session-recorder": {},
	"remote-cache":     {},
	"fingerprinting": {
		Links: []ttLink{
			{"AcoustID / Chromaprint", "https://acoustid.org/chromaprint"},
		},
	},
	"led-feedback": {
		Links: []ttLink{
			{"MIDI 1.0 message summary (midi.org)", "https://midi.org/summary-of-midi-1-0-messages"},
		},
	},
	"vrchat-announcement": {
		Links: []ttLink{
			{"VRChat groups docs", "https://creators.vrchat.com/groups/"},
		},
	},
	"camera-paths": {
		Links: []ttLink{
			{"VRChat camera docs", "https://docs.vrchat.com/docs/vrchat-camera"},
			{"VRChat OSC overview", "https://docs.vrchat.com/docs/osc-overview"},
		},
	},
	"motion-studio": {
		Links: []ttLink{
			{"VMC protocol", "https://protocol.vmc.info/english"},
			{"VRChat OSC trackers", "https://docs.vrchat.com/docs/osc-trackers"},
		},
	},
	"embedded-video": {
		Links: []ttLink{
			{"HTTP Range requests (MDN)", "https://developer.mozilla.org/en-US/docs/Web/HTTP/Range_requests"},
		},
	},
	"wave-nav": {
		Keys: waveNavKeys,
	},
	"cue-edit": {
		Keys: cueEditKeys,
	},
	"midi-mapping": {
		Links: []ttLink{
			{"MIDI 1.0 CC + relative encoder conventions (midi.org)", "https://midi.org/midi-1-0-control-change-messages"},
		},
	},
	"trim-editor": {
		Links: []ttLink{
			{"ffmpeg silencedetect", "https://ffmpeg.org/ffmpeg-filters.html#silencedetect"},
		},
	},
	"dual-alignment": {
		Links: []ttLink{
			{"Cross-correlation (Wikipedia)", "https://en.wikipedia.org/wiki/Cross-correlation"},
		},
	},
	"vrchat-presence": {
		Links: []ttLink{
			{"VRChat status docs", "https://docs.vrchat.com/docs/vrchat-safety-and-trust-system"},
		},
	},
	// ── Transcode / encoding builder (Library) ──
	"enc-container": {
		Links: []ttLink{
			{"FFmpeg formats/muxers", "https://ffmpeg.org/ffmpeg-formats.html"},
		},
	},
	"enc-video-codec": {
		Links: []ttLink{
			{"H.264 / AVC (Wikipedia)", "https://en.wikipedia.org/wiki/Advanced_Video_Coding"},
			{"H.265 / HEVC (Wikipedia)", "https://en.wikipedia.org/wiki/High_Efficiency_Video_Coding"},
			{"AV1 (AOMedia)", "https://aomedia.org/av1/"},
		},
	},
	"enc-rate": {
		Links: []ttLink{
			{"FFmpeg CRF guide", "https://trac.ffmpeg.org/wiki/Encode/H.264#crf"},
		},
	},
	"enc-audio-codec": {
		Links: []ttLink{
			{"Opus codec", "https://opus-codec.org/"},
			{"AAC (Wikipedia)", "https://en.wikipedia.org/wiki/Advanced_Audio_Coding"},
		},
	},
	"enc-loudness": {
		Links: []ttLink{
			{"EBU R128 loudness", "https://tech.ebu.ch/publications/r128"},
			{"ITU-R BS.1770 (true peak)", "https://www.itu.int/rec/R-REC-BS.1770"},
		},
	},
	"mp-loudness": {
		Links: []ttLink{
			{"EBU R128 loudness", "https://tech.ebu.ch/publications/r128"},
			{"ITU-R BS.1770 (true peak)", "https://www.itu.int/rec/R-REC-BS.1770"},
		},
	},
	// ── Automations: create/edit form (automations_editor.go) ──
	"auto-watch-dir":  {},
	"auto-match-exts": {},
	"auto-match-pattern": {
		Links: []ttLink{{"Go regexp syntax (RE2)", "https://github.com/google/re2/wiki/Syntax"}},
	},
	"auto-min-age": {},
	"auto-trim-silence": {
		Links: []ttLink{{"ffmpeg silencedetect", "https://ffmpeg.org/ffmpeg-filters.html#silencedetect"}},
	},
	"auto-rename-buffer":   {},
	"auto-rename-template": {},
	"auto-loudness": {
		Links: []ttLink{
			{"EBU R128 loudness", "https://tech.ebu.ch/publications/r128"},
			{"ITU-R BS.1770 (true peak)", "https://www.itu.int/rec/R-REC-BS.1770"},
		},
	},
	"auto-delete-action": {},
	// ── Automations: schedules + run now (automations_schedules.go / automations_runnow.go) ──
	"auto-sch-kind":     {},
	"auto-sch-interval": {},
	"auto-sch-cron": {
		Links: []ttLink{
			{"crontab(5) field reference", "https://man7.org/linux/man-pages/man5/crontab.5.html"},
			{"Cron (Wikipedia)", "https://en.wikipedia.org/wiki/Cron"},
		},
	},
	"auto-sch-idle":         {},
	"auto-sch-require-idle": {},
	"auto-sch-apps":         {},
	"auto-run-now":          {},
	// ── Settings: Timecode card (SMPTE / LTC / MTC / Art-Net) ──
	"tc-timecode": {
		Links: []ttLink{{"SMPTE timecode (Wikipedia)", "https://en.wikipedia.org/wiki/SMPTE_timecode"}},
	},
	"tc-rate":  {},
	"tc-start": {},
	"tc-ltc": {
		Links: []ttLink{{"LTC (Wikipedia)", "https://en.wikipedia.org/wiki/Linear_timecode"}},
	},
	"tc-ltc-level": {},
	"tc-mtc": {
		Links: []ttLink{{"MIDI Time Code (Wikipedia)", "https://en.wikipedia.org/wiki/MIDI_timecode"}},
	},
	"tc-artnet": {
		Links: []ttLink{{"Art-Net (Wikipedia)", "https://en.wikipedia.org/wiki/Art-Net"}},
	},
	// ── Settings: OBS media-sync card ──
	"obssync-mediasync": {},
	"obssync-fps":       {},
	"obssync-deadband":  {},
	"obssync-restart":   {},
	// ── Settings: DMX / VRSL card ──
	"account-bridge": {
		Links: []ttLink{
			{"Time-Based One-Time Password (RFC 6238)", "https://datatracker.ietf.org/doc/html/rfc6238"},
			{"End-to-end encryption (Wikipedia)", "https://en.wikipedia.org/wiki/End-to-end_encryption"},
			{"Authenticated key exchange (Wikipedia)", "https://en.wikipedia.org/wiki/Authenticated_Key_Exchange"},
		},
	},
	"bridge-local-studio": {
		Links: []ttLink{
			{"AES-GCM (Wikipedia)", "https://en.wikipedia.org/wiki/Galois/Counter_Mode"},
		},
	},
	"dmx-connect": {
		Links: []ttLink{
			{"DMX512 (Wikipedia)", "https://en.wikipedia.org/wiki/DMX512"},
			{"Art-Net (Wikipedia)", "https://en.wikipedia.org/wiki/Art-Net"},
		},
	},
	"dmx-vrsl": {
		Links: []ttLink{{"VR Stage Lighting (GitHub)", "https://github.com/AcChosen/VR-Stage-Lighting"}},
	},
	"dmx-reemit": {},
	// ── Settings: RTSP card ──
	"rtsp-why": {
		Links: []ttLink{{"RTSP (Wikipedia)", "https://en.wikipedia.org/wiki/Real_Time_Streaming_Protocol"}},
	},
	"rtsp-passthrough": {},
	"stream-why": {
		Links: []ttLink{{"VRSL (VR Stage Lighting)", "https://github.com/AcChosen/VR-Stage-Lighting"}},
	},
	// ── Settings: Media-link card ──
	"ml-accel":  {},
	"ml-budget": {},
	// ── MIDI tab: native MIDI-learn + DJ bridge ──
	"midi-learn-controllers": {
		Links: append(virtualMIDILinks(), ttLink{"Rekordbox MIDI LEARN guide", "https://rekordbox.com/en/support/faq/mapping-6/"}),
	},
	"midi-in-port": {
		Links: virtualMIDILinks(),
	},
	"midi-thru": {
		Links: append(virtualMIDILinks(), ttLink{"MIDI-OX (router/splitter)", "http://www.midiox.com/"}),
	},
	"midi-learn-grid": {},
	"midi-drv-filter": {},
	"midi-bridge": {
		Links: virtualMIDILinks(),
	},
	// ── Remote library (mirror banner, library_mirror.go) ──
	"remote-library": {},
	// ── Self-update (nav-rail block + settings Updates card) ──
	"app-updates": {
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
// callers may reference topics that land later). The prose is resolved HERE, on the render path,
// so every topic follows a language switch; two flat-map lookups per tooltip, which is what every
// other i18n.T on a render already costs (a render runs on the actWorker - it must stay this cheap,
// and no memo is needed for it).
func tipTopic(id string) string {
	t, ok := helpTopics[id]
	if !ok {
		return ""
	}
	return renderTip(id, i18n.T("help."+id+".title"), i18n.T("help."+id+".body"), t.Keys, t.Links)
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
