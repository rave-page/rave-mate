//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/zigui"
)

// Golden gate for phase B-1b SHARD 2 - the surfaces that flipped from pre-rendered `tipTopic`
// markup to structured `tipSt` (and from a pre-rendered ss-label to `ssLabelSt`). ONE file rather
// than edits spread over ten per-tab golden files: the same fixtures + whole-tab exports each
// tab's own suite uses, swept through every tooltip SHAPE in two locales, next to the raw arm.
//
// Every sweep carries the shard-1 inertness guard: the fixture must CHANGE the document bytes, a
// keybind-grid fixture must actually emit `tt-kb-keys`, and the raw bridge must reproduce the
// structured bytes exactly. A tooltip fixture whose mutation reaches no tooltip is worse than no
// fixture - it reports green forever.
//
// Run: bash scripts/build-zig.sh && GOWORK=off go test -count=1 -tags zigui ./internal/webui -run TestZigTip2

// tip2Locales: en plus a non-latin catalog - the renderers are pure, so a locale must only ever
// change the TEXT, never the tree.
var tip2Locales = []string{"en", "ja"}

// tipVariant is one tooltip shape. Registry topics, not synthetic states, so a registry/catalog
// change reaches these fixtures instead of freezing them.
type tipVariant struct {
	name string
	st   *tipSt
}

// tipVariants: absent / plain prose card / multi-link card / keybind-grid card.
func tipVariants() []tipVariant {
	return []tipVariant{
		{"tipNone", nil},
		{"tipPlain", tipTopicSt("remote-cache")},  // title + prose only
		{"tipMultiLink", tipTopicSt("midi-thru")}, // 4 authoritative-source links
		{"tipKbGrid", tipTopicSt("cue-edit")},     // 23-row grid with section headers
	}
}

// tip2Sweep runs ONE surface through every tooltip variant in every tip2Locale. mk builds a FRESH
// state carrying either the structured tooltip (tp != nil, raw == "") or the legacy pre-rendered
// markup (tp == nil, raw != ""); goHTML is the Go reference renderer, zig the export under test.
func tip2Sweep[T any](t *testing.T, what string, mk func(tp *tipSt, raw string) T,
	goHTML func(T) string, zig func([]byte) (string, bool)) {
	t.Helper()
	t.Cleanup(func() { i18n.SetLocale("en") })
	for _, loc := range tip2Locales {
		if got := i18n.SetLocale(loc); got != loc {
			t.Fatalf("locale %q did not activate (got %q)", loc, got)
		}
		bare := goHTML(mk(nil, ""))
		for _, v := range tipVariants() {
			st := mk(v.st, "")
			want := goHTML(st)
			t.Run(loc+"/"+what+"/"+v.name, func(t *testing.T) {
				zigGolden(t, what+"/"+v.name, st, want, zig)
				if v.st == nil {
					return
				}
				// inertness guard: the fixture must reach a tooltip
				if want == bare {
					t.Fatalf("%s/%s changed no bytes - the fixture reaches no tooltip", what, v.name)
				}
				if v.name == "tipKbGrid" && !strings.Contains(want, "tt-kb-keys") {
					t.Fatalf("%s/%s: the keybind grid never reached the DOM", what, v.name)
				}
				// the dual-field bridge: an un-migrated builder's pre-rendered markup must
				// reproduce the structured bytes, on both renderers
				rs := mk(nil, tipTopic(v.st.ID))
				assertBytesEqual(t, what+"/"+v.name+"/raw-bridge", want, goHTML(rs))
				zigGolden(t, what+"/"+v.name+"/raw", rs, want, zig)
			})
		}
	}
}

// ssLabelRaw is the ss-label markup a pre-B-1b builder shipped (the two components.go literals).
func ssLabelRaw(text, tipHTML string) string {
	return `<span class=ss-label>` + esc(text) + tipHTML + `</span>`
}

// tip2Sel is a filled smart-select state (Rows never nil - a null rejects the whole document).
func tip2Sel(id, cur string) selState {
	return selState{ID: id, CurLabel: cur, Rows: []selRow{{Val: cur, Label: cur, Cur: true}}}
}

// ── the shared loudness block (components.go loudSt) ──

