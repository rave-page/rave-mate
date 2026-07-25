//go:build zigui

package webui

import (
	"sort"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/ui"
	"rave.page/mate/internal/zigui"
)

// B3 fragment-scheduler parity gate: a scripted sequence of state mutations must produce the
// IDENTICAL ordered set of __patch calls through the scheduler (one call per tick, dedup in Zig)
// and through the legacy per-fragment path (one call per fragment, dedup in Go). Both are driven
// from the SAME state, so a difference is a real behavioural difference - and because a __patch
// call embeds jsQuote(html), equality also proves Zig-vs-Go byte parity for every fragment.
// Run: make zig && GOWORK=off go test -count=1 -tags zigui ./internal/webui -run TestTickSched

// newTickTestUI builds a UI with a shell (so the eval queue accepts entries) but NO eval flusher,
// so the queue can be drained deterministically per step.
func newTickTestUI(t *testing.T) *UI {
	t.Helper()
	u := &UI{svc: ui.Services{Cfg: &config.Config{}}, active: "live", started: time.Now(),
		stop: make(chan struct{}), logBus: "app", logLevel: "all", logAutoscroll: true,
		evalKick: make(chan struct{}, 1)}
	u.shell = newVirtualShell(nil, func(string) {}, func(string) {})
	t.Cleanup(func() { u.shell.terminate(); releaseUIState(u) })
	return u
}

// tickScript is the mutation sequence both paths replay: no-op repeats, whole sections
// appearing/disappearing, a single fragment changing, and the branchy golden fixtures.
func tickScript(t *testing.T) []struct {
	name string
	st   liveTickSt
} {
	t.Helper()
	fx := liveFixtures()
	get := func(n string) liveState {
		st, ok := fx[n]
		if !ok {
			t.Fatalf("fixture %q missing", n)
		}
		return st
	}
	var steps []struct {
		name string
		st   liveTickSt
	}
	add := func(name string, st liveState, tc string) {
		steps = append(steps, struct {
			name string
			st   liveTickSt
		}{name, liveTickSt{Live: st, TC: tc}})
	}
	add("unavailable", get("unavailable"), "00:00:00:00")
	add("unavailable-again", get("unavailable"), "00:00:00:00") // no-op tick: both emit nothing
	add("empty", get("empty"), "00:00:00:00")                   // every section appears
	add("empty-tc-only", get("empty"), "00:00:00:01")           // exactly one fragment changes
	add("populated", get("populated"), "01:23:45:12")

	oneDeck := get("populated")
	decks := append([]liveDeck(nil), oneDeck.Decks.Decks...)
	decks[0].Title = "changed - only this deck"
	oneDeck.Decks = liveDecksSt{Note: oneDeck.Decks.Note, Decks: decks}
	add("populated-one-deck", oneDeck, "01:23:45:12")

	add("escaping", get("escaping"), `01:23:45:12 &<">'`)
	add("long", get("long"), "01:23:45:12")
	add("unicode", get("unicode"), "01:23:45:12 ◼")
	add("back-to-unavailable", get("unavailable"), "00:00:00:00") // sections disappear

	// tip2's structured section tooltips ride the SAME LiveState the tick document carries (the
	// pilot's own Tk* mirrors were deleted when wave B-2 merged). The fragments do not render
	// section headers, so this step must be parity-identical - but it proves the envelope decodes
	// a state with the tooltip pointers set instead of refusing it. Representability is asserted
	// separately in TestTickSchedLiveCarriesTooltips.
	tips := get("populated")
	tp := tipTopicSt("cue-edit")
	tips.HasSignals, tips.HasNet, tips.HasPerf = true, true, true
	tips.SignalsTipS, tips.NetTipS, tips.TimTipS, tips.PerfTipS = tp, tp, tp, tp
	add("tips", tips, "01:23:45:12")
	return steps
}

