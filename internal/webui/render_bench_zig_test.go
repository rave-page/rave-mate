//go:build zigui

package webui

// Phase-B baseline: Go renderer vs the Zig bridge, per migrated tab, over the EXISTING golden
// fixtures (one representative populated fixture per tab - the goldens themselves cover the
// branchy states). Four families per tab:
//
//	RenderGo      pure Go renderer                     (the fallback + golden reference)
//	RenderZig     Zig renderer only, state pre-marshalled
//	RenderBridge  stateJSON + Zig                      (what a real render costs today)
//	StateMarshal  stateJSON only, reports state bytes  (the phase-B tax to remove)
//
// Run: bash scripts/build-zig.sh && GOWORK=off go test -count=1 -tags zigui ./internal/webui \
//	-run '^$' -bench 'Render|StateMarshal' -benchtime 200x
// Numbers from this machine at this commit live in .devnotes/PHASEB_BASELINE.md.
//
// Untagged counterpart: render_bench_test.go (Go path only - the fixtures above are defined in
// //go:build zigui golden files, so an untagged bench cannot reach them).

import (
	"testing"

	"rave.page/mate/internal/zigui"
)

// zigBenchCase is one tab: the SAME resolved state through both renderers. `st` is `any` at the
// json boundary only (stateJSON's contract); every fixture is a plain struct.
type zigBenchCase struct {
	tab  string
	st   any
	goFn func() string
	zig  func([]byte) (string, bool)
}

// zigBenchCases builds one case per migrated tab. State builds that need a *UI (settings, player)
// run ONCE here, outside the timed loop, exactly like the bridges do per render.
func zigBenchCases(tb testing.TB) []zigBenchCase {
	tb.Helper()
	cases := []zigBenchCase{}

	ag := agFixtures()["populated"]
	cases = append(cases, zigBenchCase{"appgroups", ag, func() string { return appGroupsHTML(ag) }, zigui.RenderAppGroups})

	lg := logsFixtures()["populated"]
	cases = append(cases, zigBenchCase{"logs", lg, func() string { return logsHTML(lg) }, zigui.RenderLogs})

	lv := liveFixtures()["populated"]
	cases = append(cases, zigBenchCase{"live", lv, func() string { return liveHTML(lv) }, zigui.RenderLive})

	mo := moFixtures()["populated"]
	cases = append(cases, zigBenchCase{"motion", mo, func() string { return motionHTML(mo) }, zigui.RenderMotion})

	pe := peersFixtures()["populated"]
	cases = append(cases, zigBenchCase{"peers", pe, func() string { return peersHTML(pe) }, zigui.RenderPeers})

	au := autoFixtures()["populated"]
	cases = append(cases, zigBenchCase{"automations", au, func() string { return automationsHTML(au) }, zigui.RenderAutomations})

	pb := pubFixtures()["tracklist"]
	cases = append(cases, zigBenchCase{"publish", pb, func() string { return publishHTML(pb) }, zigui.RenderPublish})

	// settings: the biggest tab (~40 cards over 7 sections) - state comes off a fixture UI.
	fx := setFixtures()["libmedia"]
	fx.u.setMu.Lock()
	fx.u.setSec, fx.u.setQuery = fx.sec, fx.q
	fx.u.setMu.Unlock()
	set := fx.u.settingsState()
	cases = append(cases, zigBenchCase{"settings", set, func() string { return settingsHTML(set) }, zigui.RenderSettings})

	lb := libFixtures()["populated"]
	cases = append(cases, zigBenchCase{"library", lb, func() string { return libraryHTML(lb) }, zigui.RenderLibrary})

	// player: the 30 fps surface; its waveform SVG stays Go and rides as a raw field.
	pu := &UI{}
	tb.Cleanup(func() { releaseUIState(pu) })
	pu.mu.Lock()
	pu.libSection = "collection"
	pu.mu.Unlock()
	mp := mpFixtures()["singleEdit"]
	*pu.mp(mp.host) = mp
	snap := pu.mpSnap(mp.host)
	full := mpFullSt{Host: snap.host, Inner: pu.mpInnerState(snap)}
	cases = append(cases, zigBenchCase{"player", full, func() string { return mpFullHTMLOf(full) }, zigui.RenderPlayer})

	return cases
}

