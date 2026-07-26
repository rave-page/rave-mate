package webui

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/visualeditor"
	"rave.page/mate/internal/zigui"
)

// Editor is a Zig-rendered tab (native/zigui/src/editor.zig): Go resolves the document +
// selection + i18n into edState, the Zig lib renders HTML byte-identical to the Go
// renderers below (fallback + golden reference, zigui_golden_editor_test.go).
//
// Every NUMBER is resolved to its string here (trimNum/pct/edCQW - Go's shortest
// round-trip float formatting has no guaranteed Zig equivalent), as are the two `%q`
// tokens (font-family, image URL: Go strconv.Quote semantics). Structure - which CSS
// declarations appear, in what order, the conditionals, escaping and attribute quoting -
// is ported to Zig.

// edGradStop is one resolved linear-gradient stop.
type edGradStop struct {
	RGBA string `json:"rgba"`
	Pos  string `json:"pos"` // trimNum(clamp01(pos)*100)
}

// edPaint is a leaf's background CSS resolved to its tokens. Kind "" = no paint.
type edPaint struct {
	Kind  string       `json:"kind"` // ""|solid|gradient|image
	RGBA  string       `json:"rgba"`
	Angle string       `json:"angle"` // gradient: trimNum(Angle+90)
	Stops []edGradStop `json:"stops"`
	URLQ  string       `json:"urlq"` // image: Go %q of the file:// URL (quotes included)
	Size  string       `json:"size"` // image: cover|contain|100% 100%
}

// edText is a text leaf's inner span (placeholders already substituted).
type edText struct {
	Content string `json:"content"`
	FamQ    string `json:"famq"` // Go %q of the font family (quotes included)
	Size    string `json:"size"` // cqw
	LH      string `json:"lh"`
	Align   string `json:"alignment"`
	RGBA    string `json:"rgba"`
	LS      string `json:"ls"` // cqw
}

// edInner is a leaf's inner HTML source. Kind "" = empty.
type edInner struct {
	Kind        string `json:"kind"` // ""|text|imgph
	Text        edText `json:"text"`
	Placeholder string `json:"placeholder"`
}

// edLayer is one composited preview layer (group or leaf).
type edLayer struct {
	Group   bool   `json:"group"`
	ID      string `json:"id"`
	Sel     bool   `json:"sel"`
	Blend   string `json:"blend"`   // "" = normal → no declaration
	Opacity string `json:"opacity"` // "" = 1 → no declaration

	Xform bool   `json:"xform"` // group: transform is not identity
	Tx    string `json:"tx"`
	Ty    string `json:"ty"`
	Sx    string `json:"sx"`
	Sy    string `json:"sy"`
	Rot   string `json:"rot"` // group: set when Xform; leaf: "" = no rotate()

	Left string `json:"left"` // leaf placement, all in %
	Top  string `json:"top"`
	W    string `json:"w"`
	H    string `json:"h"`

	Paint edPaint `json:"paint"`
	Inner edInner `json:"inner"`

	Children []edLayer `json:"children"`
}

// edPreviewState is the #ed-preview fragment (live WYSIWYG composite + caption).
type edPreviewState struct {
	AW     string    `json:"aw"` // stage aspect-ratio numerator
	AH     string    `json:"ah"`
	Layers []edLayer `json:"layers"`
	Cap    string    `json:"cap"`
	Hint   string    `json:"hint"`
}

// edRow is one layers-panel row (flat, in document order; rendered top-first).
type edRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Depth   int    `json:"depth"`
	Group   bool   `json:"group"`
	Sel     bool   `json:"sel"`
	Visible bool   `json:"visible"`
	Locked  bool   `json:"locked"`
}

// edActionsState is the reorder/group/delete bar + the selection's opacity/blend.
type edActionsState struct {
	Up      string   `json:"up"`
	Down    string   `json:"down"`
	Group   string   `json:"group"`
	Ungroup string   `json:"ungroup"`
	Delete  string   `json:"delete"`
	HasSel  bool     `json:"hasSel"`
	NoSel   string   `json:"noSel"` // hint shown instead of the selection controls
	Opacity uiSlider `json:"opacity"`
	Blend   selState `json:"blend"`
}

// edLayersState is the layers panel.
type edLayersState struct {
	Rows    []edRow        `json:"rows"`
	Empty   string         `json:"empty"`
	Actions edActionsState `json:"actions"`
}

// edColorRowState is a swatch + hex field (no native colour input per house rules).
type edColorRowState struct {
	RGBA  string  `json:"rgba"`
	Field uiField `json:"field"`
}