func TestTickSchedLiveMatchesLegacy(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	uNew, uOld := newTickTestUI(t), newTickTestUI(t)
	var schedCalls, legacyCalls int
	for _, step := range tickScript(t) {
		var newJS, oldJS strings.Builder
		if !uNew.tickLiveSched(&newJS, step.st) {
			t.Fatalf("%s: scheduler declined", step.name)
		}
		uOld.liveTickLegacy(&oldJS, step.st)
		if newJS.String() != oldJS.String() {
			t.Fatalf("%s: __patch call stream differs\nsched : %s\nlegacy: %s",
				step.name, trimForLog(newJS.String()), trimForLog(oldJS.String()))
		}
		// queue level: same coalescing keys, same order, same payloads
		newQ, oldQ := pendKeys(uNew, &newJS), pendKeys(uOld, &oldJS)
		if strings.Join(newQ, ",") != strings.Join(oldQ, ",") {
			t.Fatalf("%s: enqueued ids differ: %v vs %v", step.name, newQ, oldQ)
		}
		uNew.flushTick(&newJS)
		uOld.flushTick(&oldJS)
		if a, b := uNew.drainEvals(), uOld.drainEvals(); a != b {
			t.Fatalf("%s: drained eval batch differs (%d vs %d bytes)", step.name, len(a), len(b))
		}
		schedCalls++
		legacyCalls += len(oldQ)
		t.Logf("%-20s %d fragment(s) patched", step.name, len(newQ))
	}
	t.Logf("%d ticks: %d scheduler ABI calls vs %d legacy per-fragment calls", schedCalls, schedCalls, legacyCalls)
	// Narrowed per wave B-2: name the exports this test drives (the scheduler + the legacy path's
	// per-fragment dispatch), so an unrelated tab's legitimate empty-fragment fallback can't mask
	// a downgrade here - and a typo'd key can't make the assertion vacuous.
	assertNoNewFallbacksIn(t, before, "TickLive", "RenderLiveFragV2", "RenderLiveFrag")
}

// pendKeys returns the fragment ids tickPatch/recordPatch queued for this batch, in order.
func pendKeys(u *UI, js *strings.Builder) []string {
	u.fragMu.Lock()
	defer u.fragMu.Unlock()
	out := make([]string, 0, len(u.tickPend[js]))
	for _, e := range u.tickPend[js] {
		out = append(out, e.key)
	}
	return out
}

