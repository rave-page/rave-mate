package webui

import (
	"fmt"
	"html"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/audiorec"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/zigui"
)

const perfSpanWeb = 120 // 2 min at 1 Hz

// Live is a Zig-rendered tab (native/zigui/src/live.zig): Go resolves every fragment's
// state (service snapshots + i18n + all number formatting + the Go-built graph SVGs and
// tooltips, embedded verbatim) and the Zig lib renders HTML byte-identical to the Go
// renderers below, which stay fallback + golden reference (zigui_golden_live_test.go).
// The ~1 Hz tick (live_ticks.go) patches each fragment id; the client rAF runtime
// (__rt 'link') interpolates the Link phrase bar between ticks off live-link-fill /
// live-link-cap - those ids and the pre-formatted fill width are part of the contract.

// ── resolved render state ──

// liveKV is one status row: K label, KL its Go-lowered data-label, V value.
type liveKV struct {
	K  string `json:"k"`
	KL string `json:"kl"`
	V  string `json:"v"`
}

// liveSRow is one resolved statusRow (DL = Go strings.ToLower(Label)).
type liveSRow struct {
	Variant string `json:"variant"`
	Label   string `json:"label"`
	DL      string `json:"dl"`
	Line    string `json:"line"`
}

func liveSR(variant, label, line string) liveSRow {
	return liveSRow{Variant: variant, Label: label, DL: strings.ToLower(label), Line: line}
}

// liveTransportSt is the transport strip (auto-live status, pause switch, audio record,
// timecode). Ids live-stream-state / live-rec-state / live-tc are tick-patch targets.
type liveTransportSt struct {
	StreamHint  string `json:"streamHint"`
	StreamLabel string `json:"streamLabel"`
	DotVar      string `json:"dotVar"`
	State       string `json:"state"`
	MetaOnly    string `json:"metaOnly"`
	PauseLabel  string `json:"pauseLabel"`
	PauseHint   string `json:"pauseHint"`
	Paused      bool   `json:"paused"`
	HasRec      bool   `json:"hasRec"`
	RecHint     string `json:"recHint"`
	RecLabel    string `json:"recLabel"`
	RecBtn      string `json:"recBtn"`
	RecState    string `json:"recState"`
	HasTC       bool   `json:"hasTc"`
	TCLabel     string `json:"tcLabel"`
	TC          string `json:"tc"`
	StartLbl    string `json:"startLbl"`
	StopLbl     string `json:"stopLbl"`
}

// liveNPSt is the now-playing LCD.
type liveNPSt struct {
	Line1 string `json:"line1"`
	Line2 string `json:"line2"`
}

// liveStatusSt is the API/account/traktor/stream card.
type liveStatusSt struct {
	Rows []liveKV `json:"rows"`
}

// liveDeck is one deck tile; Cls is the resolved class list (trusted literals).
type liveDeck struct {
	Cls   string `json:"cls"`
	Name  string `json:"name"`
	Title string `json:"title"`
	Meta  string `json:"meta"`
	Via   string `json:"via"` // "" = no provenance line
}

type liveDecksSt struct {
	Note  string     `json:"note"` // "" = local decks (no peer-mirror note)
	Decks []liveDeck `json:"decks"`
}

// liveSignalsSt is the merged-state provenance card (k/v rows only).
type liveSignalsSt struct {
	Rows []liveKV `json:"rows"`
}

// liveCockpitRow is one OBS instance row.
type liveCockpitRow struct {
	Variant   string `json:"variant"`
	Name      string `json:"name"`
	State     string `json:"state"`
	StreamLbl string `json:"streamLbl"`
	StreamAct string `json:"streamAct"`
	RecLbl    string `json:"recLbl"`
	RecAct    string `json:"recAct"`
}

type liveCockpitSt struct {
	Empty   string           `json:"empty"`
	Caption string           `json:"caption"`
	Rows    []liveCockpitRow `json:"rows"`
}

// liveLinkSt is the Ableton Link panel. Fill is the pre-formatted "%.2f%%" width the
// rAF runtime later overwrites; Cap the beat caption.
type liveLinkSt struct {
	Available bool       `json:"available"`
	Backend   liveSRow   `json:"backend"` // unavailable-state row
	Fill      string     `json:"fill"`
	Cap       string     `json:"cap"`
	Session   liveSRow   `json:"session"`
	ResyncLbl string     `json:"resyncLbl"`
	Sources   []liveSRow `json:"sources"`
}

// liveGraphSt is one graph well (network / timing): legend + SVG are Go-built raw HTML.
type liveGraphSt struct {
	Tooltip string `json:"tooltip"`
	Legend  string `json:"legend"`
	Graph   string `json:"graph"`
}

// livePerfSt is the system-performance well (CPU + RAM graph pair + headroom line).
type livePerfSt struct {
	Tooltip   string `json:"tooltip"`
	CPULeg    string `json:"cpuLeg"`
	CPUGraph  string `json:"cpuGraph"`
	RAMLeg    string `json:"ramLeg"`
	RAMGraph  string `json:"ramGraph"`
	Head      string `json:"head"`
	HeadColor string `json:"headColor"`
}

// liveStripSt is the bottom signal strip (three plain-text spans).
type liveStripSt struct {
	Left   string `json:"left"`
	Center string `json:"center"`
	Right  string `json:"right"`
}