// edInspTextState is the text-layer inspector block.
type edInspTextState struct {
	Label   string          `json:"label"`
	Content string          `json:"content"`
	Hint    string          `json:"hint"`
	Font    selState        `json:"font"`
	Size    uiField         `json:"size"`
	LS      uiField         `json:"ls"`
	LH      uiField         `json:"lh"`
	Align   selState        `json:"alignment"`
	Color   edColorRowState `json:"color"`
}

// edInspState is the property inspector for the selection.
type edInspState struct {
	HasSel bool    `json:"hasSel"`
	Empty  string  `json:"empty"`
	Name   uiField `json:"name"`
	X      uiField `json:"x"`
	Y      uiField `json:"y"`
	ShowWH bool    `json:"showWh"` // leaves only
	W      uiField `json:"w"`
	H      uiField `json:"h"`
	SX     uiField `json:"sx"`
	SY     uiField `json:"sy"`
	Rot    uiField `json:"rot"`

	Kind  string          `json:"kind"` // ""|text|solid|gradient|image
	Text  edInspTextState `json:"text"`
	Fill  edColorRowState `json:"fill"`  // solid
	Angle uiField         `json:"angle"` // gradient
	Start edColorRowState `json:"start"`
	End   edColorRowState `json:"end"`
	Path  uiField         `json:"path"` // image
	Fit   selState        `json:"fit"`
}

// edViewState is the resolved render state for the Editor view (JSON → Zig).
type edViewState struct {
	Title        string `json:"title"`
	Sub          string `json:"sub"`
	Disabled     bool   `json:"disabled"` // MediaEditor feature off
	DisabledSub  string `json:"disabledSub"`
	DisabledHint string `json:"disabledHint"`

	SecPreview   string `json:"secPreview"`
	SecLayers    string `json:"secLayers"`
	SecInspector string `json:"secInspector"`

	Row1 []uiBtn `json:"row1"` // toolbar: add + templates
	Row2 []uiBtn `json:"row2"` // toolbar: undo/redo/save/export/canvas

	Preview edPreviewState `json:"preview"`
	Layers  edLayersState  `json:"layers"`
	Insp    edInspState    `json:"insp"`
}

// edNilSel is an unregistered smart select with a NON-NIL row slice (nil marshals to
// JSON null, which fails the Zig slice parse).
func edNilSel() selState { return selState{Rows: []selRow{}} }

// emptyEdState zeroes the view with non-nil slices everywhere.
func emptyEdState() edViewState {
	return edViewState{
		Row1: []uiBtn{}, Row2: []uiBtn{},
		Preview: edPreviewState{Layers: []edLayer{}},
		Layers:  edLayersState{Rows: []edRow{}, Actions: edActionsState{Blend: edNilSel()}},
		Insp:    edInspState{Text: edInspTextState{Font: edNilSel(), Align: edNilSel()}, Fit: edNilSel()},
	}
}

// ── state builders (all callers hold editor.mu) ──

// editorState resolves the document + selection + i18n into render state.
func (u *UI) editorState() edViewState {
	st := emptyEdState()
	st.Title = i18n.T("editor.title")
	if u.svc.Cfg != nil && !u.svc.Cfg.Features.MediaEditor.Enabled {
		st.Disabled = true
		st.DisabledSub, st.DisabledHint = i18n.T("editor.subtitleDisabled"), i18n.T("editor.disabledHint")
		return st
	}
	edEnsure()
	editor.mu.Lock()
	defer editor.mu.Unlock()

	st.Sub = i18n.T("editor.subtitle")
	st.SecPreview = i18n.T("editor.sectionPreview")
	st.SecLayers = i18n.T("editor.sectionLayers")
	st.SecInspector = i18n.T("editor.sectionInspector")
	st.Row1, st.Row2 = edToolbarState()
	st.Preview = u.edPreviewState()
	st.Layers = u.edLayersState()
	st.Insp = u.edInspState()
	return st
}

