//go:build zigui

package webui

import (
	"testing"
	"time"

	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/zigui"
)

// Phase B4a DOM gate for the engine-sample collapse. The 178 surfaces of TestZigPlayerGolden all
// render an IDLE audio transport: `&UI{}` has no player service, so the audio arm of the sampler
// returns the zero mpTr and no fixture could ever exercise a loaded/playing/paused/optimistic
// transport. The collapse changes exactly WHEN that sample is taken, so the states it produces
// need their own byte-identity coverage - Go == v1 (JSON) == v2 (RZW1) over all nine patch targets.
//
// Sampling is driven through mpMirrorOv (player_order_test.go scriptMirror, pinned), which is also
// how the untagged gates reproduce the torn render the per-consumer sampling allowed.
// Run: bash scripts/build-zig.sh && GOWORK=off go test -count=1 -tags zigui ./internal/webui -run TestZigPlayerEngine

// mpEngFixtures is the engine axis: every branch of mpSampleEng's audio arm, plus the optimistic
// override the transport RPCs install and its expiry.
func mpEngFixtures(path string) map[string]struct {
	st   featurehost.State
	opt  string
	till time.Duration // audOptUntil relative to now (<0 = already expired)
} {
	playing := featurehost.State{Path: path, Playing: true, Cur: 187.25, Total: 612.5}
	paused := featurehost.State{Path: path, Playing: true, Paused: true, Cur: 42.5, Total: 612.5}
	return map[string]struct {
		st   featurehost.State
		opt  string
		till time.Duration
	}{
		"idle":      {featurehost.State{}, "", 0},
		"otherFile": {featurehost.State{Path: `C:\sets\other.flac`, Playing: true, Cur: 5, Total: 100}, "", 0},
		"playing":   {playing, "", 0},
		"paused":    {paused, "", 0},
		"noTotal":   {featurehost.State{Path: path, Playing: true, Cur: 3.5}, "", 0},
		// inside the fixtures' momentary-LUFS grid (Step 0.1, 5 buckets): drives the
		// "M at playhead" hover branch, which a later position falls out of
		"playingInMom": {featurehost.State{Path: path, Playing: true, Cur: 0.25, Total: 612.5}, "", 0},
		"optPlay":      {paused, "play", time.Minute},
		"optPause":     {playing, "pause", time.Minute},
		"optStop":      {playing, "stop", time.Minute},
		"optExpired":   {playing, "pause", -time.Minute},
		"optNoEngine":  {featurehost.State{}, "play", time.Minute},
	}
}

// mpEngBases are the player fixtures whose surfaces the transport actually reaches: chips + hover
// + markers (wave/hov/tp), edit mode (edit/ro/export), and the adversarial escaping state.
var mpEngBases = []string{"audioChips", "audioHover", "singleEdit", "escaping"}

func TestZigPlayerEngineGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	before := zigui.FallbackCounts()
	fx := mpFixtures()
	var checked, empties, loaded int
	for _, baseName := range mpEngBases {
		base, ok := fx[baseName]
		if !ok {
			t.Fatalf("player fixture %q missing", baseName)
		}
		if len(base.media) == 0 || base.media[0].kind != "audio" {
			t.Fatalf("%s: expected an audio-first fixture", baseName)
		}
		for engName, eng := range mpEngFixtures(base.media[0].path) {
			t.Run(baseName+"/"+engName, func(t *testing.T) {
				u := &UI{}
				t.Cleanup(func() { releaseUIState(u) })
				u.mu.Lock()
				u.libSection = "collection"
				u.mu.Unlock()
				if engName != "optNoEngine" {
					u.mpMirrorOv = &scriptMirror{seq: []featurehost.State{eng.st}, pin: true}
				}
				*u.mp(base.host) = base
				snap := u.mpMut(base.host, func(v *mpSt) {
					v.audOpt = eng.opt
					if eng.opt != "" {
						v.audOptUntil = time.Now().Add(eng.till)
					}
				})
				if u.mpEng(&snap).loaded {
					loaded++
				}
				inner := u.mpInnerState(snap)
				full := mpFullSt{Host: snap.host, Inner: inner}

				check := func(what string, doc, js []byte, want string, v1fn, v2fn func([]byte) (string, bool)) {
					if len(doc) == 0 {
						t.Fatalf("%s: wire encode failed", what)
					}
					v1, ok1 := v1fn(js)
					v2, ok2 := v2fn(doc)
					if ok1 != ok2 {
						t.Fatalf("%s: v1 ok=%v but v2 ok=%v", what, ok1, ok2)
					}
					if !ok1 { // legitimately empty fragment: Go must render "" too
						if want != "" {
							t.Fatalf("%s: both Zig paths declined but Go rendered %d bytes", what, len(want))
						}
						empties++
						return
					}
					checked++
					assertBytesEqual(t, what+" go==v1", want, v1)
					assertBytesEqual(t, what+" v1==v2", v1, v2)
				}

				check("full", wireMpFull(full), stateJSON(full), mpFullHTMLOf(full), zigui.RenderPlayer, zigui.RenderPlayerV2)
				check("root", wireMpInner(inner), stateJSON(inner), mpInnerHTMLOf(inner), zigui.RenderPlayerRoot, zigui.RenderPlayerRootV2)
				check("vid", wireMpVid(inner.Vid), stateJSON(inner.Vid), mpVidHTMLOf(inner.Vid), zigui.RenderPlayerVid, zigui.RenderPlayerVidV2)
				check("wave", wireMpWave(inner.Wave), stateJSON(inner.Wave), mpWaveHTMLOf(inner.Wave), zigui.RenderPlayerWave, zigui.RenderPlayerWaveV2)
				check("tp", wireMpTp(inner.Tp), stateJSON(inner.Tp), mpTpHTMLOf(inner.Tp), zigui.RenderPlayerTp, zigui.RenderPlayerTpV2)
				check("edit", wireMpEdit(inner.EditBox), stateJSON(inner.EditBox), mpEditHTMLOf(inner.EditBox), zigui.RenderPlayerEdit, zigui.RenderPlayerEditV2)
				check("export", wireMpExport(inner.EditBox.Export), stateJSON(inner.EditBox.Export), mpExportHTMLOf(inner.EditBox.Export), zigui.RenderPlayerExport, zigui.RenderPlayerExportV2)
				check("ro", wireMpRO(inner.EditBox.RO), stateJSON(inner.EditBox.RO), mpROHTMLOf(inner.EditBox.RO), zigui.RenderPlayerRO, zigui.RenderPlayerROV2)
				check("hov", wireMpHov(inner.Hov), stateJSON(inner.Hov), mpHovHTMLOf(inner.Hov), zigui.RenderPlayerHov, zigui.RenderPlayerHovV2)
			})
		}
	}
	if loaded == 0 {
		t.Fatal("no fixture produced a LOADED transport - the whole engine axis would be vacuous")
	}
	t.Logf("%d surfaces checked (%d legitimately empty) over %d base × %d engine states; %d loaded transports",
		checked, empties, len(mpEngBases), len(mpEngFixtures("x")), loaded)
	assertFallbackDelta(t, before, 2*empties, "RenderPlayer")
}

// TestMpEngLoadedRendersDifferently keeps the suite above honest: a loaded transport must not
// render like an idle one on the surfaces that carry it, or the added coverage is decoration.
func TestMpEngLoadedRendersDifferently(t *testing.T) {
	base := mpFixtures()["audioChips"]
	render := func(mirror mpMirror) (string, string, string) {
		u := &UI{mpMirrorOv: mirror}
		defer releaseUIState(u)
		u.mu.Lock()
		u.libSection = "collection"
		u.mu.Unlock()
		*u.mp(base.host) = base
		inner := u.mpInnerState(u.mpSnap(base.host))
		return mpWaveHTMLOf(inner.Wave), mpTpHTMLOf(inner.Tp), mpHovHTMLOf(inner.Hov)
	}
	at := func(cur float64) mpMirror {
		return &scriptMirror{pin: true, seq: []featurehost.State{
			{Path: base.media[0].path, Playing: true, Cur: cur, Total: 612.5}}}
	}
	iw, it, ih := render(nil)
	// Two positions: the fixture is zoomed to 150-390 s (playhead must be INSIDE the view to be
	// drawn) while its momentary-LUFS grid only covers the first 0.5 s (hover readout data).
	pw, pt, _ := render(at(187.25))
	_, _, ph := render(at(0.25))
	if iw == pw {
		t.Error("the waveform renders a playing engine like an idle one (no playhead?)")
	}
	if it == pt {
		t.Error("the transport row renders a playing engine like an idle one")
	}
	if ih == ph {
		t.Error("the hover readout renders a playing engine like an idle one")
	}
}