func trimForLog(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// TestTickSchedLiveResendsAfterPatchMain pins the patchMain contract: the DOM was replaced, so the
// dedup cache is dropped and the NEXT tick must resend every fragment - the scheduler's hash cache
// included (an in-flight batch built before the drop is discarded, not applied).
func TestTickSchedLiveResendsAfterPatchMain(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	u := newTickTestUI(t)
	st := liveTickSt{Live: liveFixtures()["populated"], TC: "01:23:45:12"}

	var first strings.Builder
	if !u.tickLiveSched(&first, st) {
		t.Fatal("first tick declined")
	}
	n1 := len(pendKeys(u, &first))
	u.flushTick(&first)
	_ = u.drainEvals()

	var second strings.Builder
	if !u.tickLiveSched(&second, st) {
		t.Fatal("second tick declined")
	}
	if got := len(pendKeys(u, &second)); got != 0 {
		t.Fatalf("unchanged tick patched %d fragments, want 0", got)
	}
	u.flushTick(&second)
	_ = u.drainEvals()

	u.patchMain() // DOM replaced
	var third strings.Builder
	if !u.tickLiveSched(&third, st) {
		t.Fatal("post-patchMain tick declined")
	}
	if got := len(pendKeys(u, &third)); got != n1 {
		t.Fatalf("after patchMain the tick patched %d fragments, want all %d", got, n1)
	}
	u.flushTick(&third)
	_ = u.drainEvals()

	// a cache drop DURING a tick must void the batch, never apply it half-deduped
	gen := u.fragGen
	if !u.commitFrags(gen, []zigui.Frag{{ID: "live-np", Hash: 1}}) {
		t.Fatal("commit with the current generation was rejected")
	}
	if u.commitFrags(gen-1, []zigui.Frag{{ID: "live-np", Hash: 2}}) {
		t.Fatal("commit from a stale generation was accepted")
	}
	if u.fragH["live-np"] != 1 {
		t.Fatalf("stale commit overwrote the hash cache: %d", u.fragH["live-np"])
	}
}

// TestTickSchedLogsDedupsIdenticalTail is the #log-view gate. The legacy path swapped the full
// tail on every ring advance; the scheduler suppresses a swap whose bytes are unchanged. The
// sequences must therefore be equal after removing the legacy path's consecutive identical
// repeats - i.e. exactly tickPatch's dedup rule, applied to a surface that never had it.
func TestTickSchedLogsDedupsIdenticalTail(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	before := zigui.FallbackCounts()
	fx := logsFixtures()
	names := make([]string, 0, len(fx))
	for n := range fx {
		names = append(names, n)
	}
	sort.Strings(names) // map order would make a failure unreproducible

	// script: every fixture, each repeated once (the repeat is the suppression case)
	type step struct {
		name  string
		lines logsLines
	}
	var steps []step
	for _, n := range names {
		steps = append(steps, step{n, fx[n].Lines}, step{n + "-again", fx[n].Lines})
	}

	uNew, uOld := newTickTestUI(t), newTickTestUI(t)
	var lastOld string
	var swaps, suppressed int
	for _, s := range steps {
		uOld.evalLogView(logsLinesHTML(s.lines)) // legacy: always swaps
		wantOld := uOld.drainEvals()
		if wantOld == "" {
			t.Fatalf("%s: legacy path emitted nothing", s.name)
		}
		if !uNew.tickLogsSchedFrom(logsTickSt{Lines: s.lines}) {
			t.Fatalf("%s: scheduler declined", s.name)
		}
		gotNew := uNew.drainEvals()
		if wantOld == lastOld { // identical bytes → the scheduler must suppress the swap
			if gotNew != "" {
				t.Fatalf("%s: identical tail re-swapped (%d bytes)", s.name, len(gotNew))
			}
			suppressed++
			continue
		}
		if gotNew != wantOld {
			t.Fatalf("%s: swap differs from the legacy path (%d vs %d bytes)", s.name, len(gotNew), len(wantOld))
		}
		swaps++
		lastOld = wantOld
	}
	if suppressed == 0 || swaps == 0 {
		t.Fatalf("script exercised neither branch (swaps %d, suppressed %d)", swaps, suppressed)
	}
	t.Logf("%d steps: %d swaps, %d suppressed identical tails", len(steps), swaps, suppressed)
	assertNoNewFallbacksIn(t, before, "TickLogs")
}

// TestTickSchedRefusesForeignDocument pins the header contract: a document meant for another
// message (or a corrupted one) is refused, so the caller falls back instead of mis-decoding.
func TestTickSchedRefusesForeignDocument(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	st := liveTickSt{Live: liveFixtures()["populated"]}
	doc := wireTkLive(st)
	if _, ok := zigui.TickLive(doc); !ok {
		t.Fatal("valid document refused")
	}
	if frs, ok := zigui.TickLogs(doc); ok {
		t.Fatalf("logs export accepted a live document (%d fragments)", len(frs))
	}
	if _, ok := zigui.TickLive(doc[:len(doc)-1]); ok {
		t.Fatal("truncated document accepted")
	}
	if _, ok := zigui.TickLive(nil); ok {
		t.Fatal("empty document accepted")
	}
}

// TestTickSchedLiveCarriesTooltips is this layer's guard against the silent-drop class that bit
// wave B-2 twice: a Go state gains a field, the wire does not carry it, and every gate stays green
// because the fixtures leave it nil. The tick envelope embeds wave B-2's LiveState message, so
// tip2's four structured section tooltips must be ON the wire - proven by the document GROWING when
// they are set (a dropped field encodes to the same bytes) and by the batch still being accepted and
// byte-identical to the Go renderers.
func TestTickSchedLiveCarriesTooltips(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch")
	}
	before := zigui.FallbackCounts()
	base := liveFixtures()["populated"]
	base.HasSignals, base.HasNet, base.HasPerf = true, true, true
	bare := liveTickSt{Live: base, TC: "01:23:45:12"}

	for _, v := range tipVariants() {
		if v.st == nil {
			continue
		}
		withTip := bare
		withTip.Live.SignalsTipS, withTip.Live.NetTipS = v.st, v.st
		withTip.Live.TimTipS, withTip.Live.PerfTipS = v.st, v.st
		t.Run(v.name, func(t *testing.T) {
			nBare, nTip := len(wireTkLive(bare)), len(wireTkLive(withTip))
			if nBare == 0 || nTip == 0 {
				t.Fatal("wire encode failed")
			}
			if nTip <= nBare {
				t.Fatalf("tooltip did not reach the tick document: %d B with, %d B without", nTip, nBare)
			}
			// and the document is still accepted + parity-identical (tips live on section headers,
			// which the tick never patches - so the fragment stream must be unchanged)
			got, ok := zigui.TickLive(wireTkLive(withTip))
			if !ok {
				t.Fatal("scheduler declined a tooltip-carrying document")
			}
			want := legacyLiveFrags(withTip, false)
			if len(got) != len(want) {
				t.Fatalf("%d fragments vs %d from the Go path", len(got), len(want))
			}
			for i := range got {
				if got[i].ID != want[i].ID || got[i].HTML != want[i].HTML {
					t.Fatalf("fragment %d (%s): zig != go", i, got[i].ID)
				}
			}
		})
	}
	assertNoNewFallbacksIn(t, before, "TickLive")
}
