package webui

import (
	"fmt"
	"html"
	"path/filepath"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/videoedit"
	"rave.page/mate/internal/zigui"
)

// Editor video mode - resolved render state + pure Go renderers (golden
// reference for native/zigui/src/editor_video.zig). Same contract as
// render_editor.go: every number crosses as its final string; the mp component
// markup rides RAW (player.go owns that surface + its own golden gate).

// edvKfRow is one pan-keyframe chip.
type edvKfRow struct {
	Time  string `json:"time"`  // "1:23.5"
	Pos   string `json:"pos"`   // window position %
	Go    string `json:"goAct"` // edv-kf-go:<i>
	Del   string `json:"delAct"`
	DelLb string `json:"delLb"`
}

// edvFrameSt is the #edv-frame reframe box (patched during pan drags).
type edvFrameSt struct {
	Show    bool   `json:"show"` // video source + probed dims
	AW      string `json:"aw"`   // source aspect
	AH      string `json:"ah"`
	ImgURL  string `json:"imgUrl"`   // "" = extracting placeholder
	Busy    string `json:"busy"`     // placeholder text when no image yet
	HasCrop bool   `json:"hasCrop"`  // aspect != orig: shades + window
	Vert    bool   `json:"vertAxis"` // free axis is y (shades top/bottom)
	// crop window + shades in % of the frame box
	CropL string `json:"cropL"`
	CropT string `json:"cropT"`
	CropW string `json:"cropW"`
	CropH string `json:"cropH"`
}

// edvExportSt is the #edv-export block.
type edvExportSt struct {
	Preset    selState `json:"preset"`
	Out       uiField  `json:"out"`
	OutBrowse uiBtn    `json:"outBrowse"`
	Export    uiBtn    `json:"export"`
	Running   bool     `json:"running"`
	Pct       string   `json:"pct"` // progressBar fill ("42.0%")
	Stage     string   `json:"stage"`
	Cancel    uiBtn    `json:"cancel"`
	HasResult bool     `json:"hasResult"`
	Result    string   `json:"result"`
	HasErr    bool     `json:"hasErr"`
	Err       string   `json:"err"`
	TrimInfo  string   `json:"trimInfo"` // "cut 1:20–4:56 · crop 606×1080"
}

// edvViewState is the video-mode view (JSON/wire → Zig).
type edvViewState struct {
	Title string      `json:"title"`
	Sub   string      `json:"sub"`
	Modes []edModeTab `json:"modes"`

	SecSource string   `json:"secSource"`
	Browse    uiBtn    `json:"browse"`
	Caps      selState `json:"caps"`
	HasSrc    bool     `json:"hasSrc"`
	SrcName   string   `json:"srcName"`
	SrcInfo   string   `json:"srcInfo"` // "1920×1080 · 58:12"
	NoSrc     string   `json:"noSrc"`

	Player   string `json:"player"` // RAW mp component markup ("" = none)
	NoMedia  string `json:"noMedia"`
	EditHint string `json:"editHint"`

	SecReframe string     `json:"secReframe"`
	ShowRef    bool       `json:"showRef"` // video source only
	Aspect     selState   `json:"aspect"`
	Frame      edvFrameSt `json:"frame"`
	FrameBtn   uiBtn      `json:"frameBtn"`
	KfAdd      uiBtn      `json:"kfAdd"`
	KfClear    uiBtn      `json:"kfClear"`
	HasKfs     bool       `json:"hasKfs"`
	Kfs        []edvKfRow `json:"kfs"`
	RefHint    string     `json:"refHint"`

	SecExport string      `json:"secExport"`
	Export    edvExportSt `json:"export"`
}

func emptyEdvState() edvViewState {
	return edvViewState{
		Modes: []edModeTab{},
		Caps:  selState{Rows: []selRow{}},
		Kfs:   []edvKfRow{},
		Export: edvExportSt{
			Preset: selState{Rows: []selRow{}},
		},
	}
}

// ── state builders ──

// edvFmtTime renders source seconds as m:ss.d.
func edvFmtTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	m := int(sec) / 60
	s := sec - float64(m*60)
	return fmt.Sprintf("%d:%04.1f", m, s)
}

