package webui

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
)

// Unified media player/editor - render side (state/engine: player_actions.go).
// One Go-built component for playing AND editing recorded media: embedded <video>
// (loopback Range stream) + waveform strip (encoding/LUFS chips, hover momentary
// loudness) as the navigation surface, one transport, and a compact edit strip
// (trim IN/OUT, auto-trim menu, aligned dual-media export). Layout is deliberately
// dense - the verbose explanations live in tooltips (tooltip.go), not inline prose.

// mpHTML renders the host's component ("" while nothing is loaded). Signature is fixed:
// the library inspector + both publish captures panes embed the result as raw markup.
func (u *UI) mpHTML(host string) string {
	t := u.mpSnap(host)
	if len(t.media) == 0 {
		return ""
	}
	return mpRenderFull(mpFullSt{Host: t.host, Inner: u.mpInnerState(t)})
}

// mpInnerHTML is the #mp-<host>-root inner (state + renderers: render_player.go).
func (u *UI) mpInnerHTML(t mpSt) string { return mpRenderInner(u.mpInnerState(t)) }

// ── embedded video (loopback stream; state mirrored to Go via mp-vtick) ─────────

func (u *UI) mpVideoHTML(t mpSt) string { return mpRenderVid(u.mpVidState(t)) }

// ── waveform (patched fragment) ─────────────────────────────────────────────────

func (u *UI) mpWaveInner(t mpSt) string { return mpRenderWave(u.mpWaveState(t)) }

// mpWaveCols caps the vertical columns the waveform decimates to (≈ one per viewport pixel).
// Peaks are ~10 ms bins (tens of thousands on a long set); the render only ever emits this many
// columns - each the min/max of the bins it spans - so SVG size is O(viewport), not O(bins).
// Zoomed in past this many visible bins we fall to one column per bin (razor-sharp).
const mpWaveCols = 800

// mpWavePeakGamma expands waveform amplitude contrast. Peaks are LINEAR max-abs bytes, so a
// loud, brick-walled master sits at ~0.85-1.0 everywhere and renders as a flat block where
// transients can't be told apart. A gamma>1 curve pushes the loud body DOWN while leaving the
// true peaks tall, so kicks/drops stand proud - the loud parts stop drowning the detail.
const mpWavePeakGamma = 1.9

// mpShapeAmp maps a 0-255 linear peak byte to a 0..1 display height via the contrast curve.
func mpShapeAmp(mx byte) float64 { return math.Pow(float64(mx)/255.0, mpWavePeakGamma) }

// mpWaveLoud is one band's loudness-curve overlay data (zero value = no curve).
type mpWaveLoud struct {
	mom  []float64 // momentary LUFS grid (media-local)
	step float64
	on   bool    // normalization active → draw target line + projected curve
	gain float64 // planned constant gain (dB)
	targ float64 // target integrated loudness (LUFS)
}

// mpWaveLoudViz builds the per-media loudness overlays (edit mode only - the curve is a
// mixing/export aid, not a playback decoration).
func (u *UI) mpWaveLoudViz(t *mpSt) []mpWaveLoud {
	if !t.edit {
		return nil
	}
	viz := make([]mpWaveLoud, len(t.media))
	for i := range t.media {
		m := &t.media[i]
		if m.loud == nil || len(m.loud.Mom) == 0 || m.loud.Step <= 0 {
			continue
		}
		v := mpWaveLoud{mom: m.loud.Mom, step: m.loud.Step}
		if p := u.mpPlanFor(t, i); p != nil && p.applies {
			v.targ = p.targetI
			if p.haveSrc && !p.res.Skipped {
				v.on, v.gain = true, p.res.GainDB
			}
		}
		viz[i] = v
	}
	return viz
}

// mpLufsY maps a momentary LUFS value into band-local y (top = loud). Fixed −45..−3 scale so
// the curve is comparable across tracks and the target line sits where you'd expect.
func mpLufsY(lufs, y0, bandH float64) float64 {
	const lo, hi = -45.0, -3.0
	f := (clampF(lufs, lo, hi) - lo) / (hi - lo)
	return y0 + bandH - f*bandH
}

