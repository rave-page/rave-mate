package webui

import (
	"fmt"
	"html"
	"math"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/zigui"
)

// Unified media player/editor - RENDER-LAYER SPLIT (engine/state: player_actions.go,
// float geometry: player.go). Go resolves every host surface into mpSt-derived render
// state; native/zigui/src/player.zig renders the STRUCTURAL CHROME byte-identically
// (fallback + golden reference: zigui_golden_player_test.go).
//
// WHAT STAYS GO (rides through the state as trusted RAW markup - never ported):
//   - the waveform SVG (player.go mpWaveSVG/mpLoudPath: %.1f/%.2f coordinate math,
//     beatgrid + cue/drop/trim geometry, and the `mp-<host>-ph` / `-ph-veil` ids the
//     client rAF runtime rewrites 30x/s). Same rule as keywheelSVG / the campath viewer.
//   - mpLoudExtraHTML (the standalone gain-plan line + pre-listen toggle) when the PRESET
//     normalizes without an override. The shared loudness block itself now crosses as
//     structured state (components.go loudSt, phase B-1a) with its extraHTML inside it.
//   - tipTopic() tooltip cards (tooltip.go).
//   - the <video> element's inline JS handlers (they carry a %.3f volume and drive
//     shell.go __mse) - resolved Go-side, then attrQ'd identically by both renderers.
//
// Numbers NEVER cross as floats to be formatted: every clock (pubClock/pubClockF/
// mpSignedClock), percentage (progressPct "%.1f%%"), LUFS/kbps/size readout and marker
// offset ("%.2f", also a smart-select value) is pre-formatted here. The host id crosses
// as a plain field and both renderers compose the ids from it - note the QUIRK that
// mpHTML escapes the host in `mp-<host>-root` while every other id splices it RAW.

// ── leaf state ──────────────────────────────────────────────────────────────────

// mpTabSt is one media-switch subTabs item.
type mpTabSt struct {
	Val   string `json:"val"`
	Label string `json:"label"`
}

// mpKVRow is one wchip-card detail row (<b>k</b>v).
type mpKVRow struct {
	K string `json:"k"`
	V string `json:"v"`
}

// mpLinkSt is one wc-link in the loudness chip card. URL is spliced UNESCAPED (a Go
// source literal), the label is escaped.
type mpLinkSt struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

// mpChipSt is one waveform overlay chip. Kind: "" none | "dim" (loading pill, whose text
// Go splices UNESCAPED) | "chip" (the pinnable label + hover card).
type mpChipSt struct {
	Kind  string     `json:"kind"`
	Loud  bool       `json:"loud"` // loudness chip: class="wchip loud", data-label=lufs-chip, links
	Dim   string     `json:"dim"`
	Text  string     `json:"text"`
	Rows  []mpKVRow  `json:"rows,omitempty"`
	Note  string     `json:"note"`
	Links []mpLinkSt `json:"links,omitempty"`
}

// ── fragment state (one per patch target: root/vid/wave/tp/edit/export/ro/hov) ───

// mpVidSt is the #mp-<host>-vid inner. Kind: "" no video media | "err" (embedded decode
// failure) | "nostream" (loopback URL unavailable) | "video".
type mpVidSt struct {
	Host     string `json:"host"`
	Kind     string `json:"kind"`
	ErrText  string `json:"errText"`
	OpenExt  uiBtn  `json:"openExt"`
	NoStream string `json:"noStream"`
	URL      string `json:"url"`
	MSE      string `json:"mse"` // "" = plain src; else data-mse index URL
	Muted    bool   `json:"muted"`
	Ev       string `json:"ev"`     // element→Go transport mirror handler
	OnMeta   string `json:"onmeta"` // Ev + volume/first-frame nudge
	OnErr    string `json:"onerr"`
	DataIn   string `json:"dataIn"`  // trim IN local secs ("" = omit; drives loop-from-IN)
	DataOut  string `json:"dataOut"` // trim OUT local secs ("" = none; element stops there)
}

// mpWaveSt is the #mp-<host>-wave inner: the RAW Go-computed SVG plus the chip overlay
// and the per-media caption lines.
type mpWaveSt struct {
	SVG      string   `json:"svg"` // RAW: player.go mpWaveSVG
	HasChips bool     `json:"hasChips"`
	Enc      mpChipSt `json:"enc"`
	Loud     mpChipSt `json:"loud"`
	SeekTab  string   `json:"seekTab"` // "" = no seek-table chip
	Captions []string `json:"captions,omitempty"`
}

// mpHovSt is the #mp-<host>-hov readout. Raw=true replicates the two branches where Go
// returns the i18n string UNESCAPED (measuring-loudness / hover hint).
type mpHovSt struct {
	Text string `json:"text"`
	Raw  bool   `json:"raw"`
}

// mpTpSt is the #mp-<host>-tp inner (transport row + seek + volume).
type mpTpSt struct {
	Host       string    `json:"host"`
	Show       bool      `json:"show"`
	HasTabs    bool      `json:"hasTabs"`
	TabPrefix  string    `json:"tabPrefix"`
	TabActive  string    `json:"tabActive"`
	Tabs       []mpTabSt `json:"tabs,omitempty"`
	Play       uiBtn     `json:"play"`
	Stop       uiBtn     `json:"stop"`
	HasPreview bool      `json:"hasPreview"`
	Preview    uiBtn     `json:"preview"`
	HasTracks  bool      `json:"hasTracks"`
	Prev       uiBtn     `json:"prev"`
	TrackSel   selState  `json:"trackSel"`
	Next       uiBtn     `json:"next"`
	Demoted    bool      `json:"demoted"`
	MoreSel    selState  `json:"moreSel"`
	EditBtn    uiBtn     `json:"editBtn"`
	IsVideo    bool      `json:"isVideo"`
	OpenExt    uiBtn     `json:"openExt"`
	TipVideo   string    `json:"tipVideo"`             // legacy RAW tooltip markup (bridge)
	TipVideoS  *tipSt    `json:"tipVideoSt,omitempty"` // structured tipTopic("embedded-video")
	TimeTx     string    `json:"timeTx"`
	Seek       uiSlider  `json:"seek"`
	Vol        uiSlider  `json:"vol"`
}

// mpROSt is the #mp-<host>-ro trim readout (patched during handle drag).
type mpROSt struct {
	Value    string `json:"value"`
	DurLbl   string `json:"durLbl"`
	Dur      string `json:"dur"`
	InLbl    string `json:"inLbl"`
	In       string `json:"in"`
	OutLbl   string `json:"outLbl"`
	Out      string `json:"out"`
	KeepsLbl string `json:"keepsLbl"`
	Keeps    string `json:"keeps"`
}

