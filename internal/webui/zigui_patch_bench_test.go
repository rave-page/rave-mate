//go:build zigui

package webui

import (
	"fmt"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Per-surface bench for the retained-doc delta channel (B7 increment ii). Review condition: the
// design's opt-in list was PROVISIONAL and each surface only ships enabled if its own row shows a
// measurable win. Both arms measure the WHOLE dispatch, the repo's convention since B0:
//
//	stateless   encode the full RZW1 document + the stateless export (today's shipping path)
//	retained    encode the RZD1 delta (which walks the state to diff AND to fingerprint it) +
//	            the patch export (clone → merge → re-fingerprint → render)
//	unchanged   the retained channel's other outcome: the state is byte-identical, the delta comes
//	            back empty and NOTHING crosses the ABI. Only the diff walk is paid.
//
// Both arms ALTERNATE between two realistic consecutive states, because a delta is only legal
// against the state the slot holds - and because a bench that re-sends the same state would
// measure the empty-delta path by accident.
//
// Run: GOWORK=off go test -count=1 -tags zigui ./internal/webui -run '^$' -bench PatchBench -benchmem

// benchPatchPair times one surface. a and b must be consecutive states of the same surface.
func benchPatchPair[T any](b *testing.B, s patchSurface[T], a, z T) {
	// Parity gate before timing (B0's rule: a bench must not measure a decline).
	h := zigui.RetainNew(s.msgID)
	if h == 0 {
		b.Fatal("slot table full")
	}
	defer zigui.RetainFree(h)
	hashA, hashZ := s.hash(a), s.hash(z)
	if _, st := s.patch(s.seed(a, uint64(h), patchTestLoc)); st != zigui.PatchOK {
		b.Fatalf("seed declined (%s)", st)
	}
	dAZ, ok1 := s.delta(z, a, uint64(h), hashA, patchTestLoc)
	if !ok1 {
		b.Fatal("the two states are identical - pick a real step")
	}
	got, st := s.patch(dAZ)
	if st != zigui.PatchOK {
		b.Fatalf("delta declined (%s)", st)
	}
	if want := s.oracle(z); want != got {
		b.Fatalf("retained render != stateless oracle (%d vs %d bytes)", len(got), len(want))
	}
	dZA, _ := s.delta(a, z, uint64(h), hashZ, patchTestLoc)
	if _, st := s.patch(dZA); st != zigui.PatchOK {
		b.Fatalf("reverse delta declined (%s)", st) // slot now holds a again
	}
	fullA, fullZ := s.full(a), s.full(z)

	b.Run("stateless", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			v := z
			if i%2 == 1 {
				v = a
			}
			if _, ok := statelessRender(s, v); !ok {
				b.Fatal("stateless render declined")
			}
		}
	})
	b.Run("retained", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			next, prev, base := z, a, hashA
			if i%2 == 1 {
				next, prev, base = a, z, hashZ
			}
			d, changed := s.delta(next, prev, uint64(h), base, patchTestLoc)
			if !changed {
				b.Fatal("empty delta in the alternating arm")
			}
			if _, st := s.patch(d); st != zigui.PatchOK {
				b.Fatalf("delta declined mid-bench (%s)", st)
			}
		}
		if b.N%2 == 1 { // leave the slot holding a, so a re-run of this arm starts valid
			if _, st := s.patch(s.seed(a, uint64(h), patchTestLoc)); st != zigui.PatchOK {
				b.Fatalf("restore seed declined (%s)", st)
			}
		}
	})
	b.Run("unchanged", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, changed := s.delta(a, a, uint64(h), hashA, patchTestLoc); changed {
				b.Fatal("an identical state produced a delta")
			}
		}
	})
	b.Logf("full documents %d/%d B · deltas %d/%d B (%.1f%% of full)",
		len(fullA), len(fullZ), len(dAZ), len(dZA),
		100*float64(len(dAZ)+len(dZA))/float64(len(fullA)+len(fullZ)))
}

// statelessRender is the surface's shipping path: full RZW1 document + the stateless export. It
// re-encodes per call on purpose - that encode is part of what the delta channel replaces.
func statelessRender[T any](s patchSurface[T], v T) (string, bool) {
	doc := s.full(v)
	if len(doc) == 0 {
		return "", false
	}
	switch s.msgID {
	case wireMsgTwFeed:
		return zigui.RenderTwitchFeedV2(doc)
	case wireMsgMidiMonLines:
		return zigui.RenderMIDIMonRowsV2(doc)
	case wireMsgMidiPortStat:
		return zigui.RenderMIDICtlStatV2(doc)
	case wireMsgCeTopbar:
		return zigui.RenderCueEditTopbarV2(doc)
	case wireMsgTkLive:
		frs, ok := zigui.TickLive(doc)
		return flatFrags(frs), ok
	case wireMsgTkLogs:
		frs, ok := zigui.TickLogs(doc)
		return flatFrags(frs), ok
	}
	return "", false
}

