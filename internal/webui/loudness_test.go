package webui

import (
	"fmt"
	"html"
	"math"
	"strings"
	"testing"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/transcode"
)

// Phase B-1a gate: loudnessFields now renders from the structured loudSt (so the block can cross
// the ABI as data instead of pre-rendered markup). loudnessFieldsLegacy below is the pre-split
// implementation kept VERBATIM as the golden reference - the split is only correct if the two
// agree byte-for-byte on every branch. Fixtures: off / partial (override blanks) / full /
// compact / warn / extra / unit edges.

// loudnessFieldsLegacy is components.go loudnessFields as it was before the structured split.
// Do not "clean up" - it is a pinned reference, not live code.
func loudnessFieldsLegacy(o loudnessOpts) string {
	tx := func(f, def float64) (val, ph string) {
		if !o.override {
			return trimNum(f), ""
		}
		if f == 0 {
			return "", trimNum(def)
		}
		return trimNum(f), trimNum(def)
	}
	var b strings.Builder
	grp := "pb-grp"
	if o.compact {
		grp = "pb-grp pb-grp--compact"
	}
	b.WriteString(`<div class="` + grp + `">`)
	b.WriteString(toggleRowTip(o.toggleLbl, o.act("loudon"), o.vals.On, tipTopic(o.topic)))
	if o.vals.On {
		iv, iph := tx(o.vals.I, transcode.DefaultLoudnessI)
		tv, tph := tx(o.vals.TP, transcode.DefaultLoudnessTP)
		if o.compact {
			effI := o.vals.I
			if o.override && effI == 0 {
				effI = transcode.DefaultLoudnessI
			}
			b.WriteString(`<div class=lt-chips>`)
			for _, lt := range transcode.LoudnessTargets() {
				cls := "lt-chip"
				if math.Abs(effI-lt.I) < 0.01 {
					cls += " active"
				}
				b.WriteString(`<button class="` + cls + `" data-act=` + attrQ(o.act("loudtarget")) +
					` data-val=` + attrQ(fmt.Sprintf("%g|%g", lt.I, lt.TP)) + ` title=` + attrQ(lt.Label) + `>` +
					html.EscapeString(ltChipLabel(lt)) + `</button>`)
			}
			b.WriteString(`</div>`)
			b.WriteString(`<div class=lt-fields>` +
				`<span class=lt-field>` + pbFieldEx(i18n.T("library.enc.lufsTarget"), o.act("loudi"), iv, "number", iph, "") + `</span>` +
				`<span class=lt-field>` + pbFieldEx(i18n.T("library.enc.truePeak"), o.act("loudtp"), tv, "number", tph, "") + `</span>` +
				`<span class=lt-raise>` + toggleRow(i18n.T("library.enc.raiseQuiet"), o.act("loudraise"), o.vals.RaiseOnly) + `</span></div>`)
		} else {
			b.WriteString(pbFieldEx(i18n.T("library.enc.lufsTarget"), o.act("loudi"), iv, "number", iph, i18n.T("library.enc.lufsHint")))
			b.WriteString(pbFieldEx(i18n.T("library.enc.truePeak"), o.act("loudtp"), tv, "number", tph, ""))
			b.WriteString(toggleRow(i18n.T("library.enc.raiseQuiet"), o.act("loudraise"), o.vals.RaiseOnly))
		}
		if o.preset != nil && !transcode.LoudnessAppliesTo(o.preset.AudioCodec) {
			b.WriteString(hint("warn", i18n.T("library.enc.loudNeedsReencode")))
		}
		b.WriteString(o.extraHTML)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// loudFx is the shared fixture matrix (used by the parity gate AND the Zig golden suites).
func loudFx() map[string]loudnessOpts {
	libAct := func(f string) string { return "lib-pf:" + f }
	mpAct := func(f string) string { return "mp-loud:publish\x1f0\x1f" + f }
	aeAct := func(f string) string { return "auto-ed:step2\x1f" + f }
	flac := transcode.Preset{ID: "flac", AudioCodec: "flac"}
	copyAudio := transcode.Preset{ID: "remux", AudioCodec: "copy"}
	noAudio := transcode.Preset{ID: "silent", AudioCodec: "none"}

	return map[string]loudnessOpts{
		// absent: the switch is off, so nothing behind it renders
		"off": {act: libAct, toggleLbl: "Normalize loudness", topic: "enc-loudness",
			vals: loudnessVals{}, preset: &flac},
		"offCompact": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{}, override: true, preset: &flac, compact: true},
		// full: builder surface (the draft IS the preset - values printed, no placeholders)
		"full": {act: libAct, toggleLbl: "Normalize loudness", topic: "enc-loudness",
			vals: loudnessVals{On: true, I: -14, TP: -1, RaiseOnly: true}, preset: &flac},
		"fullNoRaise": {act: libAct, toggleLbl: "Normalize loudness", topic: "enc-loudness",
			vals: loudnessVals{On: true, I: -23, TP: -2}, preset: &flac},
		// partial: override surface with unset targets → blank values behind default placeholders
		"overrideUnset": {act: aeAct, toggleLbl: "Override normalization", topic: "auto-loudness",
			vals: loudnessVals{On: true}, override: true, preset: &flac},
		"overrideIOnly": {act: aeAct, toggleLbl: "Override normalization", topic: "auto-loudness",
			vals: loudnessVals{On: true, I: -16}, override: true, preset: &flac},
		"overrideTPOnly": {act: aeAct, toggleLbl: "Override normalization", topic: "auto-loudness",
			vals: loudnessVals{On: true, TP: -0.3}, override: true, preset: &flac},
		// no resolvable preset: no codec warning either way
		"noPreset": {act: aeAct, toggleLbl: "Override normalization", topic: "auto-loudness",
			vals: loudnessVals{On: true, I: -18, TP: -1}, override: true},
		// codec can't normalize → warn hint
		"copyCodec": {act: aeAct, toggleLbl: "Override normalization", topic: "auto-loudness",
			vals: loudnessVals{On: true, I: -14, TP: -1}, override: true, preset: &copyAudio},
		"noAudioCodec": {act: libAct, toggleLbl: "Normalize loudness", topic: "enc-loudness",
			vals: loudnessVals{On: true, I: -14, TP: -1}, preset: &noAudio},
		// compact: quick-pick chips; active chip tracks I (unset override = the -14 default)
		"compactDefault": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{On: true}, override: true, preset: &flac, compact: true},
		"compactApple": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{On: true, I: -16, TP: -1, RaiseOnly: true}, override: true, preset: &flac, compact: true},
		"compactClub": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{On: true, I: -8, TP: -0.3}, override: true, preset: &flac, compact: true},
		"compactNoChipMatch": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{On: true, I: -11.5, TP: -1}, override: true, preset: &flac, compact: true},
		"compactBuilder": {act: libAct, toggleLbl: "Normalize loudness", topic: "enc-loudness",
			vals: loudnessVals{On: true, I: -14, TP: -1}, preset: &flac, compact: true},
		"compactExtra": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{On: true, I: -14, TP: -1}, override: true, preset: &flac, compact: true,
			extraHTML: `<div class=mp-exloud>applied +3.2 dB <b>&amp; held</b></div>`},
		"compactCopyCodec": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{On: true, I: -14, TP: -1}, override: true, preset: &copyAudio, compact: true,
			extraHTML: `<div class=mp-exloud>no re-encode</div>`},
		// unit edges: chip-match tolerance boundary, fractional + long decimals, positive/zero
		"edgeChipTolerance": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{On: true, I: -14.009, TP: -1}, override: true, preset: &flac, compact: true},
		"edgeChipOutside": {act: mpAct, toggleLbl: "Normalize this export", topic: "mp-loudness",
			vals: loudnessVals{On: true, I: -14.02, TP: -1}, override: true, preset: &flac, compact: true},
		"edgeLongDecimals": {act: libAct, toggleLbl: "Normalize loudness", topic: "enc-loudness",
			vals: loudnessVals{On: true, I: -14.123456789, TP: -0.10000000000000001}, preset: &flac},
		"edgeZeroBuilder": {act: libAct, toggleLbl: "Normalize loudness", topic: "enc-loudness",
			vals: loudnessVals{On: true, I: 0, TP: 0}, preset: &flac},
		"edgePositive": {act: libAct, toggleLbl: "Normalize loudness", topic: "enc-loudness",
			vals: loudnessVals{On: true, I: 3, TP: 0.5}, preset: &flac},
		// escaping / unicode: label, act token and extraHTML all carry hostile bytes
		"escaping": {act: func(f string) string { return `x&"<'` + f }, toggleLbl: `Norm & "loud" <x>'`,
			topic: "enc-loudness", vals: loudnessVals{On: true, I: -14, TP: -1}, preset: &flac,
			extraHTML: `<span data-x="a&b">raw</span>`},
		"unicode": {act: libAct, toggleLbl: "ラウドネスを正規化", topic: "enc-loudness",
			vals: loudnessVals{On: true, I: -14, TP: -1, RaiseOnly: true}, preset: &flac},
	}
}