// mpAlignSt2 is the dual-media alignment row. Exactly-one-of rides as explicit flags
// (Bar/Err/Line) - never "empty means the other branch".
type mpAlignSt2 struct {
	Bar       bool     `json:"bar"`
	BarFrac   float64  `json:"-"`
	BarPct    string   `json:"barPct"`
	BarCap    string   `json:"barCap"`
	Err       bool     `json:"err"`
	ErrText   string   `json:"errText"`
	Line      string   `json:"line"` // "" = no readout span
	LineVal   string   `json:"lineVal"`
	AlignBtn  uiBtn    `json:"alignBtn"`
	Nudges    []uiBtn  `json:"nudges,omitempty"`
	OffField  uiField  `json:"offField"`
	TipAlign  string   `json:"tipAlign"`             // legacy RAW tooltip markup (bridge)
	TipAlignS *tipSt   `json:"tipAlignSt,omitempty"` // structured tipTopic("dual-alignment")
	Warns     []string `json:"warns,omitempty"`
}

// mpSumSt is the "what will this export be" chip (also the edit-preset button).
type mpSumSt struct {
	Tx    string `json:"tx"`
	Act   string `json:"act"`
	Title string `json:"title"`
}

// mpExMediaSt is one media's export block. Loud is the shared loudness block as state (its own
// extraHTML rides inside it); LoudExtra stays RAW - the standalone gain-plan line shown when the
// PRESET normalizes without an override (Go mpLoudExtraHTML).
type mpExMediaSt struct {
	PresetSel selState `json:"presetSel"`
	Summary   mpSumSt  `json:"summary"`
	OutField  uiField  `json:"outField"`
	PickBtn   uiBtn    `json:"pickBtn"`
	Loud      loudSt   `json:"loud"`
	LoudExtra string   `json:"loudExtra"`
}

// mpExportSt is the #mp-<host>-export inner.
type mpExportSt struct {
	Medias    []mpExMediaSt `json:"medias,omitempty"`
	Exporting bool          `json:"exporting"`
	RunFrac   float64       `json:"-"`
	RunPct    string        `json:"runPct"`
	RunLabel  string        `json:"runLabel"`
	Cancel    uiBtn         `json:"cancel"`
	Dual      bool          `json:"dual"`
	ScopeSel  selState      `json:"scopeSel"`
	ExportBtn uiBtn         `json:"exportBtn"`
	Est       string        `json:"est"` // "" = not estimable
	LoudTx    string        `json:"loudTx"`
	Msg       string        `json:"msg"`
}

// mpEditSt is the #mp-<host>-edit inner (empty container while edit mode is OFF).
type mpEditSt struct {
	Host     string     `json:"host"`
	Show     bool       `json:"show"`
	InField  uiField    `json:"inField"`
	OutField uiField    `json:"outField"`
	SetIn    uiBtn      `json:"setIn"`
	SetOut   uiBtn      `json:"setOut"`
	AutoSel  selState   `json:"autoSel"`
	TipTrim  string     `json:"tipTrim"`             // legacy RAW tooltip markup (bridge)
	TipTrimS *tipSt     `json:"tipTrimSt,omitempty"` // structured tipTopic("trim-editor")
	RO       mpROSt     `json:"ro"`
	Dual     bool       `json:"dual"`
	Align    mpAlignSt2 `json:"alignRow"`   // `align` is a Zig keyword
	Export   mpExportSt `json:"exportPane"` // `export` is a Zig keyword
}

// mpInnerSt is the #mp-<host>-root inner (the whole component).
type mpInnerSt struct {
	Host     string   `json:"host"`
	Title    string   `json:"title"` // "" = no mp-title row (publish + named media only)
	Vid      mpVidSt  `json:"vid"`
	Dual     bool     `json:"dual"`
	Edit     bool     `json:"edit"`
	Wave     mpWaveSt `json:"wave"`
	LaneIn   string   `json:"laneIn"`
	LaneMid  string   `json:"laneMid"`
	LaneOut  string   `json:"laneOut"`
	LaneFull string   `json:"laneFull"`
	ZIn      uiBtn    `json:"zin"`
	ZOut     uiBtn    `json:"zout"`
	FitBtn   uiBtn    `json:"fit"`
	ZInfo    string   `json:"zinfo"` // "" = fit view (no zoom readout)
	Hov      mpHovSt  `json:"hov"`
	TipWave  string   `json:"tipWave"`             // legacy RAW tooltip markup (bridge)
	TipWaveS *tipSt   `json:"tipWaveSt,omitempty"` // structured tipTopic("wave-nav")
	Tp       mpTpSt   `json:"tp"`
	EditBox  mpEditSt `json:"editBox"`
}

// mpFullSt is mpHTML's state: the .mplayer root wrapper + the inner component.
type mpFullSt struct {
	Host  string    `json:"host"`
	Inner mpInnerSt `json:"inner"`
}

// ── state builders (impure: locks, config, i18n, smart-select registration) ──────

// mpInnerState resolves the whole component. SIDE-EFFECT ORDER IS LOAD-BEARING: the
// smart selects must register in the emit order of the pre-split renderer -
// mp-track/mp-more (transport) before mp-auto, mp-preset-<i>, mp-scope (edit strip).
func (u *UI) mpInnerState(t mpSt) mpInnerSt {
	st := mpInnerSt{
		Host:     t.host,
		Dual:     t.dual(),
		Edit:     t.edit,
		LaneIn:   i18n.T("player.label.dragSetIn"),
		LaneMid:  i18n.T("player.label.clickSeekPan"),
		LaneOut:  i18n.T("player.label.dragSetOut"),
		LaneFull: i18n.T("player.label.clickSeekPanZoom"),
		ZIn:      uiBtn{Label: "＋", Variant: "ghost", Act: "mp-zin:" + t.host},
		ZOut:     uiBtn{Label: "－", Variant: "ghost", Act: "mp-zout:" + t.host},
		FitBtn:   uiBtn{Label: i18n.T("player.fit"), Variant: "ghost", Act: "mp-fit:" + t.host},
		TipWaveS: tipTopicSt("wave-nav"),
	}
	if t.host == "publish" && t.name != "" {
		st.Title = t.name
	}
	st.Vid = u.mpVidState(t)
	st.Wave = u.mpWaveState(t)
	st.ZInfo = mpZoomInfoText(t)
	st.Hov = u.mpHovState(t)
	st.Tp = u.mpTpState(t)
	st.EditBox = u.mpEditState(t)
	return st
}

// mpZoomInfoText is the zoom readout ("" = fit view). Go owns the float math + clocks.
func mpZoomInfoText(t mpSt) string {
	if t.viewSpan >= 1 {
		return ""
	}
	lo, ln := t.axis()
	z := fmt.Sprintf("×%.1f", 1/t.viewSpan)
	if ln > 0 {
		a := lo + t.viewStart*ln
		z += " · " + pubClock(a) + "–" + pubClock(a+t.viewSpan*ln)
	}
	return z
}

