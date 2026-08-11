//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Editor video-mode golden gate: Zig renderer byte-identical to the Go
// renderer, full view + the #edv-frame and #edv-export fragments.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

func edvFixtures() map[string]edvViewState {
	modes := []edModeTab{
		{Val: "image", Label: "Image"},
		{Val: "video", Label: "Video", Active: true},
	}
	labels := func(st *edvViewState) {
		st.Title, st.Sub = "Editor", "Cut, reframe & export"
		st.Modes = modes
		st.SecReframe, st.SecExport = "Reframe", "Export"
		st.ViewTitle, st.InspTitle = "Preview", "Inspector"
		st.SrcBtn = uiBtn{Label: "Change source…", Variant: "outline", Act: "edv-src-open"}
		st.NoSrc = "Pick a recording."
		st.NoMedia = "Loading media…"
		st.EditHint = "Trim works like Publish."
		st.RefHint = "Drag the bright window."
	}
	export := func() edvExportSt {
		return edvExportSt{
			Preset: edSel("edv-preset", "Target", "Reel 1080×1920",
				selRow{Val: "reel", Label: "Reel 1080×1920", Cur: true},
				selRow{Val: "square", Label: "Square"}),
			Out:       edFld("Output file", "output file", "edv-out", "", "text"),
			OutBrowse: uiBtn{Label: "Browse…", Variant: "outline", Act: "pick-save:mp4:edv-out"},
			Export:    uiBtn{Label: "Export video", Variant: "go", Act: "edv-export"},
			Cancel:    uiBtn{Label: "Cancel", Variant: "warn", Act: "edv-excancel"},
		}
	}

	empty := emptyEdvState()
	labels(&empty)
	empty.Export = export()

	// bound video source: reframe visible, crop window, keyframes, trim info
	full := emptyEdvState()
	labels(&full)
	full.HasSrc, full.SrcName, full.SrcInfo = true, "2026-08-10 22-00-42.mp4", "1920×1080 · 58:12.0"
	full.Player = `<div class=mp-root id=mp-editor-root>RAW-PLAYER</div>`
	full.ShowRef = true
	full.Aspect = edSel("edv-aspect", "Aspect", "9:16 vertical",
		selRow{Val: "orig", Label: "Original"}, selRow{Val: "9x16", Label: "9:16 vertical", Cur: true})
	full.Layout = edSel("edv-layout", "Layout", "Zoom-fill",
		selRow{Val: "crop", Label: "Zoom-fill", Cur: true}, selRow{Val: "fit", Label: "Original inside"})
	full.Frame = edvFrameSt{Show: true, AW: "1920", AH: "1080",
		ImgURL: "http://127.0.0.1:47621/img/s1/tok", HasCrop: true,
		CropL: "34.219", CropT: "0", CropW: "31.563", CropH: "100"}
	full.FrameBtn = uiBtn{Label: "Use playhead frame", Variant: "outline", Act: "edv-frame"}
	full.KfAdd = uiBtn{Label: "+ Keyframe", Variant: "secondary", Act: "edv-kf-add"}
	full.KfClear = uiBtn{Label: "Clear keyframes", Variant: "ghost", Act: "edv-kf-clear"}
	full.HasKfs = true
	full.Kfs = []edvKfRow{
		{Time: "1:23.5", Pos: "25", Go: "edv-kf-go:0", Del: "edv-kf-del:0", DelLb: "✕"},
		{Time: "4:56.0", Pos: "80", Go: "edv-kf-go:1", Del: "edv-kf-del:1", DelLb: "✕"},
	}
	full.Export = export()
	full.Export.TrimInfo = "cut 1:20.0–4:56.0 · crop 606×1080"
	full.ShowFx = true
	full.SecFx = "Effects"
	full.FxAdd = edSel("edv-fx-add", "Add effect…", "",
		selRow{Val: "0", Label: "Glow"}, selRow{Val: "1", Label: "Vignette"})
	full.FxRows = []edvFxRow{
		{Name: "Glow", Btns: []uiBtn{
			{Label: "⏻", Variant: "secondary", Act: "edv-fx-tog:0"},
			{Label: "↑", Variant: "ghost", Act: "edv-fx-up:0"},
			{Label: "↓", Variant: "ghost", Act: "edv-fx-dn:0"},
			{Label: "✕", Variant: "warn", Act: "edv-fx-del:0"},
		}, Params: []edvFxParam{
			{Slider: newSlider("blur", "edv-fx-p:0:blur", 0, 1, 0.01, 0.5, "")},
			{IsBool: true, Toggle: newToggle("invert", "edv-fx-p:0:invert", true)},
		}},
		{Name: "old_glow.dll", Missing: true, MissLb: "missing", Off: true, Btns: []uiBtn{
			{Label: "⏻", Variant: "ghost", Act: "edv-fx-tog:1"},
			{Label: "↑", Variant: "ghost", Act: "edv-fx-up:1"},
			{Label: "↓", Variant: "ghost", Act: "edv-fx-dn:1"},
			{Label: "✕", Variant: "warn", Act: "edv-fx-del:1"},
		}, Params: []edvFxParam{}},
	}
	full.FxPrev = edvFxPrevSt{Show: true, ImgURL: "http://127.0.0.1:47621/img/s2/tok",
		AW: "606", AH: "1080"}
	full.FxPrevBtn = uiBtn{Label: "Preview effects on frame", Variant: "outline", Act: "edv-fx-prev"}
	full.FxHint = "frei0r plugins from C:\\cfg\\vfx\\frei0r."

	// vertical free axis (tall source → wide target) + busy frame + running export
	tall := emptyEdvState()
	labels(&tall)
	tall.HasSrc, tall.SrcName = true, "phone.mp4"
	tall.Player = `<div>P</div>`
	tall.ShowRef = true
	tall.Aspect = edSel("edv-aspect", "Aspect", "16:9 wide",
		selRow{Val: "16x9", Label: "16:9 wide", Cur: true})
	tall.Layout = edSel("edv-layout", "Layout", "Original inside",
		selRow{Val: "fit", Label: "Original inside", Cur: true})
	tall.HasBlur = true
	tall.Blur = newSlider("Background blur", "edv-bgblur", 0, 1, 0.01, 0.35, "")
	tall.Frame = edvFrameSt{Show: true, AW: "1080", AH: "1920", Busy: "Extracting frame…",
		HasCrop: true, Vert: true, CropL: "0", CropT: "34.219", CropW: "100", CropH: "31.563"}
	tall.FrameBtn = uiBtn{Label: "Use playhead frame", Variant: "outline", Act: "edv-frame"}
	tall.KfAdd = uiBtn{Label: "+ Keyframe", Variant: "secondary", Act: "edv-kf-add"}
	tall.KfClear = uiBtn{Label: "Clear keyframes", Variant: "ghost", Act: "edv-kf-clear"}
	tall.Export = export()
	tall.Export.Running, tall.Export.Pct, tall.Export.Stage = true, "42.5%", "encode"
	tall.ShowFx = true
	tall.SecFx = "Effects"
	tall.FxAdd = edSel("edv-fx-add", "Add effect…", "")
	tall.FxNone = "No effect plugins found."
	tall.FxPrev = edvFxPrevSt{Show: true, Busy: "Extracting frame…", AW: "1080", AH: "608"}
	tall.FxPrevBtn = uiBtn{Label: "Preview", Variant: "outline", Act: "edv-fx-prev"}
	tall.FxHint = "hint"

	// error + result branches, no reframe (audio source)
	audio := emptyEdvState()
	labels(&audio)
	audio.HasSrc, audio.SrcName, audio.SrcInfo = true, "set.flac", "58:12.0"
	audio.Player = `<div>A</div>`
	audio.Export = export()
	audio.Export.HasResult, audio.Export.Result = true, `Exported: C:\sets\set-reel.mp4`
	audio.Export.HasErr, audio.Export.Err = true, "boom: <exit 1>"

	escaping := emptyEdvState()
	labels(&escaping)
	escaping.Title = `Edi&tor <"v">`
	escaping.Modes = []edModeTab{{Val: "image", Label: `I&m"g`}, {Val: "video", Label: `V&id'`, Active: true}}
	escaping.HasSrc = true
	escaping.SrcName = `a&b "<x>".mp4`
	escaping.SrcInfo = `19&20×10"80`
	escaping.ViewTitle, escaping.InspTitle = `V&iew"`, `I&nsp"`
	escaping.SrcBtn = uiBtn{Label: `S&rc"`, Variant: "outline", Act: `edv-src-open&"`}
	escaping.Player = `<div>R</div>`
	escaping.NoSrc = `n&o"<>'`
	escaping.EditHint = `h&int"<>'`
	escaping.ShowRef = true
	escaping.Aspect = edSel("edv-aspect", `A&sp"`, `O&rig'`, selRow{Val: "orig", Label: `O&rig'`, Cur: true})
	escaping.Layout = edSel("edv-layout", `L&ay"`, `C&rop'`, selRow{Val: "crop", Label: `C&rop'`, Cur: true})
	escaping.HasBlur = true
	escaping.Blur = newSlider(`B&lur"`, `edv-bgblur&"`, 0, 1, 0.01, 0.5, "")
	escaping.Frame = edvFrameSt{Show: true, AW: "1920", AH: "1080", Busy: `b&usy"<>'`}
	escaping.FrameBtn = uiBtn{Label: `F&rame"`, Variant: "outline", Act: `edv-frame&"`}
	escaping.KfAdd = uiBtn{Label: "K", Variant: "secondary", Act: "edv-kf-add"}
	escaping.KfClear = uiBtn{Label: "C", Variant: "ghost", Act: "edv-kf-clear"}
	escaping.HasKfs = true
	escaping.Kfs = []edvKfRow{{Time: `1:0"0.0`, Pos: "50", Go: `edv-kf-go:0&"`, Del: `edv-kf-del:0'`, DelLb: `✕&"`}}
	escaping.RefHint = `r&ef"<>'`
	escaping.ShowFx = true
	escaping.SecFx = `F&x"`
	escaping.FxAdd = edSel("edv-fx-add", `A&dd"`, "")
	escaping.FxNone = `n&one"<>'`
	escaping.FxRows = []edvFxRow{
		{Name: `G&low "<x>"`, Missing: true, MissLb: `m&iss"`, Btns: []uiBtn{
			{Label: `⏻&"`, Variant: "secondary", Act: `edv-fx-tog:0&"`},
		}, Params: []edvFxParam{
			{Slider: newSlider(`b&lur"`, `edv-fx-p:0:b&lur"`, 0, 1, 0.01, 0.25, "")},
			{IsBool: true, Toggle: newToggle(`i&nv"`, `edv-fx-p:0:i&nv"`, false)},
		}},
	}
	escaping.FxPrev = edvFxPrevSt{Show: true, Busy: `b&usy2"<>'`, AW: "10", AH: "20"}
	escaping.FxPrevBtn = uiBtn{Label: `P&rev"`, Variant: "outline", Act: "edv-fx-prev"}
	escaping.FxHint = `h&x"<>'`
	escaping.Export = export()
	escaping.Export.TrimInfo = `cut 0:01.0–0:02.0 · crop 2×2 &"<>'`
	escaping.Export.HasErr, escaping.Export.Err = true, `e&rr "<x>"'`

	long := emptyEdvState()
	labels(&long)
	longS := strings.Repeat("very-long-", 120)
	long.Sub = longS
	long.HasSrc, long.SrcName, long.SrcInfo = true, longS+".mp4", longS
	long.Player = `<div>` + longS + `</div>`
	long.Export = export()
	long.Export.HasResult, long.Export.Result = true, longS

	return map[string]edvViewState{
		"empty":    empty,
		"full":     full,
		"tall":     tall,
		"audio":    audio,
		"escaping": escaping,
		"long":     long,
	}
}

func TestZigEditorVideoGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range edvFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderEditorVideo(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", editorVideoHTML(st), zig)
		})
	}
}

// TestZigEditorVideoWire: the binary v2 path must match the Go renderer too.
func TestZigEditorVideoWire(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable")
	}
	for name, st := range edvFixtures() {
		t.Run(name, func(t *testing.T) {
			doc := wireEdvView(st)
			if doc == nil {
				t.Skip("over-size doc falls back to v1 by design")
			}
			zig, ok := zigui.RenderEditorVideoV2(doc)
			if !ok {
				t.Fatal("v2 render failed")
			}
			assertBytesEqual(t, "v2", editorVideoHTML(st), zig)
		})
	}
}
