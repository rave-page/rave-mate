//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/zigui"
)

// Player golden gate: the unified media player/editor must render byte-identically in Zig
// for every patch target (full component + root/vid/wave/tp/edit/export/ro/hov). Fixtures
// are built from REAL mpSt snapshots through the production state builders, so the smart
// selects, chips, sliders and the raw seams (waveform SVG, loudness block, tooltips) are
// exactly what the live surfaces cross the ABI with.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// mpFixtures covers both hosts (publish shows the media title, library demotes trim into
// the ⋯ menu), no-media / loading / idle / playing / paused / decode-error, cue+drop rows,
// single + dual edit with every alignment state, every export stage, escaping, long,
// unicode.
func mpFixtures() map[string]mpSt {
	base := func(host string) mpSt {
		return mpSt{host: host, viewStart: 0, viewSpan: 1, cursorSec: mpNone, hovT: mpNone,
			outSec: -1, inSec: 0, firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1,
			lastTrackIdx: -1}
	}
	peaks := func(n, step int) []byte {
		p := make([]byte, n)
		for i := range p {
			p[i] = byte((i * step) % 256)
		}
		return p
	}
	aud := func(dur float64) mpMedia {
		return mpMedia{path: `C:\sets\live.flac`, kind: "audio", dur: dur, size: 123456789, peaks: peaks(500, 7)}
	}
	vid := func(dur float64) mpMedia {
		return mpMedia{path: `C:\sets\live.mp4`, kind: "video", dur: dur, size: 987654321, peaks: peaks(300, 11)}
	}
	src := &transcode.SourceInfo{HasAudio: true, AudioCodec: "flac", AudioKbps: 900,
		SampleRate: 44100, Channels: 2, DurationSec: 3600, HasVideo: true, VideoCodec: "h264",
		Width: 1920, Height: 1080, VideoKbps: 6000}
	loud := &mpLoud{I: -9.13, TP: -0.27, LRA: 6.4, Step: 0.1, Mom: []float64{-70, -12.5, -9.1, -8.4, -14.2}}
	marks := []mpMark{{off: 0, label: "Opener"}, {off: 183.25, label: `A&B - "Track" <2>`}, {off: 402.5, label: "Closer"}}
	cues := []musiclib.CuePoint{
		{Name: "Intro", Kind: musiclib.CueHot, StartMs: 1000, Hotcue: 0},
		{Name: `Drop "1"`, Kind: musiclib.CueLoad, StartMs: 60000, Hotcue: 3, Sw: "traktor"},
		{Name: "", Kind: musiclib.CueLoop, StartMs: 120000, LenMs: 4000, Hotcue: -1},
	}

	out := map[string]mpSt{}
	out["empty"] = base("library")

	t1 := base("library")
	t1.media = []mpMedia{aud(600)}
	out["audioIdle"] = t1

	// every analysis in flight at once (dim chips + "Analyzing waveform…" caption)
	t2 := base("library")
	m2 := aud(600)
	m2.peaks, m2.peaksLoading, m2.srcLoading, m2.loudLoading, m2.seekTabLoading = nil, true, true, true, true
	t2.media = []mpMedia{m2}
	out["audioLoading"] = t2

	// probe + loudness chips, cue + drop rows, markers, zoomed view, click cursor
	t3 := base("library")
	m3 := aud(600)
	m3.src, m3.loud, m3.cues, m3.drops = src, loud, cues, []float64{45000, 300000}
	t3.media = []mpMedia{m3}
	t3.markers, t3.lastTrackIdx = marks, 1
	t3.viewStart, t3.viewSpan, t3.cursorSec = 0.25, 0.4, 210.5
	out["audioChips"] = t3

	// hover readout + waveform-error caption
	t4 := base("library")
	m4 := aud(600)
	m4.peaks, m4.peaksErr, m4.loud = nil, "ffprobe failed: <exit 1>", loud
	t4.media = []mpMedia{m4}
	t4.hovT = 123.45
	out["audioHover"] = t4

	// <video> mirror playing, MSE source strategy
	t5 := base("publish")
	m5 := vid(600)
	m5.fragOK, m5.src = true, src
	t5.media = []mpMedia{m5}
	t5.name = `OBS set "2026-07-25" & friends`
	t5.vid = mpVid{cur: 61.25, dur: 605, started: true}
	out["videoPlaying"] = t5

	t6 := base("publish")
	t6.media = []mpMedia{vid(600)}
	t6.name = "plain capture"
	t6.vid = mpVid{cur: 12.5, dur: 600, started: true, paused: true}
	out["videoPaused"] = t6

	t7 := base("publish")
	t7.media = []mpMedia{vid(600)}
	t7.name = "broken"
	t7.vid = mpVid{err: `NotSupportedError: "codec"`}
	out["videoErr"] = t7

	// editor host: realtime-preview feed + the resizable box (grip act + px cap)
	te := base("editor")
	te.media = []mpMedia{vid(600)}
	te.name = "editor preview"
	te.inSec, te.outSec = 4, 90
	te.vid = mpVid{cur: 30, dur: 600, started: true,
		strURL: "http://127.0.0.1:1/ms/s1/t1", strMime: `video/mp4; codecs="avc1.64002a,mp4a.40.2"`,
		strT0: 30, strAuto: true}
	out["videoEditorStream"] = te

	dual := func(align mpAlignSt) mpSt {
		t := base("publish")
		ma, mv := aud(600), vid(590)
		ma.src, ma.loud, mv.src = src, loud, src
		t.media = []mpMedia{ma, mv}
		t.name = "dual set"
		t.edit = true
		t.inSec, t.outSec = 12.5, 540.25
		t.markers = marks
		t.firstTrackSec, t.lastTrackEndSec, t.lastFaderSec = 5, 580, 575
		t.align = align
		return t
	}
	out["dualEditPrior"] = dual(mpAlignSt{off: -2.5, priorOK: true})
	out["dualEditRun"] = dual(mpAlignSt{state: "run", pct: 42.5, msg: " scanning…"})
	out["dualEditErr"] = dual(mpAlignSt{state: "err", msg: ` no overlap <"x">`})
	out["dualEditOK"] = dual(mpAlignSt{state: "ok", off: 3.75, conf: 0.87, label: "high"})
	out["dualEditManual"] = dual(mpAlignSt{state: "ok", off: -1.25, conf: 0.4, label: "low", manual: true})

	// dual export: scope select + size estimate + loudness override on the audio half
	t8 := dual(mpAlignSt{state: "ok", off: 1, conf: 0.9, label: "high"})
	t8.media[0].loudOv = loudnessVals{On: true, I: -14, TP: -1, RaiseOnly: true}
	t8.media[0].presetID, t8.exportScope = "flac", "both"
	out["dualExport"] = t8

	for _, stage := range []string{"", "queued", "prepare", "measure"} {
		t := dual(mpAlignSt{state: "ok", off: 1, conf: 0.9, label: "high"})
		t.exporting, t.exportPct, t.exportStage = true, 37.4, stage
		t.exportLoudTx, t.exportMsg = "applied +3.2 dB", `wrote C:\out\cut.flac`
		name := "exportRun"
		if stage != "" {
			name += "-" + stage
		}
		out[name] = t
	}

	// single-media edit mode (no align row), out = end, silence detection running
	t9 := base("library")
	m9 := aud(600)
	m9.src, m9.loud = src, loud
	t9.media = []mpMedia{m9}
	t9.edit, t9.detecting = true, true
	t9.inSec, t9.outSec = 30, -1
	out["singleEdit"] = t9

	// adversarial escaping everywhere Go splices a value into an attribute
	t10 := base("publish")
	m10 := aud(600)
	m10.path = `C:\a&b\<"x">'q'.flac`
	m10.outPath = `C:\out\a&b "cut".flac`
	m10.src, m10.loud, m10.cues = src, loud, cues
	t10.media = []mpMedia{m10}
	t10.name = `Set & <"escape">'me'`
	t10.markers, t10.lastTrackIdx = []mpMark{{off: 12.34, label: `Tr&ack <"1">'x'`}}, 0
	t10.edit = true
	t10.exportMsg, t10.exportLoudTx = `err & <"quoted">`, `+1.5 dB & <"tp">`
	t10.hovT = 55.5
	out["escaping"] = t10

	t11 := base("library")
	m11 := aud(7200)
	m11.src, m11.loud = src, loud
	t11.media = []mpMedia{m11}
	for i := 0; i < 60; i++ {
		t11.markers = append(t11.markers, mpMark{off: float64(i) * 97.5, label: strings.Repeat("very-long-track-title-", 8)})
	}
	t11.lastTrackIdx = 59
	t11.viewStart, t11.viewSpan = 0.9, 0.02
	out["long"] = t11

	t12 := base("publish")
	m12 := aud(600)
	m12.path = "D:/сеты/Студія 中文 🎧.flac"
	m12.src, m12.loud = src, loud
	t12.media = []mpMedia{m12}
	t12.name = "Студія 中文 🎧 größer"
	t12.markers, t12.lastTrackIdx = []mpMark{{off: 61.5, label: "Трек ①"}}, 0
	t12.edit = true
	out["unicode"] = t12
	return out
}

func TestZigPlayerGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, fx := range mpFixtures() {
		t.Run(name, func(t *testing.T) {
			u := &UI{}
			t.Cleanup(func() { releaseUIState(u) })
			u.mu.Lock()
			u.libSection = "collection" // library host → mpTrimDemoted ⋯ menu
			u.mu.Unlock()
			*u.mp(fx.host) = fx
			snap := u.mpSnap(fx.host)

			// ONE state build per surface, exactly like the bridges do.
			inner := u.mpInnerState(snap)
			full := mpFullSt{Host: snap.host, Inner: inner}

			check := func(what string, st any, want string, zigFn func([]byte) (string, bool)) {
				js := stateJSON(st)
				if js == nil {
					t.Fatalf("%s: state marshal failed", what)
				}
				got, ok := zigFn(js)
				if !ok {
					// A legitimately empty fragment returns NULL; Go must agree.
					if want != "" {
						t.Fatalf("%s: zig render failed but Go rendered %d bytes", what, len(want))
					}
					return
				}
				assertBytesEqual(t, what, want, got)
			}

			check("full", full, mpFullHTMLOf(full), zigui.RenderPlayer)
			check("root", inner, mpInnerHTMLOf(inner), zigui.RenderPlayerRoot)
			check("vid", inner.Vid, mpVidHTMLOf(inner.Vid), zigui.RenderPlayerVid)
			check("wave", inner.Wave, mpWaveHTMLOf(inner.Wave), zigui.RenderPlayerWave)
			check("tp", inner.Tp, mpTpHTMLOf(inner.Tp), zigui.RenderPlayerTp)
			check("edit", inner.EditBox, mpEditHTMLOf(inner.EditBox), zigui.RenderPlayerEdit)
			check("export", inner.EditBox.Export, mpExportHTMLOf(inner.EditBox.Export), zigui.RenderPlayerExport)
			check("ro", inner.EditBox.RO, mpROHTMLOf(inner.EditBox.RO), zigui.RenderPlayerRO)
			check("hov", inner.Hov, mpHovHTMLOf(inner.Hov), zigui.RenderPlayerHov)
		})
	}
}

