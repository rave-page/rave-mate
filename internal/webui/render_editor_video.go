package webui

import (
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vfx"
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
	ImgURL  string `json:"imgUrl"`  // "" = extracting placeholder
	Busy    string `json:"busy"`    // placeholder text when no image yet
	HasCrop bool   `json:"hasCrop"` // pan/zoom slack exists: shades + window
	// crop window + shades in % of the frame box (4 shades frame every side)
	CropL string `json:"cropL"`
	CropT string `json:"cropT"`
	CropW string `json:"cropW"`
	CropH string `json:"cropH"`
}

// edvFxParam is one plugin-parameter control (frei0r doubles are 0..1 by spec).
// Colors render as swatch + hex field; positions as two 0..1 sliders.
type edvFxParam struct {
	IsBool  bool     `json:"isBool"`
	IsColor bool     `json:"isColor"`
	Slider  uiSlider `json:"slider"`
	Toggle  uiToggle `json:"toggle"`
	Swatch  string   `json:"swatch"` // color: css rgb() of the current value
	Field   uiField  `json:"field"`  // color: hex input
}

// edvFxRow is one effect-chain entry.
type edvFxRow struct {
	Name    string       `json:"name"`
	Missing bool         `json:"missing"` // plugin file not found in the vfx dirs
	MissLb  string       `json:"missLb"`
	Off     bool         `json:"off"`
	Btns    []uiBtn      `json:"btns"` // tog/up/dn/del
	Params  []edvFxParam `json:"params"`
}

