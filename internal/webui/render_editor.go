package webui

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/visualeditor"
)

// renderEditor is the visual overlay compositor at parity with the Fyne visual editor: a
// layers stack (add/remove/reorder/select, per-layer visibility/lock), a property inspector
// (transform + blend/opacity + type-specific text/solid/gradient/image controls), a template
// picker, and a LIVE WYSIWYG preview composited in HTML/CSS (absolutely-positioned layer divs
// with mix-blend-mode + transforms, placeholders filled from now-playing). Doc + templates are
// shared on disk with the Fyne editor (same visualeditor engine + data dir).
func (u *UI) renderEditor() string {
	if u.svc.Cfg != nil && !u.svc.Cfg.Features.MediaEditor.Enabled {
		return panel(i18n.T("editor.title"), i18n.T("editor.subtitleDisabled")) +
			hint("warn", i18n.T("editor.disabledHint"))
	}
	edEnsure()
	editor.mu.Lock()
	defer editor.mu.Unlock()

	var b strings.Builder
	b.WriteString(panel(i18n.T("editor.title"), i18n.T("editor.subtitle")))
	b.WriteString(`<div class=ed-toolbar>` + u.edToolbarHTML() + `</div>`)
	b.WriteString(`<div class=ed-grid>`)
	// left column: preview + caption
	b.WriteString(`<div class=ed-col>`)
	b.WriteString(section(i18n.T("editor.sectionPreview"), `<div id=ed-preview>`+u.edPreviewHTML()+`</div>`))
	b.WriteString(`</div>`)
	// right column: layers + inspector
	b.WriteString(`<div class=ed-col>`)
	b.WriteString(section(i18n.T("editor.sectionLayers"), u.edLayersHTML()))
	b.WriteString(section(i18n.T("editor.sectionInspector"), u.edInspectorHTML()))
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// ── toolbar ──

func (u *UI) edToolbarHTML() string {
	var b strings.Builder
	b.WriteString(btnRow(
		btn("+ "+i18n.T("editor.text"), "primary", "ed-add:text", ""),
		btn("+ "+i18n.T("editor.image"), "outline", "ed-add:image", ""),
		btn("+ "+i18n.T("editor.solid"), "outline", "ed-add:solid", ""),
		btn("+ "+i18n.T("editor.gradient"), "outline", "ed-add:gradient", ""),
		btn(i18n.T("editor.templates"), "explore", "ed-tpl-open", ""),
	))
	canUndo, canRedo := len(editor.undo) > 0, len(editor.redo) > 0
	undo, redo := "ghost", "ghost"
	if canUndo {
		undo = "secondary"
	}
	if canRedo {
		redo = "secondary"
	}
	b.WriteString(btnRow(
		btn("↶ "+i18n.T("editor.undo"), undo, "ed-undo", ""),
		btn("↷ "+i18n.T("editor.redo"), redo, "ed-redo", ""),
		btn(i18n.T("editor.saveTemplate"), "outline", "ed-save-tpl", ""),
		btn(i18n.T("editor.exportPng"), "go", "ed-export", ""),
		btn(i18n.T("editor.canvasSize", i18n.A{"w": fmt.Sprint(editor.doc.W), "h": fmt.Sprint(editor.doc.H)}), "ghost", "ed-canvas", ""),
	))
	return b.String()
}

// ── preview (CSS composite) ──

func (u *UI) edPreviewHTML() string {
	d := editor.doc
	stage := u.edStageHTML(d.Root.Children, d, d.W, d.H)
	cap := i18n.T("editor.docInfo", i18n.A{"w": fmt.Sprint(d.W), "h": fmt.Sprint(d.H), "count": fmt.Sprint(edCountLeaves(d.Root.Children))})
	if l, _ := d.Find(editor.selID); l != nil {
		cap += " " + i18n.T("editor.selectedInfo", i18n.A{"name": l.Name})
	}
	return stage + `<div class=ed-cap>` + html.EscapeString(cap) + `</div>` +
		hint("info", i18n.T("editor.placeholderHint"))
}

// edStageHTML renders a fixed-aspect stage (docW×docH) with layers composited via CSS.
func (u *UI) edStageHTML(layers []*visualeditor.Layer, d *visualeditor.Document, aw, ah int) string {
	if aw <= 0 || ah <= 0 {
		aw, ah = 16, 9
	}
	return fmt.Sprintf(`<div class=ed-stage style="aspect-ratio:%d/%d">`, aw, ah) +
		u.edChildrenHTML(layers, d) + `</div>`
}

func (u *UI) edChildrenHTML(layers []*visualeditor.Layer, d *visualeditor.Document) string {
	var b strings.Builder
	for _, l := range layers {
		if l == nil || !l.Visible || l.Opacity <= 0 {
			continue
		}
		b.WriteString(u.edLayerDiv(l, d))
	}
	return b.String()
}

// edLayerDiv renders one layer as an absolutely-positioned div. Groups become a full-stage
// wrapper carrying opacity/blend + the group transform (children keep doc-space coords, matching
// the compositor's placement); leaves fold scale into size and center-correct the origin.
func (u *UI) edLayerDiv(l *visualeditor.Layer, d *visualeditor.Document) string {
	blend := ""
	if bm := edBlendCSS(l.Blend); bm != "normal" {
		blend = "mix-blend-mode:" + bm + ";"
	}
	op := ""
	if l.Opacity < 1 {
		op = "opacity:" + trimNum(clamp01(l.Opacity)) + ";"
	}
	selCls := ""
	if l.ID == editor.selID {
		selCls = " ed-sel"
	}

	if l.IsGroup() {
		xf := "transform:none;"
		tx, ty := l.Transform.X, l.Transform.Y
		sx, sy := edScale(l.Transform.ScaleX), edScale(l.Transform.ScaleY)
		if tx != 0 || ty != 0 || sx != 1 || sy != 1 || l.Transform.Rotation != 0 {
			xf = fmt.Sprintf("transform-origin:0 0;transform:translate(%s%%,%s%%) rotate(%sdeg) scale(%s,%s);",
				pct(tx, d.W), pct(ty, d.H), trimNum(l.Transform.Rotation), trimNum(sx), trimNum(sy))
		}
		style := "left:0;top:0;width:100%;height:100%;" + xf + op + blend
		return fmt.Sprintf(`<div class="ed-group%s" style=%s>%s</div>`, selCls, attrQ(style), u.edChildrenHTML(l.Children, d))
	}

	w, h := l.W, l.H
	sx, sy := math.Abs(edScale(l.Transform.ScaleX)), math.Abs(edScale(l.Transform.ScaleY))
	wDoc, hDoc := w*sx, h*sy
	left := l.Transform.X - w*(sx-1)/2
	top := l.Transform.Y - h*(sy-1)/2
	style := fmt.Sprintf("left:%s%%;top:%s%%;width:%s%%;height:%s%%;", pct(left, d.W), pct(top, d.H), pct(wDoc, d.W), pct(hDoc, d.H))
	if l.Transform.Rotation != 0 {
		style += "transform:rotate(" + trimNum(l.Transform.Rotation) + "deg);"
	}
	style += op + blend + u.edLeafStyle(l, d)
	return fmt.Sprintf(`<div class="ed-layer%s" style=%s data-act=%s data-val=%s>%s</div>`,
		selCls, attrQ(style), attrQ("ed-select:"+l.ID), attrQ(l.ID), u.edLeafInner(l))
}

// edLeafStyle returns background/paint CSS for a leaf (solid/gradient/image).
func (u *UI) edLeafStyle(l *visualeditor.Layer, d *visualeditor.Document) string {
	switch l.Kind {
	case visualeditor.KindSolid:
		if l.Solid != nil {
			return "background:" + edRGBA(l.Solid.Color) + ";"
		}
	case visualeditor.KindGradient:
		if g := l.Gradient; g != nil && len(g.Stops) >= 2 {
			var stops []string
			for _, s := range g.Stops {
				stops = append(stops, edRGBA(s.Color)+" "+trimNum(clamp01(s.Pos)*100)+"%")
			}
			// engine angle: 0 = L→R, 90 = T→B; CSS: 90deg = →right, 180deg = ↓ ⇒ +90
			return fmt.Sprintf("background:linear-gradient(%sdeg,%s);", trimNum(g.Angle+90), strings.Join(stops, ","))
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
			return fmt.Sprintf(`background-image:url(%q);background-size:%s;background-position:center;background-repeat:no-repeat;`,
				edFileURL(l.Image.Path), size)
		}
		return "" // placeholder handled in edLeafInner
	}
	return ""
}

// edLeafInner returns inner HTML for a leaf (text content, or an image placeholder).
func (u *UI) edLeafInner(l *visualeditor.Layer) string {
	switch l.Kind {
	case visualeditor.KindText:
		if t := l.Text; t != nil {
			content := html.EscapeString(visualeditor.Substitute(t.Content, edProvider{u}))
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
			st := fmt.Sprintf("font-family:%q;font-size:%scqw;line-height:%s;text-align:%s;color:%s;letter-spacing:%scqw;",
				fam, edCQW(t.FontSize, editor.doc.W), trimNum(lh), align, edRGBA(t.Color), edCQW(t.LetterSpacing, editor.doc.W))
			return `<span class=ed-txt style="` + st + `">` + content + `</span>`
		}
	case visualeditor.KindImage:
		if l.Image == nil || strings.TrimSpace(l.Image.Path) == "" {
			return `<span class=ed-imgph>` + html.EscapeString(i18n.T("editor.imagePlaceholder")) + `</span>`
		}
	}
	return ""
}

// ── layers panel ──

func (u *UI) edLayersHTML() string {
	var b strings.Builder
	b.WriteString(`<div class=ed-layers>`)
	rows := u.edLayerRows(editor.doc.Root.Children, 0)
	if len(rows) == 0 {
		b.WriteString(emptyState(i18n.T("editor.noLayers")))
	}
	for i := len(rows) - 1; i >= 0; i-- { // top layer first (matches visual stacking)
		b.WriteString(rows[i])
	}
	b.WriteString(`</div>`)
	b.WriteString(u.edLayerActionsHTML())
	return b.String()
}

func (u *UI) edLayerRows(layers []*visualeditor.Layer, depth int) []string {
	var out []string
	for _, l := range layers {
		out = append(out, u.edLayerRow(l, depth))
		if l.IsGroup() {
			out = append(out, u.edLayerRows(l.Children, depth+1)...)
		}
	}
	return out
}

func (u *UI) edLayerRow(l *visualeditor.Layer, depth int) string {
	eye, eyeV := "🙈", "warn"
	if l.Visible {
		eye, eyeV = "👁", "ghost"
	}
	lockGlyph, lockV := "🔓", "ghost"
	if l.Locked {
		lockGlyph, lockV = "🔒", "warn"
	}
	prefix := strings.Repeat("　", depth)
	if l.IsGroup() {
		prefix += "▸ "
	}
	nameCls := "ed-lr-name"
	if l.ID == editor.selID {
		nameCls += " ed-lr-sel"
	}
	name := fmt.Sprintf(`<button class=%s data-act=%s data-val=%s>%s%s</button>`,
		attrQ(nameCls), attrQ("ed-select:"+l.ID), attrQ(l.ID), html.EscapeString(prefix), html.EscapeString(l.Name))
	toggles := `<span class=ed-lr-toggles>` +
		btn(eye, eyeV, "ed-vis:"+l.ID, "") + btn(lockGlyph, lockV, "ed-lock:"+l.ID, "") + `</span>`
	return `<div class=ed-layer-row>` + name + toggles + `</div>`
}

// edLayerActionsHTML is the reorder/group/delete bar + opacity/blend for the selection.
func (u *UI) edLayerActionsHTML() string {
	acts := btnRow(
		btn("↑", "ghost", "ed-up", ""), btn("↓", "ghost", "ed-down", ""),
		btn(i18n.T("editor.group"), "ghost", "ed-group", ""), btn(i18n.T("editor.ungroup"), "ghost", "ed-ungroup", ""),
		btn(i18n.T("common.delete"), "destructive", "ed-del", ""),
	)
	l, _ := editor.doc.Find(editor.selID)
	if l == nil {
		return acts + hint("info", i18n.T("editor.selectLayerHint"))
	}
	blend := selectBox(i18n.T("editor.blend"), "ed-blend", edBlendOptions(), string(l.Blend))
	opacity := slider(i18n.T("editor.opacity"), "ed-opacity", 0, 1, 0.01, clamp01(l.Opacity), "")
	return acts + `<div class=ed-selctl>` + opacity + blend + `</div>`
}

// ── inspector ──

func (u *UI) edInspectorHTML() string {
	l, _ := editor.doc.Find(editor.selID)
	if l == nil {
		return emptyState(i18n.T("editor.noLayerSelected"))
	}
	var b strings.Builder
	b.WriteString(`<div class=ed-insp>`)
	b.WriteString(field(i18n.T("editor.name"), "ed-prop:name", l.Name, "text"))
	b.WriteString(`<div class=ed-row2>`)
	b.WriteString(field(i18n.T("editor.x"), "ed-prop:x", trimNum(l.Transform.X), "number"))
	b.WriteString(field(i18n.T("editor.y"), "ed-prop:y", trimNum(l.Transform.Y), "number"))
	b.WriteString(`</div>`)
	if !l.IsGroup() {
		b.WriteString(`<div class=ed-row2>`)
		b.WriteString(field(i18n.T("editor.w"), "ed-prop:w", trimNum(l.W), "number"))
		b.WriteString(field(i18n.T("editor.h"), "ed-prop:h", trimNum(l.H), "number"))
		b.WriteString(`</div>`)
	}
	b.WriteString(`<div class=ed-row2>`)
	b.WriteString(field(i18n.T("editor.scaleX"), "ed-prop:sx", trimNum(edScale(l.Transform.ScaleX)), "number"))
	b.WriteString(field(i18n.T("editor.scaleY"), "ed-prop:sy", trimNum(edScale(l.Transform.ScaleY)), "number"))
	b.WriteString(`</div>`)
	b.WriteString(field(i18n.T("editor.rotation"), "ed-prop:rot", trimNum(l.Transform.Rotation), "number"))

	switch l.Kind {
	case visualeditor.KindText:
		b.WriteString(u.edInspText(l))
	case visualeditor.KindSolid:
		if l.Solid != nil {
			b.WriteString(edColorRow(i18n.T("editor.fill"), "ed-solid-color", l.Solid.Color))
		}
	case visualeditor.KindGradient:
		if g := l.Gradient; g != nil && len(g.Stops) >= 2 {
			b.WriteString(field(i18n.T("editor.angle"), "ed-grad:angle", trimNum(g.Angle), "number"))
			b.WriteString(edColorRow(i18n.T("editor.start"), "ed-grad:start", g.Stops[0].Color))
			b.WriteString(edColorRow(i18n.T("editor.end"), "ed-grad:end", g.Stops[len(g.Stops)-1].Color))
		}
	case visualeditor.KindImage:
		if l.Image != nil {
			b.WriteString(field(i18n.T("editor.path"), "ed-img:path", l.Image.Path, "text"))
			fit := string(l.Image.Fit)
			if fit == "" {
				fit = "cover"
			}
			b.WriteString(selectBox(i18n.T("player.fit"), "ed-img:fit", [][2]string{
				{"cover", i18n.T("editor.fitCover")}, {"contain", i18n.T("editor.fitContain")}, {"stretch", i18n.T("editor.fitStretch")},
			}, fit))
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

func (u *UI) edInspText(l *visualeditor.Layer) string {
	t := l.Text
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<label class="field ed-ta"><span class=field-label>` + html.EscapeString(i18n.T("editor.text")) + `</span>` +
		`<textarea class=field-input rows=3 data-act="ed-txt:content" data-value="">` + html.EscapeString(t.Content) + `</textarea></label>`)
	b.WriteString(hint("info", i18n.T("editor.placeholdersList")))
	fam := t.FontFamily
	if fam == "" {
		fam = visualeditor.DefaultFontFamily
	}
	b.WriteString(selectBox(i18n.T("editor.font"), "ed-txt:font", edFontOptions(), fam))
	b.WriteString(`<div class=ed-row2>`)
	b.WriteString(field(i18n.T("editor.size"), "ed-txt:size", trimNum(t.FontSize), "number"))
	b.WriteString(field(i18n.T("editor.letterSpacing"), "ed-txt:ls", trimNum(t.LetterSpacing), "number"))
	b.WriteString(`</div>`)
	b.WriteString(field(i18n.T("editor.lineHeight"), "ed-txt:lh", trimNum(t.LineHeight), "number"))
	align := string(t.Align)
	if align == "" {
		align = "left"
	}
	b.WriteString(selectBox(i18n.T("editor.align"), "ed-txt:align", [][2]string{
		{"left", i18n.T("editor.alignLeft")}, {"center", i18n.T("editor.alignCenter")}, {"right", i18n.T("editor.alignRight")},
	}, align))
	b.WriteString(edColorRow(i18n.T("editor.color"), "ed-txt:color", t.Color))
	return b.String()
}

// edColorRow is a swatch + hex text field (no native color input per house rules).
func edColorRow(label, act string, c visualeditor.RGBA) string {
	sw := `<span class=ed-swatch style="background:` + edRGBA(c) + `"></span>`
	return `<div class=ed-color-row>` + sw + field(label, act, edHex(c), "text") + `</div>`
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
