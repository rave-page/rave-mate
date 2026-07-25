//go:build zigui

package webui

import (
	"encoding/binary"
	"strings"
	"testing"

	"rave.page/mate/internal/version"
	"rave.page/mate/internal/zigui"
)

// Three-way byte-equality gate for the RZW1 binary state wire (wave B-1 pilots):
// the Go renderer, the Zig JSON path (v1) and the Zig binary path (v2) must produce the
// SAME bytes for every fixture in the existing golden suites - full document AND every
// patched fragment. Run: make zig && GOWORK=off go test -count=1 -tags zigui ./internal/webui -run TestZigWire

func TestZigWireThreeWayAppGroups(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := agFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireAgState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderAppGroups(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderAppGroupsV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", appGroupsHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			b1, ok := zigui.RenderAppGroupsBody(js)
			if !ok {
				t.Fatal("v1 body render failed")
			}
			b2, ok := zigui.RenderAppGroupsBodyV2(doc)
			if !ok {
				t.Fatal("v2 body render failed")
			}
			assertBytesEqual(t, "body go==v1", appGroupsBodyHTML(st), b1)
			assertBytesEqual(t, "body v1==v2", b1, b2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

func TestZigWireThreeWayLogs(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := logsFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireLogsState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderLogs(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderLogsV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", logsHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			ldoc, ljs := wireLogsLines(st.Lines), stateJSON(st.Lines)
			l1, ok := zigui.RenderLogsLines(ljs)
			if !ok {
				t.Fatal("v1 lines render failed")
			}
			l2, ok := zigui.RenderLogsLinesV2(ldoc)
			if !ok {
				t.Fatal("v2 lines render failed")
			}
			assertBytesEqual(t, "lines go==v1", logsLinesHTML(st.Lines), l1)
			assertBytesEqual(t, "lines v1==v2", l1, l2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// ── B-2 fan-out ──

// TestZigWireThreeWayLive: the full cockpit plus all ten tick fragments, over the whole live
// golden fixture set. The fragments are the ~1 Hz path, so they are the reason this exists.
func TestZigWireThreeWayLive(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := liveFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireLiveState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderLive(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderLiveV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", liveHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			wireFrag(t, "transport", st.Transport, wireLiveTransport, liveTransHTML)
			wireFrag(t, "np", st.NP, wireLiveNP, liveNPHTML)
			wireFrag(t, "status", st.Status, wireLiveStatus, liveStatusFragHTML)
			wireFrag(t, "decks", st.Decks, wireLiveDecks, liveDecksFragHTML)
			wireFrag(t, "signals", st.Signals, wireLiveSignals, liveSignalsFragHTML)
			wireFrag(t, "cockpit", st.Cockpit, wireLiveCockpit, liveCockpitFragHTML)
			wireFrag(t, "link", st.Link, wireLiveLink, liveLinkFragHTML)
			wireFrag(t, "graph", st.Net, wireLiveGraph, liveGraphFragHTML)
			wireFrag(t, "graph", st.Tim, wireLiveGraph, liveGraphFragHTML)
			wireFrag(t, "perf", st.Perf, wireLivePerf, livePerfFragHTML)
			wireFrag(t, "strip", st.Strip, wireLiveStrip, liveStripFragHTML)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// wireFrag asserts Go == v1 == v2 for one live fragment kind.
func wireFrag[T any](t *testing.T, kind string, st T, wire func(T) []byte, goHTML func(T) string) {
	t.Helper()
	doc, js := wire(st), stateJSON(st)
	if len(doc) == 0 {
		t.Fatalf("%s: wire encode failed", kind)
	}
	v1, ok := zigui.RenderLiveFrag(kind, js)
	if !ok {
		t.Fatalf("%s: v1 render failed", kind)
	}
	v2, ok := zigui.RenderLiveFragV2(kind, doc)
	if !ok {
		t.Fatalf("%s: v2 render failed", kind)
	}
	assertBytesEqual(t, kind+" go==v1", goHTML(st), v1)
	assertBytesEqual(t, kind+" v1==v2", v1, v2)
}

// TestZigWireLiveFragIdsAreDistinct: every live fragment is its own root message, so feeding
// one fragment's document to another kind must be refused by the header - the property that
// keeps a mis-wired dispatch a clean v1 downgrade instead of a mis-decode.
func TestZigWireLiveFragIdsAreDistinct(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := liveFixtures()["populated"]
	docs := map[string][]byte{
		"transport": wireLiveTransport(st.Transport),
		"np":        wireLiveNP(st.NP),
		"status":    wireLiveStatus(st.Status),
		"decks":     wireLiveDecks(st.Decks),
		"signals":   wireLiveSignals(st.Signals),
		"cockpit":   wireLiveCockpit(st.Cockpit),
		"link":      wireLiveLink(st.Link),
		"graph":     wireLiveGraph(st.Net),
		"perf":      wireLivePerf(st.Perf),
		"strip":     wireLiveStrip(st.Strip),
	}
	for kind, doc := range docs {
		for other := range docs {
			if other == kind {
				continue
			}
			if _, ok := zigui.RenderLiveFragV2(other, doc); ok {
				t.Errorf("%s export accepted the %s document", other, kind)
			}
		}
		if _, ok := zigui.RenderLiveV2(doc); ok {
			t.Errorf("full-cockpit export accepted the %s fragment document", kind)
		}
	}
	if _, ok := zigui.RenderLiveFragV2("nope", docs["strip"]); ok {
		t.Error("unknown kind rendered")
	}
}

// TestWireStrAlwaysKeepsEmptyFill: live.Link.fill defaults to "0.00%" on the Zig side, so an
// empty Fill must travel as a PRESENT empty string (kStrAlways) - absent would decode to the
// default and only diverge on states where the panel has no phrase position yet.
func TestWireStrAlwaysKeepsEmptyFill(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := liveLinkSt{Available: true, Cap: "beat 1", Session: liveSR("success", "Session", "2 peers")} // Fill == ""
	v2, ok := zigui.RenderLiveFragV2("link", wireLiveLink(st))
	if !ok {
		t.Fatal("v2 render failed")
	}
	assertBytesEqual(t, "empty fill", liveLinkFragHTML(st), v2)
	if strings.Contains(v2, "0.00%") {
		t.Error("the Zig default leaked in - the field decoded as absent")
	}
}

// TestZigWireThreeWayMotion: one message, two surfaces. Motion is the optional-struct case -
// exactly one section state is built per render and the other stays nil, so `null` vs a
// zero-value struct must survive the wire (kOptPtr / OptStruct).
func TestZigWireThreeWayMotion(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := moFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireMoState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderMotion(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderMotionV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", motionHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			b1, ok := zigui.RenderMotionBody(js)
			if !ok {
				t.Fatal("v1 body render failed")
			}
			b2, ok := zigui.RenderMotionBodyV2(doc)
			if !ok {
				t.Fatal("v2 body render failed")
			}
			assertBytesEqual(t, "body go==v1", motionBodyHTML(st), b1)
			assertBytesEqual(t, "body v1==v2", b1, b2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// TestWireOptStructPresenceIsNotNull: an all-zero but PRESENT section must render as the
// section (empty), not as the absent section. Struct would drop the empty body and the field
// would decode as null - which is why the opt kinds use OptStruct.
func TestWireOptStructPresenceIsNotNull(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	absent := moState{Title: "Motion", Section: "campaths", TabCam: "Camera paths", TabStudio: "Studio"}
	present := absent
	present.Cam = &moCamSt{} // every field zero, but the section IS there

	for name, st := range map[string]moState{"absent": absent, "present": present} {
		doc := wireMoState(st)
		v2, ok := zigui.RenderMotionV2(doc)
		if !ok {
			t.Fatalf("%s: v2 render failed", name)
		}
		assertBytesEqual(t, name, motionHTML(st), v2)
	}
	if a, p := wireMoState(absent), wireMoState(present); len(a) >= len(p) {
		t.Errorf("absent (%d B) should be smaller than present-but-empty (%d B)", len(a), len(p))
	}
	if motionHTML(absent) == motionHTML(present) {
		t.Fatal("fixture is inert: absent and present render the same in Go")
	}
}

// TestZigWireThreeWayPublish: full tab + the #pub-hero tick fragment. An empty hero renders ""
// (a legitimate NULL that the Go fallback reproduces), so only shown heroes are asserted.
func TestZigWireThreeWayPublish(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := pubFixtures()
	var wireB, jsonB, heroes int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wirePub(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderPublish(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderPublishV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", publishHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			if !st.Body.Hero.Show {
				return
			}
			heroes++
			hero := st.Body.Hero
			h1, ok := zigui.RenderPublishHero(stateJSON(hero))
			if !ok {
				t.Fatal("v1 hero render failed")
			}
			h2, ok := zigui.RenderPublishHeroV2(wirePubHero(hero))
			if !ok {
				t.Fatal("v2 hero render failed")
			}
			assertBytesEqual(t, "hero go==v1", pubHeroHTML(hero), h1)
			assertBytesEqual(t, "hero v1==v2", h1, h2)
		})
	}
	if heroes == 0 {
		t.Fatal("no fixture exercised the hero fragment")
	}
	t.Logf("%d fixtures (%d heroes): wire %d B vs json %d B (%.1f%%)", len(fx), heroes, wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// TestWireUintRoundTrips: PubTrack.Num is the only numeric field on the wire (a 1-based row
// index against a Zig i64). Zero encodes as an absent tag, so row numbering must survive.
func TestWireUintRoundTrips(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := pubFixtures()["tracklist"]
	v2, ok := zigui.RenderPublishV2(wirePub(st))
	if !ok {
		t.Fatal("v2 render failed")
	}
	assertBytesEqual(t, "tracklist numbering", publishHTML(st), v2)
	if !strings.Contains(v2, "pub-track-n") {
		t.Fatal("fixture is inert: no numbered track row in the render")
	}
}

// TestZigWireThreeWaySettings: the tab, the #set-content pane and the per-card #stset-<id>
// status line. Fixture setup mirrors TestZigSettingsGolden (the state is built from a UI, not a
// literal), so the wire is gated on the same branchy states - including the search pane.
func TestZigWireThreeWaySettings(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := setFixtures()
	var wireB, jsonB, panes int
	for name, f := range fx {
		t.Run(name, func(t *testing.T) {
			f.u.setMu.Lock()
			f.u.setSec, f.u.setQuery = f.sec, f.q
			f.u.setMu.Unlock()
			if name == "updatesFeed" {
				old := version.FeedURL
				version.FeedURL = "https://feed.rave.page/mate.json"
				defer func() { version.FeedURL = old }()
			}
			if name == "selectOpen" {
				ssOpen("set-ml-codec", "h")
				defer func() {
					ssOpen("set-ml-codec", "")
					ssMu.Lock()
					ssSts["set-ml-codec"].open = false
					ssMu.Unlock()
				}()
			}

			st := f.u.settingsState()
			doc, js := wireSetState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderSettings(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderSettingsV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", settingsHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			if !st.Available {
				return // the content pane only exists on the available view
			}
			panes++
			c1, ok := zigui.RenderSettingsContent(stateJSON(st.Content))
			if !ok {
				t.Fatal("v1 content render failed")
			}
			c2, ok := zigui.RenderSettingsContentV2(wireSetContent(st.Content))
			if !ok {
				t.Fatal("v2 content render failed")
			}
			assertBytesEqual(t, "content go==v1", setContentHTML(st.Content), c1)
			assertBytesEqual(t, "content v1==v2", c1, c2)
		})
	}
	if panes == 0 {
		t.Fatal("no fixture exercised the #set-content pane")
	}
	t.Logf("%d fixtures (%d panes): wire %d B vs json %d B (%.1f%%)", len(fx), panes, wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// TestZigWireSettingsStatus: the per-card tick fragment, same state set as the golden suite.
func TestZigWireSettingsStatus(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	before := zigui.FallbackCounts()
	for _, s := range []stv{
		stOk("https://development.api.rave.page"),
		stOk(`a&b <"c"> 'd'`),
		stWarn("not listening"),
		stLive("recording 3 tracks"),
		stOk("信号 ✓ больш"),
		stWarn(strings.Repeat("e", 500)),
	} {
		st := setStatusSt{V: s.v, T: s.t}
		v1, ok := zigui.RenderSettingsStatus(stateJSON(st))
		if !ok {
			t.Fatalf("%s: v1 render failed", st.V)
		}
		v2, ok := zigui.RenderSettingsStatusV2(wireSetStatus(st))
		if !ok {
			t.Fatalf("%s: v2 render failed", st.V)
		}
		assertBytesEqual(t, st.V+" go==v1", setStatusHTML(st), v1)
		assertBytesEqual(t, st.V+" v1==v2", v1, v2)
	}
	assertNoNewFallbacks(t, before)
}

// TestZigWireThreeWayLibrary: the biggest state in the app (11 kB) plus its three patch
// targets. The tab, #lib-body and #lib-detail are separate messages because the patch paths
// send them separately.
func TestZigWireThreeWayLibrary(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := libFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireLibState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderLibrary(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderLibraryV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", libraryHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			b1, ok := zigui.RenderLibraryBody(stateJSON(st.Body))
			if !ok {
				t.Fatal("v1 body render failed")
			}
			b2, ok := zigui.RenderLibraryBodyV2(wireLibBody(st.Body))
			if !ok {
				t.Fatal("v2 body render failed")
			}
			assertBytesEqual(t, "body go==v1", libBodyHTML(st.Body), b1)
			assertBytesEqual(t, "body v1==v2", b1, b2)

			d1, ok := zigui.RenderLibraryDetail(stateJSON(st.Body.Detail))
			if !ok {
				t.Fatal("v1 detail render failed")
			}
			d2, ok := zigui.RenderLibraryDetailV2(wireLibDetail(st.Body.Detail))
			if !ok {
				t.Fatal("v2 detail render failed")
			}
			assertBytesEqual(t, "detail go==v1", libDetailHTMLOf(st.Body.Detail), d1)
			assertBytesEqual(t, "detail v1==v2", d1, d2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// TestZigWireLibraryPatchTargets: #lib-queue-body (job progress) and one cue-census cell, the
// two library fragments patched outside a full-tab render. The cell carries the only counts on
// the wire besides publish's row number (kUint: zero is an absent tag).
func TestZigWireLibraryPatchTargets(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	before := zigui.FallbackCounts()
	for name, st := range map[string]libQueueSt{
		"empty":     {Desc: "Transcode jobs", Empty: "Queue is empty"},
		"populated": libQueueFixture(),
		"unicode":   {Desc: "Задания", Empty: "пусто", Jobs: []libJobSt{{Label: "трек.flac · мастер", Status: "done", StatusVar: "success", Width: progressPct(1), Caption: "done · 100%"}}},
	} {
		v1, ok := zigui.RenderLibraryQueue(stateJSON(st))
		if !ok {
			t.Fatalf("%s: v1 queue render failed", name)
		}
		v2, ok := zigui.RenderLibraryQueueV2(wireLibQueue(st))
		if !ok {
			t.Fatalf("%s: v2 queue render failed", name)
		}
		assertBytesEqual(t, name+" queue go==v1", libQueueBodyHTML(st), v1)
		assertBytesEqual(t, name+" queue v1==v2", v1, v2)
	}
	for name, st := range map[string]libCueCellSt{
		"none":     {NoDropsTitle: "no drops", NoCuesTitle: "no cues"},
		"both":     {Drops: 2, DropsTitle: "2 drops", Cues: 4, CuesTitle: "4 cues"},
		"escaping": {Drops: 1, DropsTitle: `1 &drop "x"'<>`, NoCuesTitle: `no &cues"'<>`},
		"unicode":  {Drops: 3, DropsTitle: "3 дропа 🎛️", Cues: 1, CuesTitle: "1 кью"},
	} {
		v1, ok := zigui.RenderLibraryCueCell(stateJSON(st))
		if !ok {
			t.Fatalf("%s: v1 cuecell render failed", name)
		}
		v2, ok := zigui.RenderLibraryCueCellV2(wireLibCueCell(st))
		if !ok {
			t.Fatalf("%s: v2 cuecell render failed", name)
		}
		assertBytesEqual(t, name+" cuecell go==v1", libCueCellHTMLOf(st), v1)
		assertBytesEqual(t, name+" cuecell v1==v2", v1, v2)
	}
	assertNoNewFallbacks(t, before)
}

// TestZigWireThreeWayPlayer: nine patch targets over the player fixture set, built through the
// same UI path the bridges use. Several fragments render legitimately EMPTY (no video, no edit
// box), which NULLs both Zig paths - so the fallback assertion is exact rather than "none":
// every empty surface costs one v2 + one v1 downgrade.
func TestZigWireThreeWayPlayer(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	before := zigui.FallbackCounts()
	var wireB, jsonB, empties, checked int
	for name, fx := range mpFixtures() {
		t.Run(name, func(t *testing.T) {
			u := &UI{}
			t.Cleanup(func() { releaseUIState(u) })
			u.mu.Lock()
			u.libSection = "collection"
			u.mu.Unlock()
			*u.mp(fx.host) = fx
			snap := u.mpSnap(fx.host)
			inner := u.mpInnerState(snap)
			full := mpFullSt{Host: snap.host, Inner: inner}

			check := func(what string, doc, js []byte, want string, v1fn, v2fn func([]byte) (string, bool)) {
				if len(doc) == 0 {
					t.Fatalf("%s: wire encode failed", what)
				}
				wireB += len(doc)
				jsonB += len(js)
				v1, ok1 := v1fn(js)
				v2, ok2 := v2fn(doc)
				if ok1 != ok2 {
					t.Fatalf("%s: v1 ok=%v but v2 ok=%v", what, ok1, ok2)
				}
				if !ok1 {
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
	if checked == 0 {
		t.Fatal("no player surface rendered")
	}
	t.Logf("%d surfaces checked, %d legitimately empty: wire %d B vs json %d B (%.1f%%)",
		checked, empties, wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertFallbackDelta(t, before, 2*empties)
}

// TestZigWireThreeWayAutomations: full tab + the version-gated #auto-body tick fragment.
func TestZigWireThreeWayAutomations(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := autoFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireAutoState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderAutomations(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderAutomationsV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", automationsHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			b1, ok := zigui.RenderAutomationsBody(stateJSON(st.Body))
			if !ok {
				t.Fatal("v1 body render failed")
			}
			b2, ok := zigui.RenderAutomationsBodyV2(wireAutoBodyState(st.Body))
			if !ok {
				t.Fatal("v2 body render failed")
			}
			assertBytesEqual(t, "body go==v1", autoBodyHTML(st.Body), b1)
			assertBytesEqual(t, "body v1==v2", b1, b2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// TestZigWireThreeWayPeers: full tab + the ~1 Hz #peers-body tick. Peers carries the only
// []string on the wire (the media plane's sync lines), so this is also the kStrList gate.
func TestZigWireThreeWayPeers(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := peersFixtures()
	var wireB, jsonB, syncLines int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wirePeers(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)
			syncLines += len(st.Body.Media.SyncLines)

			v1, ok := zigui.RenderPeers(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderPeersV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", peersHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			b1, ok := zigui.RenderPeersBody(stateJSON(st.Body))
			if !ok {
				t.Fatal("v1 body render failed")
			}
			b2, ok := zigui.RenderPeersBodyV2(wirePeersBody(st.Body))
			if !ok {
				t.Fatal("v2 body render failed")
			}
			assertBytesEqual(t, "body go==v1", peersBodyHTML(st.Body), b1)
			assertBytesEqual(t, "body v1==v2", b1, b2)
		})
	}
	if syncLines == 0 {
		t.Fatal("no fixture carries media sync lines - the []string path is untested")
	}
	t.Logf("%d fixtures (%d sync lines): wire %d B vs json %d B (%.1f%%)", len(fx), syncLines, wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacks(t, before)
}

// TestWireStrListEdges: an empty []string, an element that is itself empty, and one that needs
// escaping - the cases where a list of scalars can silently lose an element.
func TestWireStrListEdges(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	base := peersFixtures()["populated"]
	seen := map[string]bool{}
	for name, lines := range map[string][]string{
		"nil":      nil,
		"empty":    {},
		"oneEmpty": {""},
		"gaps":     {"clock: leader", "", "tc: 01:02:03"},
		"escaping": {`sync &"<x>"`, "синх ✓"},
	} {
		st := base
		st.Body.Media.SyncLines = lines
		v2, ok := zigui.RenderPeersV2(wirePeers(st))
		if !ok {
			t.Fatalf("%s: v2 render failed", name)
		}
		want := peersHTML(st)
		assertBytesEqual(t, name, want, v2)
		seen[want] = true
	}
	// Inertness guard: if the sync lines never reach the markup, every case above compares the
	// same bytes and proves nothing.
	if len(seen) < 3 {
		t.Fatalf("only %d distinct renders across 5 sync-line shapes - the fixture does not render them", len(seen))
	}
}

// TestZigWireRejectsForeignDocuments pins the header contract: an export must refuse a
// document built for another message or another schema (that is what makes a stale
// libraveui.a a clean v1 downgrade instead of a mis-decode).
func TestZigWireRejectsForeignDocuments(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := agFixtures()["populated"]
	doc := wireAgState(st)

	if _, ok := zigui.RenderLogsV2(doc); ok {
		t.Error("logs export accepted an appgroups document")
	}
	if _, ok := zigui.RenderAppGroupsV2(stateJSON(st)); ok {
		t.Error("v2 export accepted a JSON document")
	}
	cases := map[string]func([]byte){
		"magic":      func(b []byte) { b[0] = 'X' },
		"msgID":      func(b []byte) { binary.LittleEndian.PutUint16(b[4:], 0xBEEF) },
		"schemaHash": func(b []byte) { binary.LittleEndian.PutUint32(b[6:], 0xDEADBEEF) },
		"arenaLen":   func(b []byte) { binary.LittleEndian.PutUint32(b[10:], 0xFFFF) },
	}
	for name, mutate := range cases {
		bad := append([]byte(nil), doc...)
		mutate(bad)
		if _, ok := zigui.RenderAppGroupsV2(bad); ok {
			t.Errorf("%s: mutated header accepted", name)
		}
	}
	for n := 0; n < len(doc); n++ { // every truncation must be refused
		if _, ok := zigui.RenderAppGroupsV2(doc[:n]); ok {
			t.Fatalf("truncation to %d bytes accepted", n)
		}
	}
}

// TestWireEmptyListsAreAbsentNotNull: the JSON path needed `,omitempty` on every nested slice
// because a nil slice marshalled `null` and the Zig parser rejected it (silently dropping a
// whole tab to Go). On the wire an empty list is simply an absent tag, and absent decodes to
// an empty slice - so nil and empty must render identically.
func TestWireEmptyListsAreAbsentNotNull(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	nilSt := agState{Title: "T", Subtitle: "S", Available: true, Empty: "none"} // Groups == nil
	emptySt := nilSt
	emptySt.Groups = []agGroup{}
	nilDoc, emptyDoc := wireAgState(nilSt), wireAgState(emptySt)
	if string(nilDoc) != string(emptyDoc) {
		t.Fatalf("nil and empty slice encode differently (%d vs %d bytes)", len(nilDoc), len(emptyDoc))
	}
	got, ok := zigui.RenderAppGroupsV2(nilDoc)
	if !ok {
		t.Fatal("v2 render of a nil-slice state failed")
	}
	assertBytesEqual(t, "nil slice", appGroupsHTML(nilSt), got)

	// Same one level deeper: a group with no apps, and a zero-value nested struct (logsState's
	// selects) - the JSON-era hazard class, now unrepresentable.
	deep := agState{Title: "T", Available: true, Launch: "Go", Groups: []agGroup{{ID: "g", Name: "n", Up: "0/0", Variant: "muted"}}}
	if h, ok := zigui.RenderAppGroupsV2(wireAgState(deep)); !ok {
		t.Fatal("v2 render of a nil apps slice failed")
	} else {
		assertBytesEqual(t, "nil nested slice", appGroupsHTML(deep), h)
	}
	zero := logsState{Title: "L"} // Level/Source/Lines all zero-value, every slice nil
	if h, ok := zigui.RenderLogsV2(wireLogsState(zero)); !ok {
		t.Fatal("v2 render of an all-zero logs state failed")
	} else {
		assertBytesEqual(t, "zero logs state", logsHTML(zero), h)
	}
}

// assertFallbackDelta fails unless EXACTLY want downgrades were recorded during the test. Used
// where some fragments are legitimately empty (an empty render is a NULL on both Zig paths, and
// the Go renderer reproduces the same ""), so "no fallbacks" would be the wrong assertion and
// "ignore fallbacks" would hide a real one.
func assertFallbackDelta(t *testing.T, before map[string]int, want int) {
	t.Helper()
	got := 0
	for k, v := range zigui.FallbackCounts() {
		if d := v - before[k]; d > 0 {
			got += d
			t.Logf("fallback %s +%d", k, d)
		}
	}
	if got != want {
		t.Errorf("fallbacks recorded = %d, want %d", got, want)
	}
}

// assertNoNewFallbacks fails when a render downgraded (v2→v1 or v1→Go) during the test.
func assertNoNewFallbacks(t *testing.T, before map[string]int) {
	t.Helper()
	for k, v := range zigui.FallbackCounts() {
		if v > before[k] {
			t.Errorf("fallback recorded during golden run: %s +%d", k, v-before[k])
		}
	}
}
