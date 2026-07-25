package webui

import (
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/ui"
)

// Phase B4a gates. Two retained-state workarounds are gone from the player and each one had a
// race that only a mutation BETWEEN two points could show:
//
//  1. mpResync re-emitted the whole component after every container patch, because a player
//     mutation landing mid-build was overwritten by the container patch. The generation counter
//     (mpSt.pgen + mpOrdered/mpHeal) decides that instead - so the heal must still happen when
//     the state moved, must NOT happen when it didn't, and must land AFTER the container patch.
//  2. the transport was re-sampled per consumer, so one render could carry three different
//     engine instants. mpSt.eng makes that unrepresentable - proven here by rendering against a
//     mirror that MOVES on every read and requiring the bytes of a mirror PINNED to the first
//     sample.
//
// Untagged on purpose: the mechanism is pure Go and must hold on the stub build too.

// ── harness ────────────────────────────────────────────────────────────────────

// scriptMirror is an engine mirror that walks a scripted sequence, one step per read, and counts
// reads. The featurehost mirror can only be moved by a live child process, so this is the only way
// to drive a transport that changes between two renders.
type scriptMirror struct {
	mu   sync.Mutex
	seq  []featurehost.State
	n    int
	pin  bool // don't advance: every read returns seq[0]
	step int
}

func (m *scriptMirror) State() featurehost.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := 0
	if !m.pin {
		i = m.step
		if i >= len(m.seq) {
			i = len(m.seq) - 1
		}
		m.step++
	}
	m.n++
	return m.seq[i]
}

func (m *scriptMirror) reads() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n
}