// mpVidState resolves the embedded <video> element. The inline JS handlers are built
// here (they carry the %.3f config volume) and ride as attrQ values.
func (u *UI) mpVidState(t mpSt) mpVidSt {
	st := mpVidSt{Host: t.host}
	vi := -1
	for i := range t.media {
		if t.media[i].kind == "video" {
			vi = i
			break
		}
	}
	if vi < 0 {
		return st
	}
	if t.vid.err != "" { // degrade honestly - no external window
		st.Kind = "err"
		st.ErrText = i18n.T("player.label.embeddedFailed", i18n.A{"reason": t.vid.err})
		st.OpenExt = uiBtn{Label: i18n.T("player.openExternally"), Variant: "ghost", Act: "mp-openext:" + t.host}
		return st
	}
	url := u.mpMediaURL(t.media[vi].path)
	if url == "" {
		st.Kind = "nostream"
		st.NoStream = i18n.T("player.label.streamUnavailable")
		return st
	}
	st.Kind, st.URL = "video", url
	// element events → Go transport mirror (throttled to 1 Hz / state flips). The OUT-marker
	// stop runs element-side first (sub-frame latency; the pause flip then rides the send).
	st.Ev = `var o=parseFloat(this.dataset.out||'-1');` +
		`if(o>0&&!this.paused&&this.currentTime>=o){this.pause();try{this.currentTime=o}catch(e){}}` +
		`var s=Math.floor(this.currentTime);var p=this.paused?'1':'0';` +
		`if(String(s)!==this.dataset.s||p!==this.dataset.p){this.dataset.s=String(s);this.dataset.p=p;` +
		`window.rave(JSON.stringify({act:'mp-vtick:` + t.host + `',val:this.currentTime+'|'+(this.duration||0)+'|'+p}))}`
	st.OnErr = `window.rave(JSON.stringify({act:'mp-verr:` + t.host + `',val:''}))`
	if t.dual() && t.active == 0 { // audio recording is the source - video is a silent preview
		st.Muted = true
	} else { // active engine: trim window rides as data attrs (element-side OUT stop, loop-from-IN)
		st.DataIn = fmt.Sprintf("%.3f", clampF(t.inSec-t.mediaStart(vi), 0, math.Max(t.media[vi].dur, 0)))
		if t.outSec > 0 {
			st.DataOut = fmt.Sprintf("%.3f", clampF(t.outSec-t.mediaStart(vi), 0, math.Max(t.media[vi].dur, 0)))
		}
	}
	// Source strategy: a fragmented MP4 (OBS recording) streams via MSE (data-mse; shell.go
	// __mse feeds init + only the fragments around the playhead using the mp4frag index) -
	// Chromium's own demuxer would range-scan every moof before playing or seeking.
	if t.media[vi].fragOK {
		if iu := u.mpIndexURL(t.media[vi].path); iu != "" {
			st.MSE = iu
		}
	}
	vol := 1.0
	if u.svc.Cfg != nil {
		vol = u.svc.Cfg.Features.Player.VolumeOr()
	}
	st.OnMeta = st.Ev + fmt.Sprintf(`;this.volume=%.3f;if(this.currentTime===0){try{this.currentTime=0.05}catch(e){}}`, vol)
	return st
}

// mpWaveState resolves the wave fragment: the RAW Go SVG (cue-editor overlay + loudness
// viz included) plus the chips and caption lines.
func (u *UI) mpWaveState(t mpSt) mpWaveSt {
	st := mpWaveSt{Captions: []string{}}
	var ov *ceOverlay
	if len(t.media) > 0 {
		ov = u.ceSnapOverlay(t.host, t.media[0].path)
	}
	st.SVG = mpWaveSVG(&t, u.mpPlayheadAxis(&t), ov, u.mpWaveLoudViz(&t))
	if m := t.activeMedia(); m != nil {
		st.HasChips = true
		st.Enc = mpEncChipState(m)
		st.Loud = mpLoudChipState(m)
		if m.seekTabLoading {
			st.SeekTab = i18n.T("player.label.buildingSeekTable")
		}
	}
	for i := range t.media {
		m := &t.media[i]
		caption := ""
		switch {
		case m.peaksLoading:
			caption = i18n.T("player.label.analyzingWave")
		case m.peaksErr != "":
			caption = i18n.T("player.label.waveUnavailable") + m.peaksErr
		case len(m.peaks) == 0:
			caption = i18n.T("player.label.noWaveform")
		}
		if caption != "" {
			if t.dual() {
				caption = strings.ToUpper(m.kind) + ": " + caption
			}
			st.Captions = append(st.Captions, caption)
		}
	}
	return st
}

// mpEncChipState: compact source-encoding chip; hover expands the full probe detail.
func mpEncChipState(m *mpMedia) mpChipSt {
	st := mpChipSt{Rows: []mpKVRow{}}
	src := m.src
	if src == nil || !src.HasAudio {
		if m.srcLoading {
			st.Kind, st.Dim = "dim", i18n.T("player.label.probing")
		}
		return st
	}
	compact := strings.ToUpper(src.AudioCodec)
	if src.SampleRate > 0 {
		compact += fmt.Sprintf(" · %.1fk", float64(src.SampleRate)/1000)
	}
	if src.Channels > 0 {
		compact += fmt.Sprintf(" · %dch", src.Channels)
	}
	row := func(k, v string) { st.Rows = append(st.Rows, mpKVRow{K: k, V: v}) }
	row(i18n.T("player.label.audioCodec"), strings.ToUpper(src.AudioCodec))
	if src.SampleRate > 0 {
		row(i18n.T("library.enc.sampleRate"), fmt.Sprintf("%d Hz", src.SampleRate))
	}
	if src.Channels > 0 {
		row(i18n.T("library.enc.channels"), fmt.Sprintf("%d", src.Channels))
	}
	if src.AudioKbps > 0 {
		row(i18n.T("library.meta.bitrate"), fmt.Sprintf("%d kbps", src.AudioKbps))
	}
	if m.size > 0 {
		row(i18n.T("player.label.fileSize"), humanBytes(uint64(m.size)))
	}
	if src.DurationSec > 0 {
		row(i18n.T("library.meta.duration"), mmss(src.DurationSec))
	}
	if src.HasVideo {
		row(i18n.T("player.label.video"), fmt.Sprintf("%s %dx%d", strings.ToUpper(src.VideoCodec), src.Width, src.Height))
	}
	st.Kind, st.Text, st.Note = "chip", compact, i18n.T("player.label.encNote")
	return st
}

// mpLoudChipState: integrated-loudness badge; hover expands the full EBU R128 explainer.
func mpLoudChipState(m *mpMedia) mpChipSt {
	st := mpChipSt{Loud: true, Rows: []mpKVRow{}, Links: []mpLinkSt{}}
	l := m.loud
	if l == nil {
		if m.loudLoading {
			st.Kind, st.Dim = "dim", i18n.T("player.label.lufsLoading")
		}
		return st
	}
	row := func(k, v string) { st.Rows = append(st.Rows, mpKVRow{K: k, V: v}) }
	row(i18n.T("player.label.integrated"), fmt.Sprintf("%.1f LUFS", l.I))
	row(i18n.T("player.label.truePeak"), fmt.Sprintf("%.1f dBTP", l.TP))
	row(i18n.T("player.label.loudnessRange"), fmt.Sprintf("%.1f LU", l.LRA))
	st.Kind = "chip"
	st.Text = fmt.Sprintf("%.1f LUFS", l.I)
	st.Note = i18n.T("player.label.loudNote")
	st.Links = append(st.Links,
		mpLinkSt{URL: "https://tech.ebu.ch/publications/r128", Label: i18n.T("player.label.ebuSpecLink")},
		mpLinkSt{URL: "https://en.wikipedia.org/wiki/LUFS", Label: i18n.T("player.label.lufsLink")})
	return st
}