// TestZigTip2LoudnessGolden sweeps the loudness block's own tooltip through the automation
// editor's transcode step (the surface that frames it most tightly). loudFx() already carries a
// topic on every fixture, so the four loudness suites exercise the structured card on every state
// axis - this pins the tooltip SHAPES and the raw arm.
func TestZigTip2LoudnessGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	tip2Sweep(t, "loud", func(tp *tipSt, raw string) aeModalSt {
		l := newLoudSt(loudnessOpts{
			act:       func(f string) string { return "auto-ed:step2\x1f" + f },
			toggleLbl: "Override normalization",
			vals:      loudnessVals{On: true, I: -14, TP: -1, RaiseOnly: true},
			override:  true,
		})
		l.TipS, l.Tip = tp, raw
		return aeModalSt{Title: "Edit automation", SecMatch: "Match", SecActions: "Actions",
			Steps: []aeStepSt{{Title: "3. Transcode", Desc: "Re-encode with a preset",
				Blocks: []aeBlockSt{{Kind: aeBlkLoud, Loud: l}}}},
			Save: "Save", Cancel: "Cancel"}
	}, aeModalHTMLOf, zigui.RenderAutoEditor)
}

// ── automations editor: the selraw ss-label (shard 1 shipped aeLabelSt; shard 2 aliased it) ──

// TestZigTip2AeSelRawGolden proves the aeLabelSt → shared ssLabelSt alias changed no bytes.
func TestZigTip2AeSelRawGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	tip2Sweep(t, "aeSelRaw", func(tp *tipSt, raw string) aeModalSt {
		b := aeBlockSt{Kind: aeBlkSelRaw, Sel: tip2Sel("auto-sch-kind", "Cron")}
		if raw != "" {
			b.LabelHTML = ssLabelRaw("Trigger", raw)
		} else {
			b.Label = &aeLabelSt{Text: "Trigger", Tip: tp}
		}
		return aeModalSt{Title: "Edit automation", SecMatch: "Match", SecActions: "Actions",
			Match: []aeBlockSt{b}, Save: "Save", Cancel: "Cancel"}
	}, aeModalHTMLOf, zigui.RenderAutoEditor)
}

// ── settings: the select ss-label (sbSelectTip), block and fpair-kid alike ──

// TestZigTip2SettingsSelectGolden pins sbSelectTip's structured ss-label against the legacy
// pre-rendered span, on the #set-content pane the settings suite already gates.
func TestZigTip2SettingsSelectGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	pane := func(blocks ...setBlock) setContentSt {
		return setContentSt{
			Nav: []setNavSt{{ID: "tips", Title: "Tips", Agg: "ok", Active: true}},
			Secs: []setSecSt{{ID: "tips", Title: "Tooltips", Desc: "select labels", Cards: []setCardSt{
				{ID: "medialink", Title: "Media link", St: setStatusSt{V: "go", T: "running"}, Blocks: blocks},
			}}},
		}
	}
	lbl := func(tp *tipSt, raw string) (*ssLabelSt, string) {
		if raw != "" {
			return nil, ssLabelRaw("Acceleration", raw)
		}
		return &ssLabelSt{Text: "Acceleration", Tip: tp}, ""
	}
	tip2Sweep(t, "setSelect", func(tp *tipSt, raw string) setContentSt {
		s, l, rw := tip2Sel("set-ml-accel", "Automatic"), (*ssLabelSt)(nil), ""
		l, rw = lbl(tp, raw)
		return pane(setBlock{K: "select", Sel: &s, SelLblS: l, SelLbl: rw})
	}, setContentHTML, zigui.RenderSettingsContent)

	tip2Sweep(t, "setSelectKid", func(tp *tipSt, raw string) setContentSt {
		s, l, rw := tip2Sel("set-ml-accel", "Automatic"), (*ssLabelSt)(nil), ""
		l, rw = lbl(tp, raw)
		return pane(setBlock{K: "fpair", Kids: []setKid{{K: "select", Sel: &s, SelLblS: l, SelLbl: rw}}})
	}, setContentHTML, zigui.RenderSettingsContent)
}

// ── MIDI controllers: port / THRU / DJ-bridge ss-labels ──

// TestZigTip2MidiCtlLabelsGolden sweeps the controller port label and the DJ-bridge port label
// through the whole MIDI tab document (the surface the ss-labels actually ship in).
func TestZigTip2MidiCtlLabelsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	tip2Sweep(t, "midiSsLabels", func(tp *tipSt, raw string) midiCtlState {
		st := midiCtlFixtures()["populated"]
		bs := make([]midiCtlBlock, len(st.Ctls.Blocks)) // copy: fixtures share the slice
		copy(bs, st.Ctls.Blocks)
		st.Ctls.Blocks = bs
		if raw != "" {
			bs[0].PortLblS, bs[0].PortLbl = nil, ssLabelRaw("Port", raw)
			st.Bridge.ToDJLblS, st.Bridge.ToDJLbl = nil, ssLabelRaw("To DJ", raw)
		} else {
			bs[0].PortLblS, bs[0].PortLbl = &ssLabelSt{Text: "Port", Tip: tp}, ""
			st.Bridge.ToDJLblS, st.Bridge.ToDJLbl = &ssLabelSt{Text: "To DJ", Tip: tp}, ""
		}
		return st
	}, midiCtlHTML, zigui.RenderMIDICtl)
}