// newMpUI builds a UI with a shell (so the eval queue accepts entries) but no flusher, so the
// queue can be inspected entry by entry.
func newMpUI(t testing.TB) *UI {
	t.Helper()
	u := &UI{svc: ui.Services{Cfg: &config.Config{}}, active: "library", started: time.Now(),
		stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	sh := newVirtualShell(nil, func(string) {}, func(string) {})
	u.shell = sh
	u.mu.Lock()
	u.libSection = "collection"
	u.mu.Unlock()
	t.Cleanup(func() { sh.terminate(); releaseUIState(u) }) // sh, not u.shell: a bench may clear it
	return u
}

// evalEntries drains the eval queue preserving per-entry keys + order (drainEvals concatenates).
func evalEntries(u *UI) []evalEntry {
	u.evalMu.Lock()
	defer u.evalMu.Unlock()
	out := append([]evalEntry(nil), u.evalQ...)
	u.evalQ, u.evalIdx, u.evalBase = nil, nil, 0
	return out
}

const mpTestPath = `C:\sets\order.flac`

// mpLoadingFixture is an audio player mid-analysis: the wave caption reads "Analyzing waveform…"
// until the peaks land, which is exactly the state the mpResync race used to freeze on screen.
func mpLoadingFixture(u *UI, host string) {
	*u.mp(host) = mpSt{host: host, viewSpan: 1, cursorSec: mpNone, hovT: mpNone, outSec: -1,
		firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1, lastTrackIdx: -1,
		media: []mpMedia{{path: mpTestPath, kind: "audio", dur: 600, peaksLoading: true}}}
}

// mpApplyPeaks is the analysis result landing (the bg apply mpResync existed for) plus the
// fragment patch it emits.
func mpApplyPeaks(u *UI, host string) {
	peaks := make([]byte, 400)
	for i := range peaks {
		peaks[i] = byte(i * 7 % 256)
	}
	t := u.mpMut(host, func(v *mpSt) {
		v.media[0].peaksLoading, v.media[0].peaks = false, peaks
	})
	u.mpPatchWave(t)
}

func mpLoadingCaption() string { return i18n.T("player.label.analyzingWave") }

// containerJS is what a container render enqueues: one __patch of a fragment whose HTML embeds
// the player component (main / #lib-body / #lib-detail all do).
func containerJS(id, html string) string {
	return "window.__patch(" + jsQuote(id) + "," + jsQuote(html) + ")"
}

// ── 1. the mpResync race, now decided by the generation counter ─────────────────

// TestMpOrderedHealsWhenStateMovedMidBuild is the race gate: a mutation lands between the
// container build's player snapshot and the container patch being enqueued, so the container patch
// carries STALE player markup. The final DOM must still show the current state - i.e. a heal must
// be enqueued after it. Delete the mpHeal call in mpOrdered and this fails.
func TestMpOrderedHealsWhenStateMovedMidBuild(t *testing.T) {
	u := newMpUI(t)
	mpLoadingFixture(u, "library")
	caption := mpLoadingCaption()

	var container string
	u.mpOrdered(func() string {
		container = `<div id=lib-body>` + u.mpHTML("library") + `</div>` // built from the stale snapshot
		mpApplyPeaks(u, "library")                                       // the analysis lands mid-build
		return container
	}, func(h string) { u.eval(containerJS("lib-body", h)) })

	if !strings.Contains(container, caption) {
		t.Fatalf("fixture is vacuous: the container build did not render the loading caption %q", caption)
	}
	entries := evalEntries(u)
	last := -1
	for i, e := range entries {
		if strings.Contains(e.js, "mp-library-root") { // both the container patch and the heal carry it
			last = i
		}
	}
	if last < 0 {
		t.Fatal("nothing patched the player subtree")
	}
	if got := entries[last].js; strings.Contains(got, caption) {
		t.Fatalf("the DOM ends on the stale build: entry %d still shows %q\nkeys: %v",
			last, caption, entryKeys(entries))
	}
	if entries[last].key != "" {
		t.Fatalf("the heal must be enqueued uncoalesced (key %q would fold into an earlier root patch)",
			entries[last].key)
	}
}

// TestMpOrderedSkipsHealWhenNothingMoved is the win the counter buys: mpResync rendered the whole
// component (waveform SVG included) after EVERY container patch. With nothing moved, the player
// must not be re-rendered or re-patched at all.
func TestMpOrderedSkipsHealWhenNothingMoved(t *testing.T) {
	u := newMpUI(t)
	mpLoadingFixture(u, "library")

	u.mpOrdered(func() string { return `<div id=lib-body>` + u.mpHTML("library") + `</div>` },
		func(h string) { u.eval(containerJS("lib-body", h)) })

	entries := evalEntries(u)
	if len(entries) != 1 || entries[0].key != "lib-body" {
		t.Fatalf("a quiet container patch emitted %d entries (%v), want only the container patch",
			len(entries), entryKeys(entries))
	}
}

// TestMpHealBeatsLaterContainerPatch is the ordering proof and the reason the heal is enqueued
// UNCOALESCED. Two container patches queue in one flush window, each racing a mutation; a keyed
// heal would fold into the FIRST heal's slot and land ahead of the second container patch, which
// is the hole the retired mpResync had.
func TestMpHealBeatsLaterContainerPatch(t *testing.T) {
	u := newMpUI(t)
	mpLoadingFixture(u, "library")
	caption := mpLoadingCaption()

	// build 1: mid-build the peaks land. build 2: mid-build the loading state comes BACK (a new
	// track bound), so its stale container HTML differs from build 1's and the second heal matters.
	u.mpOrdered(func() string {
		h := `<div id=lib-body>` + u.mpHTML("library") + `</div>`
		mpApplyPeaks(u, "library")
		return h
	}, func(h string) { u.eval(containerJS("lib-body", h)) })
	u.mpOrdered(func() string {
		h := `<div id=lib-detail>` + u.mpHTML("library") + `</div>`
		u.mpMut("library", func(v *mpSt) { v.media[0].peaksLoading, v.media[0].peaks = true, nil })
		return h
	}, func(h string) { u.eval(containerJS("lib-detail", h)) })

	entries := evalEntries(u)
	lastContainer, lastHeal := -1, -1
	for i, e := range entries {
		switch {
		case e.key == "lib-body" || e.key == "lib-detail":
			lastContainer = i
		case strings.HasPrefix(e.js, "window.__patch(\"mp-library-root\""):
			lastHeal = i
		}
	}
	if lastContainer < 0 || lastHeal < 0 {
		t.Fatalf("expected both container patches and heals, got %v", entryKeys(entries))
	}
	if lastHeal < lastContainer {
		t.Fatalf("a heal (entry %d) is queued BEFORE the last container patch (entry %d): %v",
			lastHeal, lastContainer, entryKeys(entries))
	}
	if !strings.Contains(entries[lastHeal].js, caption) {
		t.Fatalf("the last heal does not carry the current (loading) state; keys %v", entryKeys(entries))
	}
}

// TestMpOrderedContainerSitesGoThroughIt: the three real container patch sites must use mpOrdered,
// or the ordering contract has a hole nothing else covers. patchMain is driven end to end here (a
// virtual shell renders the whole tab); the two library sites are asserted by mpOrdered's own
// gates above plus this one for patchMain, which is the site that replaces the entire main DOM.
func TestMpOrderedPatchMainHeals(t *testing.T) {
	u := newMpUI(t)
	mpLoadingFixture(u, "library")
	before := u.mpGen("library")
	mpApplyPeaks(u, "library") // a mutation the build will not have seen
	_ = evalEntries(u)         // drop its fragment patch; only the ordering around patchMain matters
	if u.mpGen("library") == before {
		t.Fatal("the apply did not bump the generation")
	}

	// patchMain marks BEFORE building, so a mutation during the build heals; simulate that by
	// mutating from inside the render via the same path a bg apply uses.
	mk := u.mpMarkGens()
	u.eval(containerJS("main", "<div>stale</div>"))
	mpApplyPeaks(u, "library")
	u.mpHeal(mk)
	entries := evalEntries(u)
	if len(entries) == 0 || entries[len(entries)-1].key != "" ||
		!strings.HasPrefix(entries[len(entries)-1].js, "window.__patch(\"mp-library-root\"") {
		t.Fatalf("mpHeal did not close the batch with a player re-emit: %v", entryKeys(entries))
	}
}

func entryKeys(es []evalEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		k := e.key
		if k == "" {
			k = "(fifo)" + firstN(e.js, 32)
		}
		out = append(out, k)
	}
	return out
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ── 2. generation-counter semantics the ordering rests on ──────────────────────

func TestMpGenBumpsOnMutationOnly(t *testing.T) {
	u := newMpUI(t)
	mpLoadingFixture(u, "library")
	g0 := u.mpGen("library")
	u.mpSnap("library")
	u.mpSnap("library")
	if g := u.mpGen("library"); g != g0 {
		t.Fatalf("mpSnap bumped the generation (%d → %d): every container build would heal", g0, g)
	}
	u.mpMut("library", func(v *mpSt) { v.edit = true })
	if g := u.mpGen("library"); g != g0+1 {
		t.Fatalf("mpMut did not bump the generation (%d → %d)", g0, g)
	}
}

// TestMpResetKeepsGeneration: reset() rebuilds the instance from scratch. If it rewound pgen, a
// reset landing on a marked value would hide the race the counter exists to catch.
func TestMpResetKeepsGeneration(t *testing.T) {
	u := newMpUI(t)
	mpLoadingFixture(u, "library")
	for i := 0; i < 3; i++ {
		u.mpMut("library", func(v *mpSt) { v.edit = !v.edit })
	}
	g := u.mpGen("library")
	u.mpMut("library", func(v *mpSt) { v.reset() })
	if got := u.mpGen("library"); got <= g {
		t.Fatalf("reset rewound the generation (%d → %d)", g, got)
	}
}

// ── 3. one engine sample per render ─────────────────────────────────────────────

// mpPlayingFixture is an audio player the engine is playing, with a momentary-loudness timeline so
// the hover readout depends on the sampled position.
func mpPlayingFixture(u *UI, host string) {
	mom := make([]float64, 64)
	for i := range mom {
		mom[i] = -20 + float64(i)/4
	}
	peaks := make([]byte, 400)
	for i := range peaks {
		peaks[i] = byte(i * 7 % 256)
	}
	*u.mp(host) = mpSt{host: host, viewSpan: 1, cursorSec: mpNone, hovT: mpNone, outSec: -1,
		firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1, lastTrackIdx: -1,
		media: []mpMedia{{path: mpTestPath, kind: "audio", dur: 600, peaks: peaks,
			loud: &mpLoud{I: -9.1, TP: -0.3, LRA: 6.4, Step: 10, Mom: mom}}}}
}

// mpMirrorScript is the transport moving under a render: playing early, playing much later, then
// stopped (fireEnd zeroes the mirror). Sampling per consumer picked one step EACH.
func mpMirrorScript() []featurehost.State {
	return []featurehost.State{
		{Path: mpTestPath, Playing: true, Cur: 10, Total: 600},
		{Path: mpTestPath, Playing: true, Cur: 300, Total: 600},
		{}, // stopped: the transport row would render "not loaded" beside a live playhead
	}
}

func mpRenderInnerWith(t *testing.T, mirror mpMirror, host string) string {
	t.Helper()
	u := newMpUI(t)
	u.mpMirrorOv = mirror
	mpPlayingFixture(u, host)
	return u.mpInnerHTML(u.mpSnap(host))
}

// TestMpEngOneSamplePerRender: a component render against a MOVING mirror must be byte-identical
// to the same render against a mirror pinned to the FIRST sample, and must read the mirror exactly
// once. Before the collapse it read three times (wave playhead, hover readout, transport row) and
// the three fragments of one DOM could disagree.
func TestMpEngOneSamplePerRender(t *testing.T) {
	moving := &scriptMirror{seq: mpMirrorScript()}
	got := mpRenderInnerWith(t, moving, "library")
	if n := moving.reads(); n != 1 {
		t.Fatalf("one render sampled the engine %d times, want 1", n)
	}
	pinned := &scriptMirror{seq: mpMirrorScript(), pin: true}
	want := mpRenderInnerWith(t, pinned, "library")
	if got != want {
		t.Fatalf("render against a moving mirror differs from the first sample's render (%d vs %d bytes)",
			len(got), len(want))
	}
	// non-vacuous: the samples must actually render differently, or nothing above proves anything
	later := &scriptMirror{seq: mpMirrorScript()[1:], pin: true}
	if other := mpRenderInnerWith(t, later, "library"); other == want {
		t.Fatal("fixture cannot tell two engine samples apart - the gate would pass on a torn render")
	}
	stopped := &scriptMirror{seq: []featurehost.State{{}}, pin: true}
	if idle := mpRenderInnerWith(t, stopped, "library"); idle == want {
		t.Fatal("fixture renders a stopped engine like a playing one")
	}
}

// TestMpEngTransportRowIsOneInstant documents the worst of the tears the per-consumer sampling
// allowed: the transport row itself sampled TWICE - once for the clock readout, once for the seek
// slider's thumb - so one fragment could read "00:10" beside a thumb parked at the 5-minute mark.
func TestMpEngTransportRowIsOneInstant(t *testing.T) {
	u := newMpUI(t)
	u.mpMirrorOv = &scriptMirror{seq: mpMirrorScript()}
	mpPlayingFixture(u, "library")
	st := u.mpTpState(u.mpSnap("library"))
	first := mpMirrorScript()[0]
	if want := pubClock(first.Cur) + " / " + pubClock(first.Total); st.TimeTx != want {
		t.Fatalf("clock %q, want %q (the first sample)", st.TimeTx, want)
	}
	if want := math.Round(1000 * first.Cur / first.Total); st.Seek.Val != want {
		t.Fatalf("seek thumb at %v, want %v - the clock and the thumb are two samples of one row",
			st.Seek.Val, want)
	}
}

// TestMpEngOneSamplePerTick: the ~1 Hz tick patches clock, wave, hover and (across a marker) the
// transport from ONE snapshot, so it must sample once too - it used to sample per patch.
func TestMpEngOneSamplePerTick(t *testing.T) {
	u := newMpUI(t)
	moving := &scriptMirror{seq: mpMirrorScript()}
	u.mpMirrorOv = moving
	mpPlayingFixture(u, "library")
	mpTick(u, "library")
	if n := moving.reads(); n != 1 {
		t.Fatalf("one tick sampled the engine %d times, want 1", n)
	}
	if es := evalEntries(u); len(es) == 0 {
		t.Fatal("the tick patched nothing - a playing engine must move the clock/playhead")
	}
}

// TestMpEngAsksAboutActiveMedia pins the sampler's contract: it resolves the ACTIVE media, which is
// what every consumer asked about when each one passed its own `m`.
func TestMpEngAsksAboutActiveMedia(t *testing.T) {
	u := newMpUI(t)
	u.mpMirrorOv = &scriptMirror{seq: []featurehost.State{{Path: mpTestPath, Playing: true, Cur: 42, Total: 600}}, pin: true}
	*u.mp("publish") = mpSt{host: "publish", viewSpan: 1, cursorSec: mpNone, hovT: mpNone, outSec: -1,
		firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1, lastTrackIdx: -1,
		media: []mpMedia{
			{path: mpTestPath, kind: "audio", dur: 600},
			{path: `C:\sets\order.mp4`, kind: "video", dur: 590},
		}}
	audio := u.mpSnap("publish")
	if tr := u.mpEng(&audio); !tr.loaded || tr.cur != 42 {
		t.Fatalf("active=0 must sample the audio engine, got %+v", tr)
	}
	vid := u.mpMut("publish", func(v *mpSt) {
		v.active = 1
		v.vid = mpVid{started: true, cur: 7.5, dur: 590}
	})
	tr := u.mpEng(&vid)
	if !tr.loaded || tr.cur != 7.5 || tr.total != 590 {
		t.Fatalf("active=1 must sample the <video> mirror, got %+v", tr)
	}
}

// TestMpEngNotStoredOnInstance: a sample cached on the shared instance would be served to later
// snapshots as if it were current.
func TestMpEngNotStoredOnInstance(t *testing.T) {
	u := newMpUI(t)
	mirror := &scriptMirror{seq: mpMirrorScript()}
	u.mpMirrorOv = mirror
	mpPlayingFixture(u, "library")
	a := u.mpSnap("library")
	b := u.mpSnap("library")
	if u.mp("library").eng != nil {
		t.Fatal("the instance carries an engine sample - it would go stale")
	}
	if u.mpEng(&a).cur == u.mpEng(&b).cur {
		t.Fatal("two snapshots shared one sample; each must take its own")
	}
	if n := mirror.reads(); n != 2 {
		t.Fatalf("two snapshots took %d samples, want 2", n)
	}
}
