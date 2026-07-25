package webui

import (
	"strings"
	"testing"
)

// Guards for the two publish primitives that exist in two representations because the
// Publish tab renders from Zig (native/zigui/src/publish.zig) as well as Go.

// actionMenu (Go-rendered tabs) and actionMenuHTML(resolveActionMenu(...)) (the
// Zig-migrated publish tab) must emit the same bytes - the resolved twin exists only to
// hand the select to Zig as state, never to restyle the control.
func TestActionMenuResolvedParity(t *testing.T) {
	items := []ssOpt{
		{Val: "pub-open:c&1", Label: `Open "externally"`},
		{Val: "pub-capdel:c1", Label: "Remove"},
	}
	for _, label := range []string{"⋯ More", `⋯ M&ore"<>'`} {
		want := actionMenu("t-capmenu", label, items)
		got := actionMenuHTML(resolveActionMenu("t-capmenu", label, items))
		if want != got {
			t.Errorf("label %q:\nactionMenu     %q\nactionMenuHTML %q", label, want, got)
		}
	}
}

// pubBarSt carries a progress fraction twice: the float the Go primitive formats and the
// pre-formatted percentage the Zig renderer emits. Pin them together (same clamping, same
// "%.1f%%") so a drift shows up here instead of in the golden diff.
func TestPubBarNumberPairsAgree(t *testing.T) {
	for _, f := range []float64{-1, 0, 0.001, 0.25, 1.0 / 3.0, 0.999, 1, 2} {
		b := newPubBar(f, "cap")
		want := `style="width:` + b.Pct + `"`
		if got := progressBar(b.Frac, b.Cap); !strings.Contains(got, want) {
			t.Errorf("frac %v: progressBar has no %s\n%s", f, want, got)
		}
	}
	// empty caption: Go progressBar falls back to the percentage; the Zig helper does too.
	b := newPubBar(0.5, "")
	if got := progressBar(b.Frac, b.Cap); !strings.Contains(got, `<span class=pbar-cap>`+b.Pct+`</span>`) {
		t.Errorf("empty caption did not fall back to pct: %s", got)
	}
}