// edToolbarState resolves the two toolbar button rows.
func edToolbarState() (row1, row2 []uiBtn) {
	row1 = []uiBtn{
		{Label: "+ " + i18n.T("editor.text"), Variant: "primary", Act: "ed-add:text"},
		{Label: "+ " + i18n.T("editor.image"), Variant: "outline", Act: "ed-add:image"},
		{Label: "+ " + i18n.T("editor.solid"), Variant: "outline", Act: "ed-add:solid"},
		{Label: "+ " + i18n.T("editor.gradient"), Variant: "outline", Act: "ed-add:gradient"},
		{Label: i18n.T("editor.templates"), Variant: "explore", Act: "ed-tpl-open"},
	}
	undo, redo := "ghost", "ghost"
	if len(editor.undo) > 0 {
		undo = "secondary"
	}
	if len(editor.redo) > 0 {
		redo = "secondary"
	}
	row2 = []uiBtn{
		{Label: "↶ " + i18n.T("editor.undo"), Variant: undo, Act: "ed-undo"},
		{Label: "↷ " + i18n.T("editor.redo"), Variant: redo, Act: "ed-redo"},
		{Label: i18n.T("editor.saveTemplate"), Variant: "outline", Act: "ed-save-tpl"},
		{Label: i18n.T("editor.exportPng"), Variant: "go", Act: "ed-export"},
		{Label: i18n.T("editor.canvasSize", i18n.A{"w": fmt.Sprint(editor.doc.W), "h": fmt.Sprint(editor.doc.H)}), Variant: "ghost", Act: "ed-canvas"},
	}
	return row1, row2
}

// edPreviewState resolves the CSS composite + caption.
func (u *UI) edPreviewState() edPreviewState {
	d := editor.doc
	aw, ah := d.W, d.H
	if aw <= 0 || ah <= 0 {
		aw, ah = 16, 9
	}
	cap := i18n.T("editor.docInfo", i18n.A{"w": fmt.Sprint(d.W), "h": fmt.Sprint(d.H), "count": fmt.Sprint(edCountLeaves(d.Root.Children))})
	if l, _ := d.Find(editor.selID); l != nil {
		cap += " " + i18n.T("editor.selectedInfo", i18n.A{"name": l.Name})
	}
	return edPreviewState{
		AW: strconv.Itoa(aw), AH: strconv.Itoa(ah),
		Layers: u.edLayerStates(d.Root.Children, d),
		Cap:    cap, Hint: i18n.T("editor.placeholderHint"),
	}
}

// edLayerStates resolves the visible layers of one level (invisible/transparent skipped,
// like the compositor).
func (u *UI) edLayerStates(layers []*visualeditor.Layer, d *visualeditor.Document) []edLayer {
	out := make([]edLayer, 0, len(layers))
	for _, l := range layers {
		if l == nil || !l.Visible || l.Opacity <= 0 {
			continue
		}
		out = append(out, u.edLayerState(l, d))
	}
	return out
}

// edLayerState resolves one layer. Groups become a full-stage wrapper carrying
// opacity/blend + the group transform (children keep doc-space coords, matching the
// compositor's placement); leaves fold scale into size and center-correct the origin.
func (u *UI) edLayerState(l *visualeditor.Layer, d *visualeditor.Document) edLayer {
	st := edLayer{
		ID: l.ID, Sel: l.ID == editor.selID, Group: l.IsGroup(),
		Paint: edPaint{Stops: []edGradStop{}}, Children: []edLayer{},
	}
	if bm := edBlendCSS(l.Blend); bm != "normal" {
		st.Blend = bm
	}
	if l.Opacity < 1 {
		st.Opacity = trimNum(clamp01(l.Opacity))
	}
	if st.Group {
		tx, ty := l.Transform.X, l.Transform.Y
		sx, sy := edScale(l.Transform.ScaleX), edScale(l.Transform.ScaleY)
		if tx != 0 || ty != 0 || sx != 1 || sy != 1 || l.Transform.Rotation != 0 {
			st.Xform = true
			st.Tx, st.Ty = pct(tx, d.W), pct(ty, d.H)
			st.Rot, st.Sx, st.Sy = trimNum(l.Transform.Rotation), trimNum(sx), trimNum(sy)
		}
		st.Children = u.edLayerStates(l.Children, d)
		return st
	}
	w, h := l.W, l.H
	sx, sy := math.Abs(edScale(l.Transform.ScaleX)), math.Abs(edScale(l.Transform.ScaleY))
	st.Left, st.Top = pct(l.Transform.X-w*(sx-1)/2, d.W), pct(l.Transform.Y-h*(sy-1)/2, d.H)
	st.W, st.H = pct(w*sx, d.W), pct(h*sy, d.H)
	if l.Transform.Rotation != 0 {
		st.Rot = trimNum(l.Transform.Rotation)
	}
	st.Paint = edPaintState(l)
	st.Inner = u.edInnerState(l)
	return st
}

