package cuepattern

import (
	"math"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func almost(t *testing.T, got, want, eps float64, what string) {
	t.Helper()
	if math.Abs(got-want) > eps {
		t.Fatalf("%s: got %.4f want %.4f", what, got, want)
	}
}

// 128 BPM constant grid anchored at 1000ms; beat = 468.75ms.
func constTrack() musiclib.Track {
	return musiclib.Track{
		Title: "T", DurationSec: 300, BPM: 128,
		Beatgrid: []musiclib.GridMarker{{PositionMs: 1000, BPM: 128}},
	}
}

func TestGridSnapStepOffset(t *testing.T) {
	g, err := NewGrid(constTrack().Beatgrid, 300_000)
	if err != nil {
		t.Fatal(err)
	}
	beat := 60000.0 / 128
	almost(t, g.SnapMs(1000+beat*4+10), 1000+beat*4, 1e-6, "snap forward")
	almost(t, g.SnapMs(1000-beat*2-10), 1000-beat*2, 1e-6, "snap backward before anchor")
	almost(t, g.StepMs(1000, 16), 1000+16*beat, 1e-6, "step +16")
	almost(t, g.StepMs(1000, -1), 1000-beat, 1e-6, "step -1")
	almost(t, g.BeatsBetween(1000, 1000+32*beat), 32, 1e-9, "beats between")
	almost(t, g.OffsetMs(1000, -2.5), 1000-2.5*beat, 1e-6, "fractional negative offset")
	if g.StepMs(100, -8) != 0 {
		t.Fatalf("step past start must clamp to 0, got %v", g.StepMs(100, -8))
	}
}

func TestGridVariableTempo(t *testing.T) {
	// 120 BPM (beat 500ms) until 10s, then 150 BPM (beat 400ms)
	mk := []musiclib.GridMarker{{PositionMs: 0, BPM: 120}, {PositionMs: 10_000, BPM: 150}}
	g, err := NewGrid(mk, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	// 4 beats from 9000ms: 2 beats to 10000, then 2*400 → 10800
	almost(t, g.OffsetMs(9000, 4), 10800, 1e-6, "offset across segment")
	almost(t, g.BeatsBetween(9000, 10800), 4, 1e-9, "beats across segment")
	almost(t, g.BeatLenMs(5000), 500, 1e-9, "beat len seg1")
	almost(t, g.BeatLenMs(20000), 400, 1e-9, "beat len seg2")
}

func TestGridNoMarkers(t *testing.T) {
	if _, err := NewGrid(nil, 1000); err == nil {
		t.Fatal("want error for missing grid")
	}
}

func TestExtractRoundTrip(t *testing.T) {
	tr := constTrack()
	beat := 60000.0 / 128
	drop := 1000 + 64*beat
	tr.Cues = []musiclib.CuePoint{
		{Kind: musiclib.CueHot, Hotcue: 0, StartMs: drop, Name: "DROP"},
		{Kind: musiclib.CueHot, Hotcue: 1, StartMs: drop - 32*beat, Name: "build"},
		{Kind: musiclib.CueLoop, Hotcue: -1, StartMs: drop + 64*beat, LenMs: 4 * beat},
		{Kind: musiclib.CueGrid, Hotcue: -1, StartMs: 1000}, // ignored
	}
	p, err := Extract(tr, []int{0, 1, 2, 3}, drop, "std")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Cues) != 3 {
		t.Fatalf("want 3 pattern cues, got %d", len(p.Cues))
	}
	almost(t, p.Cues[0].Beats, -32, 1e-9, "build offset")
	almost(t, p.Cues[1].Beats, 0, 1e-9, "drop offset")
	almost(t, p.Cues[2].Beats, 64, 1e-9, "loop offset")
	almost(t, p.Cues[2].LenBeats, 4, 1e-9, "loop len beats")

	// re-apply on a fresh track: same layout lands at the same beats
	tr2 := constTrack()
	drop2 := 1000 + 128*beat
	cues, rep, err := Apply(tr2, []float64{drop2}, map[int]Pattern{0: p}, ApplyOptions{SnapDrop: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Added != 3 || rep.Cut != 0 {
		t.Fatalf("report %+v", rep)
	}
	almost(t, cues[0].StartMs, drop2-32*beat, 1e-6, "applied build")
	almost(t, cues[1].StartMs, drop2, 1e-6, "applied drop")
	almost(t, cues[2].StartMs, drop2+64*beat, 1e-6, "applied loop")
	almost(t, cues[2].LenMs, 4*beat, 1e-6, "applied loop len")
}

func TestApplySpanCutting(t *testing.T) {
	tr := constTrack()
	beat := 60000.0 / 128
	p := Pattern{Cues: []PatternCue{
		{Beats: -64, Kind: musiclib.CueHot, Hotcue: 0}, // won't fit before drop1
		{Beats: 0, Kind: musiclib.CueHot, Hotcue: 1},
		{Beats: 32, Kind: musiclib.CueHot, Hotcue: 2}, // crosses drop2 from drop1
	}}
	drop1 := 1000 + 16*beat // only 16+2.1 beats of room before it
	drop2 := drop1 + 24*beat
	cues, rep, err := Apply(tr, []float64{drop1, drop2}, map[int]Pattern{0: p, 1: p}, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// drop1: -64 cut (before start), 0 ok, +32 cut (>= drop2)
	// drop2: -64 cut (< drop1), 0 ok, +32 ok
	if rep.Added != 3 || rep.Cut != 3 {
		t.Fatalf("report %+v (cues %d)", rep, len(cues))
	}
}

func TestApplyCollisionAndSlots(t *testing.T) {
	tr := constTrack()
	beat := 60000.0 / 128
	drop := 1000 + 64*beat
	tr.Cues = []musiclib.CuePoint{
		{Kind: musiclib.CueHot, Hotcue: 0, StartMs: drop}, // occupies slot 0 AND the drop position
	}
	p := Pattern{Cues: []PatternCue{
		{Beats: 0, Kind: musiclib.CueHot, Hotcue: 0},  // collides → skipped
		{Beats: 16, Kind: musiclib.CueHot, Hotcue: 0}, // slot 0 taken → next free
	}}
	cues, rep, err := Apply(tr, []float64{drop}, map[int]Pattern{0: p}, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != 1 || rep.Added != 1 {
		t.Fatalf("report %+v", rep)
	}
	var got *musiclib.CuePoint
	for i := range cues {
		if cues[i].StartMs > drop+1 {
			got = &cues[i]
		}
	}
	if got == nil || got.Hotcue != 1 {
		t.Fatalf("slot reallocation failed: %+v", got)
	}
}

func TestApplyToMemory(t *testing.T) {
	tr := constTrack()
	p := Pattern{Cues: []PatternCue{{Beats: 0, Kind: musiclib.CueHot, Hotcue: 3, Name: "d"}}}
	cues, _, err := Apply(tr, []float64{5000}, map[int]Pattern{0: p}, ApplyOptions{ToMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	if cues[0].Kind != musiclib.CuePlain || cues[0].Hotcue != -1 {
		t.Fatalf("ToMemory not honored: %+v", cues[0])
	}
}

func TestConvertHotcuesToMemory(t *testing.T) {
	in := []musiclib.CuePoint{
		{Kind: musiclib.CueHot, Hotcue: 2, Name: "a", StartMs: 1},
		{Kind: musiclib.CueLoop, Hotcue: -1, StartMs: 2, LenMs: 100},
	}
	out := ConvertHotcuesToMemory(in, "")
	if out[0].Kind != musiclib.CuePlain || out[0].Hotcue != -1 || out[0].Name != "a" {
		t.Fatalf("hotcue not demoted: %+v", out[0])
	}
	if out[1].Kind != musiclib.CueLoop {
		t.Fatalf("loop must be untouched: %+v", out[1])
	}
	if in[0].Kind != musiclib.CueHot {
		t.Fatal("input mutated")
	}
}

func TestDrops(t *testing.T) {
	d := AddDrop(nil, 5000)
	d = AddDrop(d, 9000)
	d = AddDrop(d, 5020) // within eps of 5000 → dedup
	if len(d) != 2 || d[0] != 5000 {
		t.Fatalf("drops %v", d)
	}
	d = RemoveDrop(d, 5030)
	if len(d) != 1 || d[0] != 9000 {
		t.Fatalf("remove failed %v", d)
	}
	if NearestDrop(d, 100) != 0 || NearestDrop(nil, 1) != -1 {
		t.Fatal("nearest")
	}
}