// editorVideoState resolves the video-mode view. Caller must NOT hold editor.mu
// (mp snapshots + smart-select registration lock on their own).
func (u *UI) editorVideoState() edvViewState {
	st := emptyEdvState()
	st.Title = i18n.T("editor.title")
	st.Sub = i18n.T("editor.video.subtitle")
	st.Modes = []edModeTab{
		{Val: "image", Label: i18n.T("editor.modeImage")},
		{Val: "video", Label: i18n.T("editor.modeVideo"), Active: true},
	}
	st.SecSource = i18n.T("editor.video.sectionSource")
	st.Browse = uiBtn{Label: i18n.T("editor.browse"), Variant: "outline", Act: "pick-file:edv-src"}
	st.NoSrc = i18n.T("editor.video.noSource")
	st.NoMedia = i18n.T("editor.video.noMedia")
	st.EditHint = i18n.T("editor.video.editHint")
	st.SecReframe = i18n.T("editor.video.sectionReframe")
	st.RefHint = i18n.T("editor.video.reframeHint")
	st.SecExport = i18n.T("editor.video.sectionExport")

	// recent captures → source quick-pick
	capOpts := [][2]string{}
	if caps, _ := u.pubCapList(); len(caps) > 0 {
		n := len(caps)
		if n > 20 {
			n = 20
		}
		for _, s := range caps[:n] {
			capOpts = append(capOpts, [2]string{s.ID, filepath.Base(s.Path)})
		}
	}
	st.Caps = resolveSelectBox(i18n.T("editor.video.fromCapture"), "edv-cap:", capOpts, "")

	srcW, srcH, dur := u.edvSrcDims()

	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	proj := v.proj
	editor.mu.Unlock()

	if proj.Source != "" {
		st.HasSrc = true
		st.SrcName = filepath.Base(proj.Source)
		if srcW > 0 && srcH > 0 {
			st.SrcInfo = fmt.Sprintf("%d×%d", srcW, srcH)
		}
		if dur > 0 {
			if st.SrcInfo != "" {
				st.SrcInfo += " · "
			}
			st.SrcInfo += edvFmtTime(dur)
		}
	}

	st.Player = u.mpHTML("editor")

	if st.HasSrc && edvIsVideo(proj.Source) {
		st.ShowRef = true
		aspOpts := make([][2]string, 0, len(videoedit.Aspects))
		for _, a := range videoedit.Aspects {
			aspOpts = append(aspOpts, [2]string{a.Key, i18n.T("editor.video.aspect." + a.Key)})
		}
		st.Aspect = resolveSelectBox(i18n.T("editor.video.aspectLabel"), "edv-aspect", aspOpts, proj.Aspect)
		st.Frame = u.edvFrameState()
		st.FrameBtn = uiBtn{Label: i18n.T("editor.video.useFrame"), Variant: "outline", Act: "edv-frame"}
		st.KfAdd = uiBtn{Label: i18n.T("editor.video.addKeyframe"), Variant: "secondary", Act: "edv-kf-add"}
		st.KfClear = uiBtn{Label: i18n.T("editor.video.clearKeyframes"), Variant: "ghost", Act: "edv-kf-clear"}
		st.HasKfs = len(proj.PanKF) > 0
		for i, k := range proj.PanKF {
			st.Kfs = append(st.Kfs, edvKfRow{
				Time: edvFmtTime(k.T), Pos: trimNum(float64(int(k.X*100 + 0.5))),
				Go: "edv-kf-go:" + strconv.Itoa(i), Del: "edv-kf-del:" + strconv.Itoa(i), DelLb: "✕",
			})
		}
	}

	st.Export = u.edvExportState()
	return st
}