// mpLoudPath emits a decimated polyline of mom(+gainDB) over the visible window.
func mpLoudPath(b *strings.Builder, v *mpWaveLoud, gainDB float64, t *mpSt, i int, y0, bandH, w float64, color string, width float64) {
	lo, ln := t.axis()
	if ln <= 0 || t.viewSpan <= 0 {
		return
	}
	start := t.mediaStart(i)
	const cols = 500
	var pts strings.Builder
	n := 0
	for c := 0; c <= cols; c++ {
		fx := float64(c) / cols
		axis := lo + (t.viewStart+fx*t.viewSpan)*ln
		local := axis - start
		idx := int(local / v.step)
		if local < 0 || idx < 0 || idx >= len(v.mom) {
			continue
		}
		mv := v.mom[idx]
		if mv <= -69.5 { // silence floor - drop to keep the curve honest
			continue
		}
		y := mpLufsY(mv+gainDB, y0, bandH)
		if n > 0 {
			pts.WriteByte(' ')
		}
		fmt.Fprintf(&pts, "%.1f,%.1f", fx*w, y)
		n++
	}
	if n < 2 {
		return
	}
	fmt.Fprintf(b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="%.1f" vector-effect="non-scaling-stroke"/>`,
		pts.String(), color, width)
}

// mpWaveSVG draws every media band on the shared axis in the visible zoom window, with
// trim dim/handles (edit), track/fader/cue markers, playhead (mint) and click cursor.
// ce (nil = off) adds the cue-editor layer: beatgrid lines, drop markers, beat cursor,
// cue selection + rubber band. lviz (nil = off) adds the loudness layer per band: the
// momentary curve, the normalize target line and the projected post-gain curve.
func mpWaveSVG(t *mpSt, playAxis float64, ce *ceOverlay, lviz []mpWaveLoud) string {
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
			nn := len(m.peaks)
			hasBands := len(m.bands) >= 3*nn // spectral colour available
			half := bandH/2 - 4

			// Column count = min(mpWaveCols, visible bins): zoomed out we aggregate many bins per
			// column (min/max); zoomed in past mpWaveCols visible bins we draw one column per bin
			// (razor-sharp). Columns always span the full view width, so partial-media placement stays
			// correct (off-media columns skip below). O(viewport), not O(bins).
			visLo := clampF((lo+t.viewStart*ln-start)/dur, 0, 1)
			visHi := clampF((lo+(t.viewStart+t.viewSpan)*ln-start)/dur, 0, 1)
			if visHi > visLo {
				cols := mpWaveCols
				if vb := (visHi - visLo) * float64(nn); vb > 0 && vb < float64(cols) {
					cols = int(math.Ceil(vb))
				}
				if cols < 1 {
					cols = 1
				}
				bw := w / float64(cols)
				var top, bot []string // mono-fallback min/max envelope points (top L→R; bot reversed on close)
				// Traktor-style spectral layers: one mirrored envelope per frequency band,
				// screen-blended so overlaps brighten (bass+mid → amber, all three → white
				// transients) instead of collapsing to a single dominant-band hue.
				envT := [3][]string{}
				envB := [3][]string{}
				for c := 0; c < cols; c++ {
					// axis span of this column → media-local bucket range
					a0 := lo + (t.viewStart+(float64(c)/float64(cols))*t.viewSpan)*ln
					a1 := lo + (t.viewStart+(float64(c+1)/float64(cols))*t.viewSpan)*ln
					m0, m1 := (a0-start)/dur, (a1-start)/dur
					if m1 <= 0 || m0 >= 1 {
						continue
					}
					i0 := clampInt(int(m0*float64(nn)), 0, nn-1)
					i1 := clampInt(int(m1*float64(nn)), i0, nn-1)
					var mx, loB, miB, hiB byte
					for k := i0; k <= i1; k++ {
						if m.peaks[k] > mx {
							mx = m.peaks[k]
						}
						if hasBands {
							if v := m.bands[3*k]; v > loB {
								loB = v
							}
							if v := m.bands[3*k+1]; v > miB {
								miB = v
							}
							if v := m.bands[3*k+2]; v > hiB {
								hiB = v
							}
						}
					}
					x := float64(c) * bw
					cx := x + bw*0.5
					if hasBands {
						// per-band mirrored amplitudes; highs get a lighter curve so
						// hats/transients spike above the low/mid body like Traktor.
						la := mpShapeAmp(loB) * half
						ma := mpShapeAmp(miB) * half
						ha := mpShapeHigh(hiB) * half
						envT[0] = append(envT[0], fmt.Sprintf("%.2f %.2f", cx, mid-la))
						envB[0] = append(envB[0], fmt.Sprintf("%.2f %.2f", cx, mid+la))
						envT[1] = append(envT[1], fmt.Sprintf("%.2f %.2f", cx, mid-ma))
						envB[1] = append(envB[1], fmt.Sprintf("%.2f %.2f", cx, mid+ma))
						envT[2] = append(envT[2], fmt.Sprintf("%.2f %.2f", cx, mid-ha))
						envB[2] = append(envB[2], fmt.Sprintf("%.2f %.2f", cx, mid+ha))
						continue
					}
					amp := mpShapeAmp(mx) * half // contrast curve: loud tracks stop flat-lining the band
					top = append(top, fmt.Sprintf("%.2f %.2f", cx, mid-amp))
					bot = append(bot, fmt.Sprintf("%.2f %.2f", cx, mid+amp))
				}
				if hasBands && len(envT[0]) > 0 {
					// three <path> elements total (was one <rect> per column): low = warm
					// red core, mid = green overlay, high = cold blue-white accents.
					// mix-blend-mode:screen composes them additively on the dark band.
					for li, fill := range [3]string{mpBandLow, mpBandMid, mpBandHigh} {
						var d strings.Builder
						d.WriteString("M" + envT[li][0])
						for _, pt := range envT[li][1:] {
							d.WriteString(" L" + pt)
						}
						for k := len(envB[li]) - 1; k >= 0; k-- {
							d.WriteString(" L" + envB[li][k])
						}
						d.WriteString(" Z")
						fmt.Fprintf(&b, `<path d="%s" fill="%s" style="mix-blend-mode:screen"/>`, d.String(), fill)
					}
				}
				// mono fallback (bands unavailable): one filled min/max envelope <path>, split at the
				// playhead by a hard-stop gradient - a single element whatever the column count.
				if !hasBands && len(top) > 0 {
					var d strings.Builder
					d.WriteString("M" + top[0])
					for _, pt := range top[1:] {
						d.WriteString(" L" + pt)
					}
					for k := len(bot) - 1; k >= 0; k-- {
						d.WriteString(" L" + bot[k])
					}
					d.WriteString(" Z")
					if playX >= 0 && playX <= w {
						gid := fmt.Sprintf("mpwenv-%s-%d", t.host, i)
						gf := clampF(playX/w, 0, 1)
						fmt.Fprintf(&b, `<defs><linearGradient id="%s" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="%.0f" y2="0"><stop offset="%.4f" stop-color="#F70864"/><stop offset="%.4f" stop-color="#fafafa" stop-opacity="0.45"/></linearGradient></defs>`, gid, w, gf, gf)
						fmt.Fprintf(&b, `<path d="%s" fill="url(#%s)"/>`, d.String(), gid)
					} else {
						fmt.Fprintf(&b, `<path d="%s" fill="#fafafa" fill-opacity="0.45"/>`, d.String())
					}
				}
			}
		} else {
			fmt.Fprintf(&b, `<line x1="0" y1="%.0f" x2="%.0f" y2="%.0f" stroke="rgba(255,255,255,0.14)" stroke-width="1"/>`, mid, w, mid)
		}

		// loudness layer: momentary curve (amber), and when normalizing also the projected
		// post-gain curve (mint) + the dashed target line the projection should ride on
		if lviz != nil && i < len(lviz) && len(lviz[i].mom) > 0 {
			v := &lviz[i]
			mpLoudPath(&b, v, 0, t, i, y0, bandH, w, "rgba(255,181,71,0.55)", 1.1)
			if v.on {
				ty := mpLufsY(v.targ, y0, bandH)
				fmt.Fprintf(&b, `<line x1="0" y1="%.1f" x2="%.0f" y2="%.1f" stroke="rgba(8,247,155,0.5)" stroke-width="1" stroke-dasharray="6,5" vector-effect="non-scaling-stroke"/>`, ty, w, ty)
				fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" fill="rgba(8,247,155,0.75)" font-size="9" font-family="monospace" text-anchor="end">%.1f</text>`, w-4, ty-3, v.targ)
				if math.Abs(v.gain) >= 0.05 {
					mpLoudPath(&b, v, v.gain, t, i, y0, bandH, w, "rgba(8,247,155,0.8)", 1.3)
				}
			}
		}

		// cue markers (library tracks) stay inside their band
		if dur > 0 {
			for _, cue := range m.cues {
				if x := toX(start + cue.StartMs/1000.0); x >= 0 && x <= w {
					op := 1.0
					if ce != nil && ce.mode != "" && cue.Sw != "" && cue.Sw != ce.mode {
						op = 0.3 // another software's cue: visible but muted in this mode
					}
					fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.25" stroke-opacity="%.2f" vector-effect="non-scaling-stroke"/>`, x, y0, x, y0+bandH, cueColor(cue.Kind), op)
				}
			}
			// drop markers (libdb enrichment); the editor layer draws them when active
			if ce == nil {
				for di, dms := range m.drops {
					if x := toX(start + dms/1000); x >= 0 && x <= w {
						fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#FFB547" stroke-width="1.25" vector-effect="non-scaling-stroke"/><text x="%.1f" y="%.1f" fill="#FFB547" font-size="10" font-family="monospace" text-anchor="middle">D%d</text>`,
							x, y0, x, y0+bandH, x, y0+11, di+1)
					}
				}
			}
		}
		if n > 1 {
			fmt.Fprintf(&b, `<text x="6" y="%.1f" fill="rgba(250,250,250,0.55)" font-size="11" font-family="monospace">%s</text>`, y0+13, strings.ToUpper(m.kind))
		}
	}

	// cue editor: beatgrid, drawn ON TOP of the wave (Traktor-style) so the phrase accents
	// stay visible over loud sections - beats are short top/bottom ticks (never full-height),
	// bars are taller, every 16 bars is a full-height phrase accent, and the grid anchor gets a
	// mint handle. Phrase counting aligns to the nearest preceding drop when drops exist (else beat 1).
	if ce != nil && ce.grid != nil && ln > 0 && t.viewSpan > 0 {
		g := ce.grid
		ga0 := (lo + t.viewStart*ln) * 1000
		ga1 := (lo + (t.viewStart+t.viewSpan)*ln) * 1000
		anchor := g.SnapMs(0)
		var dropBeats []int // beat index of each drop, sorted - phrase alignment refs
		for _, d := range ce.drops {
			dropBeats = append(dropBeats, int(math.Round(g.BeatsBetween(anchor, d))))
		}
		sort.Ints(dropBeats)
		ms := g.SnapMs(ga0)
		if ms > ga0 {
			ms = g.StepMs(ms, -1)
		}
		k := int(math.Round(g.BeatsBetween(anchor, ms)))
		for guard := 0; ms <= ga1 && guard < 4200; guard++ {
			beatPx := g.BeatLenMs(ms) / 1000 / (t.viewSpan * ln) * w
			if beatPx < 0.3 { // absurd zoom-out: nothing readable
				break
			}
			if x := toX(ms / 1000); x >= 0 && x <= w {
				ref := 0 // phrase origin = nearest preceding drop, else the grid anchor
				for _, db := range dropBeats {
					if db <= k {
						ref = db
					} else {
						break
					}
				}
				switch {
				case (k-ref)%64 == 0 && beatPx*64 >= 8: // every 16 bars: phrase accent
					fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="rgba(150,100,255,0.75)" stroke-width="1.5"/>`, x, x, h)
				case k%4 == 0 && beatPx*4 >= 6: // bar / downbeat: taller top+bottom ticks
					fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="16" stroke="rgba(255,255,255,0.55)" stroke-width="1"/><line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="rgba(255,255,255,0.55)" stroke-width="1"/>`, x, x, x, h-16, x, h)
				case beatPx >= 5: // beat: short ticks, top + bottom (not through)
					fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="8" stroke="rgba(255,255,255,0.30)" stroke-width="1"/><line x1="%.1f" y1="%.0f" x2="%.1f" y2="%.0f" stroke="rgba(255,255,255,0.30)" stroke-width="1"/>`, x, x, x, h-8, x, h)
				}
			}
			ms = g.StepMs(ms, 1)
			k++
		}
		// grid anchor handle (mint) - the pinned "beat 1" reference the grid extends from
		if x := toX(g.AnchorMs() / 1000); x >= 0 && x <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#08F79B" stroke-width="1.5" opacity="0.9"/><path d="M %.1f 0 h 9 v 6 l -9 6 z" fill="#08F79B"/>`, x, x, h, x)
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
				if ce.dsel[i] { // selected drop: same glow treatment as selected cues
					fmt.Fprintf(&b, `<rect x="%.1f" y="0" width="5" height="%.0f" fill="#FFB547" opacity="0.35"/>`, x-2.5, h)
				}
				fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#FFB547" stroke-width="2"/>`, x, x, h)
				fmt.Fprintf(&b, `<path d="M %.1f 8 l 7 -8 l -14 0 z" fill="#FFB547"/>`, x)
				fmt.Fprintf(&b, `<text x="%.1f" y="22" fill="#FFB547" font-size="11" font-family="monospace" text-anchor="middle">D%s</text>`, x, ceDropLabel(i)) // matches topbar/assign-grid naming (5th = X)
			}
		}
		// cue flags: pad slot (or M = memory cue) atop each cue line; hover = name + time
		for _, cue := range ce.cues {
			if cue.Kind == musiclib.CueGrid {
				continue
			}
			x := toX(cue.StartMs / 1000)
			if x < 0 || x > w {
				continue
			}
			lbl := "M"
			if cue.Hotcue >= 0 {
				lbl = fmt.Sprint(cue.Hotcue + 1)
			}
			tip := strings.TrimSpace(cue.Name)
			if tip != "" {
				tip += " · "
			}
			tip += pubClock(cue.StartMs / 1000)
			op := 0.92
			if cue.Sw != "" { // software-scoped cue: name its app; dim it outside its mode
				tip += " · " + i18n.T("library.ce.scopeOnly", i18n.A{"app": ceSoftwareLabel(cue.Sw)})
				if ce.mode != "" && cue.Sw != ce.mode {
					op = 0.35
				}
			}
			fmt.Fprintf(&b, `<g><title>%s</title><rect x="%.1f" y="%.0f" width="15" height="13" rx="2" fill="%s" opacity="%.2f"/><text x="%.1f" y="%.0f" fill="#0a0a0a" font-size="10" font-weight="700" font-family="monospace" text-anchor="middle">%s</text></g>`,
				html.EscapeString(tip), x-7.5, h-13, cueColor(cue.Kind), op, x, h-3, html.EscapeString(lbl))
		}
		// beat distances between neighbouring markers (cues + drops)
		if ce.grid != nil {
			var pos []float64
			for _, cue := range ce.cues {
				if cue.Kind != musiclib.CueGrid {
					pos = append(pos, cue.StartMs)
				}
			}
			pos = append(pos, ce.drops...)
			sort.Float64s(pos)
			for k := 0; k+1 < len(pos); k++ {
				a, z := pos[k], pos[k+1]
				if z-a < 1 { // coincident (drop sitting on a cue)
					continue
				}
				xa, xb := toX(a/1000), toX(z/1000)
				if xb < 0 || xa > w || xb-xa < 36 {
					continue
				}
				xa, xb = math.Max(xa, 0), math.Min(xb, w)
				beats := ce.grid.BeatsBetween(a, z)
				lbl := fmt.Sprintf("%.1f", beats)
				if math.Abs(beats-math.Round(beats)) < 0.05 {
					lbl = fmt.Sprintf("%.0f", math.Round(beats))
				}
				fmt.Fprintf(&b, `<g><title>%s</title><path d="M %.1f 30 v 4 h %.1f v -4" fill="none" stroke="rgba(250,250,250,0.25)" stroke-width="1"/><text x="%.1f" y="27" fill="rgba(250,250,250,0.75)" font-size="10" font-family="monospace" text-anchor="middle">%s</text></g>`,
					html.EscapeString(i18n.Tn("library.ce.beatsBetween", int(math.Round(beats)))), xa+1, xb-xa-2, (xa+xb)/2, lbl)
			}
		}
		if x := toX(ce.cursorMs / 1000); x >= 0 && x <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#fafafa" stroke-width="1.25" vector-effect="non-scaling-stroke"/>`, x, x, h)
			fmt.Fprintf(&b, `<path d="M %.1f %.0f l 6 8 l -12 0 z" fill="#fafafa" transform="rotate(180 %.1f %.0f)"/>`, x, h-8, x, h-4)
		}
	}

	// playhead or last-click cursor. id lets the client rAF runtime (shell.go __rt)
	// interpolate x between the coarse ~1 Hz Go re-renders (mpPushRealtime feeds it).
	// The unplayed side sits behind a dark veil whose sharp edge marks the cursor
	// unmistakably over any wave colour; __rt moves it too (id contract: <ph>-veil).
	// The line itself is a HAIRLINE: non-scaling-stroke keeps it 1.25 device px however
	// wide the strip renders (the 1000-unit viewBox used to fatten it on wide windows).
	if playX >= 0 && playX <= w {
		fmt.Fprintf(&b, `<rect id="mp-%s-ph-veil" x="%.2f" y="0" width="%.2f" height="%.0f" fill="rgba(5,5,8,0.42)"/>`, t.host, playX, w-playX, h)
		fmt.Fprintf(&b, `<line id="mp-%s-ph" x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="#fafafa" stroke-width="1.25" vector-effect="non-scaling-stroke"/>`, t.host, playX, playX, h)
	} else if mpIsSet(t.cursorSec) {
		if x := toX(t.cursorSec); x >= 0 && x <= w {
			fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.0f" stroke="rgba(250,250,250,0.5)" stroke-width="1" stroke-dasharray="3,3" vector-effect="non-scaling-stroke"/>`, x, x, h)
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// Spectral layer fills (screen-blended paths): bass = warm red, mids = green, highs =
// cold blue-white. Overlaps brighten additively - bass+mid reads amber, full-spectrum
// transients read white - so the strip carries frequency structure like a DJ deck wave
// (three <path> elements total; the old per-column dominant-hue rects flattened the mix
// AND cost one <rect> per column).
const (
	mpBandLow  = "rgb(214,48,36)"
	mpBandMid  = "rgb(24,190,110)"
	mpBandHigh = "rgb(150,210,255)"
)

// mpShapeHigh lifts the high band with a lighter curve than mpShapeAmp: hat/transient
// energy is small in absolute terms and the 1.9 gamma buried it inside the body.
func mpShapeHigh(v byte) float64 { return math.Pow(float64(v)/255.0, 1.35) }

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

func (u *UI) mpReadoutLine(t mpSt) string { return mpRenderHov(u.mpHovState(t)) }

// ── transport (patched fragment) ────────────────────────────────────────────────

func (u *UI) mpTransportHTML(t mpSt) string { return mpRenderTp(u.mpTpState(t)) }

// mpTrimDemoted: in library collection/playlist context trim/cut is an occasional
// operation - it hides behind the ⋯ menu until edit mode is on.
func (u *UI) mpTrimDemoted(t *mpSt) bool {
	if t.host != "library" || t.edit {
		return false
	}
	switch u.libSectionOr() {
	case "collection", "playlists", "history":
		return true
	}
	return false
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

func (u *UI) mpEditHTML(t mpSt) string { return mpRenderEdit(u.mpEditState(t)) }

// mpReadout is the live duration / IN / OUT / kept-length line (patched during handle drag).
func mpReadout(t mpSt) string { return mpRenderRO(mpROState(t)) }

// ── alignment row + export row (patched fragments; state: render_player.go) ─────

func (u *UI) mpExportHTML(t mpSt) string { return mpRenderExport(u.mpExportState(t)) }

// mpLoudExtraHTML renders the live gain-plan line + pre-listen toggle for media i.
func (u *UI) mpLoudExtraHTML(t *mpSt, i int) string {
	m := &t.media[i]
	p := u.mpPlanFor(t, i)
	if p == nil {
		return ""
	}
	var b strings.Builder
	if !p.applies {
		// warned above by loudnessFields; offer the one-tap fix for audio captures
		if m.kind == "audio" {
			b.WriteString(`<div class=mp-planline>` + btn(i18n.T("player.label.useFlacInstead"), "outline",
				fmt.Sprintf("mp-loudfix:%s\x1f%d", t.host, i), "") + `</div>`)
		}
		return b.String()
	}
	tone, line := "", ""
	src := fmt.Sprintf("%.1f", p.srcI)
	switch {
	case !p.haveSrc:
		if m.loudLoading {
			tone, line = "dim", i18n.T("player.plan.measuring")
		} else {
			tone, line = "dim", i18n.T("player.plan.noData")
		}
	case p.res.Skipped && p.srcI <= -70:
		tone = "info"
		line = i18n.T("player.plan.silence")
	case p.res.Skipped:
		tone = "info"
		line = i18n.T("player.plan.skipRaise", i18n.A{"src": src, "target": fmt.Sprintf("%.1f", p.targetI)})
	case p.res.PeakCapped:
		tone = "warn"
		line = i18n.T("player.plan.capped", i18n.A{
			"src": src, "out": fmt.Sprintf("%.1f", p.outI), "gain": fmt.Sprintf("%+.1f", p.res.GainDB),
			"tp": fmt.Sprintf("%.1f", p.srcTP), "ceil": fmt.Sprintf("%.1f", p.ceilTP)})
	default:
		tone = "ok"
		line = i18n.T("player.plan.line", i18n.A{
			"src": src, "out": fmt.Sprintf("%.1f", p.outI), "gain": fmt.Sprintf("%+.1f", p.res.GainDB)})
	}
	if p.haveSrc {
		if p.exact {
			line += " · " + i18n.T("player.plan.measured")
		} else {
			line += " · " + i18n.T("player.plan.estimate")
		}
	}
	b.WriteString(`<div class="mp-planline mp-planline--` + tone + `" data-label="loudness plan" data-value=` + attrQ(line) + `>` +
		html.EscapeString(line) + `</div>`)
	// pre-listen: audition the planned gain on the live engine (audio media only)
	if m.kind == "audio" && p.haveSrc && !p.res.Skipped {
		cls, lbl := "lt-chip lt-monitor", "🎧 "+i18n.T("player.label.monitorLoud")
		if t.monitorLoud {
			cls += " active"
			lbl = "🎧 " + i18n.T("player.label.monitorLoudOn", i18n.A{"gain": fmt.Sprintf("%+.1f", p.res.GainDB)})
		}
		b.WriteString(`<div class=mp-monrow><button class="` + cls + `" data-act=` + attrQ("mp-monloud:"+t.host) + `>` +
			html.EscapeString(lbl) + `</button>` + tipTopic("mp-prelisten") + `</div>`)
	}
	return b.String()
}

// mpEstSizeLine estimates the export's output size from the kept duration + effective
// bitrates ("" = not estimable, e.g. CRF video). Copy streams scale the source size.
func (u *UI) mpEstSizeLine(t *mpSt) string {
	var total int64
	for i := range t.media {
		if t.dual() {
			scope := t.exportScope
			if scope != "" && scope != "both" && scope != fmt.Sprint(i) {
				continue
			}
		}
		est := u.mpEstBytes(t, i)
		if est <= 0 {
			return "" // one media not estimable → no misleading total
		}
		total += est
	}
	if total <= 0 {
		return ""
	}
	return i18n.T("player.label.estSize", i18n.A{"size": humanBytes(uint64(total))})
}

// mpEstBytes estimates one media's output bytes (0 = unknown).
func (u *UI) mpEstBytes(t *mpSt, i int) int64 {
	m := &t.media[i]
	if m.dur <= 0 {
		return 0
	}
	s, e := t.mpTrimWindow(i)
	keep := m.dur - s
	if e > 0 {
		keep = e - s
	}
	if keep <= 0 {
		return 0
	}
	eff := u.mpEffPreset(m)
	frac := keep / m.dur

	audioK := 0.0
	switch eff.AudioCodec {
	case "copy":
		if m.src != nil && m.src.AudioKbps > 0 {
			audioK = float64(m.src.AudioKbps)
		} else if m.kind == "audio" && m.size > 0 {
			return int64(float64(m.size) * frac) // pure audio copy: scale the file
		} else {
			return 0
		}
	case "none", "":
		audioK = 0
	default:
		if eff.AudioVBR {
			audioK = 245 // MP3 V0 ballpark
		} else if eff.AudioBitrateK > 0 {
			audioK = float64(eff.AudioBitrateK)
		} else if eff.AudioCodec == "flac" {
			if m.src != nil && m.src.AudioKbps > 0 {
				audioK = float64(m.src.AudioKbps)
			} else {
				audioK = 900 // typical 16/44 FLAC
			}
		} else if strings.HasPrefix(eff.AudioCodec, "pcm-") {
			audioK = 1411
		} else {
			return 0
		}
	}

	videoK := 0.0
	if m.kind == "video" && !eff.IsAudioOnly() {
		switch eff.VideoCodec {
		case "copy":
			if m.src != nil && m.src.VideoKbps > 0 {
				videoK = float64(m.src.VideoKbps)
			} else if m.size > 0 {
				return int64(float64(m.size) * frac)
			} else {
				return 0
			}
		default:
			if eff.RateMode == "bitrate" && eff.BitrateK > 0 {
				videoK = float64(eff.BitrateK)
			} else {
				return 0 // CRF: size genuinely unknown - stay honest
			}
		}
	}
	return int64((audioK + videoK) * 1000 / 8 * keep)
}