// edPaintState resolves background/paint CSS tokens for a leaf (solid/gradient/image).
func edPaintState(l *visualeditor.Layer) edPaint {
	p := edPaint{Stops: []edGradStop{}}
	switch l.Kind {
	case visualeditor.KindSolid:
		if l.Solid != nil {
			p.Kind, p.RGBA = "solid", edRGBA(l.Solid.Color)
		}
	case visualeditor.KindGradient:
		if g := l.Gradient; g != nil && len(g.Stops) >= 2 {
			// engine angle: 0 = L→R, 90 = T→B; CSS: 90deg = →right, 180deg = ↓ ⇒ +90
			p.Kind, p.Angle = "gradient", trimNum(g.Angle+90)
			for _, s := range g.Stops {
				p.Stops = append(p.Stops, edGradStop{RGBA: edRGBA(s.Color), Pos: trimNum(clamp01(s.Pos) * 100)})
			}
		}
	case visualeditor.KindImage:
		if l.Image != nil && strings.TrimSpace(l.Image.Path) != "" {
			size := "cover"
			switch l.Image.Fit {
			case visualeditor.FitContain:
				size = "contain"
			case visualeditor.FitStretch:
				size = "100% 100%"
			}
			p.Kind, p.URLQ, p.Size = "image", fmt.Sprintf("%q", edFileURL(l.Image.Path)), size
		}
		// else: placeholder handled in edInnerState
	}
	return p
}

// edInnerState resolves a leaf's inner content (text span, or an image placeholder).
func (u *UI) edInnerState(l *visualeditor.Layer) edInner {
	switch l.Kind {
	case visualeditor.KindText:
		if t := l.Text; t != nil {
			fam := t.FontFamily
			if fam == "" {
				fam = visualeditor.DefaultFontFamily
			}
			lh := t.LineHeight
			if lh <= 0 {
				lh = 1.2
			}
			align := string(t.Align)
			if align == "" {
				align = "left"
			}
			return edInner{Kind: "text", Text: edText{
				Content: visualeditor.Substitute(t.Content, edProvider{u}),
				FamQ:    fmt.Sprintf("%q", fam),
				Size:    edCQW(t.FontSize, editor.doc.W),
				LH:      trimNum(lh), Align: align, RGBA: edRGBA(t.Color),
				LS: edCQW(t.LetterSpacing, editor.doc.W),
			}}
		}
	case visualeditor.KindImage:
		if l.Image == nil || strings.TrimSpace(l.Image.Path) == "" {
			return edInner{Kind: "imgph", Placeholder: i18n.T("editor.imagePlaceholder")}
		}
	}
	return edInner{}
}

// edLayersState resolves the layers panel (flat rows in document order + the action bar).
func (u *UI) edLayersState() edLayersState {
	return edLayersState{
		Rows:    edRowStates(editor.doc.Root.Children, 0, make([]edRow, 0, 8)),
		Empty:   i18n.T("editor.noLayers"),
		Actions: edActionsStateOf(),
	}
}

func edRowStates(layers []*visualeditor.Layer, depth int, out []edRow) []edRow {
	for _, l := range layers {
		out = append(out, edRow{ID: l.ID, Name: l.Name, Depth: depth, Group: l.IsGroup(),
			Sel: l.ID == editor.selID, Visible: l.Visible, Locked: l.Locked})
		if l.IsGroup() {
			out = edRowStates(l.Children, depth+1, out)
		}
	}
	return out
}

func edActionsStateOf() edActionsState {
	st := edActionsState{
		Up: "↑", Down: "↓",
		Group: i18n.T("editor.group"), Ungroup: i18n.T("editor.ungroup"), Delete: i18n.T("common.delete"),
		NoSel: i18n.T("editor.selectLayerHint"), Blend: edNilSel(),
	}
	l, _ := editor.doc.Find(editor.selID)
	if l == nil {
		return st
	}
	st.HasSel = true
	st.Blend = resolveSelectBox(i18n.T("editor.blend"), "ed-blend", edBlendOptions(), string(l.Blend))
	st.Opacity = newSlider(i18n.T("editor.opacity"), "ed-opacity", 0, 1, 0.01, clamp01(l.Opacity), "")
	return st
}

