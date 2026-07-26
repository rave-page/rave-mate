//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Overlays golden gate: Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states, full view + all four live-patched fragments.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// ovlFixtures: unavailable, empty (all outputs off), populated, escaping edge, long values, unicode.
func ovlFixtures() map[string]ovlState {
	sel := func(id, label, cur string, rows ...selRow) selState {
		return selState{ID: id, Label: label, CurLabel: cur, Rows: rows}
	}
	base := func() ovlState {
		return ovlState{
			Title: "Overlays", Sub: "Overlay pipeline", Available: true,
			Unavailable: "Config unavailable",
			TopBtns: []uiBtn{
				{Label: "Edit style", Variant: "primary", Act: "open-url", Val: "http://127.0.0.1:8787/?edit=1"},
				{Label: "Open overlay", Variant: "explore", Act: "open-url", Val: "http://127.0.0.1:8787/"},
				{Label: "Copy URL", Variant: "ghost", Act: "copy", Val: "http://127.0.0.1:8787/"},
			},
			Appearance: ovlApprState{
				Card:  ovlCardState{Title: "Appearance"},
				Note1: "Colours live in the browser editor.",
				Btns: []uiBtn{
					{Label: "Edit colours", Variant: "primary", Act: "open-url", Val: "http://127.0.0.1:8787/?edit=1"},
					{Label: "Copy editor URL", Variant: "ghost", Act: "copy", Val: "http://127.0.0.1:8787/?edit=1"},
				},
				Fader: newToggle("Fade by fader", "ovl-fader", false),
				Note2: "Browser-owned keys survive.",
			},
			Web: ovlWebState{
				Card:    ovlCardState{Title: "Browser overlay", StatusID: "ovl-st-web", Status: newStatus("muted", "Off", ""), En: newToggle("Enabled", "ovl-en-web", false)},
				Port:    newField("Port", "set:overlay-port", "8787", "number"),
				Btns:    []uiBtn{{Label: "Open overlay", Variant: "explore", Act: "open-url", Val: "http://127.0.0.1:8787/"}},
				URL:     newKV("Overlay URL", "http://127.0.0.1:8787/"),
				Note1:   "Add as an OBS Browser source.",
				AutoAdd: newToggle("Auto-add to OBS", "ovl-obssrc", false),
				Scene:   newField("OBS scene", "ovl-obsscene", "rave.page", "text"),
				Nest:    newToggle("Nest in program scene", "ovl-obsnest", false),
				Note2:   "Requires obs-websocket.",
			},
			Wave: ovlWaveState{
				Card:      ovlCardState{Title: "Waveform", StatusID: "ovl-st-wave", Status: newStatus("muted", "Off", ""), En: newToggle("Enabled", "ovl-en-wave", false)},
				Note1:     "Scrolling waveform + EQ.",
				Zoom:      sel("ovl-wf-zoom", "Zoom", "16 s", selRow{Val: "8", Label: "8 s"}, selRow{Val: "16", Label: "16 s", Cur: true}),
				Playhead:  sel("ovl-wf-playhead", "Playhead", "Centre", selRow{Val: "0.5", Label: "Centre", Cur: true}),
				WaveColor: newField("Wave colour", "ovl-wf-wavecolor", "#08F79B", "text"),
				WaveOpac:  newSlider("Wave opacity", "ovl-wf-waveopac", 0, 1, 0.05, 0.9, ""),
				BgColor:   newField("Background colour", "ovl-wf-bgcolor", "#0a0a0a", "text"),
				BgOpac:    newSlider("Background opacity", "ovl-wf-bgopac", 0, 1, 0.05, 0.35, ""),
				Note2:     "Colours accept CSS hex.",
			},
			Png: ovlDirState{
				Card: ovlCardState{Title: "Deck PNGs", StatusID: "ovl-st-png", Status: newStatus("muted", "Off", ""), En: newToggle("Enabled", "ovl-en-png", false)},
				Dir:  newField("Output folder", "ovl-png-dir", `C:\overlays`, "text"),
				Open: uiBtn{Label: "Open folder", Variant: "outline", Act: "ovl-png-open"},
				Note: "One PNG per deck.",
			},
			Obs: ovlNoteState{
				Card: ovlCardState{Title: "OBS direct", StatusID: "ovl-st-obs", Status: newStatus("muted", "Off", ""), En: newToggle("Enabled", "ovl-en-obs", false)},
				Note: "Drives text sources over obs-websocket.",
			},
			VS: ovlVSState{
				Card:  ovlCardState{Title: "Video share", StatusID: "ovl-st-vs", Status: newStatus("muted", "Off", ""), En: newToggle("Enabled", "ovl-en-vs", false)},
				Note:  "Shares as rave.page A. No GPU backend.",
				Scale: sel("ovl-vs-scale", "Render scale", "2× (720×240)", selRow{Val: "1", Label: "1× (360×120)"}, selRow{Val: "2", Label: "2× (720×240)", Cur: true}),
				Note2: "Higher scale costs GPU.",
			},
			NP: ovlDirState{
				Card: ovlCardState{Title: "Now-playing files", StatusID: "ovl-st-np", Status: newStatus("muted", "Off", ""), En: newToggle("Enabled", "ovl-en-np", false)},
				Dir:  newField("Output folder", "ovl-np-dir", "/tmp/np", "text"),
				Open: uiBtn{Label: "Open folder", Variant: "outline", Act: "ovl-np-open"},
				Note: "now_playing.json + .txt.",
			},
			Strip: ovlStripState{
				Parts: "Web - · PNG - · OBS - · Share - · Waveform - · Files -",
				Hint:  "Toggle outputs in Settings.",
				Right: "OBS off",
			},
		}
	}

	unavailable := emptyOvlState()
	unavailable.Title, unavailable.Sub = "Overlays", "ignored"
	unavailable.Unavailable = "Config unavailable"

	empty := base()

	populated := base()
	populated.Appearance.Fader.On = true
	populated.Web.Card.Status = newStatus("success", "Serving on 8787", "")
	populated.Web.Card.En.On = true
	populated.Wave.Card.En.On = true
	populated.Png.Card.En.On = true
	populated.Obs.Card.En.On = true
	populated.VS.Card.En.On = true
	populated.NP.Card.En.On = true
	populated.Web.AutoAdd.On = true
	populated.Web.Nest.On = true
	populated.Wave.Card.Status = newStatus("success", "Rendering", "")
	populated.Wave.Zoom.Open = true
	populated.Wave.Zoom.Filter = "1"
	populated.Wave.Zoom.Rows = []selRow{{Val: "12", Label: "12 s"}, {Val: "16", Label: "16 s", Cur: true}}
	populated.Png.Card.Status = newStatus("success", "Writing PNGs", "")
	populated.Obs.Card.Status = newStatus("warning", "Enable OBS first", "")
	populated.VS.Card.Status = newStatus("success", "Sharing via Spout", "")
	populated.VS.Note = "Shares as rave.page A. Shares via Spout."
	populated.VS.Spout = true
	populated.VS.SpoutCtl = ovlSpoutState{
		Note: "SpoutLibrary.dll enables GPU sharing.", StatusLine: `Installed at C:\rave\SpoutLibrary.dll`,
		InstallLbl: "Reinstall", CanInstall: true, SdkURL: "https://spout.zeal.co/",
	}
	populated.NP.Card.Status = newStatus("success", "Writing files", "")
	populated.Strip = ovlStripState{
		Parts: "Web ✓ · PNG ✓ · OBS - · Share Spout · Waveform ✓ · Files ✓",
		Hint:  "Toggle outputs in Settings.", Right: "OBS connected",
	}

	// Spout present but not installable (no writable dir) → plain disabled button + SDK link
	gated := base()
	gated.VS.Spout = true
	gated.VS.SpoutCtl = ovlSpoutState{
		Note: "SpoutLibrary.dll enables GPU sharing.", StatusLine: "Not found",
		InstallLbl: "Install", CanInstall: false, OpenSdk: "Open the Spout SDK", SdkURL: "https://spout.zeal.co/",
	}

	escaping := base()
	escaping.Title = `Over&lays <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.TopBtns = []uiBtn{{Label: `E&dit "style"`, Variant: "primary", Act: "open-url", Val: `http://x/?a&b='c'`}}
	escaping.Appearance.Card.Title = `App&earance<">`
	escaping.Appearance.Note1 = `n&ote "1"<>'`
	escaping.Appearance.Btns = []uiBtn{{Label: `C&opy'`, Variant: "ghost", Act: "copy", Val: `u&rl"<>'`}}
	escaping.Appearance.Fader = uiToggle{Label: `F&ade"<>'`, DL: `f&ade"<>'`, Act: `ovl-fader&"`, On: true}
	escaping.Appearance.Note2 = `n&ote "2"`
	escaping.Web.Card.Title = `W&eb"`
	escaping.Web.Card.Status = uiStatus{Variant: "warning", Label: `S&t"<>'`, DL: `s&t"<>'`, Line: `l&ine"<>'`}
	escaping.Web.Card.En = uiToggle{Label: `En&abled"<>'`, DL: `en&abled"<>'`, Act: `ovl-en-web&"`, On: true}
	escaping.Web.Port = uiField{Label: `P&ort"<>'`, DL: `p&ort"<>'`, Act: `set:overlay-port&"`, Value: `8&7"<>'`, Type: "number"}
	escaping.Web.URL = uiKV{Label: `U&RL"<>'`, DL: `u&rl"<>'`, Value: `http://x/?a&b="c"'`}
	escaping.Web.Scene = uiField{Label: `Sc&ene"'`, DL: `sc&ene"'`, Act: "ovl-obsscene", Value: `r&ave "page"'`, Type: "text"}
	escaping.Web.Note1, escaping.Web.Note2 = `w&1"<>'`, `w&2"<>'`
	escaping.Wave.WaveColor = uiField{Label: `C&ol"<>'`, DL: `c&ol"<>'`, Act: "ovl-wf-wavecolor", Value: `#08&F7"`, Type: "text"}
	escaping.Wave.WaveOpac = newSlider(`O&pac"<>'`, `ovl-wf-waveopac&"`, 0, 1, 0.05, 0.9, `%&"'`)
	escaping.Wave.Zoom = selState{ID: "ovl-wf-zoom", Label: `Z&oom"<>'`, CurLabel: `1&6"<>'`, Open: true,
		Filter: `f&"<>'`, Rows: []selRow{{Val: `v&"'<>`, Label: `L&"'<>`, Sub: `s&"'<>`, Badge: `b&"'<>`, Cur: true}}}
	escaping.Png.Dir = uiField{Label: `D&ir"<>'`, DL: `d&ir"<>'`, Act: "ovl-png-dir", Value: `C:\p&th"<>'`, Type: "text"}
	escaping.Png.Open = uiBtn{Label: `Op&en"<>'`, Variant: "outline", Act: `ovl-png-open&"`}
	escaping.Png.Note = `p&ng"<>'`
	escaping.Obs.Note = `o&bs"<>'`
	escaping.VS.Note = `v&s"<>'`
	escaping.VS.Spout = true
	escaping.VS.SpoutCtl = ovlSpoutState{Note: `sp&out"<>'`, StatusLine: `at C:\x&"<>'`,
		InstallLbl: `In&stall"<>'`, CanInstall: false, OpenSdk: `S&DK"<>'`, SdkURL: `https://x/?a&b="c"'`}
	escaping.Strip = ovlStripState{Parts: `W&eb ✓ · P"NG -`, Hint: `h&int'<>`, Right: `O&BS "off"`}

	long := base()
	longS := strings.Repeat("very-long-", 120)
	long.Sub = longS
	long.Web.Port = newField(longS, "set:overlay-port", strings.Repeat("9", 400), "number")
	long.Web.URL = newKV(longS, "http://127.0.0.1:8787/"+strings.Repeat("p/", 400))
	long.Wave.Zoom = selState{ID: "ovl-wf-zoom", Label: longS, CurLabel: strings.Repeat("x", 400), Open: true,
		Rows: []selRow{{Val: strings.Repeat("v", 300), Label: strings.Repeat("x", 400), Cur: true}}}
	long.Wave.WaveOpac = newSlider(longS, "ovl-wf-waveopac", 0, 1, 0.001, 0.123, strings.Repeat("u", 50))
	long.Png.Dir = newField("Output folder", "ovl-png-dir", strings.Repeat("d/", 500), "text")
	long.Strip.Parts = longS

	unicode := base()
	unicode.Title = "オーバーレイ 🎧"
	unicode.Sub = "größer Оверлеи"
	unicode.TopBtns = []uiBtn{{Label: "Змінити стиль", Variant: "primary", Act: "open-url", Val: "http://127.0.0.1:8787/?edit=1"}}
	unicode.Appearance.Card.Title = "Внешний вид"
	unicode.Appearance.Fader = newToggle("Затухання фейдером", "ovl-fader", true)
	unicode.Web.Card.Title = "ブラウザ オーバーレイ"
	unicode.Web.Card.Status = newStatus("success", "Обслуживание 8787", "порт открыт")
	unicode.Web.Port = newField("Порт", "set:overlay-port", "8787", "number")
	unicode.Web.Scene = newField("シーン", "ovl-obsscene", "рейв.страница", "text")
	unicode.Wave.Zoom = selState{ID: "ovl-wf-zoom", Label: "ズーム", CurLabel: "16 秒",
		Rows: []selRow{{Val: "16", Label: "16 秒", Cur: true}}}
	unicode.Wave.WaveOpac = newSlider("Прозрачность", "ovl-wf-waveopac", 0, 1, 0.05, 0.5, " ед")
	unicode.Png.Dir = newField("Папка вывода", "ovl-png-dir", "C:\\Музыка\\оверлеи", "text")
	unicode.Png.Card.En = newToggle("Увімкнено", "ovl-en-png", true)
	unicode.Strip = ovlStripState{Parts: "Веб ✓ · PNG - · 中文 ✓", Hint: "подсказка 🎛️", Right: "OBS 接続済み"}

	return map[string]ovlState{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"gated":       gated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

func TestZigOverlaysGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range ovlFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderOverlays(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", overlaysHTML(st), zig)

			if !st.Available {
				return // the fragments only exist on the available view
			}
			zigFrag(t, "appearance", ovlApprHTML(st.Appearance), stateJSON(st.Appearance), zigui.RenderOverlaysAppearance)
			zigFrag(t, "strip", ovlStripHTMLOf(st.Strip), stateJSON(st.Strip), zigui.RenderOverlaysStrip)
			zigFrag(t, "status", st.Web.Card.Status.html(), stateJSON(st.Web.Card.Status), zigui.RenderOverlaysStatus)
			if st.VS.Spout {
				zigFrag(t, "spout", ovlSpoutHTML(st.VS.SpoutCtl), stateJSON(st.VS.SpoutCtl), zigui.RenderOverlaysSpout)
			}
		})
	}
}

// zigFrag asserts one fragment renderer is byte-identical to its Go reference.
func zigFrag(t *testing.T, what, want string, js []byte, f func([]byte) (string, bool)) {
	t.Helper()
	if js == nil {
		t.Fatalf("%s: state marshal failed", what)
	}
	got, ok := f(js)
	if !ok {
		t.Fatalf("%s: zig render failed", what)
	}
	assertBytesEqual(t, what, want, got)
}