// TestZigPlayerLoudnessGolden sweeps the SHARED loudness block (components.go loudSt, phase
// B-1a) through the #mp-<host>-export patch target, which used to embed it as raw markup.
// The base state is a REAL dual-export snapshot, so the block sits in its production frame
// (preset row + summary + output field + the standalone gain-plan line beside it).
func TestZigPlayerLoudnessGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	u := &UI{}
	t.Cleanup(func() { releaseUIState(u) })
	fx := mpFixtures()["dualExport"]
	*u.mp(fx.host) = fx
	base := u.mpInnerState(u.mpSnap(fx.host)).EditBox.Export
	if len(base.Medias) == 0 {
		t.Fatal("dualExport fixture has no export medias")
	}
	for name, o := range loudFx() {
		t.Run(name, func(t *testing.T) {
			st := base
			st.Medias = append([]mpExMediaSt(nil), base.Medias...)
			st.Medias[0].Loud = newLoudSt(o)
			zigGolden(t, "playerLoud", st, mpExportHTMLOf(st), zigui.RenderPlayerExport)
		})
	}
}

// TestPlayerStatesHaveNoNullSlices guards the nil-slice trap: a nil Go slice marshals to
// JSON null, which the Zig parser rejects → the WHOLE surface silently falls back to Go.
func TestPlayerStatesHaveNoNullSlices(t *testing.T) {
	for name, fx := range mpFixtures() {
		u := &UI{}
		inner := u.mpInnerState(fx)
		js := string(stateJSON(mpFullSt{Host: fx.host, Inner: inner}))
		if strings.Contains(js, ":null") {
			t.Errorf("%s: state carries a null (nil slice → Zig parse reject): %s", name, js)
		}
		releaseUIState(u)
	}
}

// TestZigPlayerTipGolden pins the tooltip seam on the media player (phase B1b). The real fixtures
// already exercise all four sites (tipWave = the wave-nav keybind grid, tipVideo on the video
// hosts, tipTrim in edit mode, tipAlign on the dual row) in their present/absent states; this adds
// the two shapes the player's own topics do not have - a MULTI-LINK card - and the raw-bridge
// fallback an un-migrated builder would still ship, across every locale.
func TestZigPlayerTipGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	t.Cleanup(func() { i18n.SetLocale("en") })
	fx := mpFixtures()["dualEditOK"] // edit mode + dual align + video half: all four sites live

	for _, loc := range i18n.Available() {
		i18n.SetLocale(loc.Code)
		for _, variant := range []string{"structured", "multiLink", "rawBridge"} {
			t.Run(loc.Code+"/"+variant, func(t *testing.T) {
				u := &UI{}
				t.Cleanup(func() { releaseUIState(u) })
				*u.mp(fx.host) = fx
				inner := u.mpInnerState(u.mpSnap(fx.host))
				switch variant {
				case "multiLink":
					// three authoritative links + a two-link topic, no keybind grid
					inner.TipWaveS = tipTopicSt("account-bridge")
					inner.Tp.TipVideoS = tipTopicSt("midi-learn-controllers")
					inner.EditBox.TipTrimS = tipTopicSt("enc-video-codec")
					inner.EditBox.Align.TipAlignS = tipTopicSt("mp-loudness")
				case "rawBridge":
					// un-migrated shape: structured absent, pre-rendered markup carried raw
					inner.TipWaveS, inner.TipWave = nil, tipTopic("wave-nav")
					inner.Tp.TipVideoS, inner.Tp.TipVideo = nil, tipTopic("embedded-video")
					inner.EditBox.TipTrimS, inner.EditBox.TipTrim = nil, tipTopic("trim-editor")
					inner.EditBox.Align.TipAlignS, inner.EditBox.Align.TipAlign = nil, tipTopic("dual-alignment")
				}
				full := mpFullSt{Host: fx.host, Inner: inner}
				zigFrag(t, "full", mpFullHTMLOf(full), stateJSON(full), zigui.RenderPlayer)
			})
		}
	}
}