// mpHovState resolves the hover / momentary-LUFS readout line.
func (u *UI) mpHovState(t mpSt) mpHovSt {
	m := t.activeMedia()
	if m == nil {
		return mpHovSt{}
	}
	if mpIsSet(t.hovT) {
		tx := "@ " + pubClock(t.hovT)
		if mv, ok := m.loud.momAt(t.hovT - t.mediaStart(t.active)); ok {
			tx += fmt.Sprintf(" · M %.1f LUFS", mv)
		}
		return mpHovSt{Text: tx}
	}
	tr := u.mpEng(&t)
	if tr.loaded && tr.playing {
		if mv, ok := m.loud.momAt(tr.cur); ok {
			return mpHovSt{Text: i18n.T("player.label.momAtPlayhead", i18n.A{"lufs": fmt.Sprintf("%.1f", mv)})}
		}
	}
	// Both remaining branches splice the i18n string UNESCAPED in Go - replicated raw.
	if m.loud == nil && m.loudLoading {
		return mpHovSt{Text: i18n.T("player.label.measuringLoudness"), Raw: true}
	}
	return mpHovSt{Text: i18n.T("player.label.hoverHint"), Raw: true}
}

// mpTpState resolves the transport row. Registers mp-track / mp-more (in that order).
func (u *UI) mpTpState(t mpSt) mpTpSt {
	host := t.host
	st := mpTpSt{Host: host, Tabs: []mpTabSt{}, TrackSel: emptySel(), MoreSel: emptySel()}
	m := t.activeMedia()
	if m == nil {
		return st
	}
	st.Show = true

	tr := u.mpEng(&t)
	playLbl, playVar := "▶ "+i18n.T("player.play"), "go"
	switch {
	case t.audLoading && m.kind == "audio" && !tr.loaded:
		playLbl, playVar = "⏳ "+i18n.T("player.loadingAudio"), "outline"
	case tr.loaded && tr.playing:
		playLbl, playVar = "⏸ "+i18n.T("player.pause"), "outline"
	case tr.loaded && tr.paused:
		playLbl = "▶ " + i18n.T("player.resume")
	}
	// media switch (audio ↔ video of the same set)
	if len(t.media) > 1 {
		st.HasTabs = true
		st.TabPrefix = "mp-media:" + host + "\x1f"
		st.TabActive = fmt.Sprint(t.active)
		for i := range t.media {
			st.Tabs = append(st.Tabs, mpTabSt{Val: fmt.Sprint(i), Label: strings.ToUpper(t.media[i].kind)})
		}
	}
	st.Play = uiBtn{Label: playLbl, Variant: playVar, Act: "mp-play:" + host}
	st.Stop = uiBtn{Label: "⏹", Variant: "outline", Act: "mp-stop:" + host}
	if t.edit {
		st.HasPreview = true
		st.Preview = uiBtn{Label: "⇤ " + i18n.T("player.inPreview"), Variant: "secondary", Act: "mp-preview:" + host}
	}

	// track navigation: prev / current-track select / next
	if len(t.markers) > 0 {
		st.HasTracks = true
		st.Prev = uiBtn{Label: "⏮", Variant: "ghost", Act: "mp-prevtrack:" + host}
		cur := ""
		if t.lastTrackIdx >= 0 && t.lastTrackIdx < len(t.markers) {
			cur = fmt.Sprintf("%.2f", t.markers[t.lastTrackIdx].off)
		}
		opts := make([]ssOpt, 0, len(t.markers)+1)
		opts = append(opts, ssOpt{Val: "", Label: i18n.T("player.jumpToTrack")})
		for i, mk := range t.markers {
			opts = append(opts, ssOpt{Val: fmt.Sprintf("%.2f", mk.off),
				Label: fmt.Sprintf("%d. %s", i+1, mk.label), Badge: pubClock(mk.off)})
		}
		optsCopy := opts
		st.TrackSel = resolveSmartSelect("mp-track-"+host, "mp-jump:"+host, cur, func() []ssOpt { return optsCopy })
		st.Next = uiBtn{Label: "⏭", Variant: "ghost", Act: "mp-nexttrack:" + host}
	}

	editLbl, editVar := "✎ "+i18n.T("player.trimEdit"), "secondary"
	if t.edit {
		editLbl, editVar = i18n.T("player.done"), "outline"
	}
	if u.mpTrimDemoted(&t) {
		// collection/playlist context: trim/cut is occasional - lives in the ⋯ menu
		st.Demoted = true
		opts := []ssOpt{{Val: "", Label: "⋯ " + i18n.T("player.more")},
			{Val: "edit", Label: "✎ " + i18n.T("player.trimEdit"), Sub: i18n.T("player.trimEditSub")}}
		st.MoreSel = resolveSmartSelect("mp-more-"+host, "mp-more:"+host, "", func() []ssOpt { return opts })
	} else {
		st.EditBtn = uiBtn{Label: editLbl, Variant: editVar, Act: "mp-edit:" + host}
	}
	if m.kind == "video" {
		st.IsVideo = true
		st.OpenExt = uiBtn{Label: i18n.T("player.openExternally"), Variant: "ghost", Act: "mp-openext:" + host}
		st.TipVideoS = tipTopicSt("embedded-video")
	}

	cur, total := 0.0, m.dur
	if tr.loaded {
		cur = tr.cur
		if tr.total > 0 {
			total = tr.total
		}
	}
	st.TimeTx = pubClock(cur) + " / " + pubClock(total)

	// axis seek slider (waveform click does the same; the slider is the coarse thumb control)
	lo, ln := t.axis()
	frac := 0.0
	if p := u.mpPlayheadAxis(&t); ln > 0 && mpIsSet(p) {
		frac = clampF((p-lo)/ln, 0, 1)
	}
	st.Seek = newSlider(i18n.T("player.seek"), "mp-seek:"+host, 0, 1000, 1, math.Round(1000*frac), "")
	// global volume (persisted config; one value across every playback surface + restarts)
	vol := 1.0
	if u.svc.Cfg != nil {
		vol = u.svc.Cfg.Features.Player.VolumeOr()
	}
	st.Vol = newSlider(i18n.T("player.label.volume"), "mp-vol:"+host, 0, 100, 1, math.Round(vol*100), "%")
	return st
}