// edvFrameState resolves the reframe box (also the #edv-frame drag fragment).
func (u *UI) edvFrameState() edvFrameSt {
	srcW, srcH, _ := u.edvSrcDims()

	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	proj := v.proj
	framePath, frameBusy := v.framePath, v.frameBusy
	panDrag, panLive := v.panDrag, v.panLive
	editor.mu.Unlock()

	st := edvFrameSt{}
	if proj.Source == "" || !edvIsVideo(proj.Source) || srcW <= 0 || srcH <= 0 {
		return st
	}
	st.Show = true
	st.AW, st.AH = strconv.Itoa(srcW), strconv.Itoa(srcH)
	if framePath != "" {
		st.ImgURL = u.imgURL(framePath, 960)
	} else if frameBusy {
		st.Busy = i18n.T("editor.video.extracting")
	} else {
		st.Busy = i18n.T("editor.video.noFrame")
	}

	a := videoedit.AspectByKey(proj.Aspect)
	cw, ch, axis := videoedit.CropSize(srcW, srcH, a)
	if axis == "" {
		return st
	}
	pos := proj.Pan
	if len(proj.PanKF) > 0 {
		pos = proj.PanKF[0].X // static preview: first key (drag shows live)
	}
	if panDrag {
		pos = panLive
	}
	st.HasCrop = true
	st.Vert = axis == "y"
	wPct := float64(cw) / float64(srcW) * 100
	hPct := float64(ch) / float64(srcH) * 100
	var lPct, tPct float64
	if axis == "x" {
		lPct = float64(srcW-cw) / float64(srcW) * 100 * pos
	} else {
		tPct = float64(srcH-ch) / float64(srcH) * 100 * pos
	}
	st.CropL, st.CropT = trimPct(lPct), trimPct(tPct)
	st.CropW, st.CropH = trimPct(wPct), trimPct(hPct)
	return st
}

