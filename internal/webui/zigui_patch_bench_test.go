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

func BenchmarkPatchBenchTwitchFeed(b *testing.B) {
	benchSkipUnavailable(b)
	st := twFixtures()["populated"].Feed
	if len(st.Rows) == 0 {
		b.Skip("no rows in the populated twitch fixture")
	}
	next := st
	next.Rows = append([]twRow{{Kind: "chat", Name: "viewer", NameStyle: "color:#F70864", Tags: []twTag{}, Text: "another message"}}, st.Rows...)
	benchPatchPair(b, twFeedSurface(), st, next)
}

func BenchmarkPatchBenchMIDIMonRows(b *testing.B) {
	benchSkipUnavailable(b)
	var st midiMonLines
	for _, s := range midiMonStates() {
		if len(s.Rows) > len(st.Rows) {
			st = s
		}
	}
	if len(st.Rows) == 0 {
		b.Skip("no midi-monitor fixture with rows")
	}
	next := st
	next.Rows = append([]midiMonRow{{Ago: "0s", Src: "Denon", Msg: "CC 21 = 64"}}, st.Rows...)
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

func BenchmarkPatchBenchCueEditTopbar(b *testing.B) {
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
	next := st // a drag moves the cursor readout, nothing else
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