// zigBenchState marshals + renders once and asserts the Zig path AGREES with Go, so a benchmark
// can never quietly measure a rejected state (a nil slice → JSON null → ok=false → the "Zig"
// number would be a parse failure, orders of magnitude too fast).
func zigBenchState(tb testing.TB, c zigBenchCase) []byte {
	tb.Helper()
	js := stateJSON(c.st)
	if js == nil {
		tb.Fatalf("%s: state marshal failed", c.tab)
	}
	got, ok := c.zig(js)
	if !ok {
		tb.Fatalf("%s: zig render failed (ok=false) - bench would measure a fallback", c.tab)
	}
	if want := c.goFn(); want != got {
		tb.Fatalf("%s: zig != go (%d vs %d bytes) - fix the golden before trusting numbers",
			c.tab, len(got), len(want))
	}
	return js
}

func benchSkipUnavailable(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable / ABI mismatch - run `bash scripts/build-zig.sh` first")
	}
}

// BenchmarkRenderGo: the pure Go renderer (state → HTML), no bridge.
func BenchmarkRenderGo(b *testing.B) {
	for _, c := range zigBenchCases(b) {
		b.Run(c.tab, func(b *testing.B) {
			out := c.goFn()
			b.SetBytes(int64(len(out)))
			b.ResetTimer()
			for range b.N {
				sink(c.goFn())
			}
			// ReportMetric AFTER the loop - ResetTimer clears extra metrics (SetBytes survives).
			b.ReportMetric(float64(len(out)), "html_B")
		})
	}
}

// BenchmarkRenderZig: the Zig renderer alone (state already marshalled) - the honest
// renderer-vs-renderer comparison against BenchmarkRenderGo.
func BenchmarkRenderZig(b *testing.B) {
	benchSkipUnavailable(b)
	for _, c := range zigBenchCases(b) {
		b.Run(c.tab, func(b *testing.B) {
			js := zigBenchState(b, c)
			out, _ := c.zig(js)
			b.SetBytes(int64(len(out)))
			b.ResetTimer()
			for range b.N {
				h, ok := c.zig(js)
				if !ok {
					b.Fatal("zig render failed mid-bench")
				}
				sink(h)
			}
			b.ReportMetric(float64(len(out)), "html_B")
		})
	}
}

// BenchmarkRenderBridge: marshal + Zig = what the app actually pays per render today. The gap to
// BenchmarkRenderZig is the phase-A round trip phase B removes.
func BenchmarkRenderBridge(b *testing.B) {
	benchSkipUnavailable(b)
	for _, c := range zigBenchCases(b) {
		b.Run(c.tab, func(b *testing.B) {
			zigBenchState(b, c) // parity gate before timing
			b.ResetTimer()
			for range b.N {
				h, ok := c.zig(stateJSON(c.st))
				if !ok {
					b.Fatal("zig render failed mid-bench")
				}
				sink(h)
			}
		})
	}
}

// BenchmarkStateMarshal: the marshal half on its own; state_B is the JSON size crossing the ABI.
func BenchmarkStateMarshal(b *testing.B) {
	for _, c := range zigBenchCases(b) {
		b.Run(c.tab, func(b *testing.B) {
			js := stateJSON(c.st)
			if js == nil {
				b.Fatalf("%s: state marshal failed", c.tab)
			}
			b.SetBytes(int64(len(js)))
			b.ResetTimer()
			for range b.N {
				sinkB(stateJSON(c.st))
			}
			b.ReportMetric(float64(len(js)), "state_B")
		})
	}
}