// mpEditState resolves the edit strip. Registers mp-auto, then the export pane's
// mp-preset-<host>-<i> and mp-scope-<host> - the pre-split emit order.
func (u *UI) mpEditState(t mpSt) mpEditSt {
	st := mpEditSt{Host: t.host, AutoSel: emptySel(), Dual: t.dual()}
	st.Export.Medias = []mpExMediaSt{}
	st.Export.ScopeSel = emptySel()
	if !t.edit {
		return st
	}
	st.Show = true
	outVal := "end"
	if t.outSec >= 0 {
		outVal = pubClockF(t.outSec)
	}
	st.InField = newField(i18n.T("player.label.inField"), "mp-in:"+t.host, pubClockF(t.inSec), "text")
	st.OutField = newField(i18n.T("player.label.outField"), "mp-out:"+t.host, outVal, "text")
	st.SetIn = uiBtn{Label: i18n.T("player.setIn"), Variant: "outline", Act: "mp-setin:" + t.host}
	st.SetOut = uiBtn{Label: i18n.T("player.setOut"), Variant: "outline", Act: "mp-setout:" + t.host}
	st.AutoSel = u.mpAutoSelState(t)
	st.TipTrimS = tipTopicSt("trim-editor")
	st.RO = mpROState(t)
	if t.dual() {
		st.Align = u.mpAlignState(t)
	}
	st.Export = u.mpExportState(t)
	return st
}

// mpAutoSelState is the condensed auto-trim menu (smart-select as an action menu).
func (u *UI) mpAutoSelState(t mpSt) selState {
	opts := []ssOpt{{Val: "", Label: i18n.T("player.label.autoTrim")}}
	if t.firstTrackSec >= 0 || t.lastTrackEndSec >= 0 {
		opts = append(opts, ssOpt{Val: "tracks", Label: i18n.T("player.label.tracklistBounds"), Sub: i18n.T("player.label.tracklistBoundsSub")})
	}
	sil := i18n.T("player.label.detectSilence")
	if t.detecting {
		sil = i18n.T("player.label.detectingSilence")
	}
	opts = append(opts, ssOpt{Val: "silence", Label: sil, Sub: i18n.T("player.label.silenceSub")})
	if t.lastFaderSec > 0 {
		opts = append(opts, ssOpt{Val: "fader", Label: i18n.T("player.label.lastFader"), Sub: i18n.T("player.label.lastFaderSub")})
	}
	if len(t.markers) > 0 {
		opts = append(opts,
			ssOpt{Val: "snapin", Label: i18n.T("player.label.snapIn"), Sub: i18n.T("player.label.snapInSub")},
			ssOpt{Val: "snapout", Label: i18n.T("player.label.snapOut"), Sub: i18n.T("player.label.snapOutSub")})
	}
	opts = append(opts, ssOpt{Val: "clear", Label: i18n.T("player.label.clearRange"), Sub: i18n.T("player.label.clearRangeSub")})
	optsCopy := opts
	return resolveSmartSelect("mp-auto-"+t.host, "mp-auto:"+t.host, "", func() []ssOpt { return optsCopy })
}

// mpROState resolves the live duration / IN / OUT / kept-length readout.
func mpROState(t mpSt) mpROSt {
	_, ln := t.axis()
	keeps := math.Max(t.axisOutEff()-t.inSec, 0)
	outTx := "end"
	if t.outSec >= 0 {
		outTx = pubClockF(t.outSec)
	}
	return mpROSt{
		Value:    fmt.Sprintf("in=%s out=%s keeps=%s", pubClockF(t.inSec), outTx, pubClockF(keeps)),
		DurLbl:   i18n.T("library.meta.duration"),
		Dur:      pubClock(ln),
		InLbl:    i18n.T("player.inPreview"),
		In:       pubClockF(t.inSec),
		OutLbl:   i18n.T("player.label.out"),
		Out:      outTx,
		KeepsLbl: i18n.T("player.label.keeps"),
		Keeps:    pubClockF(keeps),
	}
}

// mpAlignState resolves the dual-pair alignment row.
func (u *UI) mpAlignState(t mpSt) mpAlignSt2 {
	a := t.align
	st := mpAlignSt2{Nudges: []uiBtn{}, Warns: []string{}, TipAlignS: tipTopicSt("dual-alignment")}

	rel := i18n.T("player.label.after")
	if a.off < 0 {
		rel = i18n.T("player.label.before")
	}
	switch {
	case a.state == "run":
		st.Bar, st.BarFrac = true, a.pct/100
		st.BarPct, st.BarCap = progressPct(a.pct/100), i18n.T("player.label.aligning")+a.msg
	case a.state == "err":
		st.Err, st.ErrText = true, i18n.T("player.label.alignFailed")+a.msg
	case a.state == "ok" && !a.manual:
		st.Line = i18n.T("player.label.alignedLine", i18n.A{"offset": mpSignedClock(math.Abs(a.off)), "rel": rel, "label": a.label, "conf": fmt.Sprintf("%.2f", a.conf)})
	case a.manual:
		st.Line = i18n.T("player.label.manualLine", i18n.A{"offset": mpSignedClock(math.Abs(a.off)), "rel": rel})
	default:
		st.Line = i18n.T("player.label.priorLine", i18n.A{"offset": mpSignedClock(a.off)})
	}
	st.LineVal = fmt.Sprintf("off=%.2f conf=%.2f", a.off, a.conf)

	alignLbl := i18n.T("player.label.autoAlign")
	if a.state == "ok" || a.manual {
		alignLbl = i18n.T("player.label.reAlign")
	}
	st.AlignBtn = uiBtn{Label: alignLbl, Variant: "secondary", Act: "mp-align:" + t.host}
	for _, n := range []struct {
		ms  int
		lbl string
	}{{-1000, "−1s"}, {-100, "−0.1s"}, {100, "+0.1s"}, {1000, "+1s"}} {
		st.Nudges = append(st.Nudges, uiBtn{Label: n.lbl, Variant: "ghost",
			Act: fmt.Sprintf("mp-nudge:%s\x1f%d", t.host, n.ms)})
	}
	st.OffField = newField(i18n.T("player.label.videoOffsetField"), "mp-aoff:"+t.host, mpSignedClock(a.off), "text")

	// warn when the trim range extends past one of the recordings
	outEff := t.axisOutEff()
	for i := range t.media {
		s, d := t.mediaStart(i), t.media[i].dur
		if d <= 0 {
			continue
		}
		if t.inSec < s-0.05 || outEff > s+d+0.05 {
			st.Warns = append(st.Warns, i18n.T("player.label.rangeExceeds", i18n.A{"kind": t.media[i].kind}))
		}
	}
	return st
}

