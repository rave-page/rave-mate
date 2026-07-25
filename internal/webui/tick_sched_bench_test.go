//go:build zigui

package webui

import (
	"testing"

	"rave.page/mate/internal/zigui"
)

// B0 left a gap: "fragment renderers are not benched yet - the ~1 Hz tick patches them far more
// often than a full tab is rendered". This closes it for the two B3 pilot surfaces and measures
// what a TICK costs end to end, which is the only number that matters here:
//
//	legacy_zig   the pre-B3 path: per fragment stateJSON + one cgo render call + Go-side dedup
//	legacy_go    the same per-fragment path with the Go renderers (stub-build / fallback cost)
//	sched_all    B3 with no prev hashes: one encode, one cgo call, every fragment comes back
//	sched_same   B3 in steady state: one encode, one cgo call, NOTHING comes back (the common
//	             case - most fragments are byte-identical from second to second)
//	encode_*     the serialization halves alone (what the wire replaces)
//
// Run: GOWORK=off go test -count=1 -tags zigui ./internal/webui -run '^$' -bench TickBench -benchmem

// tickBenchLive is the realistic Live tick: every optional service wired, every fragment present.
func tickBenchLive() liveTickSt {
	return liveTickSt{Live: liveFixtures()["populated"], TC: "01:23:45:12"}
}

// legacyLiveFrags renders the tick's fragments the pre-B3 way and returns them in patch order.
// zig=false uses the Go renderers only (no ABI crossing).
func legacyLiveFrags(st liveTickSt, zig bool) []zigui.Frag {
	out := make([]zigui.Frag, 0, len(liveTickIDs))
	add := func(id, html string) { out = append(out, zigui.Frag{ID: id, HTML: html}) }
	// post-B-2 the per-fragment path is a BINARY dispatch (RenderLiveFragV2), which is the honest
	// baseline for B3: one document + one cgo call per fragment vs one for the whole surface.
	frag := func(id, kind string, doc []byte, goHTML string) {
		if zig {
			if h, ok := zigui.RenderLiveFragV2(kind, doc); ok {
				add(id, h)
				return
			}
		}
		add(id, goHTML)
	}
	l := st.Live
	add("live-tc", htmlEscape(st.TC))
	if l.Transport.HasRec {
		add("live-rec-state", htmlEscape(l.Transport.RecState))
	}
	frag("live-np", "np", wireLiveNP(l.NP), liveNPHTML(l.NP))
	frag("live-status", "status", wireLiveStatus(l.Status), liveStatusFragHTML(l.Status))
	frag("live-decks", "decks", wireLiveDecks(l.Decks), liveDecksFragHTML(l.Decks))
	if l.HasSignals {
		frag("live-signals", "signals", wireLiveSignals(l.Signals), liveSignalsFragHTML(l.Signals))
	}
	if l.HasCockpit {
		frag("live-cockpit", "cockpit", wireLiveCockpit(l.Cockpit), liveCockpitFragHTML(l.Cockpit))
	}
	if l.HasLink {
		frag("live-ablelink", "link", wireLiveLink(l.Link), liveLinkFragHTML(l.Link))
	}
	if l.HasNet {
		frag("live-net", "graph", wireLiveGraph(l.Net), liveGraphFragHTML(l.Net))
		frag("live-tim", "graph", wireLiveGraph(l.Tim), liveGraphFragHTML(l.Tim))
	}
	if l.HasPerf {
		frag("live-perf2", "perf", wireLivePerf(l.Perf), livePerfFragHTML(l.Perf))
	}
	frag("live-strip", "strip", wireLiveStrip(l.Strip), liveStripFragHTML(l.Strip))
	return out
}

// legacyLiveMarshal is the old path's serialization half: one document per fragment (post-B-2 that
// is a wire encode, not a json.Marshal - ten WireWriters instead of one).
func legacyLiveMarshal(st liveTickSt) int {
	l, n := st.Live, 0
	for _, doc := range [][]byte{wireLiveNP(l.NP), wireLiveStatus(l.Status), wireLiveDecks(l.Decks),
		wireLiveSignals(l.Signals), wireLiveCockpit(l.Cockpit), wireLiveLink(l.Link),
		wireLiveGraph(l.Net), wireLiveGraph(l.Tim), wireLivePerf(l.Perf), wireLiveStrip(l.Strip)} {
		n += len(doc)
	}
	return n
}

// tickBenchParity gates a bench before it times anything: the scheduler's fragments must match the
// legacy path's ids, order AND bytes, or the number measures a divergence (B0's rule).
func tickBenchParity(tb testing.TB, st liveTickSt) []zigui.Frag {
	tb.Helper()
	want := legacyLiveFrags(st, false)
	got, ok := zigui.TickLive(wireTkLive(st))
	if !ok {
		tb.Fatal("scheduler declined - bench would measure a fallback")
	}
	if len(got) != len(want) {
		tb.Fatalf("scheduler returned %d fragments, legacy path %d", len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			tb.Fatalf("fragment %d: id %q vs %q", i, got[i].ID, want[i].ID)
		}
		if got[i].HTML != want[i].HTML {
			tb.Fatalf("%s: zig != go (%d vs %d bytes)", got[i].ID, len(got[i].HTML), len(want[i].HTML))
		}
	}
	return got
}

