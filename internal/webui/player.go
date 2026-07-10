package webui

import (
	"fmt"
	"html"
	"math"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/transcode"
)

// Unified media player/editor - render side (state/engine: player_actions.go).
// One Go-built component for playing AND editing recorded media: embedded <video>
// (loopback Range stream) + waveform strip (encoding/LUFS chips, hover momentary
// loudness) as the navigation surface, one transport, and a compact edit strip
// (trim IN/OUT, auto-trim menu, aligned dual-media export). Layout is deliberately
// dense - the verbose explanations live in tooltips (tooltip.go), not inline prose.

// mpHTML renders the host's component ("" while nothing is loaded).
func (u *UI) mpHTML(host string) string {
	t := u.mpSnap(host)
	if len(t.media) == 0 {
		return ""
	}
	return `<div id=mp-` + html.EscapeString(host) + `-root class=mplayer>` + u.mpInnerHTML(t) + `</div>`
}

func (u *UI) mpInnerHTML(t mpSt) string {
	host := t.host
	var b strings.Builder

	// what's loaded (publish: the set / loose capture name; library shows it in the inspector)
	if host == "publish" && t.name != "" {
		b.WriteString(`<div class=mp-title data-label="player media" data-value=` + attrQ(t.name) + `>` +
			html.EscapeString(t.name) + `</div>`)
	}

	// embedded video (when the set has a video half / is a video file)
	b.WriteString(u.mpVideoHTML(t))

	// wavebox: patched SVG inside, interaction lanes on top (lanes stay OUTSIDE the
	// patched region so pointer capture survives repaints)
	cls := "mp-wavebox"
	if t.dual() {
		cls += " mp-wavebox--dual"
	}
	b.WriteString(`<div class="` + cls + `" data-actwheel=` + attrQ("mp-zoomw:"+host) + `>`)
	b.WriteString(`<div id=mp-` + host + `-wave class=mp-wave>` + u.mpWaveInner(t) + `</div>`)
	if t.edit {
		b.WriteString(`<div class="mp-lane mp-lane--in" data-actpos=` + attrQ("mp-hin:"+host) + ` title=` + attrQ(i18n.T("player.label.dragSetIn")) + `></div>`)
		b.WriteString(`<div class="mp-lane mp-lane--mid" data-actpos=` + attrQ("mp-surf:"+host) +
			` data-acthover=` + attrQ("mp-hov:"+host) + ` title=` + attrQ(i18n.T("player.label.clickSeekPan")) + `></div>`)
		b.WriteString(`<div class="mp-lane mp-lane--out" data-actpos=` + attrQ("mp-hout:"+host) + ` title=` + attrQ(i18n.T("player.label.dragSetOut")) + `></div>`)
	} else {
		b.WriteString(`<div class="mp-lane mp-lane--full" data-actpos=` + attrQ("mp-surf:"+host) +
			` data-acthover=` + attrQ("mp-hov:"+host) + ` title=` + attrQ(i18n.T("player.label.clickSeekPanZoom")) + `></div>`)
	}
	b.WriteString(`</div>`)

	// zoom + hover readout row (compact; how-to lives in the tooltip)
	b.WriteString(`<div class=mp-zoom>` +
		btn("＋", "ghost", "mp-zin:"+host, "") + btn("－", "ghost", "mp-zout:"+host, "") +
		btn(i18n.T("player.fit"), "ghost", "mp-fit:"+host, "") + u.mpZoomInfo(t) +
		`<span id=mp-` + host + `-hov class=mp-hovline>` + u.mpReadoutLine(t) + `</span>` +
		tipTopic("wave-nav") + `</div>`)

	// transport
	b.WriteString(`<div id=mp-` + host + `-tp>` + u.mpTransportHTML(t) + `</div>`)

	// edit strip (empty container while OFF so the toggle patch has a target)
	b.WriteString(`<div id=mp-` + host + `-edit>` + u.mpEditHTML(t) + `</div>`)
	return b.String()
}

func (u *UI) mpZoomInfo(t mpSt) string {
	if t.viewSpan >= 1 {
		return ""
	}
	lo, ln := t.axis()
	z := fmt.Sprintf("×%.1f", 1/t.viewSpan)
	if ln > 0 {
		a := lo + t.viewStart*ln
		z += " · " + pubClock(a) + "–" + pubClock(a+t.viewSpan*ln)
	}
	return `<span class=mp-zinfo>` + html.EscapeString(z) + `</span>`
}