// mpExportState resolves the export pane. The shared loudness block crosses as structured state
// (components.go loudSt); its extraHTML (mpLoudExtraHTML) rides inside it as raw markup.
func (u *UI) mpExportState(t mpSt) mpExportSt {
	st := mpExportSt{Medias: []mpExMediaSt{}, ScopeSel: emptySel(), Dual: t.dual()}
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	presets := transcode.AllPresets(custom)

	for i := range t.media {
		m := &t.media[i]
		cur := u.mpActivePreset(m)
		out := m.outPath
		if out == "" {
			out = mpOutPath(m.path, cur)
		}
		opts := make([]ssOpt, 0, len(presets))
		for _, p := range presets {
			if m.kind == "audio" && !p.IsAudioOnly() {
				continue // a video/remux-to-mp4 preset makes no sense for an audio capture
			}
			opts = append(opts, ssOpt{Val: p.ID, Label: p.Label, Sub: p.Desc, Badge: strings.ToUpper(mpExt(m.path, p))})
		}
		optsCopy := opts
		curID := cur.ID
		if m.inline != nil {
			// unsaved editor result: select shows the base preset, the summary chip carries ✎
			curID = m.presetID
		}
		label := i18n.T("player.label.encodePreset")
		if t.dual() {
			label = i18n.T("player.label.kindPreset", i18n.A{"kind": strings.ToUpper(m.kind)})
		}
		ms := mpExMediaSt{}
		sel := resolveSmartSelect(fmt.Sprintf("mp-preset-%s-%d", t.host, i),
			fmt.Sprintf("mp-preset:%s\x1f%d", t.host, i), curID, func() []ssOpt { return optsCopy })
		sel.Label = label
		ms.PresetSel = sel
		ms.Summary = u.mpSummaryState(&t, i)
		ms.OutField = newField(i18n.T("player.label.outputFile"), fmt.Sprintf("mp-outpath:%s\x1f%d", t.host, i), out, "text")
		ms.PickBtn = uiBtn{Label: "…", Variant: "ghost",
			Act: "pick-save:" + mpExt(m.path, cur) + ":mp-outpath:" + t.host + "\x1f" + fmt.Sprint(i)}
		// per-media loudness override of the chosen preset (the shared block, components.go);
		// the live gain-plan line + pre-listen toggle collapse with the switch
		ms.Loud = newLoudSt(loudnessOpts{
			act:       func(f string) string { return fmt.Sprintf("mp-loud:%s\x1f%d\x1f%s", t.host, i, f) },
			toggleLbl: i18n.T("library.enc.normalizeOverride"),
			topic:     "mp-loudness",
			vals:      m.loudOv,
			override:  true,
			preset:    &cur,
			compact:   true,
			extraHTML: u.mpLoudExtraHTML(&t, i),
		})
		// the preset itself normalizes (no override): still show what will happen
		if !m.loudOv.On {
			if eff := u.mpEffPreset(m); eff.LoudnessOn {
				ms.LoudExtra = u.mpLoudExtraHTML(&t, i)
			}
		}
		st.Medias = append(st.Medias, ms)
	}

	if t.exporting {
		st.Exporting = true
		pct := i18n.A{"pct": fmt.Sprintf("%.0f", t.exportPct)}
		label := i18n.T("player.label.exportingPct", pct)
		switch t.exportStage { // caption tracks the job's actual stage (queued/prepare/measure)
		case "queued":
			label = i18n.T("player.label.exportQueued")
		case "prepare":
			label = i18n.T("player.label.exportPreparing")
		case "measure":
			label = i18n.T("player.label.exportMeasuring", pct)
		}
		st.RunFrac, st.RunPct, st.RunLabel = t.exportPct/100, progressPct(t.exportPct/100), label
		st.Cancel = uiBtn{Label: i18n.T("common.cancel"), Variant: "destructive", Act: "mp-excancel:" + t.host}
	} else {
		if t.dual() {
			scope := t.exportScope
			if scope == "" {
				scope = "both"
			}
			scopeOpts := []ssOpt{
				{Val: "both", Label: i18n.T("player.label.bothAligned"), Sub: i18n.T("player.label.bothAlignedSub")},
				{Val: "0", Label: i18n.T("player.label.audioOnly")},
				{Val: "1", Label: i18n.T("player.label.videoOnly")},
			}
			sel := resolveSmartSelect("mp-scope-"+t.host, "mp-scope:"+t.host, scope, func() []ssOpt { return scopeOpts })
			sel.Label = i18n.T("player.label.exportTarget")
			st.ScopeSel = sel
		}
		st.ExportBtn = uiBtn{Label: i18n.T("player.exportCut"), Variant: "primary", Act: "mp-export:" + t.host}
		st.Est = u.mpEstSizeLine(&t)
	}
	st.LoudTx, st.Msg = t.exportLoudTx, t.exportMsg
	return st
}

// mpSummaryState is the at-a-glance "what will this export be" chip: codec · bitrate ·
// container (+ ✎ when riding an unsaved edited preset, + →target LUFS when normalizing).
func (u *UI) mpSummaryState(t *mpSt, i int) mpSumSt {
	m := &t.media[i]
	eff := u.mpEffPreset(m)
	var parts []string
	if m.kind == "video" && !eff.IsAudioOnly() {
		v := strings.ToUpper(eff.VideoCodec)
		if eff.VideoCodec == "copy" {
			v = "COPY"
		} else if eff.Height > 0 {
			v += fmt.Sprintf(" %dp", eff.Height)
		}
		parts = append(parts, v)
	}
	switch eff.AudioCodec {
	case "copy":
		parts = append(parts, "AUDIO COPY")
	case "none", "":
		parts = append(parts, i18n.T("player.label.noAudio"))
	default:
		a := strings.ToUpper(eff.AudioCodec)
		switch {
		case eff.AudioVBR:
			a += fmt.Sprintf(" V%d", eff.AudioVBRQuality)
		case eff.AudioBitrateK > 0:
			a += fmt.Sprintf(" %dk", eff.AudioBitrateK)
		}
		parts = append(parts, a)
	}
	parts = append(parts, strings.ToUpper(mpExt(m.path, eff)))
	eff = transcode.MigrateLoudness(eff)
	if eff.LoudnessOn && transcode.LoudnessAppliesTo(eff.AudioCodec) {
		ti := eff.LoudnessI
		if ti == 0 {
			ti = transcode.DefaultLoudnessI
		}
		parts = append(parts, fmt.Sprintf("→ %g LUFS", ti))
	}
	tx := strings.Join(parts, " · ")
	if m.inline != nil {
		tx = "• " + tx // unsaved inline edit marker
	}
	return mpSumSt{Tx: tx, Act: fmt.Sprintf("mp-pedit:%s\x1f%d", t.host, i), Title: i18n.T("player.label.editPreset")}
}

// ── pure renderers (golden reference; byte-identical to native/zigui player.zig) ──

// mpFullHTMLOf mirrors mpHTML's wrapper. NOTE the quirk: only THIS id escapes the host.
func mpFullHTMLOf(st mpFullSt) string {
	return `<div id=mp-` + html.EscapeString(st.Host) + `-root class=mplayer>` + mpInnerHTMLOf(st.Inner) + `</div>`
}