// edvFxPrevSt is the #edv-fxprev box.
type edvFxPrevSt struct {
	Show   bool   `json:"show"`
	ImgURL string `json:"imgUrl"`
	Busy   string `json:"busy"`
	AW     string `json:"aw"`
	AH     string `json:"ah"`
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

// edvViewState is the video-mode view (JSON/wire → Zig). NLE-style panes:
// viewer center, inspector right, timeline bottom (sources live in a modal).
type edvViewState struct {
	Title string      `json:"title"`
	Sub   string      `json:"sub"`
	Modes []edModeTab `json:"modes"`

	SrcBtn  uiBtn  `json:"srcBtn"` // opens the sources modal
	HasSrc  bool   `json:"hasSrc"`
	SrcName string `json:"srcName"`
	SrcInfo string `json:"srcInfo"` // "1920×1080 · 58:12"
	NoSrc   string `json:"noSrc"`

	ViewTitle string `json:"viewTitle"` // preview pane title
	InspTitle string `json:"inspTitle"` // inspector pane title

	Layout  selState `json:"layout"`  // crop | fit
	HasBlur bool     `json:"hasBlur"` // fit layout: show bg blur slider
	Blur    uiSlider `json:"blur"`

	Player     string `json:"player"`     // RAW mp component markup ("" = none)
	PlayerCls  string `json:"playerCls"`  // " edv-reframe"/" edv-reframe-fit" = live reframe preview
	PlayerVars string `json:"playerVars"` // CSS vars driving the preview crop
	NoMedia    string `json:"noMedia"`
	EditHint   string `json:"editHint"`

	SecReframe string   `json:"secReframe"`
	ShowRef    bool     `json:"showRef"` // video source only
	Aspect     selState `json:"aspect"`
	HasZoom    bool     `json:"hasZoom"`
	Zoom       uiSlider `json:"zoom"`       // crop zoom 1..4 (#edv-zoomrow)
	ReframeBtn uiBtn    `json:"reframeBtn"` // opens the reframe/area-select modal

	SecFx     string      `json:"secFx"`
	ShowFx    bool        `json:"showFx"` // video source only
	FxAdd     selState    `json:"fxAdd"`
	FxNone    string      `json:"fxNone"` // no-plugins / empty-chain hint
	FxRows    []edvFxRow  `json:"fxRows"`
	FxPrev    edvFxPrevSt `json:"fxPrev"`
	FxPrevBtn uiBtn       `json:"fxPrevBtn"`
	FxHint    string      `json:"fxHint"`
	FxSrc     []uiBtn     `json:"fxSrc"` // shader/plugin discovery: folders, sites, pack fetch

	SecExport string      `json:"secExport"`
	Export    edvExportSt `json:"export"`
}

// edvReframeSt is the reframe/area-select modal body (JSON/wire → Zig).
type edvReframeSt struct {
	Title    string     `json:"title"`
	Frame    edvFrameSt `json:"frame"`
	FrameBtn uiBtn      `json:"frameBtn"`
	KfAdd    uiBtn      `json:"kfAdd"`
	KfClear  uiBtn      `json:"kfClear"`
	HasKfs   bool       `json:"hasKfs"`
	Kfs      []edvKfRow `json:"kfs"`
	RefHint  string     `json:"refHint"`
}

func emptyEdvState() edvViewState {
	return edvViewState{
		Modes:  []edModeTab{},
		Layout: selState{Rows: []selRow{}},
		FxAdd:  selState{Rows: []selRow{}},
		FxRows: []edvFxRow{},
		FxSrc:  []uiBtn{},
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
	st.SrcBtn = uiBtn{Label: i18n.T("editor.video.changeSource"), Variant: "outline", Act: "edv-src-open"}
	st.NoSrc = i18n.T("editor.video.noSource")
	st.NoMedia = i18n.T("editor.video.noMedia")
	st.EditHint = i18n.T("editor.video.editHint")
	st.SecReframe = i18n.T("editor.video.sectionReframe")
	st.SecExport = i18n.T("editor.video.sectionExport")

	st.ViewTitle = i18n.T("editor.video.panePreview")
	st.InspTitle = i18n.T("editor.video.paneInspector")
	u.pubCapList() // prime the recent-captures cache so the sources modal opens filled

	srcW, srcH, dur := u.edvSrcDims()

	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	proj := v.proj
	editor.mu.Unlock()

	if proj.Source != "" {
		u.edvRebind() // restart: player re-binds the remembered source (one-shot)
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
		u.edvFxScan() // effect discovery, once per session (async)
		st.ShowRef = true
		aspOpts := make([][2]string, 0, len(videoedit.Aspects))
		for _, a := range videoedit.Aspects {
			aspOpts = append(aspOpts, [2]string{a.Key, i18n.T("editor.video.aspect." + a.Key)})
		}
		st.Aspect = resolveSelectBox(i18n.T("editor.video.aspectLabel"), "edv-aspect", aspOpts, proj.Aspect)
		st.HasZoom = true
		zoom := proj.Zoom
		if zoom < 1 {
			zoom = 1
		}
		st.Zoom = newSlider(i18n.T("editor.video.zoomLabel"), "edv-zoomset", 1, 4, 0.05, zoom, "")
		st.Layout = resolveSelectBox(i18n.T("editor.video.layoutLabel"), "edv-layout", [][2]string{
			{"crop", i18n.T("editor.video.layout.crop")},
			{"fit", i18n.T("editor.video.layout.fit")},
		}, proj.Layout)
		if proj.Layout == "fit" {
			st.HasBlur = true
			st.Blur = newSlider(i18n.T("editor.video.bgBlur"), "edv-bgblur", 0, 1, 0.01, proj.BGBlur, "")
		}
		st.ReframeBtn = uiBtn{Label: i18n.T("editor.video.reframeOpen"), Variant: "secondary", Act: "edv-reframe-open"}
	}

	if st.ShowRef {
		u.edvFxState(&st)
		st.PlayerCls, st.PlayerVars = u.edvPlayerReframe(proj, srcW, srcH)
	}
	st.Export = u.edvExportState()
	return st
}

// edvReframeState resolves the reframe-modal body state.
func (u *UI) edvReframeState() edvReframeSt {
	editor.mu.Lock()
	edvEnsure()
	proj := editor.video.proj
	editor.mu.Unlock()
	st := edvReframeSt{
		Title:    i18n.T("editor.video.reframeTitle"),
		Frame:    u.edvFrameState(),
		FrameBtn: uiBtn{Label: i18n.T("editor.video.useFrame"), Variant: "outline", Act: "edv-frame"},
		KfAdd:    uiBtn{Label: i18n.T("editor.video.addKeyframe"), Variant: "secondary", Act: "edv-kf-add"},
		KfClear:  uiBtn{Label: i18n.T("editor.video.clearKeyframes"), Variant: "ghost", Act: "edv-kf-clear"},
		HasKfs:   len(proj.PanKF) > 0,
		Kfs:      []edvKfRow{},
		RefHint:  i18n.T("editor.video.reframeHint"),
	}
	for i, k := range proj.PanKF {
		st.Kfs = append(st.Kfs, edvKfRow{
			Time: edvFmtTime(k.T), Pos: trimNum(float64(int(k.X*100 + 0.5))),
			Go: "edv-kf-go:" + strconv.Itoa(i), Del: "edv-kf-del:" + strconv.Itoa(i), DelLb: "✕",
		})
	}
	return st
}

// edvPlayerReframe resolves the timeline player's live reframe-preview class +
// CSS vars ("" = raw playback). Crop layout shows the selected slice; fit shows
// the original inside the target frame.
func (u *UI) edvPlayerReframe(proj videoedit.Project, srcW, srcH int) (cls, vars string) {
	a := videoedit.AspectByKey(proj.Aspect)
	cw, ch, axis := videoedit.CropSizeZoom(srcW, srcH, a, proj.Zoom)
	if cw <= 0 || (cw >= srcW-1 && ch >= srcH-1) {
		return "", ""
	}
	if proj.Layout == "fit" {
		aw, ah := a.W, a.H
		if aw <= 0 {
			aw, ah = cw, ch
		}
		return " edv-reframe-fit", fmt.Sprintf("--edv-ar:%d/%d", aw, ah)
	}
	t := u.edvPlayhead()
	pos, pos2 := proj.PanAt(t), proj.Pan2At(t)
	posX, posY := pos, pos2
	if axis == "y" {
		posX, posY = pos2, pos
	}
	x := float64(srcW-cw) * posX
	y := float64(srcH-ch) * posY
	return " edv-reframe", fmt.Sprintf("--edv-ar:%d/%d;--edv-vs:%s;--edv-vx:%s;--edv-vy:%s",
		cw, ch, trimNum(float64(srcW)/float64(cw)), trimPct(x/float64(cw)*100), trimPct(y/float64(ch)*100))
}

// edvFxState fills the effect-chain section of the view.
func (u *UI) edvFxState(st *edvViewState) {
	editor.mu.Lock()
	edvEnsure()
	effects := editor.video.proj.Effects
	plugins := editor.video.fxPlugins
	editor.mu.Unlock()

	st.ShowFx = true
	st.SecFx = i18n.T("editor.video.sectionFx")
	dir := ""
	if d, err := config.Dir(); err == nil {
		dir = filepath.Join(d, "vfx", "frei0r")
	}
	st.FxAdd = edvFxAddSel(plugins)
	switch {
	case len(plugins) == 0:
		st.FxNone = i18n.T("editor.video.fxNoPlugins", i18n.A{"dir": dir})
	case len(effects) == 0:
		st.FxNone = i18n.T("editor.video.fxEmpty")
	}
	for i, e := range effects {
		row := edvFxRow{Off: e.Off, Params: []edvFxParam{}}
		var pl *vfx.Plugin
		for j := range plugins {
			if filepath.Base(plugins[j].Ref) == e.Ref {
				pl = &plugins[j]
				break
			}
		}
		if pl == nil {
			row.Name, row.Missing = e.Ref, true
			row.MissLb = i18n.T("editor.video.fxMissing")
		} else {
			row.Name = pl.Name
		}
		is := strconv.Itoa(i)
		togVariant := "secondary"
		if e.Off {
			togVariant = "ghost"
		}
		row.Btns = []uiBtn{
			{Label: "⏻", Variant: togVariant, Act: "edv-fx-tog:" + is},
			{Label: "↑", Variant: "ghost", Act: "edv-fx-up:" + is},
			{Label: "↓", Variant: "ghost", Act: "edv-fx-dn:" + is},
			{Label: "✕", Variant: "warn", Act: "edv-fx-del:" + is},
		}
		if pl != nil && !e.Off {
			for _, prm := range pl.Params {
				act := "edv-fx-p:" + is + ":" + prm.Name
				val, ok := e.Params[prm.Name]
				if !ok {
					val = prm.Def[0]
				}
				switch prm.Type {
				case "double":
					row.Params = append(row.Params,
						edvFxParam{Slider: newSlider(prm.Name, act, 0, 1, 0.01, val, "")})
				case "bool":
					row.Params = append(row.Params,
						edvFxParam{IsBool: true, Toggle: newToggle(prm.Name, act, val >= 0.5)})
				case "color": // dotted channel overrides else listing default (ISF alpha stays shader-side)
					r := edvFxCh(e.Params, prm.Name, "r", prm.Def[0])
					g := edvFxCh(e.Params, prm.Name, "g", prm.Def[1])
					bl := edvFxCh(e.Params, prm.Name, "b", prm.Def[2])
					hex := fmt.Sprintf("#%02x%02x%02x", int(r*255+0.5), int(g*255+0.5), int(bl*255+0.5))
					fld := newField(prm.Name, "edv-fx-c:"+is+":"+prm.Name, hex, "text")
					fld.PH = "#rrggbb"
					row.Params = append(row.Params, edvFxParam{
						IsColor: true,
						Swatch:  fmt.Sprintf("rgb(%d,%d,%d)", int(r*255+0.5), int(g*255+0.5), int(bl*255+0.5)),
						Field:   fld,
					})
				case "position": // normalized 0..1 per frei0r spec / ISF norm coords
					row.Params = append(row.Params,
						edvFxParam{Slider: newSlider(prm.Name+" X", act+".x", 0, 1, 0.01, edvFxCh(e.Params, prm.Name, "x", prm.Def[0]), "")},
						edvFxParam{Slider: newSlider(prm.Name+" Y", act+".y", 0, 1, 0.01, edvFxCh(e.Params, prm.Name, "y", prm.Def[1]), "")})
				}
			}
		}
		st.FxRows = append(st.FxRows, row)
	}
	st.FxPrev = u.edvFxPrevState()
	st.FxPrevBtn = uiBtn{Label: i18n.T("editor.video.fxPreview"), Variant: "outline", Act: "edv-fx-prev"}
	st.FxHint = i18n.T("editor.video.fxHint", i18n.A{"dir": dir})
	packLbl := i18n.T("editor.video.fxPack")
	if edvPackBusy() {
		packLbl = i18n.T("editor.video.fxPackBusy")
	}
	st.FxSrc = []uiBtn{
		{Label: i18n.T("editor.video.fxWwwIsf"), Variant: "ghost", Act: "edv-fx-www:isf"},
		{Label: packLbl, Variant: "outline", Act: "edv-fx-getpack"},
		{Label: i18n.T("editor.video.fxDirIsf"), Variant: "ghost", Act: "edv-fx-dir:isf"},
		{Label: i18n.T("editor.video.fxDirF0r"), Variant: "ghost", Act: "edv-fx-dir:frei0r"},
	}
}

// edvFxAddSel builds the effect picker: filterable rich rows sorted by standard
// (frei0r, ISF) then name; badge = standard (+ "generator" for source shaders),
// sub = ISF categories else description - all filter-matchable. Val stays the
// fxPlugins index.
func edvFxAddSel(plugins []vfx.Plugin) selState {
	idx := make([]int, len(plugins))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		pa, pb := &plugins[idx[a]], &plugins[idx[b]]
		if pa.Kind != pb.Kind {
			return pa.Kind < pb.Kind // frei0r before isf
		}
		return strings.ToLower(pa.Name) < strings.ToLower(pb.Name)
	})
	gen := i18n.T("editor.video.fxGen")
	mix := i18n.T("editor.video.fxMix")
	opts := make([]ssOpt, 0, len(plugins))
	for _, i := range idx {
		p := &plugins[i]
		badge := p.Kind
		if p.Kind == "isf" {
			badge = "ISF"
		}
		switch p.PlugType {
		case "generator":
			badge += " · " + gen
		case "mixer":
			badge += " · " + mix
		}
		sub := p.Categories
		if sub == "" {
			sub = p.Desc
		}
		if len(sub) > 90 {
			r := []rune(sub)
			if len(r) > 90 {
				r = r[:90]
			}
			sub = string(r) + "…"
		}
		opts = append(opts, ssOpt{Val: strconv.Itoa(i), Label: p.Name, Sub: sub, Badge: badge})
	}
	optsCopy := opts
	s := resolveSmartSelect("edv-fx-add", "edv-fx-add", "", func() []ssOpt { return optsCopy })
	s.Label = i18n.T("editor.video.fxAdd")
	return s
}