func prevOf(frs []zigui.Frag) []tickPrev {
	out := make([]tickPrev, 0, len(frs))
	for _, f := range frs {
		out = append(out, tickPrev{ID: f.ID, Hash: f.Hash})
	}
	return out
}

func htmlBytes(frs []zigui.Frag) int {
	n := 0
	for _, f := range frs {
		n += len(f.HTML)
	}
	return n
}

func BenchmarkTickBenchLive(b *testing.B) {
	benchSkipUnavailable(b)
	st := tickBenchLive()
	frs := tickBenchParity(b, st)
	same := st
	same.Prev = prevOf(frs)
	docAll, docSame := len(wireTkLive(st)), len(wireTkLive(same))
	jsonB, htmlB := legacyLiveMarshal(st), htmlBytes(frs)

	b.Run("legacy_zig", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(legacyLiveFrags(st, true)) == 0 {
				b.Fatal("no fragments")
			}
		}
	})
	b.Run("legacy_go", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(legacyLiveFrags(st, false)) == 0 {
				b.Fatal("no fragments")
			}
		}
	})
	b.Run("sched_all", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, ok := zigui.TickLive(wireTkLive(st))
			if !ok || len(got) != len(frs) {
				b.Fatal("scheduler declined / wrong fragment count")
			}
		}
	})
	b.Run("sched_same", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, ok := zigui.TickLive(wireTkLive(same))
			if !ok || len(got) != 0 {
				b.Fatalf("scheduler declined / %d fragments on an unchanged tick", len(got))
			}
		}
	})
	// Full tick cost: a fragment that is patched must also be jsQuote'd into the eval batch, and
	// THAT is what the scheduler removes for an unchanged fragment (not the render).
	b.Run("legacy_zig_quoted", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			n := 0
			for _, f := range legacyLiveFrags(st, true) {
				n += len(jsQuote(f.HTML))
			}
			if n == 0 {
				b.Fatal("no fragments")
			}
		}
	})
	b.Run("sched_all_quoted", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, ok := zigui.TickLive(wireTkLive(st))
			if !ok {
				b.Fatal("scheduler declined")
			}
			n := 0
			for _, f := range got {
				n += len(jsQuote(f.HTML))
			}
			if n == 0 {
				b.Fatal("nothing quoted")
			}
		}
	})
	b.Run("encode_wire", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(wireTkLive(st)) == 0 {
				b.Fatal("encode failed")
			}
		}
	})
	b.Run("encode_wire_perfrag", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if legacyLiveMarshal(st) == 0 {
				b.Fatal("marshal failed")
			}
		}
	})
	b.Logf("%d fragments · html %d B · wire doc %d B (steady state %d B) · per-fragment json %d B",
		len(frs), htmlB, docAll, docSame, jsonB)
}

func BenchmarkTickBenchLogView(b *testing.B) {
	benchSkipUnavailable(b)
	lines := wireBenchTail()
	st := logsTickSt{Lines: lines}
	frs, ok := zigui.TickLogs(wireTkLogs(st))
	if !ok || len(frs) != 1 {
		b.Fatal("scheduler declined")
	}
	if want := logsLinesHTML(lines); want != frs[0].HTML {
		b.Fatalf("zig != go (%d vs %d bytes)", len(frs[0].HTML), len(want))
	}
	same := st
	same.Prev = prevOf(frs)

	b.Run("legacy_zig_v1", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := zigui.RenderLogsLines(stateJSON(lines)); !ok {
				b.Fatal("v1 render failed")
			}
		}
	})
	b.Run("legacy_zig_v2", func(b *testing.B) { // wave B-1's single-fragment binary export
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := zigui.RenderLogsLinesV2(wireLogsLines(lines)); !ok {
				b.Fatal("v2 render failed")
			}
		}
	})
	b.Run("legacy_go", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if logsLinesHTML(lines) == "" {
				b.Fatal("go render failed")
			}
		}
	})
	b.Run("sched_all", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, ok := zigui.TickLogs(wireTkLogs(st))
			if !ok || len(got) != 1 {
				b.Fatal("scheduler declined")
			}
		}
	})
	b.Run("sched_same", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, ok := zigui.TickLogs(wireTkLogs(same))
			if !ok || len(got) != 0 {
				b.Fatalf("scheduler declined / %d fragments on an unchanged tail", len(got))
			}
		}
	})
	b.Run("legacy_zig_v2_quoted", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			h, ok := zigui.RenderLogsLinesV2(wireLogsLines(lines))
			if !ok || len(jsQuote(h)) == 0 {
				b.Fatal("v2 render failed")
			}
		}
	})
	b.Run("legacy_go_quoted", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if len(jsQuote(logsLinesHTML(lines))) == 0 {
				b.Fatal("go render failed")
			}
		}
	})
	b.Run("sched_all_quoted", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			got, ok := zigui.TickLogs(wireTkLogs(st))
			if !ok || len(got) != 1 || len(jsQuote(got[0].HTML)) == 0 {
				b.Fatal("scheduler declined")
			}
		}
	})
	b.Logf("html %d B · wire doc %d B · json %d B · quoted %d B",
		len(frs[0].HTML), len(wireTkLogs(st)), len(stateJSON(lines)), len(jsQuote(frs[0].HTML)))
}