// edInspState resolves the property inspector for the selection.
func (u *UI) edInspState() edInspState {
	st := edInspState{
		Empty: i18n.T("editor.noLayerSelected"),
		Text:  edInspTextState{Font: edNilSel(), Align: edNilSel()},
		Fit:   edNilSel(),
	}
	l, _ := editor.doc.Find(editor.selID)
	if l == nil {
		return st
	}
	st.HasSel = true
	st.Name = newField(i18n.T("editor.name"), "ed-prop:name", l.Name, "text")
	st.X = newField(i18n.T("editor.x"), "ed-prop:x", trimNum(l.Transform.X), "number")
	st.Y = newField(i18n.T("editor.y"), "ed-prop:y", trimNum(l.Transform.Y), "number")
	if !l.IsGroup() {
		st.ShowWH = true
		st.W = newField(i18n.T("editor.w"), "ed-prop:w", trimNum(l.W), "number")
		st.H = newField(i18n.T("editor.h"), "ed-prop:h", trimNum(l.H), "number")
	}
	st.SX = newField(i18n.T("editor.scaleX"), "ed-prop:sx", trimNum(edScale(l.Transform.ScaleX)), "number")
	st.SY = newField(i18n.T("editor.scaleY"), "ed-prop:sy", trimNum(edScale(l.Transform.ScaleY)), "number")
	st.Rot = newField(i18n.T("editor.rotation"), "ed-prop:rot", trimNum(l.Transform.Rotation), "number")

	switch l.Kind {
	case visualeditor.KindText:
		if t := l.Text; t != nil {
			st.Kind, st.Text = "text", edInspTextStateOf(t)
		}
	case visualeditor.KindSolid:
		if l.Solid != nil {
			st.Kind, st.Fill = "solid", edColorRowStateOf(i18n.T("editor.fill"), "ed-solid-color", l.Solid.Color)
		}
	case visualeditor.KindGradient:
		if g := l.Gradient; g != nil && len(g.Stops) >= 2 {
			st.Kind = "gradient"
			st.Angle = newField(i18n.T("editor.angle"), "ed-grad:angle", trimNum(g.Angle), "number")
			st.Start = edColorRowStateOf(i18n.T("editor.start"), "ed-grad:start", g.Stops[0].Color)
			st.End = edColorRowStateOf(i18n.T("editor.end"), "ed-grad:end", g.Stops[len(g.Stops)-1].Color)
		}
	case visualeditor.KindImage:
		if l.Image != nil {
			st.Kind = "image"
			st.Path = newField(i18n.T("editor.path"), "ed-img:path", l.Image.Path, "text")
			fit := string(l.Image.Fit)
			if fit == "" {
				fit = "cover"
			}
			st.Fit = resolveSelectBox(i18n.T("player.fit"), "ed-img:fit", [][2]string{
				{"cover", i18n.T("editor.fitCover")}, {"contain", i18n.T("editor.fitContain")}, {"stretch", i18n.T("editor.fitStretch")},
			}, fit)
		}
	}
	return st
}

func edInspTextStateOf(t *visualeditor.TextProps) edInspTextState {
	fam := t.FontFamily
	if fam == "" {
		fam = visualeditor.DefaultFontFamily
	}
	align := string(t.Align)
	if align == "" {
		align = "left"
	}
	return edInspTextState{
		Label:   i18n.T("editor.text"),
		Content: t.Content,
		Hint:    i18n.T("editor.placeholdersList"),
		Font:    resolveSelectBox(i18n.T("editor.font"), "ed-txt:font", edFontOptions(), fam),
		Size:    newField(i18n.T("editor.size"), "ed-txt:size", trimNum(t.FontSize), "number"),
		LS:      newField(i18n.T("editor.letterSpacing"), "ed-txt:ls", trimNum(t.LetterSpacing), "number"),
		LH:      newField(i18n.T("editor.lineHeight"), "ed-txt:lh", trimNum(t.LineHeight), "number"),
		Align: resolveSelectBox(i18n.T("editor.align"), "ed-txt:align", [][2]string{
			{"left", i18n.T("editor.alignLeft")}, {"center", i18n.T("editor.alignCenter")}, {"right", i18n.T("editor.alignRight")},
		}, align),
		Color: edColorRowStateOf(i18n.T("editor.color"), "ed-txt:color", t.Color),
	}
}

func edColorRowStateOf(label, act string, c visualeditor.RGBA) edColorRowState {
	return edColorRowState{RGBA: edRGBA(c), Field: newField(label, act, edHex(c), "text")}
}

// ── bridges ──