// edvFxPrevState resolves the fx preview box (also the #edv-fxprev fragment).
func (u *UI) edvFxPrevState() edvFxPrevSt {
	srcW, srcH, _ := u.edvSrcDims()

	editor.mu.Lock()
	edvEnsure()
	v := &editor.video
	proj := v.proj
	prev, busy := v.fxPrev, v.fxPrevBusy
	editor.mu.Unlock()

	st := edvFxPrevSt{}
	if srcW <= 0 || srcH <= 0 {
		return st
	}
	cw, ch, _ := videoedit.CropSizeZoom(srcW, srcH, videoedit.AspectByKey(proj.Aspect), proj.Zoom)
	if cw == 0 {
		cw, ch = srcW, srcH
	}
	st.AW, st.AH = strconv.Itoa(cw), strconv.Itoa(ch)
	if busy {
		st.Show = true
		st.Busy = i18n.T("editor.video.extracting")
		return st
	}
	if prev != "" {
		st.Show = true
		st.ImgURL = u.imgURL(prev, 480)
	}
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
	panDrag, panLive, panLive2 := v.panDrag, v.panLive, v.panLive2
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
	cw, ch, axis := videoedit.CropSizeZoom(srcW, srcH, a, proj.Zoom)
	if cw == 0 || (srcW-cw < 2 && srcH-ch < 2) {
		return st
	}
	pos, pos2 := clamp01(proj.Pan), clamp01(0.5+proj.Pan2)
	if len(proj.PanKF) > 0 { // static preview: first key (drag shows live)
		pos, pos2 = proj.PanKF[0].X, clamp01(0.5+proj.PanKF[0].Y)
	}
	if panDrag {
		pos, pos2 = panLive, panLive2
	}
	posX, posY := pos, pos2
	if axis == "y" {
		posX, posY = pos2, pos
	}
	st.HasCrop = true
	wPct := float64(cw) / float64(srcW) * 100
	hPct := float64(ch) / float64(srcH) * 100
	lPct := float64(srcW-cw) / float64(srcW) * 100 * posX
	tPct := float64(srcH-ch) / float64(srcH) * 100 * posY
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
	if cw, ch, _ := videoedit.CropSizeZoom(srcW, srcH, videoedit.AspectByKey(proj.Aspect), proj.Zoom); cw > 0 && (cw < srcW || ch < srcH) {
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

// renderEditorVideoInsp renders the inspector fragment (#edv-insp inner).
func (u *UI) renderEditorVideoInsp() string {
	st := u.editorVideoState()
	if zigui.Available() {
		if h, ok := zigWire("RenderEditorVideoInspV2", wireEdvView(st), zigui.RenderEditorVideoInspV2,
			wireNoV1, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return editorVideoInspHTML(st)
}

// edvReframeBodyHTML renders the reframe-modal body.
func (u *UI) edvReframeBodyHTML(st edvReframeSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderEdvReframeV2", wireEdvReframe(st), zigui.RenderEdvReframeV2,
			wireNoV1, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return edvReframeHTML(st)
}

// edvFrameFragHTML renders the frame block fragment (#edv-frame inner).
func (u *UI) edvFrameFragHTML(st edvFrameSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderEdvFrameV2", wireEdvFrame(st), zigui.RenderEdvFrameV2,
			wireNoV1, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return edvFrameHTML(st)
}

// edvKfBoxFragHTML renders the keyframe box fragment (#edv-kfbox inner).
func (u *UI) edvKfBoxFragHTML(st edvReframeSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderEdvKfBoxV2", wireEdvReframe(st), zigui.RenderEdvKfBoxV2,
			wireNoV1, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return edvKfBoxHTML(st)
}

// ── pure Go renderers (golden reference; byte-identical to Zig) ──

func editorVideoHTML(st edvViewState) string {
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	b.WriteString(edModesHTML(st.Modes))
	b.WriteString(`<div class=edv-nle>`)

	// ── viewer pane: THE one preview - the mp player (video + wave + transport).
	// Inspector edits patch #edv-insp only, so the playing <video> is never rebuilt.
	b.WriteString(`<div class="edv-pane edv-pane-view"><div class=edv-pane-title>` +
		html.EscapeString(st.ViewTitle) + `</div>`)
	if st.Player != "" {
		b.WriteString(`<div class="edv-player` + st.PlayerCls + `"`)
		if st.PlayerVars != "" {
			b.WriteString(` style=` + attrQ(st.PlayerVars))
		}
		b.WriteString(`>` + st.Player + `</div>`)
		b.WriteString(hint("info", st.EditHint))
	} else if st.HasSrc {
		b.WriteString(emptyState(st.NoMedia))
	} else {
		b.WriteString(emptyState(st.NoSrc))
	}
	b.WriteString(`</div>`)

	// ── inspector pane (#edv-insp = the patch target for every non-media edit) ──
	b.WriteString(`<div class="edv-pane edv-pane-insp"><div class=edv-pane-title>` +
		html.EscapeString(st.InspTitle) + `</div>`)
	b.WriteString(`<div id=edv-insp>` + editorVideoInspHTML(st) + `</div>`)
	b.WriteString(`</div>`)

	b.WriteString(`</div>`)
	return b.String()
}

// editorVideoInspHTML renders the inspector body (#edv-insp inner).
func editorVideoInspHTML(st edvViewState) string {
	var b strings.Builder
	if st.HasSrc {
		b.WriteString(`<div class=edv-src><span class=edv-srcname>` + html.EscapeString(st.SrcName) + `</span>`)
		if st.SrcInfo != "" {
			b.WriteString(`<span class=edv-srcinfo>` + html.EscapeString(st.SrcInfo) + `</span>`)
		}
		b.WriteString(`</div>`)
	} else {
		b.WriteString(hint("info", st.NoSrc))
	}
	b.WriteString(btnRow(st.SrcBtn.html()))
	if st.ShowRef {
		b.WriteString(`<div class=edv-insp-sec>` + html.EscapeString(st.SecReframe) + `</div>`)
		b.WriteString(selHTML(st.Aspect))
		if st.HasZoom {
			b.WriteString(`<div id=edv-zoomrow>` + st.Zoom.html() + `</div>`)
		}
		b.WriteString(selHTML(st.Layout))
		if st.HasBlur {
			b.WriteString(st.Blur.html())
		}
		b.WriteString(btnRow(st.ReframeBtn.html()))
	}
	if st.ShowFx {
		b.WriteString(`<div class=edv-insp-sec>` + html.EscapeString(st.SecFx) + `</div>`)
		b.WriteString(selHTML(st.FxAdd))
		if st.FxNone != "" {
			b.WriteString(hint("info", st.FxNone))
		}
		for _, r := range st.FxRows {
			b.WriteString(edvFxRowHTML(r))
		}
		b.WriteString(btnRow(st.FxPrevBtn.html()))
		b.WriteString(`<div id=edv-fxprev>` + edvFxPrevHTML(st.FxPrev) + `</div>`)
		b.WriteString(hint("info", st.FxHint))
		if len(st.FxSrc) > 0 {
			var sb strings.Builder
			for _, btn := range st.FxSrc {
				sb.WriteString(btn.html())
			}
			b.WriteString(btnRow(sb.String()))
		}
	}
	b.WriteString(`<div class=edv-insp-sec>` + html.EscapeString(st.SecExport) + `</div>`)
	b.WriteString(`<div id=edv-export>` + edvExportHTML(st.Export) + `</div>`)
	return b.String()
}

// edvReframeHTML renders the reframe modal body (frame + keyframe controls).
func edvReframeHTML(st edvReframeSt) string {
	var b strings.Builder
	b.WriteString(`<div id=edv-frame>` + edvFrameHTML(st.Frame) + `</div>`)
	b.WriteString(`<div id=edv-kfbox>` + edvKfBoxHTML(st) + `</div>`)
	b.WriteString(hint("info", st.RefHint))
	return b.String()
}

// edvKfBoxHTML renders the keyframe button row + chips (#edv-kfbox inner).
func edvKfBoxHTML(st edvReframeSt) string {
	var b strings.Builder
	b.WriteString(btnRow(st.FrameBtn.html(), st.KfAdd.html(), st.KfClear.html()))
	if st.HasKfs {
		b.WriteString(`<div class=edv-kfs>`)
		for _, k := range st.Kfs {
			b.WriteString(`<span class=edv-kf><button class=edv-kf-go data-act=` + attrQ(k.Go) + `>` +
				html.EscapeString(k.Time) + ` · ` + k.Pos + `%</button>` +
				`<button class=edv-kf-del data-act=` + attrQ(k.Del) + `>` + html.EscapeString(k.DelLb) + `</button></span>`)
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

// edvFxRowHTML renders one effect-chain entry.
func edvFxRowHTML(r edvFxRow) string {
	var b strings.Builder
	if r.Off {
		b.WriteString(`<div class="edv-fx edv-fx-off">`)
	} else {
		b.WriteString(`<div class=edv-fx>`)
	}
	b.WriteString(`<div class=edv-fx-head><span class=edv-fx-name>` + html.EscapeString(r.Name) + `</span>`)
	if r.Missing {
		b.WriteString(`<span class=edv-fx-miss>` + html.EscapeString(r.MissLb) + `</span>`)
	}
	b.WriteString(uiBtnRow(r.Btns))
	b.WriteString(`</div>`)
	for _, p := range r.Params {
		switch {
		case p.IsColor:
			b.WriteString(`<div class=edv-fx-color><span class=edv-fx-swatch style=` +
				attrQ("background:"+p.Swatch) + `></span>` + p.Field.html() + `</div>`)
		case p.IsBool:
			b.WriteString(p.Toggle.html())
		default:
			b.WriteString(p.Slider.html())
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

// edvFxPrevHTML renders the fx preview box (the #edv-fxprev fragment).
func edvFxPrevHTML(st edvFxPrevSt) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=edv-fxprev-box style="aspect-ratio:` + st.AW + `/` + st.AH + `">`)
	if st.ImgURL != "" {
		b.WriteString(`<img class=edv-fimg src=` + attrQ(st.ImgURL) + ` alt="">`)
	} else {
		b.WriteString(`<span class=edv-fbusy>` + html.EscapeString(st.Busy) + `</span>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// edvFrameHTML renders the reframe box (the #edv-frame fragment). The crop
// overlay nests in #edv-fovl so drags patch ONLY the overlay - replacing the
// actpos element itself would drop the pointer capture (shell.go __pcur).
func edvFrameHTML(st edvFrameSt) string {
	if !st.Show {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=edv-fbox data-actpos=edv-pan data-actwheel=edv-zoom style="aspect-ratio:` + st.AW + `/` + st.AH + `">`)
	if st.ImgURL != "" {
		b.WriteString(`<img class=edv-fimg src=` + attrQ(st.ImgURL) + ` alt="">`)
	} else {
		b.WriteString(`<span class=edv-fbusy>` + html.EscapeString(st.Busy) + `</span>`)
	}
	b.WriteString(`<div id=edv-fovl>` + edvFrameOvlHTML(st) + `</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// edvFrameOvlHTML renders the shades + crop rect (the #edv-fovl fragment).
// Four shades frame the window on every side - with zoom both axes have slack.
func edvFrameOvlHTML(st edvFrameSt) string {
	if !st.HasCrop {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=edv-shade style="left:0;right:0;top:0;height:` + st.CropT + `%"></div>`)
	b.WriteString(`<div class=edv-shade style="left:0;right:0;top:calc(` + st.CropT + `% + ` + st.CropH + `%);bottom:0"></div>`)
	b.WriteString(`<div class=edv-shade style="left:0;width:` + st.CropL + `%;top:` + st.CropT + `%;height:` + st.CropH + `%"></div>`)
	b.WriteString(`<div class=edv-shade style="left:calc(` + st.CropL + `% + ` + st.CropW + `%);right:0;top:` + st.CropT + `%;height:` + st.CropH + `%"></div>`)
	b.WriteString(`<div class=edv-crop style="left:` + st.CropL + `%;top:` + st.CropT + `%;width:` + st.CropW + `%;height:` + st.CropH + `%"></div>`)
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
