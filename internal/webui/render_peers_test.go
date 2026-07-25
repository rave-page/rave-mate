package webui

import (
	"fmt"
	"strings"
	"testing"
)

// progressBar must stay ONE markup source: the float entry point has to be exactly
// progressBarStr(progressPct(frac), …), because the peers Zig state path carries only the
// pre-formatted width (floats never cross the ABI).
func TestProgressBarDelegatesToPct(t *testing.T) {
	for _, frac := range []float64{-1, 0, 0.001, 0.4237, 0.5, 0.999999, 1, 2} {
		for _, cap := range []string{"", "4.2 MB / 10.0 MB", `a&b<"c">`} {
			want := progressBar(frac, cap)
			got := progressBarStr(progressPct(frac), cap)
			if want != got {
				t.Fatalf("frac=%v cap=%q: %q != %q", frac, cap, want, got)
			}
		}
	}
	if got := progressPct(0.4237); got != "42.4%" {
		t.Fatalf("progressPct(0.4237) = %q", got)
	}
	if got := progressPct(2); got != "100.0%" { // clamped
		t.Fatalf("progressPct(2) = %q", got)
	}
}

// The peers row/list renderers are the shared shape behind connections, discovered and
// remembered - pin the markup so a refactor can't silently move the dot or the tail span.
func TestPeerRowHTML(t *testing.T) {
	got := peerRowHTML(peerRowSt{
		Dot: "muted", Name: "A&B", Sub: "offline",
		Btns:  []uiBtn{{Label: "Forget", Variant: "ghost", Act: "peer-forget:n1"}},
		Decks: []peerDeckSt{{Audible: true, Line: "Deck A"}, {Line: "Deck B"}},
	})
	want := `<div class=row><span class=row-label><span class="dot dot--muted"></span> A&amp;B ` +
		`<span class=np-artist>offline</span></span>` +
		`<div class=btn-row><button class="rp-btn rp-btn--ghost" data-act="peer-forget:n1">Forget</button></div></div>` +
		`<div class="peer-np">▶ Deck A</div><div class="peer-np peer-np--quiet">▷ Deck B</div>`
	if got != want {
		t.Fatalf("peerRowHTML:\n got %q\nwant %q", got, want)
	}
	if got := peerListHTML(peerListSt{Empty: "none"}); got != emptyState("none") {
		t.Fatalf("empty list = %q", got)
	}
}

// camPropHTML wires the ctl surface for one UVC knob: the act carries a 0x1f-separated
// node/prop pair (escaped, never %q) and value/data-value must agree.
func TestCamPropHTML(t *testing.T) {
	got := camPropHTML(camPropSt{
		Label: "Zoom", MinS: "0", MaxS: "500", StepS: "5", ValS: "120",
		Act: "peers-cam-prop:n1\x1fzoom", Disabled: true, CanAuto: true, Auto: true,
		AutoAct: "peers-cam-auto:n1\x1fzoom", AutoLbl: "Auto",
	})
	for _, want := range []string{
		`min=0 max=500 step=5 value=120`,
		` data-act="peers-cam-prop:n1` + "\x1f" + `zoom" data-value=120 disabled oninput=`,
		`<span class=cam-prop-v>120</span>`,
		`<input type=checkbox checked data-act="peers-cam-auto:n1` + "\x1f" + `zoom" data-value="true">Auto</label>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("camPropHTML missing %q in %q", want, got)
		}
	}
	if n := strings.Count(got, fmt.Sprintf("%d", 120)); n != 3 { // value=, data-value=, cam-prop-v
		t.Fatalf("value appears %d times, want 3: %q", n, got)
	}
}