// The golden fixtures are correctness fixtures - several of them carry ONE row where the live
// surface carries a full buffer, and a fixed-cost-dominated state would flatter the channel. These
// three build the state the running app actually patches (same intent as wireBenchTail for the log
// tail), so the opt-in decision is made on realistic sizes.

// benchTwFeed is a busy chat feed: the rolling buffer is capped at 250 rows and a live chat fills it.
func benchTwFeed(n int) twFeedState {
	rows := make([]twRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, twRow{Kind: "chat", Name: fmt.Sprintf("viewer%d", i%37),
			NameStyle: "color:#F70864", Tags: []twTag{{Text: "sub", Variant: "sub"}},
			Mod: true, ModVal: fmt.Sprintf("m%d|u%d|viewer%d", i, i%37, i%37), ModTitle: "Delete",
			Text: fmt.Sprintf("message %d - this set is going hard right now", i)})
	}
	return twFeedState{Empty: "no messages yet", Rows: rows}
}

// benchMidiMon is a full monitor card (midiMonRows is what the renderer shows).
func benchMidiMon() midiMonLines {
	rows := make([]midiMonRow, 0, midiMonRows)
	for i := 0; i < midiMonRows; i++ {
		rows = append(rows, midiMonRow{Ago: fmt.Sprintf("%ds", i), Src: "Denon SC6000 MIDI",
			Msg: fmt.Sprintf("CC %d ch1 = %d", 20+i%16, (i*7)%128)})
	}
	return midiMonLines{Empty: "no traffic", Rows: rows}
}

func BenchmarkPatchBenchTwitchFeed(b *testing.B) {
	benchSkipUnavailable(b)
	st := benchTwFeed(120)
	next := st
	next.Rows = append([]twRow{{Kind: "chat", Name: "viewer", NameStyle: "color:#F70864",
		Tags: []twTag{}, Text: "another message"}}, st.Rows[:len(st.Rows)-1]...)
	benchPatchPair(b, twFeedSurface(), st, next)
}

func BenchmarkPatchBenchMIDIMonRows(b *testing.B) {
	benchSkipUnavailable(b)
	st := benchMidiMon()
	next := st
	next.Rows = append([]midiMonRow{{Ago: "0s", Src: "Denon SC6000 MIDI", Msg: "CC 21 ch1 = 64"}},
		st.Rows[:len(st.Rows)-1]...)
	benchPatchPair(b, midiMonSurface(), st, next)
}

func BenchmarkPatchBenchMIDICtlStat(b *testing.B) {
	benchSkipUnavailable(b)
	var st midiPortStat
	for _, s := range midiStatStates() {
		if s.HasRow && len(s.Line) > len(st.Line) {
			st = s
		}
	}
	if !st.HasRow {
		b.Skip("no populated midi-ctlstat fixture")
	}
	next := st
	next.Line = st.Line + " · 128 msg/s"
	benchPatchPair(b, midiStatSurface(), st, next)
}

// BenchmarkPatchBenchCueEditTopbar: the TYPICAL topbar (3 drops, short title) - the drag case the
// channel was opted in for. The stress variant is benched separately below.
func BenchmarkPatchBenchCueEditTopbar(b *testing.B) {
	benchSkipUnavailable(b)
	st := ceTopbarFixtures()["local"]
	if !st.Show {
		b.Skip("no shown cue-edit topbar fixture")
	}
	next := st // a drag moves the cursor readout, nothing else
	next.Cursor = "02:13.480"
	next.BarBeat = "17.3"
	benchPatchPair(b, ceTopbarSurface(), st, next)
}

// BenchmarkPatchBenchCueEditTopbarLong: the worst topbar in the fixture set (40 drops, 720 B
// eyebrow, 1 kB census) - the delta is still the same three fields.
func BenchmarkPatchBenchCueEditTopbarLong(b *testing.B) {
	benchSkipUnavailable(b)
	var st ceTopbarSt
	for _, s := range ceTopbarStates() {
		if s.Show && len(s.Drops) > len(st.Drops) {
			st = s
		}
	}
	if !st.Show {
		b.Skip("no shown cue-edit topbar fixture")
	}
	next := st
	next.Cursor = "02:13.480"
	next.BarBeat = "17.3"
	benchPatchPair(b, ceTopbarSurface(), st, next)
}