func mpInnerHTMLOf(st mpInnerSt) string {
	host := st.Host
	var b strings.Builder

	// what's loaded (publish: the set / loose capture name; library shows it in the inspector)
	if st.Title != "" {
		b.WriteString(`<div class=mp-title data-label="player media" data-value=` + attrQ(st.Title) + `>` +
			html.EscapeString(st.Title) + `</div>`)
	}

	// embedded video (own patch target so the async fMP4-index resolve can swap
	// plain-src → MSE before playback starts)
	b.WriteString(`<div id=mp-` + host + `-vid>` + mpVidHTMLOf(st.Vid) + `</div>`)

	// wavebox: patched SVG inside, interaction lanes on top (lanes stay OUTSIDE the
	// patched region so pointer capture survives repaints)
	cls := "mp-wavebox"
	if st.Dual {
		cls += " mp-wavebox--dual"
	}
	b.WriteString(`<div class="` + cls + `" data-actwheel=` + attrQ("mp-zoomw:"+host) + `>`)
	b.WriteString(`<div id=mp-` + host + `-wave class=mp-wave>` + mpWaveHTMLOf(st.Wave) + `</div>`)
	if st.Edit {
		b.WriteString(`<div class="mp-lane mp-lane--in" data-actpos=` + attrQ("mp-hin:"+host) + ` title=` + attrQ(st.LaneIn) + `></div>`)
		b.WriteString(`<div class="mp-lane mp-lane--mid" data-actpos=` + attrQ("mp-surf:"+host) +
			` data-acthover=` + attrQ("mp-hov:"+host) + ` title=` + attrQ(st.LaneMid) + `></div>`)
		b.WriteString(`<div class="mp-lane mp-lane--out" data-actpos=` + attrQ("mp-hout:"+host) + ` title=` + attrQ(st.LaneOut) + `></div>`)
	} else {
		b.WriteString(`<div class="mp-lane mp-lane--full" data-actpos=` + attrQ("mp-surf:"+host) +
			` data-acthover=` + attrQ("mp-hov:"+host) + ` title=` + attrQ(st.LaneFull) + `></div>`)
	}
	b.WriteString(`</div>`)

	// zoom + hover readout row (compact; how-to lives in the tooltip)
	zinfo := ""
	if st.ZInfo != "" {
		zinfo = `<span class=mp-zinfo>` + html.EscapeString(st.ZInfo) + `</span>`
	}
	b.WriteString(`<div class=mp-zoom>` +
		st.ZIn.html() + st.ZOut.html() +
		st.FitBtn.html() + zinfo +
		`<span id=mp-` + host + `-hov class=mp-hovline>` + mpHovHTMLOf(st.Hov) + `</span>` +
		tipOr(st.TipWaveS, st.TipWave) + `</div>`)

	// transport
	b.WriteString(`<div id=mp-` + host + `-tp>` + mpTpHTMLOf(st.Tp) + `</div>`)

	// edit strip (empty container while OFF so the toggle patch has a target)
	b.WriteString(`<div id=mp-` + host + `-edit>` + mpEditHTMLOf(st.EditBox) + `</div>`)
	return b.String()
}

func mpVidHTMLOf(st mpVidSt) string {
	switch st.Kind {
	case "err":
		return `<div class=mp-viderr>` + hint("warn", st.ErrText) + btnRow(st.OpenExt.html()) + `</div>`
	case "nostream":
		return hint("warn", st.NoStream)
	case "video":
	default:
		return ""
	}
	src := ` src=` + attrQ(st.URL)
	if st.MSE != "" {
		src = ` data-mse=` + attrQ(st.MSE) + ` data-mse-src=` + attrQ(st.URL)
	}
	muted := ""
	if st.Muted {
		muted = " muted"
	}
	trim := ""
	if st.DataIn != "" {
		trim = ` data-in=` + attrQ(st.DataIn)
	}
	if st.DataOut != "" {
		trim += ` data-out=` + attrQ(st.DataOut)
	}
	return `<div class=mp-videobox><video id=` + attrQ("mp-vid-"+st.Host) + ` class=mp-video` + src +
		` preload=none playsinline` + muted + trim +
		` ontimeupdate=` + attrQ(st.Ev) + ` onplay=` + attrQ(st.Ev) + ` onpause=` + attrQ(st.Ev) +
		` onseeked=` + attrQ(st.Ev) + ` onended=` + attrQ(st.Ev) + ` onloadedmetadata=` + attrQ(st.OnMeta) +
		` onerror=` + attrQ(st.OnErr) + `></video></div>`
}

func mpWaveHTMLOf(st mpWaveSt) string {
	var b strings.Builder
	b.WriteString(`<div class=mp-wrap>`)
	b.WriteString(st.SVG)
	if st.HasChips {
		seekChip := ""
		if st.SeekTab != "" {
			seekChip = `<span class="wchip dim">` + html.EscapeString(st.SeekTab) + `</span>`
		}
		b.WriteString(`<div class=wchips>` + mpChipHTMLOf(st.Enc) + mpChipHTMLOf(st.Loud) + seekChip + `</div>`)
	}
	b.WriteString(`</div>`)
	for _, caption := range st.Captions {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(caption) + `</p>`)
	}
	return b.String()
}

// mpChipHTMLOf renders one wave overlay chip. The "dim" pill's text is spliced UNESCAPED
// (both Go originals did) - replicated verbatim.
func mpChipHTMLOf(st mpChipSt) string {
	switch st.Kind {
	case "dim":
		return `<span class="wchip dim">` + st.Dim + `</span>`
	case "chip":
	default:
		return ""
	}
	var d strings.Builder
	for _, r := range st.Rows {
		d.WriteString(`<span class=wc-row><b>` + html.EscapeString(r.K) + `</b>` + html.EscapeString(r.V) + `</span>`)
	}
	d.WriteString(`<span class=wc-note>` + html.EscapeString(st.Note) + `</span>`)
	for _, l := range st.Links {
		d.WriteString(`<a class=wc-link data-act=open-url data-val="` + l.URL + `">` + html.EscapeString(l.Label) + `</a>`)
	}
	// click/tap pins the card (checkbox pin, same pattern as the tooltip primitive);
	// hover still previews. ctl: `set enc-chip true` pins for screenshots.
	open := `<label class=wchip data-label="enc-chip">`
	if st.Loud {
		open = `<label class="wchip loud" data-label="lufs-chip">`
	}
	return open + `<input type=checkbox class=wchip-x tabindex=-1>` +
		html.EscapeString(st.Text) + `<span class=wchip-card>` + d.String() + `</span></label>`
}

func mpHovHTMLOf(st mpHovSt) string {
	if st.Raw {
		return st.Text
	}
	return html.EscapeString(st.Text)
}

func mpTpHTMLOf(st mpTpSt) string {
	if !st.Show {
		return ""
	}
	host := st.Host
	var b strings.Builder
	var row []string
	if st.HasTabs {
		items := make([][2]string, 0, len(st.Tabs))
		for _, tb := range st.Tabs {
			items = append(items, [2]string{tb.Val, tb.Label})
		}
		row = append(row, subTabs(st.TabPrefix, st.TabActive, items...))
	}
	row = append(row, st.Play.html(), st.Stop.html())
	if st.HasPreview {
		row = append(row, st.Preview.html())
	}
	if st.HasTracks {
		row = append(row, st.Prev.html())
		row = append(row, `<span class=mp-trksel>`+selHTML(st.TrackSel)+`</span>`)
		row = append(row, st.Next.html())
	}
	if st.Demoted {
		row = append(row, `<span class=mp-moresel>`+selHTML(st.MoreSel)+`</span>`)
	} else {
		row = append(row, st.EditBtn.html())
	}
	if st.IsVideo {
		row = append(row, st.OpenExt.html(), tipOr(st.TipVideoS, st.TipVideo))
	}
	b.WriteString(`<div class=mp-tp>` + strings.Join(row, "") +
		`<span class="mp-time" id=mp-` + host + `-time data-label=` + attrQ("player time") +
		` data-value=` + attrQ(st.TimeTx) + `>` +
		html.EscapeString(st.TimeTx) + `</span></div>`)
	b.WriteString(st.Seek.html())
	b.WriteString(`<div class=mp-volrow>` + st.Vol.html() + `</div>`)
	return b.String()
}