// ── contract tests (untagged behaviour, no lib needed beyond the build tag) ──

// TestSsLabelStIsTheOneMarkupSource pins the shared label state against the two literals it
// replaced (components.go selectBoxTip / resolveSelectBoxTip) and the automations alias.
func TestSsLabelStIsTheOneMarkupSource(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	i18n.SetLocale("en")
	for _, topic := range append(tipIDs(), "no-such-topic") {
		l := ssLabelSt{Text: `P&ort <"x">`, Tip: tipTopicSt(topic)}
		if want := ssLabelRaw(`P&ort <"x">`, tipTopic(topic)); l.html() != want {
			assertBytesEqual(t, "ssLabelSt/"+topic, want, l.html())
		}
	}
	// aeLabelSt is an ALIAS - assignable both ways, one html()
	var al aeLabelSt = ssLabelSt{Text: "T"}
	if al.html() != (ssLabelSt{Text: "T"}).html() {
		t.Fatal("aeLabelSt is no longer the shared ssLabelSt")
	}
	// ssSelHTML's three arms
	s := tip2Sel("x", "cur")
	lbl := ssLabelSt{Text: "L", Tip: tipTopicSt("icecast")}
	if got, want := ssSelHTML(s, &lbl, "IGNORED"), selHTMLRaw(s, lbl.html()); got != want {
		t.Fatalf("ssSelHTML: structured arm lost\n got %s\nwant %s", got, want)
	}
	if got, want := ssSelHTML(s, nil, "<span class=ss-label>raw</span>"), selHTMLRaw(s, "<span class=ss-label>raw</span>"); got != want {
		t.Fatalf("ssSelHTML: raw arm lost\n got %s\nwant %s", got, want)
	}
	if got, want := ssSelHTML(s, nil, ""), selHTML(s); got != want {
		t.Fatalf("ssSelHTML: plain arm lost\n got %s\nwant %s", got, want)
	}
	// selectBoxTip (the Go-only path) must still equal resolve + render
	opts := [][2]string{{"auto", "Automatic"}}
	sel, l2 := resolveSelectBoxTip("Acceleration", "set:ml-accel", opts, "auto", "ml-accel")
	if got, want := selectBoxTip("Acceleration", "set:ml-accel", opts, "auto", "ml-accel"),
		selHTMLRaw(sel, l2.html()); got != want {
		t.Fatalf("selectBoxTip diverged from resolveSelectBoxTip + selHTMLRaw\n got %s\nwant %s", got, want)
	}
}

// TestLoudStTipBridge pins the loudness block's tooltip bridge (structured wins, raw arm intact)
// and that the sweep's variant list is non-degenerate.
func TestLoudStTipBridge(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	i18n.SetLocale("en")
	o := loudnessOpts{act: func(f string) string { return "lib-pf:" + f },
		toggleLbl: "Normalize loudness", topic: "enc-loudness", vals: loudnessVals{On: true, I: -14, TP: -1}}
	st := newLoudSt(o)
	if st.TipS == nil || st.Tip != "" {
		t.Fatalf("newLoudSt must resolve the topic structurally (tipSt=%v raw=%q)", st.TipS, st.Tip)
	}
	structured := st.html()
	bridged := st
	bridged.TipS, bridged.Tip = nil, tipTopic("enc-loudness")
	if got := bridged.html(); got != structured {
		assertBytesEqual(t, "loudSt raw bridge", structured, got)
	}
	both := st
	both.Tip = "<span class=SHOULD-NOT-RENDER></span>"
	if got := both.html(); got != structured {
		t.Fatal("loudSt: the structured tooltip must WIN over the raw bridge value")
	}
	o.topic = ""
	if bare := newLoudSt(o); bare.TipS != nil || strings.Contains(bare.html(), "class=tt") {
		t.Fatal("an empty topic must resolve to no tooltip at all")
	}
	seen := map[string]string{}
	for _, v := range tipVariants() {
		if v.st == nil {
			continue
		}
		h := renderTipSt(*v.st)
		for n, prev := range seen {
			if prev == h {
				t.Fatalf("tipVariants %q and %q render identically", n, v.name)
			}
		}
		seen[v.name] = h
	}
	if len(seen) != 3 {
		t.Fatalf("expected 3 non-nil tooltip variants, got %d", len(seen))
	}
}
