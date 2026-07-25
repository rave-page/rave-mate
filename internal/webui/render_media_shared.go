package webui

import "strings"

// Reusable control state for the Zig-migrated media-batch tabs (overlays, twitch,
// editor): the shared components.go primitives resolved to plain JSON-able state, so
// the Go renderer and native/zigui/src/components.zig render the same bytes from the
// same input. Every `dl` field is the Go-resolved strings.ToLower(label) (Unicode
// lowercasing stays in Go) and is AUTHORITATIVE for both renderers - html() delegates
// to the caller-resolved *DL primitives, so the markup has exactly ONE Go source.

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

func (f uiField) html() string { return fieldExDL(f.Label, f.DL, f.Act, f.Value, f.Type, f.PH, "") }

// uiKV is a kv() call as state.
type uiKV struct {
	Label string `json:"label"`
	DL    string `json:"dl"`
	Value string `json:"value"`
}

func newKV(label, value string) uiKV {
	return uiKV{Label: label, DL: strings.ToLower(label), Value: value}
}

func (k uiKV) html() string { return kvDL(k.Label, k.DL, k.Value) }

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
	return statusRowDL(s.Variant, s.Label, s.DL, s.Line)
}

// uiSlider is a slider() call as state. Numbers ride BOTH as floats (the Go path feeds
// the shared slider() primitive unchanged) and pre-formatted via trimNum (the Zig path
// never formats a float - Go's shortest-round-trip formatting has no guaranteed Zig
// equivalent), so the golden gate catches any drift between the two. Field names +
// json tags match native/zigui components.zig `Slider` (shared with the motion batch's
// moSliderSt).
type uiSlider struct {
	Label  string  `json:"label"`
	DL     string  `json:"dl"`
	Act    string  `json:"act"`
	Unit   string  `json:"unit"`
	UnitJS string  `json:"unitJs"` // jsQuote(Unit) - a JS string literal, inserted raw
	Min    float64 `json:"-"`
	Max    float64 `json:"-"`
	Step   float64 `json:"-"`
	Val    float64 `json:"-"`
	MinS   string  `json:"minS"`
	MaxS   string  `json:"maxS"`
	StepS  string  `json:"stepS"`
	ValS   string  `json:"valS"`
}

func newSlider(label, act string, minV, maxV, step, val float64, unit string) uiSlider {
	return uiSlider{
		Label: label, DL: strings.ToLower(label), Act: act, Unit: unit, UnitJS: jsQuote(unit),
		Min: minV, Max: maxV, Step: step, Val: val,
		MinS: trimNum(minV), MaxS: trimNum(maxV), StepS: trimNum(step), ValS: trimNum(val),
	}
}

func (s uiSlider) html() string {
	return slider(s.Label, s.Act, s.Min, s.Max, s.Step, s.Val, s.Unit)
}
