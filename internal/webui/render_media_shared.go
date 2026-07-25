package webui

import (
	"fmt"
	"html"
	"strings"
)

// Reusable control state for the Zig-migrated media-batch tabs (overlays, twitch,
// editor): the shared components.go primitives resolved to plain JSON-able state, so
// the Go renderer and native/zigui/src/components.zig render the same bytes from the
// same input. Every `dl` field is the Go-resolved strings.ToLower(label) (Unicode
// lowercasing stays in Go); html() delegates to the components.go primitive so the
// markup has exactly ONE Go source.

// uiBtn is a btn() call as state.
type uiBtn struct {
	Label   string `json:"label"`
	Variant string `json:"variant"`
	Act     string `json:"act"`
	Val     string `json:"val"`
}

func (b uiBtn) html() string { return btn(b.Label, b.Variant, b.Act, b.Val) }

// uiBtnRow renders btnRow over a slice (empty slice still emits the row, like btnRow()).
func uiBtnRow(bs []uiBtn) string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.html())
	}
	return btnRow(out...)
}

// uiToggle is a toggleRow() call as state.
type uiToggle struct {
	Label string `json:"label"`
	DL    string `json:"dl"`
	Act   string `json:"act"`
	On    bool   `json:"on"`
}

func newToggle(label, act string, on bool) uiToggle {
	return uiToggle{Label: label, DL: strings.ToLower(label), Act: act, On: on}
}

func (t uiToggle) html() string { return toggleRowDL(t.Label, t.DL, t.Act, t.On) }

// uiField is a fieldEx() call as state (Type "" → "text"; PH "" → no placeholder).
type uiField struct {
	Label string `json:"label"`
	DL    string `json:"dl"`
	Act   string `json:"act"`
	Value string `json:"value"`
	Type  string `json:"inputType"`
	PH    string `json:"ph"`
}

func newField(label, act, value, inputType string) uiField {
	return uiField{Label: label, DL: strings.ToLower(label), Act: act, Value: value, Type: inputType}
}

func (f uiField) html() string { return fieldEx(f.Label, f.Act, f.Value, f.Type, f.PH, "") }

// uiKV is a kv() call as state.
type uiKV struct {
	Label string `json:"label"`
	DL    string `json:"dl"`
	Value string `json:"value"`
}

func newKV(label, value string) uiKV {
	return uiKV{Label: label, DL: strings.ToLower(label), Value: value}
}

func (k uiKV) html() string { return kv(k.Label, k.Value) }

// uiStatus is a statusRow() call as state. Variant "" = no row at all (the ovlStatus
// "unknown kind" case, which renders nothing).
type uiStatus struct {
	Variant string `json:"variant"`
	Label   string `json:"label"`
	DL      string `json:"dl"`
	Line    string `json:"line"`
}

func newStatus(variant, label, line string) uiStatus {
	return uiStatus{Variant: variant, Label: label, DL: strings.ToLower(label), Line: line}
}

func (s uiStatus) html() string {
	if s.Variant == "" {
		return ""
	}
	return statusRow(s.Variant, s.Label, s.Line)
}

// uiSlider is a slider() call as state. The numbers arrive PRE-FORMATTED (trimNum) and
// UnitJS pre-quoted (jsQuote): Go's shortest-round-trip float formatting has no
// guaranteed Zig equivalent, so it stays on the Go side. html() therefore re-implements
// slider() over strings - TestUISliderMatchesPrimitive pins the two together.
type uiSlider struct {
	Label  string `json:"label"`
	DL     string `json:"dl"`
	Act    string `json:"act"`
	Min    string `json:"min"`
	Max    string `json:"max"`
	Step   string `json:"step"`
	Val    string `json:"val"`
	Unit   string `json:"unit"`
	UnitJS string `json:"unitJs"` // jsQuote(Unit) - a JS string literal, inserted raw
}

func newSlider(label, act string, min, max, step, val float64, unit string) uiSlider {
	return uiSlider{Label: label, DL: strings.ToLower(label), Act: act,
		Min: trimNum(min), Max: trimNum(max), Step: trimNum(step), Val: trimNum(val),
		Unit: unit, UnitJS: jsQuote(unit)}
}

func (s uiSlider) html() string {
	oninput := `oninput='var b=this.parentNode.querySelector(".slider-val");if(b)b.textContent=this.value+` + s.UnitJS + `'`
	return fmt.Sprintf(`<label class=slider data-label=%s><span class=field-label>%s <b class=slider-val>%s%s</b></span>`+
		`<input class=slider-input type=range min=%s max=%s step=%s value=%s data-act=%s data-value=%s %s></label>`,
		attrQ(s.DL), html.EscapeString(s.Label), s.Val, html.EscapeString(s.Unit),
		s.Min, s.Max, s.Step, s.Val, attrQ(s.Act), attrQ(s.Val), oninput)
}