// liveState is the resolved render state for the whole Live view (JSON → Zig).
type liveState struct {
	Title        string          `json:"title"`
	Sub          string          `json:"sub"`
	Transport    liveTransportSt `json:"transport"`
	NP           liveNPSt        `json:"np"`
	StatusTitle  string          `json:"statusTitle"`
	Status       liveStatusSt    `json:"status"`
	DecksTitle   string          `json:"decksTitle"`
	Decks        liveDecksSt     `json:"decks"`
	HasSignals   bool            `json:"hasSignals"`
	SignalsTitle string          `json:"signalsTitle"`
	SignalsTip   string          `json:"signalsTip"`             // legacy RAW tooltip markup (bridge)
	SignalsTipS  *tipSt          `json:"signalsTipSt,omitempty"` // structured tooltip - wins over SignalsTip
	Signals      liveSignalsSt   `json:"signals"`
	HasCockpit   bool            `json:"hasCockpit"`
	CockpitTitle string          `json:"cockpitTitle"`
	Cockpit      liveCockpitSt   `json:"cockpit"`
	HasLink      bool            `json:"hasLink"`
	LinkTitle    string          `json:"linkTitle"`
	Link         liveLinkSt      `json:"link"`
	HasNet       bool            `json:"hasNet"`
	NetTitle     string          `json:"netTitle"`
	NetTip       string          `json:"netTip"`             // legacy RAW tooltip markup (bridge)
	NetTipS      *tipSt          `json:"netTipSt,omitempty"` // structured tooltip - wins over NetTip
	Net          liveGraphSt     `json:"net"`
	TimTitle     string          `json:"timTitle"`
	TimTip       string          `json:"timTip"`             // legacy RAW tooltip markup (bridge)
	TimTipS      *tipSt          `json:"timTipSt,omitempty"` // structured tooltip - wins over TimTip
	Tim          liveGraphSt     `json:"tim"`
	HasPerf      bool            `json:"hasPerf"`
	PerfTitle    string          `json:"perfTitle"`
	PerfTip      string          `json:"perfTip"`             // legacy RAW tooltip markup (bridge)
	PerfTipS     *tipSt          `json:"perfTipSt,omitempty"` // structured tooltip - wins over PerfTip
	Perf         livePerfSt      `json:"perf"`
	Strip        liveStripSt     `json:"strip"`
}

// liveState resolves every fragment of the cockpit into render state.
func (u *UI) liveState() liveState {
	st := liveState{
		Title: i18n.T("live.title"), Sub: i18n.T("live.subtitle"),
		Transport: u.liveTransportState(), NP: u.liveNPState(),
		StatusTitle: i18n.T("live.status.title"), Status: u.liveStatusState(),
		DecksTitle: i18n.T("live.decks.title"), Decks: u.liveDecksState(),
		Signals: liveSignalsSt{Rows: []liveKV{}},
		Cockpit: liveCockpitSt{Rows: []liveCockpitRow{}},
		Link:    liveLinkSt{Sources: []liveSRow{}},
		Strip:   u.liveStripState(),
	}
	if u.svc.Session != nil {
		st.HasSignals, st.SignalsTitle, st.SignalsTipS = true, i18n.T("live.signals.title"), tipTopicSt("signal-sources")
		st.Signals = u.liveSignalsState()
	}
	if u.svc.OBSControl != nil {
		st.HasCockpit, st.CockpitTitle = true, i18n.T("live.cockpit.title")
		st.Cockpit = u.liveCockpitState()
	}
	if u.svc.AbleLink != nil {
		st.HasLink, st.LinkTitle = true, i18n.T("live.ablelink.title")
		st.Link = u.liveLinkState()
	}
	// tips live on the STATIC section titles - the well contents tick at 1 Hz.
	if u.svc.NetStats != nil {
		st.HasNet = true
		st.NetTitle, st.NetTipS, st.Net = i18n.T("live.network.title"), tipTopicSt("network-graph"), u.liveNetState()
		st.TimTitle, st.TimTipS, st.Tim = i18n.T("live.timing.title"), tipTopicSt("timing-graph"), u.liveTimState()
	}
	if u.svc.Perf != nil {
		st.HasPerf, st.PerfTitle, st.PerfTipS = true, i18n.T("live.sysperf.title"), tipTopicSt("perf-graph")
		st.Perf = u.livePerfState()
	}
	return st
}

// renderLive is the mid-set cockpit at parity with the Fyne Live tab: transport strip (stream /
// record / timecode), now-playing LCD, status, Decks A–D, streaming cockpit, live Network / Timing /
// System-performance graphs, and the bottom signal strip. livePush patches every live fragment.
func (u *UI) renderLive() string {
	st := u.liveState()
	if zigui.Available() {
		if h, ok := zigui.RenderLive(stateJSON(st)); ok {
			return h
		}
	}
	return liveHTML(st)
}

// liveHTML is the pure Go renderer (golden reference; byte-identical to Zig).
func liveHTML(st liveState) string {
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	b.WriteString(`<div id=live-transport>` + liveTransHTML(st.Transport) + `</div>`)
	b.WriteString(`<div id=live-np>` + liveNPHTML(st.NP) + `</div>`)
	b.WriteString(section(st.StatusTitle, `<div id=live-status>`+liveStatusFragHTML(st.Status)+`</div>`))
	b.WriteString(section(st.DecksTitle, `<div id=live-decks>`+liveDecksFragHTML(st.Decks)+`</div>`))
	if st.HasSignals {
		b.WriteString(sectionTip(st.SignalsTitle, tipOr(st.SignalsTipS, st.SignalsTip), `<div id=live-signals>`+liveSignalsFragHTML(st.Signals)+`</div>`))
	}
	if st.HasCockpit {
		b.WriteString(section(st.CockpitTitle, `<div id=live-cockpit>`+liveCockpitFragHTML(st.Cockpit)+`</div>`))
	}
	if st.HasLink {
		b.WriteString(section(st.LinkTitle, `<div id=live-ablelink>`+liveLinkFragHTML(st.Link)+`</div>`))
	}
	if st.HasNet {
		b.WriteString(sectionTip(st.NetTitle, tipOr(st.NetTipS, st.NetTip), `<div id=live-net>`+liveGraphFragHTML(st.Net)+`</div>`))
		b.WriteString(sectionTip(st.TimTitle, tipOr(st.TimTipS, st.TimTip), `<div id=live-tim>`+liveGraphFragHTML(st.Tim)+`</div>`))
	}
	if st.HasPerf {
		b.WriteString(sectionTip(st.PerfTitle, tipOr(st.PerfTipS, st.PerfTip), `<div id=live-perf2>`+livePerfFragHTML(st.Perf)+`</div>`))
	}
	b.WriteString(`<div id=live-strip class=livestrip>` + liveStripFragHTML(st.Strip) + `</div>`)
	return b.String()
}

// liveFrag renders one tick-patched fragment through Zig when available.
func liveFrag[T any](kind string, st T, goHTML func(T) string) string {
	if zigui.Available() {
		if h, ok := zigui.RenderLiveFrag(kind, stateJSON(st)); ok {
			return h
		}
	}
	return goHTML(st)
}

// ── transport ──

