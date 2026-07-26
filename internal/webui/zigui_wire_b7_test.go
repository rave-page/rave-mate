//go:build zigui

package webui

import (
	"testing"

	"rave.page/mate/internal/zigui"
)

// B7 fan-out registry + three-way gates (root ids 45-99). Same contract as wave B-2
// (zigui_wire_b2_test.go): every _v2 export and every base document registers here, the
// mutation fuzz cross-feeds all of them, and each tab gets a Go == v1 == v2 byte-equality
// gate over its FULL golden fixture set with FallbackCounts asserted per-export.

func wireExportsB7() []wireExport {
	return []wireExport{
		{"overlays_v2", zigui.RenderOverlaysV2},
		{"overlays_appearance_v2", zigui.RenderOverlaysAppearanceV2},
		{"overlays_spout_v2", zigui.RenderOverlaysSpoutV2},
		{"overlays_status_v2", zigui.RenderOverlaysStatusV2},
		{"overlays_strip_v2", zigui.RenderOverlaysStripV2},
	}
}

func wireBasesB7() []wireBase {
	var out []wireBase
	for n, st := range ovlFixtures() {
		out = append(out,
			wireBase{"ovl/" + n, wireOvlState(st)},
			wireBase{"ovl/" + n + "/appearance", wireOvlAppr(st.Appearance)},
			wireBase{"ovl/" + n + "/spout", wireOvlSpout(st.VS.SpoutCtl)},
			wireBase{"ovl/" + n + "/status", wireUiStatus(st.Web.Card.Status)},
			wireBase{"ovl/" + n + "/strip", wireOvlStrip(st.Strip)})
	}
	return out
}

// TestZigWireThreeWayOverlays: full tab + the four live-patched fragments over the whole
// overlays golden fixture set.
func TestZigWireThreeWayOverlays(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := ovlFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireOvlState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderOverlays(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderOverlaysV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", overlaysHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			if !st.Available {
				return // fragments only exist on the available view (a zero uiStatus renders "" - the exports decline empty output)
			}
			threeWayFrag(t, "appearance", ovlApprHTML(st.Appearance), stateJSON(st.Appearance),
				wireOvlAppr(st.Appearance), zigui.RenderOverlaysAppearance, zigui.RenderOverlaysAppearanceV2)
			threeWayFrag(t, "spout", ovlSpoutHTML(st.VS.SpoutCtl), stateJSON(st.VS.SpoutCtl),
				wireOvlSpout(st.VS.SpoutCtl), zigui.RenderOverlaysSpout, zigui.RenderOverlaysSpoutV2)
			threeWayFrag(t, "status", st.Web.Card.Status.html(), stateJSON(st.Web.Card.Status),
				wireUiStatus(st.Web.Card.Status), zigui.RenderOverlaysStatus, zigui.RenderOverlaysStatusV2)
			threeWayFrag(t, "strip", ovlStripHTMLOf(st.Strip), stateJSON(st.Strip),
				wireOvlStrip(st.Strip), zigui.RenderOverlaysStrip, zigui.RenderOverlaysStripV2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacksIn(t, before,
		"RenderOverlays", "RenderOverlaysV2", "RenderOverlaysAppearance", "RenderOverlaysAppearanceV2",
		"RenderOverlaysSpout", "RenderOverlaysSpoutV2", "RenderOverlaysStatus", "RenderOverlaysStatusV2",
		"RenderOverlaysStrip", "RenderOverlaysStripV2")
}

// ── bench: whole dispatch (serialize + Zig render), v1 JSON vs v2 wire ──

func BenchmarkWireBenchOverlays(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := ovlFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderOverlays(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderOverlaysV2(wireOvlState(st)) })
}

func BenchmarkWireBenchOverlaysStatus(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := ovlFixtures()["populated"].Web.Card.Status
	benchPair(b,
		func() (string, bool) { return zigui.RenderOverlaysStatus(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderOverlaysStatusV2(wireUiStatus(st)) })
}

func BenchmarkWireBenchOverlaysStrip(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := ovlFixtures()["populated"].Strip
	benchPair(b,
		func() (string, bool) { return zigui.RenderOverlaysStrip(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderOverlaysStripV2(wireOvlStrip(st)) })
}

// threeWayFrag asserts one fragment renderer three ways: Go == v1(JSON) == v2(RZW1).
func threeWayFrag(t *testing.T, what, want string, js, doc []byte,
	v1 func([]byte) (string, bool), v2 func([]byte) (string, bool)) {
	t.Helper()
	if js == nil {
		t.Fatalf("%s: state marshal failed", what)
	}
	if len(doc) == 0 {
		t.Fatalf("%s: wire encode failed", what)
	}
	h1, ok := v1(js)
	if !ok {
		t.Fatalf("%s: v1 render failed", what)
	}
	h2, ok := v2(doc)
	if !ok {
		t.Fatalf("%s: v2 render failed", what)
	}
	assertBytesEqual(t, what+" go==v1", want, h1)
	assertBytesEqual(t, what+" v1==v2", h1, h2)
}
