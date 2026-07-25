package webui

import (
	"strings"
	"testing"
)

// uiSlider carries every number twice: floats for the Go slider() primitive, trimNum
// strings for Zig. If the two ever disagree the golden gate reports a byte divergence in
// a confusing place - assert the pairing at the source instead.
func TestUISliderNumberPairsAgree(t *testing.T) {
	cases := []struct {
		label, act          string
		min, max, step, val float64
		unit                string
	}{
		{"Wave opacity", "ovl-wf-waveopac", 0, 1, 0.05, 0.85, ""},
		{"Background opacity", "ovl-wf-bgopac", 0, 1, 0.05, 0, ""},
		{"Gain", "x-gain", -24, 24, 0.5, -6.5, " dB"},
		{"Zoom", "z", 1, 100, 1, 100, "%"},
		{`Odd "label" & <x>`, `act:"1'`, 0, 0.333, 0.001, 0.333, `u"n'<>`},
	}
	for _, c := range cases {
		s := newSlider(c.label, c.act, c.min, c.max, c.step, c.val, c.unit)
		for _, p := range []struct{ got, want string }{
			{s.MinS, trimNum(c.min)}, {s.MaxS, trimNum(c.max)},
			{s.StepS, trimNum(c.step)}, {s.ValS, trimNum(c.val)},
		} {
			if p.got != p.want {
				t.Errorf("slider(%q): pre-formatted %q != trimNum %q", c.label, p.got, p.want)
			}
		}
		if s.DL != strings.ToLower(c.label) {
			t.Errorf("slider(%q): dl %q is not the lowered label", c.label, s.DL)
		}
		if s.UnitJS != jsQuote(c.unit) {
			t.Errorf("slider(%q): unitJs %q != jsQuote", c.label, s.UnitJS)
		}
		// the Go path must still go through the shared primitive verbatim
		if got, want := s.html(), slider(c.label, c.act, c.min, c.max, c.step, c.val, c.unit); got != want {
			t.Errorf("slider(%q) drift:\nwant %s\ngot  %s", c.label, want, got)
		}
	}
}

// The other media-batch control structs delegate straight to their primitive; assert the
// delegation (and the Go-resolved data-labels) stay wired.
func TestMediaControlStateDelegates(t *testing.T) {
	if got, want := newField("Port", "set:p", "8080", "number").html(), fieldExDL("Port", "port", "set:p", "8080", "number", "", ""); got != want {
		t.Errorf("uiField drift:\n%s\n%s", got, want)
	}
	if got, want := newKV("Overlay URL", "http://x/").html(), kvDL("Overlay URL", "overlay url", "http://x/"); got != want {
		t.Errorf("uiKV drift:\n%s\n%s", got, want)
	}
	if got, want := newToggle("Auto add", "a", true).html(), toggleRow("Auto add", "a", true); got != want {
		t.Errorf("uiToggle drift:\n%s\n%s", got, want)
	}
	if got, want := newStatus("success", "Serving", "line").html(), statusRowDL("success", "Serving", "serving", "line"); got != want {
		t.Errorf("uiStatus drift:\n%s\n%s", got, want)
	}
	if got := (uiStatus{}).html(); got != "" {
		t.Errorf("empty-variant status should render nothing, got %q", got)
	}
	if got, want := uiBtnRow([]uiBtn{{Label: "A", Variant: "go", Act: "a"}}), btnRow(btn("A", "go", "a", "")); got != want {
		t.Errorf("uiBtnRow drift:\n%s\n%s", got, want)
	}
}