func mpEditHTMLOf(st mpEditSt) string {
	if !st.Show {
		return ""
	}
	host := st.Host
	var b strings.Builder
	b.WriteString(`<div class=mp-editbox>`)

	// row 1: trim range - fields + set-at-playhead + auto menu + live readout
	b.WriteString(`<div class=mp-erow>` +
		`<span class=mp-tfield>` + st.InField.html() + `</span>` +
		`<span class=mp-tfield>` + st.OutField.html() + `</span>` +
		st.SetIn.html() + st.SetOut.html() +
		`<span class=mp-autosel>` + selHTML(st.AutoSel) + `</span>` +
		tipOr(st.TipTrimS, st.TipTrim) + `</div>`)
	b.WriteString(`<div id=mp-` + host + `-ro>` + mpROHTMLOf(st.RO) + `</div>`)

	if st.Dual {
		b.WriteString(mpAlignHTMLOf(st.Align))
	}
	b.WriteString(`<div id=mp-` + host + `-export>` + mpExportHTMLOf(st.Export) + `</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

func mpROHTMLOf(st mpROSt) string {
	return `<div class=mp-rol data-label="trim readout" data-value="` + html.EscapeString(st.Value) + `">` +
		`<span>` + html.EscapeString(st.DurLbl) + ` <b>` + html.EscapeString(st.Dur) + `</b></span>` +
		`<span>` + html.EscapeString(st.InLbl) + ` <b>` + html.EscapeString(st.In) + `</b></span>` +
		`<span>` + html.EscapeString(st.OutLbl) + ` <b>` + html.EscapeString(st.Out) + `</b></span>` +
		`<span>` + html.EscapeString(st.KeepsLbl) + ` <b>` + html.EscapeString(st.Keeps) + `</b></span></div>`
}

func mpAlignHTMLOf(st mpAlignSt2) string {
	var b strings.Builder
	b.WriteString(`<div class=mp-align>`)
	switch {
	case st.Bar:
		b.WriteString(progressBarStr(st.BarPct, st.BarCap))
	case st.Err:
		b.WriteString(hint("bad", st.ErrText))
	}
	if st.Line != "" {
		b.WriteString(`<span class=mp-align-line data-label="align offset" data-value=` +
			attrQ(st.LineVal) + `>` + html.EscapeString(st.Line) + `</span>`)
	}
	b.WriteString(st.AlignBtn.html())
	for _, n := range st.Nudges {
		b.WriteString(n.html())
	}
	b.WriteString(`<span class=mp-aoff>` + st.OffField.html() + `</span>` + tipOr(st.TipAlignS, st.TipAlign))
	for _, w := range st.Warns {
		b.WriteString(hint("warn", w))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func mpExportHTMLOf(st mpExportSt) string {
	var b strings.Builder
	b.WriteString(`<div class=mp-export>`)
	for _, m := range st.Medias {
		b.WriteString(`<div class=mp-exmedia>`)
		// one dense row: preset · edit · summary · output path · picker (wraps when narrow)
		b.WriteString(`<div class="mp-erow mp-erow--preset">` +
			`<span class=mp-presel>` + selHTML(m.PresetSel) + `</span>` +
			mpSumHTMLOf(m.Summary) +
			`<span class=mp-outwrap><span class=mp-outfield>` + m.OutField.html() + `</span>` +
			m.PickBtn.html() + `</span>` +
			`</div>`)
		b.WriteString(m.Loud.html())
		b.WriteString(m.LoudExtra)
		b.WriteString(`</div>`)
	}

	if st.Exporting {
		b.WriteString(`<div class=mp-exrun>` + progressBarStr(st.RunPct, st.RunLabel) +
			st.Cancel.html() + `</div>`)
	} else {
		var rowBits []string
		if st.Dual {
			rowBits = append(rowBits, `<span class=mp-scopesel>`+selHTML(st.ScopeSel)+`</span>`)
		}
		rowBits = append(rowBits, st.ExportBtn.html())
		if st.Est != "" {
			rowBits = append(rowBits, `<span class=mp-est data-label="export estimate" data-value=`+attrQ(st.Est)+`>`+html.EscapeString(st.Est)+`</span>`)
		}
		b.WriteString(`<div class="mp-erow mp-erow--go">` + strings.Join(rowBits, "") + `</div>`)
	}
	if st.LoudTx != "" {
		b.WriteString(`<div class=mp-exloud>` + html.EscapeString(st.LoudTx) + `</div>`)
	}
	if st.Msg != "" {
		b.WriteString(`<div class=mp-exmsg>` + html.EscapeString(st.Msg) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// mpSumHTMLOf: the chip IS the edit-preset button (shows what you'll get, click to change).
func mpSumHTMLOf(st mpSumSt) string {
	return `<button class=mp-sum data-label="preset summary" data-value=` + attrQ(st.Tx) +
		` data-act=` + attrQ(st.Act) +
		` title=` + attrQ(st.Title) + `>` + html.EscapeString(st.Tx) + ` ✎</button>`
}

// ── bridges (zigui.Available() → Zig, else the Go renderers above) ───────────────

func mpRenderFull(st mpFullSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerV2", wireMpFull(st), zigui.RenderPlayerV2,
			zigui.RenderPlayer, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpFullHTMLOf(st)
}

func mpRenderInner(st mpInnerSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerRootV2", wireMpInner(st), zigui.RenderPlayerRootV2,
			zigui.RenderPlayerRoot, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpInnerHTMLOf(st)
}

func mpRenderVid(st mpVidSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerVidV2", wireMpVid(st), zigui.RenderPlayerVidV2,
			zigui.RenderPlayerVid, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpVidHTMLOf(st)
}

func mpRenderWave(st mpWaveSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerWaveV2", wireMpWave(st), zigui.RenderPlayerWaveV2,
			zigui.RenderPlayerWave, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpWaveHTMLOf(st)
}

func mpRenderTp(st mpTpSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerTpV2", wireMpTp(st), zigui.RenderPlayerTpV2,
			zigui.RenderPlayerTp, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpTpHTMLOf(st)
}

func mpRenderEdit(st mpEditSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerEditV2", wireMpEdit(st), zigui.RenderPlayerEditV2,
			zigui.RenderPlayerEdit, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpEditHTMLOf(st)
}

func mpRenderExport(st mpExportSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerExportV2", wireMpExport(st), zigui.RenderPlayerExportV2,
			zigui.RenderPlayerExport, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpExportHTMLOf(st)
}

func mpRenderRO(st mpROSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerROV2", wireMpRO(st), zigui.RenderPlayerROV2,
			zigui.RenderPlayerRO, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpROHTMLOf(st)
}

func mpRenderHov(st mpHovSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderPlayerHovV2", wireMpHov(st), zigui.RenderPlayerHovV2,
			zigui.RenderPlayerHov, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return mpHovHTMLOf(st)
}
