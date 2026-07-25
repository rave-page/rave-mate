//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Editor golden gate: Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states, full view + the #ed-preview fragment.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

func edFld(label, dl, act, value, typ string) uiField {
	return uiField{Label: label, DL: dl, Act: act, Value: value, Type: typ}
}

func edSel(id, label, cur string, rows ...selRow) selState {
	if rows == nil {
		rows = []selRow{}
	}
	return selState{ID: id, Label: label, CurLabel: cur, Rows: rows}
}

func edColor(rgba, label, dl, act, hex string) edColorRowState {
	return edColorRowState{RGBA: rgba, Field: edFld(label, dl, act, hex, "text")}
}

func edLeaf(id string, sel bool) edLayer {
	return edLayer{ID: id, Sel: sel, Left: "10", Top: "20", W: "30", H: "40",
		Paint: edPaint{Stops: []edGradStop{}}, Children: []edLayer{}}
}

// edFixtures: disabled, empty doc, populated (nested groups + every paint/inner kind),
// escaping edge, long values, unicode.
func edFixtures() map[string]edViewState {
	labels := func(st *edViewState) {
		st.Title, st.Sub = "Editor", "Compose overlays"
		st.SecPreview, st.SecLayers, st.SecInspector = "Preview", "Layers", "Inspector"
		st.Row1 = []uiBtn{
			{Label: "+ Text", Variant: "primary", Act: "ed-add:text"},
			{Label: "+ Image", Variant: "outline", Act: "ed-add:image"},
			{Label: "+ Solid", Variant: "outline", Act: "ed-add:solid"},
			{Label: "+ Gradient", Variant: "outline", Act: "ed-add:gradient"},
			{Label: "Templates", Variant: "explore", Act: "ed-tpl-open"},
		}
		st.Row2 = []uiBtn{
			{Label: "↶ Undo", Variant: "ghost", Act: "ed-undo"},
			{Label: "↷ Redo", Variant: "ghost", Act: "ed-redo"},
			{Label: "Save as template", Variant: "outline", Act: "ed-save-tpl"},
			{Label: "Export PNG", Variant: "go", Act: "ed-export"},
			{Label: "Canvas 1920×1080", Variant: "ghost", Act: "ed-canvas"},
		}
	}

	base := func() edViewState {
		st := emptyEdState()
		labels(&st)
		st.Preview = edPreviewState{AW: "1920", AH: "1080", Layers: []edLayer{},
			Cap: "1920×1080, 0 layers", Hint: "Placeholders fill from now-playing"}
		st.Layers = edLayersState{Rows: []edRow{}, Empty: "No layers yet",
			Actions: edActionsState{Up: "↑", Down: "↓", Group: "Group", Ungroup: "Ungroup",
				Delete: "Delete", NoSel: "Select a layer", Blend: edNilSel()}}
		st.Insp = edInspState{Empty: "No layer selected",
			Text: edInspTextState{Font: edNilSel(), Align: edNilSel()}, Fit: edNilSel()}
		return st
	}

	disabled := emptyEdState()
	disabled.Title, disabled.Disabled = "Editor", true
	disabled.DisabledSub, disabled.DisabledHint = "Editor disabled", "Enable it in Settings"

	empty := base()

	// nested groups + one leaf per paint kind + text and image-placeholder inners
	populated := base()
	txt := edLeaf("t1", true)
	txt.Blend, txt.Opacity, txt.Rot = "screen", "0.75", "12.5"
	txt.Inner = edInner{Kind: "text", Text: edText{Content: "{artist} - {title}",
		FamQ: `"Orbitron"`, Size: "3.125", LH: "1.2", Align: "center",
		RGBA: "rgba(250,250,250,1)", LS: "0.104"}}
	solid := edLeaf("s1", false)
	solid.Paint = edPaint{Kind: "solid", RGBA: "rgba(10,10,10,0.5)", Stops: []edGradStop{}}
	grad := edLeaf("g1", false)
	grad.Paint = edPaint{Kind: "gradient", Angle: "180", Stops: []edGradStop{
		{RGBA: "rgba(247,8,100,1)", Pos: "0"}, {RGBA: "rgba(8,247,155,1)", Pos: "100"}}}
	img := edLeaf("i1", false)
	img.Paint = edPaint{Kind: "image", URLQ: `"file:///C:/art/cover.png"`, Size: "contain", Stops: []edGradStop{}}
	imgPH := edLeaf("i2", false)
	imgPH.Inner = edInner{Kind: "imgph", Placeholder: "Pick an image"}
	inner := edLayer{Group: true, ID: "grp-in", Xform: true, Tx: "5", Ty: "-2.5",
		Sx: "1.5", Sy: "0.5", Rot: "45", Opacity: "0.9",
		Paint: edPaint{Stops: []edGradStop{}}, Children: []edLayer{grad, img}}
	outer := edLayer{Group: true, ID: "grp-out", Sel: false, Blend: "multiply",
		Paint: edPaint{Stops: []edGradStop{}}, Children: []edLayer{inner, imgPH}}
	populated.Preview.Layers = []edLayer{txt, solid, outer}
	populated.Preview.Cap = "1920×1080, 5 layers — selected: Title text"
	populated.Layers.Rows = []edRow{
		{ID: "t1", Name: "Title text", Depth: 0, Sel: true, Visible: true},
		{ID: "s1", Name: "Backdrop", Depth: 0, Visible: true, Locked: true},
		{ID: "grp-out", Name: "Art group", Depth: 0, Group: true, Visible: true},
		{ID: "grp-in", Name: "Inner", Depth: 1, Group: true, Visible: true},
		{ID: "g1", Name: "Gradient", Depth: 2},
		{ID: "i1", Name: "Cover", Depth: 2, Visible: true, Locked: true},
	}
	populated.Layers.Actions.HasSel = true
	populated.Layers.Actions.Opacity = newSlider("Opacity", "ed-opacity", 0, 1, 0.01, 0.75, "")
	populated.Layers.Actions.Blend = edSel("ed-blend", "Blend", "screen",
		selRow{Val: "normal", Label: "normal"}, selRow{Val: "screen", Label: "screen", Cur: true})
	populated.Insp = edInspState{
		HasSel: true, Empty: "No layer selected",
		Name:   edFld("Name", "name", "ed-prop:name", "Title text", "text"),
		X:      edFld("X", "x", "ed-prop:x", "120", "number"),
		Y:      edFld("Y", "y", "ed-prop:y", "64", "number"),
		ShowWH: true,
		W:      edFld("W", "w", "ed-prop:w", "1680", "number"),
		H:      edFld("H", "h", "ed-prop:h", "180", "number"),
		SX:     edFld("Scale X", "scale x", "ed-prop:sx", "1", "number"),
		SY:     edFld("Scale Y", "scale y", "ed-prop:sy", "1", "number"),
		Rot:    edFld("Rotation", "rotation", "ed-prop:rot", "12.5", "number"),
		Kind:   "text",
		Text: edInspTextState{
			Label: "Text", Content: "{artist} - {title}", Hint: "Placeholders: {artist} {title}",
			Font:  edSel("ed-txt-font", "Font", "Orbitron", selRow{Val: "Orbitron", Label: "Orbitron", Cur: true}),
			Size:  edFld("Size", "size", "ed-txt:size", "60", "number"),
			LS:    edFld("Letter spacing", "letter spacing", "ed-txt:ls", "2", "number"),
			LH:    edFld("Line height", "line height", "ed-txt:lh", "1.2", "number"),
			Align: edSel("ed-txt-align", "Align", "Centre", selRow{Val: "center", Label: "Centre", Cur: true}),
			Color: edColor("rgba(250,250,250,1)", "Colour", "colour", "ed-txt:color", "#fafafa"),
		},
		Fill: edColorRowState{}, Fit: edNilSel(),
	}

	// gradient inspector + no selection in the layers bar (distinct branch mix)
	gradient := base()
	gradient.Layers.Rows = []edRow{{ID: "g1", Name: "Sweep", Visible: true}}
	gradient.Insp = edInspState{
		HasSel: true, Empty: "No layer selected",
		Name:   edFld("Name", "name", "ed-prop:name", "Sweep", "text"),
		X:      edFld("X", "x", "ed-prop:x", "0", "number"),
		Y:      edFld("Y", "y", "ed-prop:y", "0", "number"),
		ShowWH: false, // a group hides W/H
		SX:     edFld("Scale X", "scale x", "ed-prop:sx", "1", "number"),
		SY:     edFld("Scale Y", "scale y", "ed-prop:sy", "1", "number"),
		Rot:    edFld("Rotation", "rotation", "ed-prop:rot", "0", "number"),
		Kind:   "gradient",
		Angle:  edFld("Angle", "angle", "ed-grad:angle", "90", "number"),
		Start:  edColor("rgba(0,0,0,1)", "Start", "start", "ed-grad:start", "#000000"),
		End:    edColor("rgba(255,255,255,0.502)", "End", "end", "ed-grad:end", "#ffffff80"),
		Text:   edInspTextState{Font: edNilSel(), Align: edNilSel()}, Fit: edNilSel(),
	}

	// image inspector + solid inspector variants
	image := base()
	image.Insp = edInspState{
		HasSel: true, Empty: "No layer selected",
		Name:   edFld("Name", "name", "ed-prop:name", "Cover", "text"),
		X:      edFld("X", "x", "ed-prop:x", "0", "number"),
		Y:      edFld("Y", "y", "ed-prop:y", "0", "number"),
		ShowWH: true,
		W:      edFld("W", "w", "ed-prop:w", "512", "number"),
		H:      edFld("H", "h", "ed-prop:h", "512", "number"),
		SX:     edFld("Scale X", "scale x", "ed-prop:sx", "1", "number"),
		SY:     edFld("Scale Y", "scale y", "ed-prop:sy", "1", "number"),
		Rot:    edFld("Rotation", "rotation", "ed-prop:rot", "0", "number"),
		Kind:   "image",
		Path:   edFld("Path", "path", "ed-img:path", `C:\art\cover.png`, "text"),
		Fit:    edSel("ed-img-fit", "Fit", "Contain", selRow{Val: "contain", Label: "Contain", Cur: true}),
		Text:   edInspTextState{Font: edNilSel(), Align: edNilSel()},
	}

	escaping := base()
	escaping.Title = `Edi&tor <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.SecPreview, escaping.SecLayers, escaping.SecInspector = `P&rev"`, `L&ayers<>'`, `I&nsp"`
	escaping.Row1 = []uiBtn{{Label: `+ T&ext "x"'<>`, Variant: "primary", Act: `ed-add:text&"`}}
	escaping.Row2 = []uiBtn{{Label: `Canvas 1&920"×"1080`, Variant: "ghost", Act: "ed-canvas"}}
	escTxt := edLeaf(`l:1"x'<&>`, true)
	escTxt.Inner = edInner{Kind: "text", Text: edText{Content: `msg &<>"' end`,
		FamQ: `"O&rbitron"`, Size: "3", LH: "1.2", Align: "left", RGBA: "rgba(1,2,3,1)", LS: "0"}}
	escImg := edLeaf(`i&"'<>`, false)
	escImg.Paint = edPaint{Kind: "image", URLQ: `"file:///C:/a&b \"q\".png"`, Size: "cover", Stops: []edGradStop{}}
	escPH := edLeaf("ph", false)
	escPH.Inner = edInner{Kind: "imgph", Placeholder: `p&ick "one"'<>`}
	escaping.Preview.Layers = []edLayer{escTxt, escImg, escPH}
	escaping.Preview.Cap = `1920×1080, 3 &layers <"sel">'`
	escaping.Preview.Hint = `h&int"<>'`
	escaping.Layers.Rows = []edRow{
		{ID: `r:1&"'<>`, Name: `N&ame "x"'<>`, Depth: 2, Group: true, Sel: true, Visible: true, Locked: true},
	}
	escaping.Layers.Empty = `e&mpty"<>'`
	escaping.Layers.Actions = edActionsState{
		Up: "↑", Down: "↓", Group: `G&roup"`, Ungroup: `U&ngroup'`, Delete: `D&el<>`,
		HasSel:  true,
		Opacity: newSlider(`O&pac"<>'`, `ed-opacity&"`, 0, 1, 0.01, 0.5, `u&"'`),
		Blend: selState{ID: "ed-blend", Label: `B&lend"<>'`, CurLabel: `s&creen"`, Open: true,
			Filter: `f&"<>'`, Rows: []selRow{{Val: `v&"'<>`, Label: `L&"'<>`, Cur: true}}},
	}
	escaping.Insp = edInspState{
		HasSel: true, Empty: "x",
		Name: edFld(`N&ame"<>'`, `n&ame"<>'`, `ed-prop:name&"`, `V&al "x"'<>`, "text"),
		X:    edFld(`X&"`, `x&"`, "ed-prop:x", "-12.5", "number"),
		Y:    edFld(`Y&"`, `y&"`, "ed-prop:y", "0", "number"),
		SX:   edFld("SX", "sx", "ed-prop:sx", "1", "number"),
		SY:   edFld("SY", "sy", "ed-prop:sy", "1", "number"),
		Rot:  edFld("R", "r", "ed-prop:rot", "0", "number"),
		Kind: "solid",
		Fill: edColor("rgba(1,2,3,0.5)", `F&ill"<>'`, `f&ill"<>'`, "ed-solid-color", "#01020380"),
		Text: edInspTextState{Font: edNilSel(), Align: edNilSel()}, Fit: edNilSel(),
	}

	long := base()
	longS := strings.Repeat("very-long-", 120)
	long.Sub = longS
	longLeaf := edLeaf(strings.Repeat("id-", 200), false)
	longLeaf.Inner = edInner{Kind: "text", Text: edText{Content: longS,
		FamQ: `"` + strings.Repeat("F", 300) + `"`, Size: "0.0000001", LH: "1.23456789",
		Align: "right", RGBA: "rgba(255,255,255,0.996)", LS: "12.5"}}
	long.Preview.Layers = []edLayer{longLeaf}
	long.Preview.Cap = longS
	long.Layers.Rows = []edRow{{ID: strings.Repeat("r", 300), Name: longS, Depth: 6, Group: true, Visible: true}}
	long.Insp = edInspState{
		HasSel: true, Empty: "x",
		Name: edFld(longS, strings.ToLower(longS), "ed-prop:name", longS, "text"),
		X:    edFld("X", "x", "ed-prop:x", "-99999.99999", "number"),
		Y:    edFld("Y", "y", "ed-prop:y", "0.000001", "number"),
		SX:   edFld("SX", "sx", "ed-prop:sx", "1", "number"),
		SY:   edFld("SY", "sy", "ed-prop:sy", "1", "number"),
		Rot:  edFld("R", "r", "ed-prop:rot", "359.999", "number"),
		Text: edInspTextState{Font: edNilSel(), Align: edNilSel()}, Fit: edNilSel(),
	}

	unicode := base()
	unicode.Title = "エディター 🎧"
	unicode.Sub = "größer Редактор"
	unicode.SecPreview, unicode.SecLayers, unicode.SecInspector = "プレビュー", "Шари", "Инспектор"
	unicode.Row1 = []uiBtn{{Label: "+ Текст", Variant: "primary", Act: "ed-add:text"}}
	uniLeaf := edLeaf("u☂", true)
	uniLeaf.Inner = edInner{Kind: "text", Text: edText{Content: "中文 emoji 🎛️ ラヴ",
		FamQ: `"Кириллица Sans"`, Size: "3", LH: "1.2", Align: "center", RGBA: "rgba(8,247,155,1)", LS: "0"}}
	unicode.Preview.Layers = []edLayer{uniLeaf}
	unicode.Preview.Cap = "1920×1080, 1 шар — вибрано: Заголовок"
	unicode.Layers.Rows = []edRow{{ID: "u☂", Name: "Кириллица + 中文 🎛️", Depth: 1, Sel: true, Visible: true}}
	unicode.Layers.Empty = "немає шарів"
	unicode.Insp = edInspState{
		HasSel: true, Empty: "x",
		Name: edFld("Назва", "назва", "ed-prop:name", "Заголовок", "text"),
		X:    edFld("X", "x", "ed-prop:x", "0", "number"),
		Y:    edFld("Y", "y", "ed-prop:y", "0", "number"),
		SX:   edFld("SX", "sx", "ed-prop:sx", "1", "number"),
		SY:   edFld("SY", "sy", "ed-prop:sy", "1", "number"),
		Rot:  edFld("R", "r", "ed-prop:rot", "0", "number"),
		Text: edInspTextState{Font: edNilSel(), Align: edNilSel()}, Fit: edNilSel(),
	}

	return map[string]edViewState{
		"disabled":  disabled,
		"empty":     empty,
		"populated": populated,
		"gradient":  gradient,
		"image":     image,
		"escaping":  escaping,
		"long":      long,
		"unicode":   unicode,
	}
}

func TestZigEditorGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range edFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderEditor(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", editorHTML(st), zig)

			if st.Disabled {
				return // no preview fragment on the disabled view
			}
			zigFrag(t, "preview", edPreviewHTMLOf(st.Preview), stateJSON(st.Preview), zigui.RenderEditorPreview)
		})
	}
}