func BenchmarkPatchBenchTickLive(b *testing.B) {
	benchSkipUnavailable(b)
	a := tickBenchLive()
	z := a
	z.TC = "01:23:46:03"
	z.Live.NP.Line2 = a.Live.NP.Line2 + " · 128.0 BPM"
	z.Live.Status.Rows = append([]liveKV{}, a.Live.Status.Rows...)
	if len(z.Live.Status.Rows) > 0 {
		z.Live.Status.Rows[0].V = "changed"
	}
	// Each state carries the OTHER's fragment hashes, which is what a real tick does: Go snapshots
	// what it last pushed, so the scheduler only returns the fragments this state changed.
	fa, ok1 := zigui.TickLive(wireTkLive(a))
	fz, ok2 := zigui.TickLive(wireTkLive(z))
	if !ok1 || !ok2 {
		b.Fatal("scheduler declined")
	}
	a.Prev, z.Prev = prevOf(fz), prevOf(fa)
	benchPatchPair(b, tkLiveSurface(), a, z)
}

// BenchmarkPatchBenchTickLiveChurn is the LIVE app's real tick, measured: `ctl perf` on an
// isolated instance reported the delta at 7.4 KB against an 11.0 KB full document for the SAME 82
// tick states - 67%, not the 18% a hand-picked two-field step suggests. Nearly every card carries a
// counter (uptime, signal rates, net/timecode graphs, perf), so nearly every list is replaced.
// This arm reproduces that churn so the opt-in decision is made against the app, not a fixture.
func BenchmarkPatchBenchTickLiveChurn(b *testing.B) {
	benchSkipUnavailable(b)
	a := tickBenchLive()
	z := liveChurnStep(a)
	fa, ok1 := zigui.TickLive(wireTkLive(a))
	fz, ok2 := zigui.TickLive(wireTkLive(z))
	if !ok1 || !ok2 {
		b.Fatal("scheduler declined")
	}
	a.Prev, z.Prev = prevOf(fz), prevOf(fa)
	benchPatchPair(b, tkLiveSurface(), a, z)
}

// shiftGraph mimics a graph gaining one sample: same length, different bytes.
func shiftGraph(g string) string {
	if g == "" {
		return g
	}
	return g[len(g)/2:] + g[:len(g)/2]
}

// liveChurnStep advances every counter-bearing fragment by one second, the way the running app does.
func liveChurnStep(a liveTickSt) liveTickSt {
	z := a
	z.TC = "01:23:46:03"
	z.Live.Transport.TC = z.TC
	z.Live.NP.Line2 = a.Live.NP.Line2 + " · 128.0 BPM"
	bump := func(rows []liveKV, tag string) []liveKV {
		out := append([]liveKV{}, rows...)
		for i := range out {
			out[i].V = out[i].V + tag
		}
		return out
	}
	z.Live.Status.Rows = bump(a.Live.Status.Rows, "+1s")
	z.Live.Signals.Rows = bump(a.Live.Signals.Rows, "+1s")
	// The graphs are PRE-RENDERED strings (rule 6: Go formats every number), so one new sample
	// replaces the whole string - this is the single biggest reason the live delta is 67% and not 18%.
	z.Live.Perf.CPUGraph = shiftGraph(a.Live.Perf.CPUGraph)
	z.Live.Perf.RAMGraph = shiftGraph(a.Live.Perf.RAMGraph)
	z.Live.Perf.Head = a.Live.Perf.Head + "+"
	z.Live.Net.Graph = shiftGraph(a.Live.Net.Graph)
	z.Live.Net.Legend = a.Live.Net.Legend + "+"
	z.Live.Tim.Graph = shiftGraph(a.Live.Tim.Graph)
	z.Live.Tim.Legend = a.Live.Tim.Legend + "+"
	z.Live.Decks.Decks = append([]liveDeck{}, a.Live.Decks.Decks...)
	for i := range z.Live.Decks.Decks {
		z.Live.Decks.Decks[i].Meta = z.Live.Decks.Decks[i].Meta + " ·"
	}
	return z
}

func BenchmarkPatchBenchTickLogView(b *testing.B) {
	benchSkipUnavailable(b)
	a := logsTickSt{Lines: wireBenchTail()}
	z := logsTickSt{Lines: wireBenchTail()}
	// One new line arrives and the oldest scrolls out - the real ~1 Hz mutation of a 400-line tail.
	z.Lines.Entries = append(append([]logsEntry{}, a.Lines.Entries[1:]...),
		logsEntry{Time: "09:15:02.001", Lvl: "INFO", Cls: "INFO", Src: "session",
			Msg: fmt.Sprintf("merge tick %d", logTailN), Fields: "map[bpm:128]"})
	fa, ok1 := zigui.TickLogs(wireTkLogs(a))
	fz, ok2 := zigui.TickLogs(wireTkLogs(z))
	if !ok1 || !ok2 {
		b.Fatal("scheduler declined")
	}
	a.Prev, z.Prev = prevOf(fz), prevOf(fa)
	benchPatchPair(b, tkLogsSurface(), a, z)
}