func (u *UI) liveTransportState() liveTransportSt {
	// Auto-live: OBS stream drives the now-playing broadcast (no manual go-live). Read-only status +
	// a pause switch for private / non-DJ streams.
	signedIn := u.svc.Auth != nil && u.svc.Auth.SignedIn()
	isLive := u.svc.Stream != nil && u.svc.Stream.Status().IsLive
	paused := u.svc.Cfg != nil && u.svc.Cfg.Features.StreamBridge.PauseLiveSignal
	streaming := false
	if u.svc.OBSControl != nil {
		for _, in := range u.svc.OBSControl.Statuses() {
			if in.Local && in.Streaming {
				streaming = true
			}
		}
	}
	dotVar, stateKey := "muted", "live.transport.autoIdle"
	switch {
	case isLive:
		dotVar, stateKey = "success", "live.transport.autoLive"
	case paused:
		dotVar, stateKey = "warning", "live.transport.autoPaused"
	case streaming && !signedIn:
		dotVar, stateKey = "warning", "live.transport.autoSignIn"
	}
	st := liveTransportSt{
		// STREAM label carries the full "metadata only, no A/V" explanation as a tooltip.
		StreamHint: i18n.T("live.transport.streamHint"), StreamLabel: i18n.T("live.transport.streamLabel"),
		DotVar: dotVar, State: i18n.T(stateKey),
		// Always-visible clarifier: "live" = now-playing metadata, never audio/video.
		MetaOnly:   i18n.T("live.transport.metaOnly"),
		PauseLabel: i18n.T("live.transport.pauseLabel"), PauseHint: i18n.T("live.transport.pauseHint"),
		Paused: paused,
	}
	if u.svc.AudioRec != nil {
		rec := u.svc.AudioRec.Status()
		label := i18n.T("live.transport.recordAudio")
		if rec.Recording {
			label = i18n.T("player.stop")
		}
		st.HasRec, st.RecHint, st.RecLabel = true, i18n.T("live.transport.recHint"), i18n.T("live.transport.recLabel")
		// Idle: name the destination; recording: recStateText shows the live file.
		st.RecBtn, st.RecState = label, recSideText(rec)
	}
	if u.svc.Timecode != nil {
		st.HasTC, st.TCLabel, st.TC = true, i18n.T("live.transport.tcLabel"), u.tcText()
		st.StartLbl, st.StopLbl = i18n.T("live.transport.start"), i18n.T("live.transport.stopCaps")
	}
	return st
}

func (u *UI) liveTransportHTML() string {
	return liveFrag("transport", u.liveTransportState(), liveTransHTML)
}