// ── embedded video (loopback stream; state mirrored to Go via mp-vtick) ─────────

func (u *UI) mpVideoHTML(t mpSt) string {
	vi := -1
	for i := range t.media {
		if t.media[i].kind == "video" {
			vi = i
			break
		}
	}
	if vi < 0 {
		return ""
	}
	host := t.host
	if t.vid.err != "" { // degrade honestly - no external window
		return `<div class=mp-viderr>` + hint("warn",
			i18n.T("player.label.embeddedFailed", i18n.A{"reason": t.vid.err})) +
			btnRow(btn(i18n.T("player.openExternally"), "ghost", "mp-openext:"+host, "")) + `</div>`
	}
	url := u.mpMediaURL(t.media[vi].path)
	if url == "" {
		return hint("warn", i18n.T("player.label.streamUnavailable"))
	}
	// element events → Go transport mirror (throttled to 1 Hz / state flips)
	ev := `var s=Math.floor(this.currentTime);var p=this.paused?'1':'0';` +
		`if(String(s)!==this.dataset.s||p!==this.dataset.p){this.dataset.s=String(s);this.dataset.p=p;` +
		`window.rave(JSON.stringify({act:'mp-vtick:` + host + `',val:this.currentTime+'|'+(this.duration||0)+'|'+p}))}`
	onerr := `window.rave(JSON.stringify({act:'mp-verr:` + host + `',val:''}))`
	muted := ""
	if t.dual() && t.active == 0 { // audio recording is the source - video is a silent preview
		muted = " muted"
	}
	return `<div class=mp-videobox><video id=` + attrQ("mp-vid-"+host) + ` class=mp-video src=` + attrQ(url) +
		` preload=metadata playsinline` + muted +
		` ontimeupdate=` + attrQ(ev) + ` onplay=` + attrQ(ev) + ` onpause=` + attrQ(ev) +
		` onended=` + attrQ(ev) + ` onloadedmetadata=` + attrQ(ev+`;if(this.currentTime===0){try{this.currentTime=0.05}catch(e){}}`) + ` onerror=` + attrQ(onerr) +
		`></video></div>`
}

// ── waveform (patched fragment) ─────────────────────────────────────────────────

func (u *UI) mpWaveInner(t mpSt) string {
	var b strings.Builder
	b.WriteString(`<div class=mp-wrap>`)
	var ov *ceOverlay
	if len(t.media) > 0 {
		ov = u.ceSnapOverlay(t.host, t.media[0].path)
	}
	b.WriteString(mpWaveSVG(&t, u.mpPlayheadAxis(&t), ov))
	if m := t.activeMedia(); m != nil {
		b.WriteString(`<div class=wchips>` + mpEncChip(m) + mpLoudChip(m) + `</div>`)
	}
	b.WriteString(`</div>`)
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
			b.WriteString(`<p class=page-sub>` + html.EscapeString(caption) + `</p>`)
		}
	}
	return b.String()
}