func TestLoudnessFieldsStructuredMatchesLegacy(t *testing.T) {
	for name, o := range loudFx() {
		t.Run(name, func(t *testing.T) {
			want := loudnessFieldsLegacy(o)
			if got := loudnessFields(o); got != want {
				t.Errorf("loudnessFields drifted\n got: %s\nwant: %s", got, want)
			}
			if got := newLoudSt(o).html(); got != want {
				t.Errorf("loudSt.html() drifted\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// TestLoudStResolvesEverythingGoSide pins the ABI contract: no float, no locale-dependent
// lowercasing and no unresolved i18n key may reach the renderer.
func TestLoudStResolvesEverythingGoSide(t *testing.T) {
	for name, o := range loudFx() {
		st := newLoudSt(o)
		if st.Toggle.DL != strings.ToLower(st.Toggle.Label) {
			t.Errorf("%s: toggle data-label not lowercased Go-side (%q)", name, st.Toggle.DL)
		}
		if !st.Toggle.On {
			if len(st.Chips) != 0 || st.IField.Label != "" || st.Raise.Label != "" || st.HasWarn {
				t.Errorf("%s: switch off but the body resolved anyway", name)
			}
			continue
		}
		if st.IField.Type != "number" || st.TPField.Type != "number" {
			t.Errorf("%s: target fields must be number inputs", name)
		}
		if st.Raise.DL != strings.ToLower(st.Raise.Label) {
			t.Errorf("%s: raise data-label not lowercased Go-side (%q)", name, st.Raise.DL)
		}
		if st.Compact {
			if len(st.Chips) != len(transcode.LoudnessTargets()) {
				t.Errorf("%s: compact block needs one chip per industry target, got %d", name, len(st.Chips))
			}
			if st.ChipAct == "" {
				t.Errorf("%s: compact chips need an act", name)
			}
		} else if len(st.Chips) != 0 || st.ChipAct != "" {
			t.Errorf("%s: stacked block must not carry chips", name)
		}
	}
}

// TestLoudStNoNullSlices pins the nil-slice trap: a nil Go slice marshals to JSON null, which the
// Zig parser rejects - the whole surface would silently fall back to Go.
func TestLoudStNoNullSlices(t *testing.T) {
	for name, o := range loudFx() {
		js := stateJSON(newLoudSt(o))
		if js == nil {
			t.Fatalf("%s: state marshal failed", name)
		}
		if strings.Contains(string(js), `null`) {
			t.Errorf("%s: loudSt marshalled a null: %s", name, js)
		}
	}
}