// liveTransHTML is the pure transport-strip renderer.
func liveTransHTML(st liveTransportSt) string {
	var b strings.Builder
	b.WriteString(`<div class=transport><span class=tlabel title=` + attrQ(st.StreamHint) + `>` +
		html.EscapeString(st.StreamLabel) + `</span>`)
	b.WriteString(`<span class=np-artist id=live-stream-state title=` + attrQ(st.StreamHint) + `>` +
		dot(st.DotVar) + ` ` + html.EscapeString(st.State) + `</span>`)
	b.WriteString(`<span class=tlabel style="opacity:.7">` + html.EscapeString(st.MetaOnly) + `</span>`)
	pauseChecked := ""
	if st.Paused {
		pauseChecked = " checked"
	}
	b.WriteString(`<span class=tlabel>` + html.EscapeString(st.PauseLabel) + `</span>`)
	b.WriteString(`<label class=switch data-label="private stream" title=` + attrQ(st.PauseHint) +
		`><input type=checkbox` + pauseChecked + ` data-act=stream-pause data-value=` + attrQ(boolStr(st.Paused)) +
		`><span class=switch-track></span></label>`)
	if st.HasRec {
		b.WriteString(`<span class=tsep></span><span class=tlabel title=` + attrQ(st.RecHint) + `>` +
			html.EscapeString(st.RecLabel) + `</span>`)
		b.WriteString(`<button class="rp-btn rp-btn--outline" data-act=arec-toggle title=` + attrQ(st.RecHint) + `>` +
			html.EscapeString(st.RecBtn) + `</button>`)
		b.WriteString(`<span class=np-artist id=live-rec-state title=` + attrQ(st.RecHint) + `>` +
			html.EscapeString(st.RecState) + `</span>`)
	}
	if st.HasTC {
		b.WriteString(`<span class=tsep></span><span class=tlabel>` + html.EscapeString(st.TCLabel) + `</span>`)
		b.WriteString(`<span class=tmono id=live-tc>` + html.EscapeString(st.TC) + `</span>`)
		b.WriteString(`<button class="rp-btn rp-btn--go" data-act=tc-start>` + html.EscapeString(st.StartLbl) + `</button>`)
		b.WriteString(`<button class="rp-btn rp-btn--outline" data-act=tc-stop>` + html.EscapeString(st.StopLbl) + `</button>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// recSideText is the record-state readout: the live file while recording, else the idle
// destination hint (so the button always says WHAT it records + WHERE it lands).
func recSideText(s audiorec.Status) string {
	if t := recStateText(s); t != "" {
		return t
	}
	return i18n.T("live.transport.recTarget")
}

func recStateText(s audiorec.Status) string {
	if !s.Recording {
		return ""
	}
	src := i18n.T("live.transport.recManual")
	if s.Auto {
		src = i18n.T("live.transport.recAuto")
	}
	if s.Path != "" {
		src += " · " + filepath.Base(s.Path)
	}
	return "● " + src
}

// ── ableton link ──

// liveLinkState resolves the Link sync panel: phrase-phase bar, tempo/peers summary, and
// (when OBS media-sync is present) the per-source chase/delay-comp readout.
func (u *UI) liveLinkState() liveLinkSt {
	lst := u.svc.AbleLink.StateNow() // fresh phase (mirror snapshot is ~10 Hz)
	st := liveLinkSt{Sources: []liveSRow{}}
	if !lst.Available {
		st.Backend = liveSR("warning", i18n.T("live.ablelink.backend"), i18n.T("live.ablelink.unavailable"))
		return st
	}
	st.Available = true
	quantum := int(lst.Quantum)
	if quantum <= 0 {
		quantum = 16
	}
	beat := int(lst.Phase) + 1 // 1-based beat within the phrase
	st.Fill = pbarPct(lst.PhraseFraction() * 100)
	st.Cap = i18n.T("live.ablelink.phraseBeat", i18n.A{"beat": fmt.Sprint(beat), "quantum": fmt.Sprint(quantum)})
	variant, state := "success", i18n.T("live.ablelink.enabled")
	if !lst.Enabled {
		variant, state = "off", i18n.T("live.ablelink.disabled")
	}
	st.Session = liveSR(variant, i18n.T("live.ablelink.session"),
		i18n.T("live.ablelink.summary", i18n.A{"tempo": fmt.Sprintf("%.1f", lst.Tempo), "peers": fmt.Sprint(lst.Peers), "state": state}))
	st.ResyncLbl = i18n.T("settings.body.ablelink.resync")
	// Per-source media-sync / delay-comp chase readout (mirrors the Fyne obssync view).
	if u.svc.OBSControl != nil {
		for _, ss := range u.svc.OBSControl.SyncStatuses() {
			line := ss.LastAction
			sv := "success"
			if ss.Err != "" {
				line, sv = "error: "+ss.Err, "error"
			} else if ss.Active && !ss.Playing {
				line, sv = i18n.T("live.ablelink.waiting"), "warning"
			}
			src := ss.Source
			if src == "" {
				src = "?"
			}
			st.Sources = append(st.Sources, liveSR(sv, src,
				fmt.Sprintf("%s · err %+dms · %.0f corr/min", line, ss.ErrorMs, ss.CorrectionsPerMin)))
		}
	}
	return st
}

func (u *UI) ableLinkHTML() string {
	return liveFrag("link", u.liveLinkState(), liveLinkFragHTML)
}

// liveLinkFragHTML is the pure Link-panel renderer.
func liveLinkFragHTML(st liveLinkSt) string {
	if !st.Available {
		return liveStatusRow(st.Backend)
	}
	var b strings.Builder
	b.WriteString(linkPhraseBarStr(st.Fill, st.Cap))
	b.WriteString(liveStatusRow(st.Session))
	b.WriteString(btnRow(btn(st.ResyncLbl, "outline", "ablelink-resync", "")))
	for _, s := range st.Sources {
		b.WriteString(liveStatusRow(s))
	}
	return b.String()
}

// liveStatusRow renders a resolved statusRow through the shared primitive.
func liveStatusRow(r liveSRow) string { return statusRow(r.Variant, r.Label, r.Line) }

// linkPhraseBar renders the Link phrase progress bar carrying stable ids so the client rAF
// runtime (__rt 'link', pushAbleLink) can advance the fill width + beat number at display
// refresh between the ~1 Hz ticks. fillPct in [0,100].
func linkPhraseBar(fillPct float64, cap string) string {
	return linkPhraseBarStr(pbarPct(fillPct), cap)
}

// pbarPct clamps + formats a percentage width (the Zig path never formats a float).
func pbarPct(fillPct float64) string {
	if fillPct < 0 {
		fillPct = 0
	}
	if fillPct > 100 {
		fillPct = 100
	}
	return fmt.Sprintf("%.2f%%", fillPct)
}

// linkPhraseBarStr is linkPhraseBar from a pre-formatted width (shared by both renderers).
func linkPhraseBarStr(fill, cap string) string {
	return `<div class=pbar><div class="pbar-fill" id=live-link-fill style="width:` + fill +
		`"></div><span class="pbar-cap" id=live-link-cap>` + html.EscapeString(cap) + `</span></div>`
}

// pushAbleLink hands the client rAF runtime (__rt 'link') the Link phrase so the phrase bar
// advances smoothly at display refresh between the ~1 Hz ticks: phase (beats) + tempo drive a
// local phase = (phase + tempo/60·dt) mod quantum → fill width + beat number. rate 0
// (disabled/unavailable) = static snap + loop stop. Called each tick after the panel patch.
func (u *UI) pushAbleLink() {
	if u.shell == nil || u.svc.AbleLink == nil {
		return
	}
	st := u.svc.AbleLink.StateNow() // extrapolate the ~10 Hz mirror to now so the pushed phase is fresh
	q := st.Quantum
	if q <= 0 {
		q = 16
	}
	// caption template with a sentinel where the (interpolated) beat number goes → split so the
	// client can rebuild it per language without re-running i18n.
	tmpl := i18n.T("live.ablelink.phraseBeat", i18n.A{"beat": "\x00", "quantum": fmt.Sprint(int(q))})
	pre, post, _ := strings.Cut(tmpl, "\x00")
	rate := 0.0
	if st.Available && st.Enabled && st.Tempo > 0 {
		rate = 1.0
	}
	u.enqueueEval("rtlink", fmt.Sprintf(
		"window.__rt&&window.__rt('link','live-link',{fill:'live-link-fill',cap:'live-link-cap',phase:%.4f,tempo:%.4f,q:%.2f,rate:%.1f,pre:%s,post:%s})",
		st.Phase, st.Tempo, q, rate, jsQuote(pre), jsQuote(post)))
}

// ── now playing ──

func (u *UI) liveNPState() liveNPSt {
	d, ok := u.masterDeck()
	st := liveNPSt{Line1: i18n.T("live.nowPlaying.noTrack")}
	if !ok {
		return st
	}
	t := strings.TrimSpace(d.Artist)
	if t != "" && d.Title != "" {
		t += " - "
	}
	t += d.Title
	if len(t) > 40 {
		t = t[:39] + "…"
	}
	st.Line1 = "♪ " + strings.ToUpper(t)
	st.Line2 = i18n.T("live.nowPlaying.line2", i18n.A{"deck": d.Deck, "elapsed": mmss(d.ElapsedTime), "total": mmss(d.TrackLength)})
	if d.BPM > 0 {
		st.Line2 += "    " + i18n.T("live.bpm", i18n.A{"bpm": fmt.Sprintf("%.1f", d.BPM)})
	}
	if d.Key != "" {
		st.Line2 += "    " + d.Key
	}
	return st
}

func (u *UI) nowPlayingHTML() string { return liveFrag("np", u.liveNPState(), liveNPHTML) }

// liveNPHTML is the pure now-playing LCD renderer.
func liveNPHTML(st liveNPSt) string {
	return `<div class=lcd><div class=lcd-1 data-label="now playing" data-value="` + html.EscapeString(st.Line1) + `">` +
		html.EscapeString(st.Line1) + `</div><div class=lcd-2>` + html.EscapeString(st.Line2) + `</div></div>`
}

func (u *UI) masterDeck() (session.DeckSnapshot, bool) {
	if u.svc.Session != nil {
		ov := u.svc.Session.Snapshot().BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
		for _, d := range ov.Decks {
			if d.Deck == ov.Master.Deck {
				return d, true
			}
		}
	}
	if u.svc.PeerBridge != nil {
		var best *peerbridge.RemoteState
		for _, s := range u.svc.PeerBridge.RemoteStates() {
			s := s
			if !s.NowPlaying.Playing || time.Since(s.UpdatedAt) > session.NowPlayingStaleAfter {
				continue
			}
			if best == nil || s.UpdatedAt.After(best.UpdatedAt) {
				best = &s
			}
		}
		if best != nil {
			np := best.NowPlaying
			return session.DeckSnapshot{Deck: np.Deck, Title: np.Title, Artist: np.Artist,
				BPM: np.BPM, Key: np.Key, ElapsedTime: np.Elapsed, TrackLength: np.Length, IsPlaying: np.Playing}, true
		}
	}
	return session.DeckSnapshot{}, false
}

// ── status ──

func (u *UI) liveStatusState() liveStatusSt {
	api := ""
	if u.svc.API != nil {
		api = u.svc.API.BaseURL()
	}
	acct := i18n.T("live.status.notSignedIn")
	if u.svc.Auth != nil && u.svc.Auth.SignedIn() {
		acct = i18n.T("live.status.signedIn")
	}
	trk := "-"
	if u.svc.Traktor != nil {
		port := 8080
		if u.svc.Cfg != nil {
			port = u.svc.Cfg.Features.Traktor.ResolvedPort()
		}
		switch {
		case u.svc.Cfg != nil && !u.svc.Cfg.Features.Traktor.Enabled:
			trk = i18n.T("common.disabled")
		case u.svc.Traktor.Listening():
			trk = i18n.T("live.status.trkListening", i18n.A{"port": fmt.Sprint(port)})
		default:
			trk = i18n.T("live.status.trkNotBound", i18n.A{"port": fmt.Sprint(port)})
		}
	}
	strm := i18n.T("live.status.idle")
	if u.svc.Stream != nil {
		s := u.svc.Stream.Status()
		switch {
		case s.IsLive:
			strm = i18n.T("live.status.strmLive", i18n.A{"title": s.Title, "count": fmt.Sprint(s.PendingEventCount)})
		case s.LastError != "":
			strm = i18n.T("live.status.idleError", i18n.A{"error": s.LastError})
		}
	}
	return liveStatusSt{Rows: []liveKV{
		liveRow(i18n.T("live.status.apiLabel"), api),
		liveRow(i18n.T("live.status.accountLabel"), acct),
		liveRow(i18n.T("live.status.traktorLabel"), trk),
		liveRow(i18n.T("live.status.streamLabel"), strm),
	}}
}

// liveRow resolves one k/v status row (KL = the Go-lowered data-label).
func liveRow(k, v string) liveKV { return liveKV{K: k, KL: strings.ToLower(k), V: v} }

func (u *UI) liveStatusHTML() string {
	return liveFrag("status", u.liveStatusState(), liveStatusFragHTML)
}

// liveStatusFragHTML is the pure status-card renderer.
func liveStatusFragHTML(st liveStatusSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	for _, r := range st.Rows {
		b.WriteString(`<div class=st-row><span class=st-k>` + html.EscapeString(r.K) + `</span>` +
			`<span data-label="` + r.KL + `" data-value="` + html.EscapeString(r.V) + `">` + html.EscapeString(r.V) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── decks ──

func (u *UI) liveDecksState() liveDecksSt {
	byDeck := map[string]session.DeckSnapshot{}
	viaByDeck := map[string]string{} // deck id → "src1, src2" provenance (Fyne-parity "via" line)
	audible := ""
	if u.svc.Session != nil {
		snap := u.svc.Session.Snapshot()
		ov := snap.BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
		for _, d := range ov.Decks {
			byDeck[d.Deck] = d
		}
		audible = ov.Master.Deck
		for id, fields := range snap.Decks {
			set := map[string]bool{}
			for _, fv := range fields {
				if fv.Source != "" {
					set[fv.Source] = true
				}
			}
			viaByDeck[id] = joinSorted(set)
		}
	}
	// No local deck playing → mirror all playing decks from the freshest linked peer.
	note := ""
	localLive := false
	for _, d := range byDeck {
		if d.IsPlaying {
			localLive = true
			break
		}
	}
	if !localLive && u.svc.PeerBridge != nil {
		var best *peerbridge.RemoteState
		for _, s := range u.svc.PeerBridge.RemoteStates() {
			s := s
			if time.Since(s.UpdatedAt) > session.NowPlayingStaleAfter || len(s.NowPlaying.AllDecks()) == 0 {
				continue
			}
			if best == nil || s.UpdatedAt.After(best.UpdatedAt) {
				best = &s
			}
		}
		if best != nil {
			byDeck, audible = map[string]session.DeckSnapshot{}, ""
			for _, ds := range best.NowPlaying.AllDecks() {
				byDeck[ds.Deck] = session.DeckSnapshot{Deck: ds.Deck, Title: ds.Title, Artist: ds.Artist,
					BPM: ds.BPM, Key: ds.Key, ElapsedTime: ds.Elapsed, TrackLength: ds.Length, IsPlaying: true}
				if ds.Audible {
					audible = ds.Deck
				}
			}
			note = i18n.T("live.decks.fromPeer", i18n.A{"name": peerName("", best.NodeID)})
		}
	}
	// A-D always render; extra peer deck ids append after.
	ids := []string{"A", "B", "C", "D"}
	seen := map[string]bool{"A": true, "B": true, "C": true, "D": true}
	for id := range byDeck {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids[4:])
	st := liveDecksSt{Note: note, Decks: make([]liveDeck, 0, len(ids))}
	for _, id := range ids {
		d, ok := byDeck[id]
		cls := "deckbig"
		title, meta := "–", "-"
		if ok {
			if cur := strings.TrimSpace(d.Artist + " - " + d.Title); cur != "-" && cur != "" {
				title = cur
			}
			meta = fmt.Sprintf("%s / %s", mmss(d.ElapsedTime), mmss(d.TrackLength))
			if d.BPM > 0 {
				meta += " · " + i18n.T("live.bpm", i18n.A{"bpm": fmt.Sprintf("%.1f", d.BPM)})
			}
			if d.Key != "" {
				meta += " · " + d.Key
			}
			if d.IsPlaying {
				cls += " deckbig--live"
			}
			if id == audible && d.IsPlaying {
				cls += " deckbig--audible"
			}
		}
		via := ""
		if v := viaByDeck[id]; v != "" {
			via = i18n.T("live.decks.via", i18n.A{"sources": v})
		}
		st.Decks = append(st.Decks, liveDeck{Cls: cls, Name: i18n.T("live.deck.name", i18n.A{"id": id}),
			Title: title, Meta: meta, Via: via})
	}
	return st
}

func (u *UI) decksHTML() string { return liveFrag("decks", u.liveDecksState(), liveDecksFragHTML) }

// liveDecksFragHTML is the pure decks-grid renderer.
func liveDecksFragHTML(st liveDecksSt) string {
	var b strings.Builder
	if st.Note != "" {
		b.WriteString(`<div class=decks-note>` + html.EscapeString(st.Note) + `</div>`)
	}
	b.WriteString(`<div class=decks-grid>`)
	for _, d := range st.Decks {
		via := ""
		if d.Via != "" {
			via = `<div class="deckbig-m deckbig-src">` + html.EscapeString(d.Via) + `</div>`
		}
		b.WriteString(`<div class="` + d.Cls + `"><div class=deckbig-id>` + html.EscapeString(d.Name) + `</div>` +
			`<div class=deckbig-t>` + html.EscapeString(d.Title) + `</div><div class=deckbig-m>` +
			html.EscapeString(d.Meta) + `</div>` + via + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// joinSorted renders a string set as a stable comma list.
func joinSorted(set map[string]bool) string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// ── signal sources (Fyne view_session parity: per-signal provenance + source coverage) ──

// liveSignalsState resolves the merged-state provenance panel: which source feeds each
// channel's mixer signals (EQ/filter/fader - "—" = nobody, the instant answer to "why
// are my EQ knobs dead"), every registered source's liveness, and ravemidi driver bind
// health for managed controllers.
func (u *UI) liveSignalsState() liveSignalsSt {
	st := liveSignalsSt{Rows: []liveKV{}}
	snap := u.svc.Session.Snapshot()
	// per-channel mixer coverage: the signals the streamed overlay draws
	mixer := [][2]string{{session.FieldEQLow, "EQ lo"}, {session.FieldEQMid, "EQ mid"},
		{session.FieldEQHigh, "EQ hi"}, {session.FieldFilter, "Filter"}, {session.FieldFader, "Fader"}}
	for _, ch := range []string{"1", "2", "3", "4"} {
		fields := snap.Channels[ch]
		parts := make([]string, 0, len(mixer))
		for _, m := range mixer {
			src := "—"
			if fv, ok := fields[m[0]]; ok && fv.Source != "" {
				src = fv.Source
			}
			parts = append(parts, m[1]+" "+src)
		}
		st.Rows = append(st.Rows, liveRow(i18n.T("live.signals.channel", i18n.A{"n": ch}), strings.Join(parts, " · ")))
	}
	// source liveness. Disabled sources are skipped: planned stubs (qml, nowplaying) register
	// disabled to advertise in Settings, and an "off" row here reads as a fault (the QML mod's
	// data arrives via the traktor HTTP source, not a separate "qml" row).
	for _, s := range u.svc.Session.Sources() {
		if !s.Enabled {
			continue
		}
		state := i18n.T("live.signals.off")
		switch {
		case s.Receiving:
			state = i18n.T("live.signals.receiving", i18n.A{"age": agoShort(s.LastSeen)})
		case s.Running:
			state = i18n.T("live.signals.idle")
		}
		st.Rows = append(st.Rows, liveRow(s.ID, state))
	}
	// ravemidi managed-controller bind health: an unbound input = the driver lost the
	// hardware (another app grabbed it?) = EQ/filter silently dead. Say it HERE.
	if midi.DriverInstalled() {
		if sts, err := midi.QueryDriverInputs(); err == nil {
			for _, dst := range sts {
				v := i18n.T("live.signals.driverBound")
				if !dst.Bound {
					v = i18n.T("live.signals.driverUnbound", i18n.A{"retries": fmt.Sprint(dst.RetryCount)})
				}
				st.Rows = append(st.Rows, liveRow(dst.Name, v))
			}
		}
	}
	if u.svc.MIDISource != nil {
		for _, p := range u.svc.MIDISource.FailedInputPorts() {
			st.Rows = append(st.Rows, liveRow(p, i18n.T("live.signals.portFailed")))
		}
		// A muted loopback opens fine but delivers NOTHING (LoopBe1 anti-feedback mute) -
		// the exact silent state that kills EQ/filter mid-set. Detected by echo probe.
		for _, p := range u.svc.MIDISource.MutedInputPorts() {
			st.Rows = append(st.Rows, liveRow(p, i18n.T("live.signals.portMuted")))
		}
	}
	return st
}

func (u *UI) signalsHTML() string {
	if u.svc.Session == nil {
		return ""
	}
	return liveFrag("signals", u.liveSignalsState(), liveSignalsFragHTML)
}

// liveSignalsFragHTML is the pure signals-card renderer (rows carry no data-label).
func liveSignalsFragHTML(st liveSignalsSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	for _, r := range st.Rows {
		b.WriteString(`<div class=st-row><span class=st-k>` + html.EscapeString(r.K) + `</span>` +
			`<span>` + html.EscapeString(r.V) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── streaming cockpit ──

func (u *UI) liveCockpitState() liveCockpitSt {
	insts := u.svc.OBSControl.Statuses()
	st := liveCockpitSt{Empty: i18n.T("live.cockpit.empty"), Rows: make([]liveCockpitRow, 0, len(insts))}
	if len(insts) == 0 {
		return st
	}
	// Caption: what the OBS controls below do + where recordings land + auto tracklist capture.
	st.Caption = i18n.T("live.cockpit.caption")
	for _, in := range insts {
		name := in.Label
		if name == "" {
			name = peerName("", in.Node)
		}
		if in.Local {
			name += " " + i18n.T("live.cockpit.thisPc")
		}
		state, sv := i18n.T("live.cockpit.offline"), "muted"
		switch {
		case in.Streaming:
			state, sv = i18n.T("live.cockpit.streaming", i18n.A{"kbps": fmt.Sprint(in.BitrateKbps)}), "error"
		case in.Recording:
			state, sv = i18n.T("live.cockpit.recording"), "error"
		case in.Reconnecting:
			state, sv = i18n.T("live.cockpit.reconnecting"), "warning"
		case in.Connected:
			state, sv = i18n.T("live.cockpit.ready"), "success"
		}
		streamLabel, recLabel := i18n.T("live.cockpit.startStream"), i18n.T("live.cockpit.startRec")
		if in.Streaming {
			streamLabel = i18n.T("live.cockpit.stopStream")
		}
		if in.Recording {
			recLabel = i18n.T("live.cockpit.stopRec")
		}
		st.Rows = append(st.Rows, liveCockpitRow{Variant: sv, Name: name, State: state,
			StreamLbl: streamLabel, StreamAct: "obs-stream:" + in.ID,
			RecLbl: recLabel, RecAct: "obs-record:" + in.ID})
	}
	return st
}

func (u *UI) cockpitHTML() string {
	return liveFrag("cockpit", u.liveCockpitState(), liveCockpitFragHTML)
}

// liveCockpitFragHTML is the pure OBS-cockpit renderer.
func liveCockpitFragHTML(st liveCockpitSt) string {
	if len(st.Rows) == 0 {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(`<div class=decks-note>` + html.EscapeString(st.Caption) + `</div>`)
	b.WriteString(`<div class="rp-card">`)
	for _, r := range st.Rows {
		b.WriteString(`<div class=row><span class=row-label>` + dot(r.Variant) + ` ` + html.EscapeString(r.Name) +
			` <span class=np-artist>` + html.EscapeString(r.State) + `</span></span>` +
			btnRow(btn(r.StreamLbl, "primary", r.StreamAct, ""), btn(r.RecLbl, "outline", r.RecAct, "")) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── graphs ──

func (u *UI) liveNetState() liveGraphSt {
	snap := u.svc.NetStats.Snapshot()
	graph := sparklineSVG([]sparkSeries{
		{snap.PeerIn, sparkMint, true},
		{snap.PeerOut, sparkHot, true},
		{snap.APIIn, sparkViolet, false},
		{snap.APIOut, sparkAmber, false},
	}, 600, 56)
	legend := fmt.Sprintf(`<span style="color:%s">%s</span><span style="color:%s">%s</span>`+
		`<span style="color:%s">%s</span><span style="color:%s">%s</span>`+
		`<span style="color:%s">%s</span>`,
		sparkMint, html.EscapeString(i18n.T("live.network.peerDown", i18n.A{"rate": rateStr(snap.PeerIn)})),
		sparkHot, html.EscapeString(i18n.T("live.network.upRate", i18n.A{"rate": rateStr(snap.PeerOut)})),
		sparkViolet, html.EscapeString(i18n.T("live.network.apiDown", i18n.A{"rate": rateStr(snap.APIIn)})),
		sparkAmber, html.EscapeString(i18n.T("live.network.upRate", i18n.A{"rate": rateStr(snap.APIOut)})),
		sparkMint, html.EscapeString(i18n.T("live.network.session", i18n.A{
			"down": humanBytes(snap.SessPeerIn + snap.SessAPIIn),
			"up":   humanBytes(snap.SessPeerOut + snap.SessAPIOut),
		})))
	return liveGraphSt{Tooltip: i18n.T("live.network.tooltip"), Legend: legend, Graph: graph}
}

func (u *UI) networkHTML() string { return liveFrag("graph", u.liveNetState(), liveGraphFragHTML) }

func (u *UI) liveTimState() liveGraphSt {
	snap := u.svc.NetStats.Snapshot()
	pal := []string{sparkMint, sparkHot, sparkViolet, sparkAmber, sparkInfo}
	var series []sparkSeries
	var legend strings.Builder
	for i, r := range snap.RTT {
		c := pal[i%len(pal)]
		series = append(series, sparkSeries{r.Ms, c, false})
		ms := "-"
		if r.Has {
			ms = fmt.Sprintf("%.1fms", r.LatestMs)
		}
		fmt.Fprintf(&legend, `<span style="color:%s">%s %s</span>`, c, html.EscapeString(strings.ToUpper(r.Label)), ms)
	}
	if len(series) == 0 {
		series = []sparkSeries{{make([]float64, snap.Span), sparkMuted, false}}
		legend.WriteString(`<span>` + html.EscapeString(i18n.T("live.timing.noPeers")) + `</span>`)
	}
	return liveGraphSt{Tooltip: i18n.T("live.timing.tooltip"), Legend: legend.String(), Graph: sparklineSVG(series, 600, 56)}
}

func (u *UI) timingHTML() string { return liveFrag("graph", u.liveTimState(), liveGraphFragHTML) }

// liveGraphFragHTML is the pure graph-well renderer (legend + SVG are Go-built).
func liveGraphFragHTML(st liveGraphSt) string {
	return `<div class=gwell title=` + attrQ(st.Tooltip) + `><div class=glegend>` + st.Legend + `</div>` + st.Graph + `</div>`
}

func (u *UI) livePerfState() livePerfSt {
	ss := u.svc.Perf.Snapshot()
	if len(ss) > perfSpanWeb {
		ss = ss[len(ss)-perfSpanWeb:]
	}
	appC := make([]float64, len(ss))
	sysC := make([]float64, len(ss))
	appR := make([]float64, len(ss))
	sysR := make([]float64, len(ss))
	for i, s := range ss {
		appC[i], appR[i] = s.CPUPct, s.RSSMB
		if s.SysOK {
			sysC[i], sysR[i] = s.SysCPUPct, s.SysMemUsedMB
		} else {
			sysC[i], sysR[i] = math.NaN(), math.NaN()
		}
	}
	appLbl, sysLbl := html.EscapeString(i18n.T("live.perf.app")), html.EscapeString(i18n.T("live.perf.sys"))
	cpuLeg := `<span>` + appLbl + ` -</span><span>` + sysLbl + ` -</span>`
	ramLeg := `<span>` + appLbl + ` -</span><span>` + sysLbl + ` -</span>`
	head := i18n.T("live.perf.headroom") + " -"
	if len(ss) > 0 {
		l := ss[len(ss)-1]
		sysCPU, sysRAM := "-", "-"
		if l.SysOK {
			sysCPU = fmt.Sprintf("%.0f%%", l.SysCPUPct)
			sysRAM = fmt.Sprintf("%.1f/%.1f GB", l.SysMemUsedMB/1024, l.SysMemTotalMB/1024)
			head = i18n.T("live.perf.headroomLine", i18n.A{
				"gb":  fmt.Sprintf("%.1f", (l.SysMemTotalMB-l.SysMemUsedMB)/1024),
				"cpu": fmt.Sprintf("%.0f", math.Max(0, 100-l.SysCPUPct)),
			})
		}
		cpuLeg = fmt.Sprintf(`<span style="color:%s">%s %.0f%%</span><span style="color:%s">%s %s</span>`, sparkMint, appLbl, l.CPUPct, sparkHot, sysLbl, sysCPU)
		ramLeg = fmt.Sprintf(`<span style="color:%s">%s %.0f MB</span><span style="color:%s">%s %s</span>`, sparkViolet, appLbl, l.RSSMB, sparkAmber, sysLbl, sysRAM)
	}
	return livePerfSt{
		Tooltip: i18n.T("live.perf.tooltip"),
		CPULeg:  cpuLeg, CPUGraph: sparklineSVG([]sparkSeries{{appC, sparkMint, true}, {sysC, sparkHot, false}}, 600, 56),
		RAMLeg: ramLeg, RAMGraph: sparklineSVG([]sparkSeries{{sysR, sparkAmber, false}, {appR, sparkViolet, true}}, 600, 56),
		Head: head, HeadColor: sparkMint,
	}
}

func (u *UI) sysperfHTML() string { return liveFrag("perf", u.livePerfState(), livePerfFragHTML) }

// livePerfFragHTML is the pure system-performance well renderer.
func livePerfFragHTML(st livePerfSt) string {
	return `<div class=gwell title=` + attrQ(st.Tooltip) + `><div class=glegend>` + st.CPULeg + `</div>` + st.CPUGraph +
		`<div class=glegend>` + st.RAMLeg + `</div>` + st.RAMGraph +
		`<div class=glegend><span style="color:` + st.HeadColor + `">` + html.EscapeString(st.Head) + `</span></div></div>`
}

// ── bottom signal strip (port of liveStatusLeft/Center/Right) ──

func (u *UI) liveStripState() liveStripSt {
	return liveStripSt{Left: u.stripLeft(), Center: u.stripCenter(), Right: u.stripRight()}
}

func (u *UI) liveStripHTML() string { return liveFrag("strip", u.liveStripState(), liveStripFragHTML) }

// liveStripFragHTML is the pure signal-strip renderer.
func liveStripFragHTML(st liveStripSt) string {
	return `<span>` + html.EscapeString(st.Left) + `</span><span>` + html.EscapeString(st.Center) +
		`</span><span>` + html.EscapeString(st.Right) + `</span>`
}

func (u *UI) stripLeft() string {
	var p []string
	if u.svc.OBS != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.OBS.Enabled {
		o := u.svc.OBS.Status()
		switch {
		case o.Recording:
			p = append(p, i18n.T("live.strip.obsRec", i18n.A{"dur": time.Since(o.RecStartedAt).Truncate(time.Second).String()}))
		case o.Connected:
			p = append(p, i18n.T("live.strip.obsOk"))
		default:
			p = append(p, i18n.T("live.strip.obsOff"))
		}
	}
	if u.svc.SetCapture != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.SetCapture.Enabled {
		c := u.svc.SetCapture.Snapshot()
		switch {
		case c.Connected:
			p = append(p, i18n.T("live.strip.capBytes", i18n.A{"bytes": humanBytes(uint64(c.Bytes))}))
		case c.Reconnecting:
			p = append(p, i18n.T("live.strip.capReconnecting"))
		case c.Listening:
			p = append(p, i18n.T("live.strip.capListening"))
		}
	}
	if u.svc.AudioRec != nil && u.svc.AudioRec.Status().Recording {
		p = append(p, i18n.T("live.strip.recOn"))
	}
	return strings.Join(p, " · ")
}

func (u *UI) stripCenter() string {
	var p []string
	if u.svc.Timecode != nil {
		if tc, running := u.svc.Timecode.Now(); running {
			p = append(p, i18n.T("live.strip.tc", i18n.A{"tc": tc.String()}))
		}
	}
	if u.svc.DMX != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.DMX.Enabled {
		snap := u.svc.DMX.Status()
		receiving := false
		for _, un := range snap.Universes {
			if un.PPS > 0 {
				receiving = true
			}
		}
		switch {
		case !snap.Running:
			p = append(p, i18n.T("live.strip.dmxOff"))
		case receiving:
			p = append(p, i18n.T("live.strip.dmxActive"))
		default:
			p = append(p, i18n.T("live.strip.dmxIdle"))
		}
	}
	return strings.Join(p, " · ")
}

func (u *UI) stripRight() string {
	var p []string
	if m := u.svc.Twitch; m != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.Twitch.Enabled {
		switch {
		case m.SignedIn() && m.Self().Login != "":
			p = append(p, i18n.T("live.strip.twitchUser", i18n.A{"login": m.Self().Login}))
		case m.SignedIn():
			p = append(p, i18n.T("live.strip.twitchConnecting"))
		}
	}
	if u.svc.Perf != nil {
		if ss := u.svc.Perf.Snapshot(); len(ss) > 0 {
			last := ss[len(ss)-1]
			if last.SysOK {
				p = append(p, i18n.T("live.strip.freeResources", i18n.A{
					"cpu": fmt.Sprintf("%.0f", math.Max(0, 100-last.SysCPUPct)),
					"gb":  fmt.Sprintf("%.1f", (last.SysMemTotalMB-last.SysMemUsedMB)/1024),
				}))
			}
		}
	}
	return strings.Join(p, " · ")
}

// ── small helpers ──

func mmss(s float64) string {
	if s <= 0 {
		return "0:00"
	}
	t := int(s)
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

func lastVal(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	if v := vals[len(vals)-1]; !math.IsNaN(v) {
		return v
	}
	return 0
}

func rateStr(vals []float64) string { return humanBytes(uint64(lastVal(vals))) + "/s" }

func (u *UI) tcText() string {
	if u.svc.Timecode == nil {
		return "--:--:--:--"
	}
	tc, running := u.svc.Timecode.Now()
	if running {
		return tc.String()
	}
	return tc.String() + " ◼"
}