// renderEditor is the visual overlay compositor at parity with the Fyne visual editor: a
// layers stack (add/remove/reorder/select, per-layer visibility/lock), a property inspector
// (transform + blend/opacity + type-specific text/solid/gradient/image controls), a template
// picker, and a LIVE WYSIWYG preview composited in HTML/CSS (absolutely-positioned layer divs
// with mix-blend-mode + transforms, placeholders filled from now-playing). Doc + templates are
// shared on disk with the Fyne editor (same visualeditor engine + data dir).
func (u *UI) renderEditor() string {
	st := u.editorState()
	if zigui.Available() {
		if h, ok := zigWire("RenderEditorV2", wireEdView(st), zigui.RenderEditorV2,
			zigui.RenderEditor, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return editorHTML(st)
}

// edPreviewHTML is the #ed-preview fragment (~1 Hz placeholder refresh). Caller holds editor.mu.
func (u *UI) edPreviewHTML() string {
	st := u.edPreviewState()
	if zigui.Available() {
		if h, ok := zigWire("RenderEditorPreviewV2", wireEdPreview(st), zigui.RenderEditorPreviewV2,
			zigui.RenderEditorPreview, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return edPreviewHTMLOf(st)
}

// ── pure Go renderers (golden reference; byte-identical to Zig) ──

func editorHTML(st edViewState) string {
	if st.Disabled {
		return panel(st.Title, st.DisabledSub) + hint("warn", st.DisabledHint)
	}
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	b.WriteString(`<div class=ed-toolbar>` + edToolbarHTML(st) + `</div>`)
	b.WriteString(`<div class=ed-grid>`)
	// left column: preview + caption
	b.WriteString(`<div class=ed-col>`)
	b.WriteString(section(st.SecPreview, `<div id=ed-preview>`+edPreviewHTMLOf(st.Preview)+`</div>`))
	b.WriteString(`</div>`)
	// right column: layers + inspector
	b.WriteString(`<div class=ed-col>`)
	b.WriteString(section(st.SecLayers, edLayersHTML(st.Layers)))
	b.WriteString(section(st.SecInspector, edInspHTML(st.Insp)))
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

func edToolbarHTML(st edViewState) string { return uiBtnRow(st.Row1) + uiBtnRow(st.Row2) }

// ── preview (CSS composite) ──

func edPreviewHTMLOf(st edPreviewState) string {
	return edStageHTMLOf(st.AW, st.AH, st.Layers) +
		`<div class=ed-cap>` + html.EscapeString(st.Cap) + `</div>` +
		hint("info", st.Hint)
}

// edStageHTMLOf renders a fixed-aspect stage (docW×docH) with layers composited via CSS.
func edStageHTMLOf(aw, ah string, layers []edLayer) string {
	return `<div class=ed-stage style="aspect-ratio:` + aw + `/` + ah + `">` + edChildrenHTML(layers) + `</div>`
}

// edStageHTML renders an arbitrary layer list as a stage - the template-picker modal's
// per-template preview (a modal fragment; the tab preview goes through edPreviewState).
// Caller holds editor.mu.
func (u *UI) edStageHTML(layers []*visualeditor.Layer, d *visualeditor.Document, aw, ah int) string {
	if aw <= 0 || ah <= 0 {
		aw, ah = 16, 9
	}
	return edStageHTMLOf(strconv.Itoa(aw), strconv.Itoa(ah), u.edLayerStates(layers, d))
}

func edChildrenHTML(layers []edLayer) string {
	var b strings.Builder
	for _, l := range layers {
		b.WriteString(edLayerDivHTML(l))
	}
	return b.String()
}

func edLayerDivHTML(l edLayer) string {
	blend := ""
	if l.Blend != "" {
		blend = "mix-blend-mode:" + l.Blend + ";"
	}
	op := ""
	if l.Opacity != "" {
		op = "opacity:" + l.Opacity + ";"
	}
	selCls := ""
	if l.Sel {
		selCls = " ed-sel"
	}
	if l.Group {
		xf := "transform:none;"
		if l.Xform {
			xf = "transform-origin:0 0;transform:translate(" + l.Tx + "%," + l.Ty + "%) rotate(" +
				l.Rot + "deg) scale(" + l.Sx + "," + l.Sy + ");"
		}
		style := "left:0;top:0;width:100%;height:100%;" + xf + op + blend
		return `<div class="ed-group` + selCls + `" style=` + attrQ(style) + `>` + edChildrenHTML(l.Children) + `</div>`
	}
	style := "left:" + l.Left + "%;top:" + l.Top + "%;width:" + l.W + "%;height:" + l.H + "%;"
	if l.Rot != "" {
		style += "transform:rotate(" + l.Rot + "deg);"
	}
	style += op + blend + edPaintHTML(l.Paint)
	return `<div class="ed-layer` + selCls + `" style=` + attrQ(style) +
		` data-act=` + attrQ("ed-select:"+l.ID) + ` data-val=` + attrQ(l.ID) + `>` + edInnerHTML(l.Inner) + `</div>`
}

func edPaintHTML(p edPaint) string {
	switch p.Kind {
	case "solid":
		return "background:" + p.RGBA + ";"
	case "gradient":
		stops := make([]string, 0, len(p.Stops))
		for _, s := range p.Stops {
			stops = append(stops, s.RGBA+" "+s.Pos+"%")
		}
		return "background:linear-gradient(" + p.Angle + "deg," + strings.Join(stops, ",") + ");"
	case "image":
		return `background-image:url(` + p.URLQ + `);background-size:` + p.Size +
			`;background-position:center;background-repeat:no-repeat;`
	}
	return ""
}

func edInnerHTML(in edInner) string {
	switch in.Kind {
	case "text":
		t := in.Text
		st := "font-family:" + t.FamQ + ";font-size:" + t.Size + "cqw;line-height:" + t.LH +
			";text-align:" + t.Align + ";color:" + t.RGBA + ";letter-spacing:" + t.LS + "cqw;"
		return `<span class=ed-txt style="` + st + `">` + html.EscapeString(t.Content) + `</span>`
	case "imgph":
		return `<span class=ed-imgph>` + html.EscapeString(in.Placeholder) + `</span>`
	}
	return ""
}

// ── layers panel ──

func edLayersHTML(st edLayersState) string {
	var b strings.Builder
	b.WriteString(`<div class=ed-layers>`)
	if len(st.Rows) == 0 {
		b.WriteString(emptyState(st.Empty))
	}
	for i := len(st.Rows) - 1; i >= 0; i-- { // top layer first (matches visual stacking)
		b.WriteString(edLayerRowHTML(st.Rows[i]))
	}
	b.WriteString(`</div>`)
	b.WriteString(edActionsHTML(st.Actions))
	return b.String()
}

func edLayerRowHTML(r edRow) string {
	eye, eyeV := "🙈", "warn"
	if r.Visible {
		eye, eyeV = "👁", "ghost"
	}
	lockGlyph, lockV := "🔓", "ghost"
	if r.Locked {
		lockGlyph, lockV = "🔒", "warn"
	}
	prefix := strings.Repeat("　", r.Depth)
	if r.Group {
		prefix += "▸ "
	}
	nameCls := "ed-lr-name"
	if r.Sel {
		nameCls += " ed-lr-sel"
	}
	name := `<button class=` + attrQ(nameCls) + ` data-act=` + attrQ("ed-select:"+r.ID) + ` data-val=` + attrQ(r.ID) + `>` +
		html.EscapeString(prefix) + html.EscapeString(r.Name) + `</button>`
	toggles := `<span class=ed-lr-toggles>` +
		btn(eye, eyeV, "ed-vis:"+r.ID, "") + btn(lockGlyph, lockV, "ed-lock:"+r.ID, "") + `</span>`
	return `<div class=ed-layer-row>` + name + toggles + `</div>`
}

// edActionsHTML is the reorder/group/delete bar + opacity/blend for the selection.
func edActionsHTML(st edActionsState) string {
	acts := btnRow(
		btn(st.Up, "ghost", "ed-up", ""), btn(st.Down, "ghost", "ed-down", ""),
		btn(st.Group, "ghost", "ed-group", ""), btn(st.Ungroup, "ghost", "ed-ungroup", ""),
		btn(st.Delete, "destructive", "ed-del", ""),
	)
	if !st.HasSel {
		return acts + hint("info", st.NoSel)
	}
	return acts + `<div class=ed-selctl>` + st.Opacity.html() + selHTML(st.Blend) + `</div>`
}

// ── inspector ──

func edInspHTML(st edInspState) string {
	if !st.HasSel {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(`<div class=ed-insp>`)
	b.WriteString(st.Name.html())
	b.WriteString(`<div class=ed-row2>` + st.X.html() + st.Y.html() + `</div>`)
	if st.ShowWH {
		b.WriteString(`<div class=ed-row2>` + st.W.html() + st.H.html() + `</div>`)
	}
	b.WriteString(`<div class=ed-row2>` + st.SX.html() + st.SY.html() + `</div>`)
	b.WriteString(st.Rot.html())
	switch st.Kind {
	case "text":
		b.WriteString(edInspTextHTML(st.Text))
	case "solid":
		b.WriteString(edColorRowHTML(st.Fill))
	case "gradient":
		b.WriteString(st.Angle.html())
		b.WriteString(edColorRowHTML(st.Start))
		b.WriteString(edColorRowHTML(st.End))
	case "image":
		b.WriteString(st.Path.html())
		b.WriteString(selHTML(st.Fit))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func edInspTextHTML(st edInspTextState) string {
	var b strings.Builder
	b.WriteString(`<label class="field ed-ta"><span class=field-label>` + html.EscapeString(st.Label) + `</span>` +
		`<textarea class=field-input rows=3 data-act="ed-txt:content" data-value="">` + html.EscapeString(st.Content) + `</textarea></label>`)
	b.WriteString(hint("info", st.Hint))
	b.WriteString(selHTML(st.Font))
	b.WriteString(`<div class=ed-row2>` + st.Size.html() + st.LS.html() + `</div>`)
	b.WriteString(st.LH.html())
	b.WriteString(selHTML(st.Align))
	b.WriteString(edColorRowHTML(st.Color))
	return b.String()
}

// edColorRowHTML is a swatch + hex text field (no native color input per house rules).
func edColorRowHTML(st edColorRowState) string {
	return `<div class=ed-color-row><span class=ed-swatch style="background:` + st.RGBA + `"></span>` +
		st.Field.html() + `</div>`
}

// ── render helpers ──

func edBlendOptions() [][2]string {
	out := make([][2]string, len(visualeditor.BlendModes))
	for i, m := range visualeditor.BlendModes {
		out[i] = [2]string{string(m), string(m)}
	}
	return out
}

func edFontOptions() [][2]string {
	fams := editor.comp.Fonts().Families()
	out := make([][2]string, 0, len(fams)+1)
	has := false
	for _, f := range fams {
		out = append(out, [2]string{f, f})
		if f == visualeditor.DefaultFontFamily {
			has = true
		}
	}
	if !has {
		out = append([][2]string{{visualeditor.DefaultFontFamily, visualeditor.DefaultFontFamily}}, out...)
	}
	return out
}

func edCountLeaves(layers []*visualeditor.Layer) int {
	n := 0
	for _, l := range layers {
		if l.IsGroup() {
			n += edCountLeaves(l.Children)
		} else {
			n++
		}
	}
	return n
}

// edBlendCSS maps an engine BlendMode to the closest CSS mix-blend-mode. add/subtract have no
// exact CSS equivalent (approximated); the on-disk PNG export uses the exact engine blend.
func edBlendCSS(m visualeditor.BlendMode) string {
	switch m {
	case visualeditor.BlendAdd:
		return "lighten"
	case visualeditor.BlendSubtract:
		return "difference"
	case "", visualeditor.BlendNormal:
		return "normal"
	default:
		return string(m) // multiply/screen/overlay/darken/lighten/difference/soft-light/hard-light/color-dodge/color-burn
	}
}

func pct(v float64, base int) string {
	if base <= 0 {
		return "0"
	}
	return trimNum(v / float64(base) * 100)
}

// edCQW expresses a doc-px length as container-query width units (1cqw = 1% of stage width),
// so text scales with the responsive stage without knowing its pixel size.
func edCQW(px float64, docW int) string {
	if docW <= 0 {
		return "0"
	}
	return trimNum(px / float64(docW) * 100)
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func edScale(s float64) float64 {
	if s == 0 {
		return 1
	}
	return s
}

func edRGBA(c visualeditor.RGBA) string {
	return fmt.Sprintf("rgba(%d,%d,%d,%s)", c.R, c.G, c.B, trimNum(float64(c.A)/255))
}

func edHex(c visualeditor.RGBA) string {
	if c.A == 0xff {
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
}

// edParseHex parses #rgb / #rrggbb / #rrggbbaa (alpha defaults to opaque).
func edParseHex(s string) (visualeditor.RGBA, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 && len(s) != 8 {
		return visualeditor.RGBA{}, false
	}
	n, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return visualeditor.RGBA{}, false
	}
	c := visualeditor.RGBA{A: 0xff}
	if len(s) == 6 {
		c.R, c.G, c.B = uint8(n>>16), uint8(n>>8), uint8(n)
	} else {
		c.R, c.G, c.B, c.A = uint8(n>>24), uint8(n>>16), uint8(n>>8), uint8(n)
	}
	return c, true
}

// edFileURL turns a filesystem path into a file:// URL (best-effort; the webview may block
// local files under CSP - the inspector still shows the path and the export renders it).
func edFileURL(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return "file://" + p
	}
	return "file:///" + p
}
