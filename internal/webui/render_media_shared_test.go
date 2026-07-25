package webui

import "testing"

// uiSlider.html() re-implements the slider() primitive over pre-formatted strings (Go's
// shortest-round-trip float formatting has no guaranteed Zig equivalent). Pin the two
// together, or the Zig golden gate would only prove Zig matches a drifted copy.
func TestUISliderMatchesPrimitive(t *testing.T) {
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
		want := slider(c.label, c.act, c.min, c.max, c.step, c.val, c.unit)
		got := newSlider(c.label, c.act, c.min, c.max, c.step, c.val, c.unit).html()
		if want != got {
			t.Errorf("slider(%q) drift:\nwant %s\ngot  %s", c.label, want, got)
		}
	}
}

// The other media-batch control structs delegate straight to their primitive; assert the
// delegation (and the Go-resolved data-labels) stay wired.
func TestMediaControlStateDelegates(t *testing.T) {
	if got, want := newField("Port", "set:p", "8080", "number").html(), field("Port", "set:p", "8080", "number"); got != want {
		t.Errorf("uiField drift:\n%s\n%s", got, want)
	}
	if got, want := newKV("Overlay URL", "http://x/").html(), kv("Overlay URL", "http://x/"); got != want {
		t.Errorf("uiKV drift:\n%s\n%s", got, want)
	}
	if got, want := newToggle("Auto add", "a", true).html(), toggleRow("Auto add", "a", true); got != want {
		t.Errorf("uiToggle drift:\n%s\n%s", got, want)
	}
	if got, want := newStatus("success", "Serving", "line").html(), statusRow("success", "Serving", "line"); got != want {
		t.Errorf("uiStatus drift:\n%s\n%s", got, want)
	}
	if got := (uiStatus{}).html(); got != "" {
		t.Errorf("empty-variant status should render nothing, got %q", got)
	}
	if got, want := uiBtnRow([]uiBtn{{Label: "A", Variant: "go", Act: "a"}}), btnRow(btn("A", "go", "a", "")); got != want {
		t.Errorf("uiBtnRow drift:\n%s\n%s", got, want)
	}
}