// mpWaveSVG draws every media band on the shared axis in the visible zoom window, with
// trim dim/handles (edit), track/fader/cue markers, playhead (mint) and click cursor.
// ce (nil = off) adds the cue-editor layer: beatgrid lines, drop markers, beat cursor,
// cue selection + rubber band.
func mpWaveSVG(t *mpSt, playAxis float64, ce *ceOverlay) string {
	const w = 1000.0
	n := len(t.media)
	if n == 0 {
		return ""
	}
	bandH, gap := 170.0, 0.0
	if n > 1 {
		bandH, gap = 120.0, 14.0
	}
	h := bandH*float64(n) + gap*float64(n-1)
	lo, ln := t.axis()

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class=mp-svg viewBox="0 0 %d %d" preserveAspectRatio=none>`, int(w), int(h))
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="rgba(255,255,255,0.015)"/>`, int(w), int(h))

	toX := func(axisSec float64) float64 {
		if ln <= 0 || t.viewSpan <= 0 {
			return -1e9
		}
		return ((axisSec-lo)/ln - t.viewStart) / t.viewSpan * w
	}
	playX := -1e9
	if mpIsSet(playAxis) {
		playX = toX(playAxis)
	}

	// cue editor: beatgrid lines (only when beats are ≥5px apart at this zoom)
	if ce != nil && ce.grid != nil && ln > 0 && t.viewSpan > 0 {
		a0 := (lo + t.viewStart*ln) * 1000
		a1 := (lo + (t.viewStart+t.viewSpan)*ln) * 1000
		anchor := ce.grid.SnapMs(0)
		ms := ce.grid.SnapMs(a0)
		if ms > a0 {
			ms = ce.grid.StepMs(ms, -1)
		}
		guard := 0
		for ms <= a1 && guard < 4000 {
			guard++
			bl := ce.grid.BeatLenMs(ms)
			if bl/1000/(t.viewSpan*ln)*w < 5 { // too dense to read - skip grid at this zoom
				break
			}
			if x := toX(ms / 1000); x >= 0 && x <= w {
				op, sw := 0.10, 1.0
				if int(math.Round(ce.grid.BeatsBetween(anchor, ms)))%4 == 0 {
					op, sw = 0.28, 1.0 // downbeat
				}
				fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="rgba(250,250,250,%.2f)" stroke-width="%.0f"/>`, x, x, h, op, sw)
			}
			ms = ce.grid.StepMs(ms, 1)
		}
	}

	for i := 0; i < n; i++ {
		m := &t.media[i]
		y0 := float64(i) * (bandH + gap)
		mid := y0 + bandH/2
		start, dur := t.mediaStart(i), m.dur

		// media extent backdrop
		if dur > 0 {
			x0, x1 := math.Max(toX(start), 0), math.Min(toX(start+dur), w)
			if x1 > x0 {
				fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="rgba(255,255,255,0.025)"/>`, x0, y0, x1-x0, bandH)
			}
		}

		if len(m.peaks) > 0 && dur > 0 && ln > 0 {
			cols := 500
			bw := w / float64(cols)
			nn := float64(len(m.peaks))
			for c := 0; c < cols; c++ {
				// axis span of this column → media-local bucket range
				a0 := lo + (t.viewStart+(float64(c)/float64(cols))*t.viewSpan)*ln
				a1 := lo + (t.viewStart+(float64(c+1)/float64(cols))*t.viewSpan)*ln
				m0, m1 := (a0-start)/dur, (a1-start)/dur
				if m1 <= 0 || m0 >= 1 {
					continue
				}
				i0 := clampInt(int(m0*nn), 0, len(m.peaks)-1)
				i1 := clampInt(int(m1*nn), i0, len(m.peaks)-1)
				var mx byte
				for k := i0; k <= i1; k++ {
					if m.peaks[k] > mx {
						mx = m.peaks[k]
					}
				}
				x := float64(c) * bw
				bh := (float64(mx) / 255.0) * (bandH/2 - 4)
				col := "rgba(250,250,250,0.45)"
				if x+bw*0.5 < playX {
					col = "#F70864"
				}
				fmt.Fprintf(&b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" fill="%s"/>`, x, mid-bh, bw*0.8, bh*2, col)
			}
		} else {
			fmt.Fprintf(&b, `<line x1="0" y1="%.0f" x2="%.0f" y2="%.0f" stroke="rgba(255,255,255,0.14)" stroke-width="1"/>`, mid, w, mid)
		}

		// cue markers (library tracks) stay inside their band
		if dur > 0 {
			for _, cue := range m.cues {
				if x := toX(start + cue.StartMs/1000.0); x >= 0 && x <= w {
					fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.5"/>`, x, y0, x, y0+bandH, cueColor(cue.Kind))
				}
			}
		}
		if n > 1 {
			fmt.Fprintf(&b, `<text x="6" y="%.1f" fill="rgba(250,250,250,0.55)" font-size="11" font-family="monospace">%s</text>`, y0+13, strings.ToUpper(m.kind))
		}
	}

	// track-start markers (violet) + last-fader marker (amber) span all bands
	for _, mk := range t.markers {
		if x := toX(mk.off); x >= 0 && x <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#7C3AED" stroke-width="1" opacity="0.8"/>`, x, x, h)
		}
	}
	if t.lastFaderSec > 0 {
		if x := toX(t.lastFaderSec); x >= 0 && x <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#FFB547" stroke-width="1.5" stroke-dasharray="5,4"/>`, x, x, h)
		}
	}

	// trim dim + IN/OUT handles (edit mode)
	if t.edit && ln > 0 {
		inX, outX := toX(t.inSec), toX(t.axisOutEff())
		if x := math.Min(math.Max(inX, 0), w); x > 0 {
			fmt.Fprintf(&b, `<rect x="0" y="0" width="%.1f" height="%.0f" fill="rgba(0,0,0,0.55)"/>`, x, h)
		}
		if x := math.Min(math.Max(outX, 0), w); x < w {
			fmt.Fprintf(&b, `<rect x="%.1f" y="0" width="%.1f" height="%.0f" fill="rgba(0,0,0,0.55)"/>`, x, w-x, h)
		}
		if inX >= 0 && inX <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#F70864" stroke-width="2"/><path d="M %.1f 0 l 9 0 l -9 12 z" fill="#F70864"/>`, inX, inX, h, inX)
		}
		if outX >= 0 && outX <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#FF3E8A" stroke-width="2"/><path d="M %.1f %.0f l -9 0 l 9 -12 z" fill="#FF3E8A"/>`, outX, outX, h, outX, h)
		}
	}

	// cue editor: selected-cue glow, rubber band, drop markers, beat cursor
	if ce != nil {
		for i, cue := range ce.cues {
			if !ce.sel[i] {
				continue
			}
			if x := toX(cue.StartMs / 1000); x >= 0 && x <= w {
				fmt.Fprintf(&b, `<rect x="%.1f" y="0" width="5" height="%.0f" fill="%s" opacity="0.35"/>`, x-2.5, h, cueColor(cue.Kind))
			}
		}
		if ce.dragA >= 0 {
			xa, xb := toX(ce.dragA/1000), toX(ce.dragB/1000)
			if xb < xa {
				xa, xb = xb, xa
			}
			xa, xb = math.Max(xa, 0), math.Min(xb, w)
			if xb > xa {
				fmt.Fprintf(&b, `<rect x="%.1f" y="0" width="%.1f" height="%.0f" fill="rgba(247,8,100,0.12)" stroke="rgba(247,8,100,0.5)" stroke-width="1"/>`, xa, xb-xa, h)
			}
		}
		for i, d := range ce.drops {
			if x := toX(d / 1000); x >= 0 && x <= w {
				fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#FFB547" stroke-width="2"/>`, x, x, h)
				fmt.Fprintf(&b, `<path d="M %.1f 8 l 7 -8 l -14 0 z" fill="#FFB547"/>`, x)
				fmt.Fprintf(&b, `<text x="%.1f" y="22" fill="#FFB547" font-size="11" font-family="monospace" text-anchor="middle">D%d</text>`, x, i+1)
			}
		}
		if x := toX(ce.cursorMs / 1000); x >= 0 && x <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#fafafa" stroke-width="1.5"/>`, x, x, h)
			fmt.Fprintf(&b, `<path d="M %.1f %.0f l 6 8 l -12 0 z" fill="#fafafa" transform="rotate(180 %.1f %.0f)"/>`, x, h-8, x, h-4)
		}
	}

	// playhead (mint) or last-click cursor (white)
	if playX >= 0 && playX <= w {
		fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#08F79B" stroke-width="1.5"/>`, playX, playX, h)
	} else if mpIsSet(t.cursorSec) {
		if x := toX(t.cursorSec); x >= 0 && x <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="rgba(250,250,250,0.5)" stroke-width="1" stroke-dasharray="3,3"/>`, x, x, h)
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func cueColor(k musiclib.CueKind) string {
	switch k {
	case musiclib.CueHot:
		return "#F70864"
	case musiclib.CueLoop:
		return "#7C3AED"
	case musiclib.CueLoad:
		return "#08F79B"
	case musiclib.CueFade:
		return "#FFB547"
	default:
		return "#A78BFA"
	}
}

// ── hover / momentary-LUFS readout line ─────────────────────────────────────────

func (u *UI) mpReadoutLine(t mpSt) string {
	m := t.activeMedia()
	if m == nil {
		return ""
	}
	if mpIsSet(t.hovT) {
		tx := "@ " + pubClock(t.hovT)
		if mv, ok := m.loud.momAt(t.hovT - t.mediaStart(t.active)); ok {
			tx += fmt.Sprintf(" · M %.1f LUFS", mv)
		}
		return html.EscapeString(tx)
	}
	tr := u.mpEngineState(&t, m)
	if tr.loaded && tr.playing {
		if mv, ok := m.loud.momAt(tr.cur); ok {
			return html.EscapeString(i18n.T("player.label.momAtPlayhead", i18n.A{"lufs": fmt.Sprintf("%.1f", mv)}))
		}
	}
	if m.loud == nil && m.loudLoading {
		return i18n.T("player.label.measuringLoudness")
	}
	return i18n.T("player.label.hoverHint")
}

// ── overlay chips (encoding + EBU R128 loudness - "learn while using", never trimmed) ──

// mpEncChip: compact source-encoding chip; hover expands the full probe detail.
func mpEncChip(m *mpMedia) string {
	src := m.src
	if src == nil || !src.HasAudio {
		if m.srcLoading {
			return `<span class="wchip dim">` + i18n.T("player.label.probing") + `</span>`
		}
		return ""
	}
	compact := strings.ToUpper(src.AudioCodec)
	if src.SampleRate > 0 {
		compact += fmt.Sprintf(" · %.1fk", float64(src.SampleRate)/1000)
	}
	if src.Channels > 0 {
		compact += fmt.Sprintf(" · %dch", src.Channels)
	}
	row := func(k, v string) string {
		return `<span class=wc-row><b>` + html.EscapeString(k) + `</b>` + html.EscapeString(v) + `</span>`
	}
	var d strings.Builder
	d.WriteString(row(i18n.T("player.label.audioCodec"), strings.ToUpper(src.AudioCodec)))
	if src.SampleRate > 0 {
		d.WriteString(row(i18n.T("library.enc.sampleRate"), fmt.Sprintf("%d Hz", src.SampleRate)))
	}
	if src.Channels > 0 {
		d.WriteString(row(i18n.T("library.enc.channels"), fmt.Sprintf("%d", src.Channels)))
	}
	if src.AudioKbps > 0 {
		d.WriteString(row(i18n.T("library.meta.bitrate"), fmt.Sprintf("%d kbps", src.AudioKbps)))
	}
	if m.size > 0 {
		d.WriteString(row(i18n.T("player.label.fileSize"), humanBytes(uint64(m.size))))
	}
	if src.DurationSec > 0 {
		d.WriteString(row(i18n.T("library.meta.duration"), mmss(src.DurationSec)))
	}
	if src.HasVideo {
		d.WriteString(row(i18n.T("player.label.video"), fmt.Sprintf("%s %dx%d", strings.ToUpper(src.VideoCodec), src.Width, src.Height)))
	}
	d.WriteString(`<span class=wc-note>` + html.EscapeString(i18n.T("player.label.encNote")) + `</span>`)
	// click/tap pins the card (checkbox pin, same pattern as the tooltip primitive);
	// hover still previews. ctl: `set enc-chip true` pins for screenshots.
	return `<label class=wchip data-label="enc-chip"><input type=checkbox class=wchip-x tabindex=-1>` +
		html.EscapeString(compact) + `<span class=wchip-card>` + d.String() + `</span></label>`
}

// mpLoudChip: integrated-loudness badge; hover expands the full EBU R128 explainer.
func mpLoudChip(m *mpMedia) string {
	l := m.loud
	if l == nil {
		if m.loudLoading {
			return `<span class="wchip dim">` + i18n.T("player.label.lufsLoading") + `</span>`
		}
		return ""
	}
	row := func(k, v string) string {
		return `<span class=wc-row><b>` + html.EscapeString(k) + `</b>` + html.EscapeString(v) + `</span>`
	}
	var d strings.Builder
	d.WriteString(row(i18n.T("player.label.integrated"), fmt.Sprintf("%.1f LUFS", l.I)))
	d.WriteString(row(i18n.T("player.label.truePeak"), fmt.Sprintf("%.1f dBTP", l.TP)))
	d.WriteString(row(i18n.T("player.label.loudnessRange"), fmt.Sprintf("%.1f LU", l.LRA)))
	d.WriteString(`<span class=wc-note>` + html.EscapeString(i18n.T("player.label.loudNote")) + `</span>`)
	d.WriteString(`<a class=wc-link data-act=open-url data-val="https://tech.ebu.ch/publications/r128">` + html.EscapeString(i18n.T("player.label.ebuSpecLink")) + `</a>` +
		`<a class=wc-link data-act=open-url data-val="https://en.wikipedia.org/wiki/LUFS">` + html.EscapeString(i18n.T("player.label.lufsLink")) + `</a>`)
	return `<label class="wchip loud" data-label="lufs-chip"><input type=checkbox class=wchip-x tabindex=-1>` +
		html.EscapeString(fmt.Sprintf("%.1f LUFS", l.I)) + `<span class=wchip-card>` + d.String() + `</span></label>`
}

// ── transport (patched fragment) ────────────────────────────────────────────────

func (u *UI) mpTransportHTML(t mpSt) string {
	host := t.host
	m := t.activeMedia()
	if m == nil {
		return ""
	}
	var b strings.Builder

	tr := u.mpEngineState(&t, m)
	playLbl, playVar := "▶ "+i18n.T("player.play"), "go"
	switch {
	case tr.loaded && tr.playing:
		playLbl, playVar = "⏸ "+i18n.T("player.pause"), "outline"
	case tr.loaded && tr.paused:
		playLbl = "▶ " + i18n.T("player.resume")
	}
	var row []string
	// media switch (audio ↔ video of the same set)
	if len(t.media) > 1 {
		items := make([][2]string, len(t.media))
		for i := range t.media {
			items[i] = [2]string{fmt.Sprint(i), strings.ToUpper(t.media[i].kind)}
		}
		row = append(row, subTabs("mp-media:"+host+"\x1f", fmt.Sprint(t.active), items...))
	}
	row = append(row, btn(playLbl, playVar, "mp-play:"+host, ""), btn("⏹", "outline", "mp-stop:"+host, ""))
	if t.edit {
		row = append(row, btn("▶ "+i18n.T("player.inPreview"), "secondary", "mp-preview:"+host, ""))
	}

	// track navigation: prev / current-track select / next (replaces the old jump list)
	if len(t.markers) > 0 {
		row = append(row, btn("⏮", "ghost", "mp-prevtrack:"+host, ""))
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
		row = append(row, `<span class=mp-trksel>`+
			smartSelect("mp-track-"+host, "", "mp-jump:"+host, cur, func() []ssOpt { return optsCopy })+`</span>`)
		row = append(row, btn("⏭", "ghost", "mp-nexttrack:"+host, ""))
	}

	editLbl, editVar := "✎ "+i18n.T("player.trimEdit"), "secondary"
	if t.edit {
		editLbl, editVar = i18n.T("player.done"), "outline"
	}
	row = append(row, btn(editLbl, editVar, "mp-edit:"+host, ""))
	if m.kind == "video" {
		row = append(row, btn(i18n.T("player.openExternally"), "ghost", "mp-openext:"+host, ""), tipTopic("embedded-video"))
	}

	cur, total := 0.0, m.dur
	if tr.loaded {
		cur = tr.cur
		if tr.total > 0 {
			total = tr.total
		}
	}
	b.WriteString(`<div class=mp-tp>` + strings.Join(row, "") +
		`<span class="mp-time" id=mp-` + host + `-time data-label=` + attrQ("player time") +
		` data-value=` + attrQ(pubClock(cur)+" / "+pubClock(total)) + `>` +
		html.EscapeString(pubClock(cur)+" / "+pubClock(total)) + `</span></div>`)

	// axis seek slider (waveform click does the same; the slider is the coarse thumb control)
	lo, ln := t.axis()
	frac := 0.0
	if p := u.mpPlayheadAxis(&t); ln > 0 && mpIsSet(p) {
		frac = clampF((p-lo)/ln, 0, 1)
	}
	b.WriteString(slider(i18n.T("player.seek"), "mp-seek:"+host, 0, 1000, 1, math.Round(1000*frac), ""))
	return b.String()
}

// mpTimeText is the transport clock (tick-patched separately from the buttons).
func (u *UI) mpTimeText(t mpSt) string {
	m := t.activeMedia()
	if m == nil {
		return ""
	}
	cur, total := 0.0, m.dur
	if tr := u.mpEngineState(&t, m); tr.loaded {
		cur = tr.cur
		if tr.total > 0 {
			total = tr.total
		}
	}
	return pubClock(cur) + " / " + pubClock(total)
}

// ── edit strip (patched fragment; compact rows, verbose help in tooltips) ───────

func (u *UI) mpEditHTML(t mpSt) string {
	if !t.edit {
		return ""
	}
	host := t.host
	var b strings.Builder
	b.WriteString(`<div class=mp-editbox>`)

	// row 1: trim range - fields + set-at-playhead + auto menu + live readout
	outVal := "end"
	if t.outSec >= 0 {
		outVal = pubClockF(t.outSec)
	}
	b.WriteString(`<div class=mp-erow>` +
		`<span class=mp-tfield>` + field(i18n.T("player.label.inField"), "mp-in:"+host, pubClockF(t.inSec), "text") + `</span>` +
		`<span class=mp-tfield>` + field(i18n.T("player.label.outField"), "mp-out:"+host, outVal, "text") + `</span>` +
		btn(i18n.T("player.setIn"), "outline", "mp-setin:"+host, "") +
		btn(i18n.T("player.setOut"), "outline", "mp-setout:"+host, "") +
		u.mpAutoSelect(t) +
		tipTopic("trim-editor") + `</div>`)
	b.WriteString(`<div id=mp-` + host + `-ro>` + mpReadout(t) + `</div>`)

	if t.dual() {
		b.WriteString(u.mpAlignHTML(t))
	}
	b.WriteString(`<div id=mp-` + host + `-export>` + u.mpExportHTML(t) + `</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// mpAutoSelect is the condensed auto-trim menu (smart-select as an action menu).
func (u *UI) mpAutoSelect(t mpSt) string {
	host := t.host
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
	return `<span class=mp-autosel>` + smartSelect("mp-auto-"+host, "", "mp-auto:"+host, "", func() []ssOpt { return optsCopy }) + `</span>`
}

// mpReadout is the live duration / IN / OUT / kept-length line (patched during handle drag).
func mpReadout(t mpSt) string {
	_, ln := t.axis()
	keeps := math.Max(t.axisOutEff()-t.inSec, 0)
	outTx := "end"
	if t.outSec >= 0 {
		outTx = pubClockF(t.outSec)
	}
	return `<div class=mp-rol data-label="trim readout" data-value="` +
		html.EscapeString(fmt.Sprintf("in=%s out=%s keeps=%s", pubClockF(t.inSec), outTx, pubClockF(keeps))) + `">` +
		`<span>` + html.EscapeString(i18n.T("library.meta.duration")) + ` <b>` + html.EscapeString(pubClock(ln)) + `</b></span>` +
		`<span>` + html.EscapeString(i18n.T("player.inPreview")) + ` <b>` + html.EscapeString(pubClockF(t.inSec)) + `</b></span>` +
		`<span>` + html.EscapeString(i18n.T("player.label.out")) + ` <b>` + html.EscapeString(outTx) + `</b></span>` +
		`<span>` + html.EscapeString(i18n.T("player.label.keeps")) + ` <b>` + html.EscapeString(pubClockF(keeps)) + `</b></span></div>`
}

// ── alignment row (dual pairs; compact) ─────────────────────────────────────────

func (u *UI) mpAlignHTML(t mpSt) string {
	host := t.host
	a := t.align
	var b strings.Builder
	b.WriteString(`<div class=mp-align>`)

	rel := i18n.T("player.label.after")
	if a.off < 0 {
		rel = i18n.T("player.label.before")
	}
	line := ""
	switch {
	case a.state == "run":
		b.WriteString(progressBar(a.pct/100, i18n.T("player.label.aligning")+a.msg))
	case a.state == "err":
		b.WriteString(hint("bad", i18n.T("player.label.alignFailed")+a.msg))
	case a.state == "ok" && !a.manual:
		line = i18n.T("player.label.alignedLine", i18n.A{"offset": mpSignedClock(math.Abs(a.off)), "rel": rel, "label": a.label, "conf": fmt.Sprintf("%.2f", a.conf)})
	case a.manual:
		line = i18n.T("player.label.manualLine", i18n.A{"offset": mpSignedClock(math.Abs(a.off)), "rel": rel})
	default:
		line = i18n.T("player.label.priorLine", i18n.A{"offset": mpSignedClock(a.off)})
	}
	if line != "" {
		b.WriteString(`<span class=mp-align-line data-label="align offset" data-value=` +
			attrQ(fmt.Sprintf("off=%.2f conf=%.2f", a.off, a.conf)) + `>` + html.EscapeString(line) + `</span>`)
	}

	alignLbl := i18n.T("player.label.autoAlign")
	if a.state == "ok" || a.manual {
		alignLbl = i18n.T("player.label.reAlign")
	}
	nudge := func(ms int, lbl string) string {
		return btn(lbl, "ghost", fmt.Sprintf("mp-nudge:%s\x1f%d", host, ms), "")
	}
	b.WriteString(btn(alignLbl, "secondary", "mp-align:"+host, "") +
		nudge(-1000, "−1s") + nudge(-100, "−0.1s") + nudge(100, "+0.1s") + nudge(1000, "+1s") +
		`<span class=mp-aoff>` + field(i18n.T("player.label.videoOffsetField"), "mp-aoff:"+host, mpSignedClock(a.off), "text") + `</span>` +
		tipTopic("dual-alignment"))

	// warn when the trim range extends past one of the recordings
	outEff := t.axisOutEff()
	for i := range t.media {
		s, d := t.mediaStart(i), t.media[i].dur
		if d <= 0 {
			continue
		}
		if t.inSec < s-0.05 || outEff > s+d+0.05 {
			b.WriteString(hint("warn", i18n.T("player.label.rangeExceeds", i18n.A{"kind": t.media[i].kind})))
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ── export row (patched fragment; compact) ──────────────────────────────────────

func (u *UI) mpExportHTML(t mpSt) string {
	host := t.host
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	presets := transcode.AllPresets(custom)

	var b strings.Builder
	b.WriteString(`<div class=mp-export>`)
	for i := range t.media {
		m := &t.media[i]
		cur := mpPreset(u, m.presetID)
		out := m.outPath
		if out == "" {
			out = mpOutPath(m.path, cur)
		}
		opts := make([]ssOpt, 0, len(presets))
		for _, p := range presets {
			if m.kind == "audio" && !p.IsAudioOnly() && p.ID != "remux" {
				continue
			}
			opts = append(opts, ssOpt{Val: p.ID, Label: p.Label, Sub: p.Desc, Badge: strings.ToUpper(p.Container)})
		}
		optsCopy := opts
		label := i18n.T("player.label.encodePreset")
		if t.dual() {
			label = i18n.T("player.label.kindPreset", i18n.A{"kind": strings.ToUpper(m.kind)})
		}
		b.WriteString(`<div class=mp-erow>` +
			`<span class=mp-presel>` + smartSelect(fmt.Sprintf("mp-preset-%s-%d", host, i), label,
			fmt.Sprintf("mp-preset:%s\x1f%d", host, i), cur.ID, func() []ssOpt { return optsCopy }) + `</span>` +
			`<span class=mp-outfield>` + field(i18n.T("player.label.outputFile"), fmt.Sprintf("mp-outpath:%s\x1f%d", host, i), out, "text") + `</span>` +
			btn("…", "ghost", "pick-save:"+mpExt(m.path, cur)+":mp-outpath:"+host+"\x1f"+fmt.Sprint(i), "") +
			`</div>`)
	}

	if t.exporting {
		b.WriteString(progressBar(t.exportPct/100, i18n.T("player.label.exportingPct", i18n.A{"pct": fmt.Sprintf("%.0f", t.exportPct)})))
	} else {
		var rowBits []string
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
			rowBits = append(rowBits, `<span class=mp-scopesel>`+
				smartSelect("mp-scope-"+host, i18n.T("player.label.exportTarget"), "mp-scope:"+host, scope, func() []ssOpt { return scopeOpts })+`</span>`)
		}
		rowBits = append(rowBits, btn(i18n.T("player.exportCut"), "primary", "mp-export:"+host, ""))
		b.WriteString(`<div class=mp-erow>` + strings.Join(rowBits, "") + `</div>`)
	}
	if t.exportMsg != "" {
		b.WriteString(`<div class=mp-exmsg>` + html.EscapeString(t.exportMsg) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}
