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
	full.ReframeBtn = uiBtn{Label: "Reframe & zoom window…", Variant: "secondary", Act: "edv-reframe-open"}
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
			{IsColor: true, Swatch: "rgb(255,77,153)",
				Field: newField("tint", "edv-fx-c:0:tint", "#ff4d99", "text")},
			{Slider: newSlider("center X", "edv-fx-p:0:center.x", 0, 1, 0.01, 0.5, "")},
			{Slider: newSlider("center Y", "edv-fx-p:0:center.y", 0, 1, 0.01, 0.25, "")},
		}},
		{Name: "old_glow.dll", Missing: true, MissLb: "missing", Off: true, Btns: []uiBtn{
			{Label: "⏻", Variant: "ghost", Act: "edv-fx-tog:1"},
			{Label: "↑", Variant: "ghost", Act: "edv-fx-up:1"},
			{Label: "↓", Variant: "ghost", Act: "edv-fx-dn:1"},
			{Label: "✕", Variant: "warn", Act: "edv-fx-del:1"},
		}, Params: []edvFxParam{}},
	}
	full.PrevRes = edSel("edv-prevres", "Preview quality", "540p",
		selRow{Val: "1080", Label: "1080p"}, selRow{Val: "540", Label: "540p", Cur: true})
	full.FxHint = "frei0r plugins from C:\\cfg\\vfx\\frei0r."
	full.FxSrc = []uiBtn{
		{Label: "ISF library ↗", Variant: "ghost", Act: "edv-fx-www:isf"},
		{Label: "Get Vidvox pack (MIT)", Variant: "outline", Act: "edv-fx-getpack"},
		{Label: "ISF folder", Variant: "ghost", Act: "edv-fx-dir:isf"},
	}

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
	tall.HasZoom = true
	tall.Zoom = newSlider("Zoom", "edv-zoomset", 1, 4, 0.05, 1.6, "")
	tall.ReframeBtn = uiBtn{Label: "Reframe & zoom window…", Variant: "secondary", Act: "edv-reframe-open"}
	tall.Export = export()
	tall.Export.Running, tall.Export.Pct, tall.Export.Stage = true, "42.5%", "encode"
	tall.ShowFx = true
	tall.SecFx = "Effects"
	tall.FxAdd = edSel("edv-fx-add", "Add effect…", "")
	tall.FxNone = "No effect plugins found."
	tall.PrevRes = edSel("edv-prevres", "Preview quality", "240p",
		selRow{Val: "240", Label: "240p", Cur: true})
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
	escaping.ReframeBtn = uiBtn{Label: `R&ef"`, Variant: "secondary", Act: `edv-reframe-open&"`}
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
	escaping.PrevRes = edSel("edv-prevres", `P&rev"`, `5&40"`, selRow{Val: "540", Label: `5&40"`, Cur: true})
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

// TestZigEditorVideoInspWire: the #edv-insp fragment renderer (same EdvView doc).
func TestZigEditorVideoInspWire(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable")
	}
	for name, st := range edvFixtures() {
		t.Run(name, func(t *testing.T) {
			doc := wireEdvView(st)
			if doc == nil {
				t.Skip("over-size doc falls back to v1 by design")
			}
			zig, ok := zigui.RenderEditorVideoInspV2(doc)
			if !ok {
				t.Fatal("insp render failed")
			}
			assertBytesEqual(t, "insp", editorVideoInspHTML(st), zig)
		})
	}
}

func edvReframeFixtures() map[string]edvReframeSt {
	plain := edvReframeSt{
		Title: "Reframe & zoom area",
		Frame: edvFrameSt{Show: true, AW: "1920", AH: "1080",
			ImgURL: "http://127.0.0.1:47621/img/s1/tok", HasCrop: true,
			CropL: "34.219", CropT: "0", CropW: "31.563", CropH: "100"},
		FrameBtn: uiBtn{Label: "Use playhead frame", Variant: "outline", Act: "edv-frame"},
		KfAdd:    uiBtn{Label: "+ Keyframe", Variant: "secondary", Act: "edv-kf-add"},
		KfClear:  uiBtn{Label: "Clear keyframes", Variant: "ghost", Act: "edv-kf-clear"},
		HasKfs:   true,
		Kfs: []edvKfRow{
			{Time: "1:23.5", Pos: "25", Go: "edv-kf-go:0", Del: "edv-kf-del:0", DelLb: "✕"},
			{Time: "4:56.0", Pos: "80", Go: "edv-kf-go:1", Del: "edv-kf-del:1", DelLb: "✕"},
		},
		RefHint: "Drag the bright window.",
	}
	busy := edvReframeSt{
		Title:    "Reframe",
		Frame:    edvFrameSt{Show: true, AW: "1080", AH: "1920", Busy: "Extracting frame…"},
		FrameBtn: uiBtn{Label: "Use playhead frame", Variant: "outline", Act: "edv-frame"},
		KfAdd:    uiBtn{Label: "+ Keyframe", Variant: "secondary", Act: "edv-kf-add"},
		KfClear:  uiBtn{Label: "Clear keyframes", Variant: "ghost", Act: "edv-kf-clear"},
		Kfs:      []edvKfRow{},
		RefHint:  "hint",
	}
	escaping := edvReframeSt{
		Title:    `R&ef "<t>"`,
		Frame:    edvFrameSt{Show: true, AW: "1920", AH: "1080", Busy: `b&usy"<>'`},
		FrameBtn: uiBtn{Label: `F&rame"`, Variant: "outline", Act: `edv-frame&"`},
		KfAdd:    uiBtn{Label: "K", Variant: "secondary", Act: "edv-kf-add"},
		KfClear:  uiBtn{Label: "C", Variant: "ghost", Act: "edv-kf-clear"},
		HasKfs:   true,
		Kfs:      []edvKfRow{{Time: `1:0"0.0`, Pos: "50", Go: `edv-kf-go:0&"`, Del: `edv-kf-del:0'`, DelLb: `✕&"`}},
		RefHint:  `r&ef"<>'`,
	}
	return map[string]edvReframeSt{"plain": plain, "busy": busy, "escaping": escaping}
}

// TestZigEdvReframeWire: reframe modal body + kf box + frame fragments.
func TestZigEdvReframeWire(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable")
	}
	for name, st := range edvReframeFixtures() {
		t.Run(name, func(t *testing.T) {
			doc := wireEdvReframe(st)
			if doc == nil {
				t.Fatal("wire marshal failed")
			}
			zig, ok := zigui.RenderEdvReframeV2(doc)
			if !ok {
				t.Fatal("reframe render failed")
			}
			assertBytesEqual(t, "reframe", edvReframeHTML(st), zig)

			zig, ok = zigui.RenderEdvKfBoxV2(doc)
			if !ok {
				t.Fatal("kfbox render failed")
			}
			assertBytesEqual(t, "kfbox", edvKfBoxHTML(st), zig)

			fdoc := wireEdvFrame(st.Frame)
			if fdoc == nil {
				t.Fatal("frame marshal failed")
			}
			zig, ok = zigui.RenderEdvFrameV2(fdoc)
			if !ok {
				t.Fatal("frame render failed")
			}
			assertBytesEqual(t, "frame", edvFrameHTML(st.Frame), zig)
		})
	}
}