// trimPct formats a percentage with 3 decimals, trailing zeros trimmed.
func trimPct(f float64) string {
	s := strconv.FormatFloat(f, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// edvExportState resolves the export block (also the #edv-export fragment).
func (u *UI) edvExportState() edvExportSt {
	srcW, srcH, _ := u.edvSrcDims()
	t := u.mpSnap("editor")

	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	proj := v.proj
	ex := v.export
	editor.mu.Unlock()

	opts := make([][2]string, 0, len(videoedit.ExportPresets))
	for _, e := range videoedit.ExportPresets {
		opts = append(opts, [2]string{e.Key, i18n.T("editor.video.preset." + e.LabelKey)})
	}
	st := edvExportSt{
		Preset:    resolveSelectBox(i18n.T("editor.video.presetLabel"), "edv-preset", opts, proj.PresetKey),
		Out:       newField(i18n.T("editor.video.outPath"), "edv-out", proj.OutPath, "text"),
		OutBrowse: uiBtn{Label: i18n.T("editor.browse"), Variant: "outline", Act: "pick-save:mp4:edv-out"},
		Export:    uiBtn{Label: i18n.T("editor.video.export"), Variant: "go", Act: "edv-export"},
		Cancel:    uiBtn{Label: i18n.T("common.cancel"), Variant: "warn", Act: "edv-excancel"},
	}
	st.Out.PH = i18n.T("editor.video.outAuto")

	// trim + crop summary
	var parts []string
	if len(t.media) > 0 {
		in := 0.0
		if t.inSec > 0 {
			in = t.inSec
		}
		if t.outSec > 0 {
			parts = append(parts, i18n.T("editor.video.cutInfo",
				i18n.A{"in": edvFmtTime(in), "out": edvFmtTime(t.outSec)}))
		} else if in > 0 {
			parts = append(parts, i18n.T("editor.video.cutFrom", i18n.A{"in": edvFmtTime(in)}))
		}
	}
	if cw, ch, axis := videoedit.CropSize(srcW, srcH, videoedit.AspectByKey(proj.Aspect)); axis != "" {
		parts = append(parts, fmt.Sprintf("crop %d×%d", cw, ch))
	}
	st.TrimInfo = strings.Join(parts, " · ")

	st.Running = ex.running
	if ex.running {
		st.Pct = progressPct(ex.pct / 100)
		st.Stage = ex.stage
	}
	if ex.result != "" {
		st.HasResult, st.Result = true, i18n.T("editor.video.exportedTo", i18n.A{"path": ex.result})
	}
	if ex.err != "" {
		st.HasErr, st.Err = true, ex.err
	}
	return st
}

// ── bridge ──

// renderEditorVideo is the video-mode view (mode switch routed in renderEditor).
func (u *UI) renderEditorVideo() string {
	st := u.editorVideoState()
	if zigui.Available() {
		if h, ok := zigWire("RenderEditorVideoV2", wireEdvView(st), zigui.RenderEditorVideoV2,
			zigui.RenderEditorVideo, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return editorVideoHTML(st)
}

// ── pure Go renderers (golden reference; byte-identical to Zig) ──

func editorVideoHTML(st edvViewState) string {
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	b.WriteString(edModesHTML(st.Modes))

	// source row
	var src strings.Builder
	src.WriteString(`<div class=edv-src>`)
	src.WriteString(st.Browse.html())
	src.WriteString(selHTML(st.Caps))
	if st.HasSrc {
		src.WriteString(`<span class=edv-srcname>` + html.EscapeString(st.SrcName) + `</span>`)
		if st.SrcInfo != "" {
			src.WriteString(`<span class=edv-srcinfo>` + html.EscapeString(st.SrcInfo) + `</span>`)
		}
	} else {
		src.WriteString(hint("info", st.NoSrc))
	}
	src.WriteString(`</div>`)
	b.WriteString(section(st.SecSource, src.String()))

	// embedded player/editor
	if st.Player != "" {
		b.WriteString(`<div class=edv-player>` + st.Player + `</div>`)
		b.WriteString(hint("info", st.EditHint))
	} else if st.HasSrc {
		b.WriteString(emptyState(st.NoMedia))
	}

	if st.ShowRef {
		var rf strings.Builder
		rf.WriteString(selHTML(st.Aspect))
		rf.WriteString(`<div id=edv-frame>` + edvFrameHTML(st.Frame) + `</div>`)
		rf.WriteString(btnRow(st.FrameBtn.html(), st.KfAdd.html(), st.KfClear.html()))
		if st.HasKfs {
			rf.WriteString(`<div class=edv-kfs>`)
			for _, k := range st.Kfs {
				rf.WriteString(`<span class=edv-kf><button class=edv-kf-go data-act=` + attrQ(k.Go) + `>` +
					html.EscapeString(k.Time) + ` · ` + k.Pos + `%</button>` +
					`<button class=edv-kf-del data-act=` + attrQ(k.Del) + `>` + html.EscapeString(k.DelLb) + `</button></span>`)
			}
			rf.WriteString(`</div>`)
		}
		rf.WriteString(hint("info", st.RefHint))
		b.WriteString(section(st.SecReframe, rf.String()))
	}

	b.WriteString(section(st.SecExport, `<div id=edv-export>`+edvExportHTML(st.Export)+`</div>`))
	return b.String()
}

// edvFrameHTML renders the reframe box (the #edv-frame fragment).
func edvFrameHTML(st edvFrameSt) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=edv-fbox data-actpos=edv-pan style="aspect-ratio:` + st.AW + `/` + st.AH + `">`)
	if st.ImgURL != "" {
		b.WriteString(`<img class=edv-fimg src=` + attrQ(st.ImgURL) + ` alt="">`)
	} else {
		b.WriteString(`<span class=edv-fbusy>` + html.EscapeString(st.Busy) + `</span>`)
	}
	if st.HasCrop {
		if st.Vert {
			b.WriteString(`<div class=edv-shade style="left:0;right:0;top:0;height:` + st.CropT + `%"></div>`)
			b.WriteString(`<div class=edv-shade style="left:0;right:0;top:calc(` + st.CropT + `% + ` + st.CropH + `%);bottom:0"></div>`)
		} else {
			b.WriteString(`<div class=edv-shade style="top:0;bottom:0;left:0;width:` + st.CropL + `%"></div>`)
			b.WriteString(`<div class=edv-shade style="top:0;bottom:0;left:calc(` + st.CropL + `% + ` + st.CropW + `%);right:0"></div>`)
		}
		b.WriteString(`<div class=edv-crop style="left:` + st.CropL + `%;top:` + st.CropT + `%;width:` + st.CropW + `%;height:` + st.CropH + `%"></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// edvExportHTML renders the export block (the #edv-export fragment).
func edvExportHTML(st edvExportSt) string {
	var b strings.Builder
	b.WriteString(selHTML(st.Preset))
	b.WriteString(`<div class=edv-outrow>` + st.Out.html() + st.OutBrowse.html() + `</div>`)
	if st.TrimInfo != "" {
		b.WriteString(`<div class=edv-triminfo>` + html.EscapeString(st.TrimInfo) + `</div>`)
	}
	if st.Running {
		b.WriteString(progressBarStr(st.Pct, st.Stage))
		b.WriteString(btnRow(st.Cancel.html()))
	} else {
		b.WriteString(btnRow(st.Export.html()))
	}
	if st.HasResult {
		b.WriteString(hint("ok", st.Result))
	}
	if st.HasErr {
		b.WriteString(hint("bad", st.Err))
	}
	return b.String()
}
