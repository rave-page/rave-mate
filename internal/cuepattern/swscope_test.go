package cuepattern

import (
	"testing"

	"rave.page/mate/internal/musiclib"
)

func hot(ms float64, slot int, sw string) musiclib.CuePoint {
	return musiclib.CuePoint{Kind: musiclib.CueHot, Hotcue: slot, StartMs: ms, Sw: sw}
}
func mem(ms float64, sw string) musiclib.CuePoint {
	return musiclib.CuePoint{Kind: musiclib.CuePlain, Hotcue: -1, StartMs: ms, Sw: sw}
}

// FilterForSoftware: target sees shared + own cues, never a sibling scope's.
func TestFilterForSoftware(t *testing.T) {
	cues := []musiclib.CuePoint{
		hot(100, 0, ""), hot(200, 1, "traktor"), hot(300, 2, "rekordbox"),
		{Kind: musiclib.CueGrid, StartMs: 0, Hotcue: -1, Sw: "rekordbox"}, // grid always passes
	}
	got := FilterForSoftware(cues, "traktor")
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (shared+traktor+grid)", len(got))
	}
	for _, c := range got {
		if c.Kind == musiclib.CueHot && c.Sw == "rekordbox" {
			t.Fatalf("rekordbox cue leaked into traktor export: %+v", c)
		}
	}
	if all := FilterForSoftware(cues, ""); len(all) != 4 {
		t.Fatalf("all view len=%d want 4", len(all))
	}
}

// ClearMusical: scope-only wipe; grid + other scopes survive.
func TestClearMusical(t *testing.T) {
	cues := []musiclib.CuePoint{
		hot(100, 0, ""), mem(200, "traktor"), hot(300, 1, "rekordbox"),
		{Kind: musiclib.CueGrid, StartMs: 0, Hotcue: -1},
	}
	out, n := ClearMusical(cues, "traktor")
	if n != 2 { // shared + traktor cleared; rekordbox + grid stay
		t.Fatalf("cleared=%d want 2", n)
	}
	if len(out) != 2 {
		t.Fatalf("len=%d want 2", len(out))
	}
	out, n = ClearMusical(cues, "")
	if n != 3 || len(out) != 1 || out[0].Kind != musiclib.CueGrid {
		t.Fatalf("all-scope clear: n=%d out=%+v", n, out)
	}
}

// CapPads single pool: closest to the drop win, excess demote, slots re-pack under max.
func TestCapPadsClosest(t *testing.T) {
	drop := []float64{60000}
	cues := []musiclib.CuePoint{
		hot(59000, 5, ""), hot(61000, 6, ""), hot(10000, 0, ""), hot(110000, 1, ""),
	}
	out, demoted := CapPads(cues, drop, "", 2, false)
	if demoted != 2 {
		t.Fatalf("demoted=%d want 2", demoted)
	}
	kept := map[float64]int{}
	for _, c := range out {
		if c.Kind == musiclib.CueHot {
			kept[c.StartMs] = c.Hotcue
		}
	}
	if _, ok := kept[59000]; !ok {
		t.Fatalf("closest-before-drop demoted: %+v", out)
	}
	if _, ok := kept[61000]; !ok {
		t.Fatalf("closest-after-drop demoted: %+v", out)
	}
	for ms, slot := range kept {
		if slot < 0 || slot >= 2 {
			t.Fatalf("kept cue at %v has slot %d outside cap 2", ms, slot)
		}
	}
}

// CapPads splitEven: budget splits across drops; a sparse drop's spare refills globally.
func TestCapPadsSplitEven(t *testing.T) {
	drops := []float64{30000, 120000}
	cues := []musiclib.CuePoint{
		// 3 near drop A, 1 near drop B, cap 4 → even split 2+2, B has 1, spare refills A's 3rd
		hot(29000, 0, ""), hot(31000, 1, ""), hot(33000, 2, ""),
		hot(119000, 3, ""),
	}
	out, demoted := CapPads(cues, drops, "", 4, true)
	if demoted != 0 {
		t.Fatalf("demoted=%d want 0 (spare capacity refills)", demoted)
	}
	// cap 3 → A keeps its 2 closest, B keeps 1, A's 3rd-farthest demotes
	out, demoted = CapPads(cues, drops, "", 3, true)
	if demoted != 1 {
		t.Fatalf("demoted=%d want 1", demoted)
	}
	for _, c := range out {
		if c.StartMs == 33000 && c.Kind == musiclib.CueHot {
			t.Fatalf("farthest-from-A survived the split cap: %+v", out)
		}
		if c.StartMs == 119000 && c.Kind != musiclib.CueHot {
			t.Fatalf("drop B's only cue demoted: %+v", c)
		}
	}
}

// CapPads scope: another software's pads are untouched and don't consume the budget.
func TestCapPadsScope(t *testing.T) {
	cues := []musiclib.CuePoint{
		hot(1000, 0, "rekordbox"), hot(2000, 1, "rekordbox"),
		hot(3000, 2, "traktor"), hot(4000, 3, "traktor"), hot(5000, 4, "traktor"),
	}
	out, demoted := CapPads(cues, nil, "traktor", 2, false)
	if demoted != 1 {
		t.Fatalf("demoted=%d want 1", demoted)
	}
	for _, c := range out {
		if c.Sw == "rekordbox" && c.Kind != musiclib.CueHot {
			t.Fatalf("rekordbox pad touched by traktor cap: %+v", c)
		}
	}
}

// Apply with Software + Overwrite: in-scope cues replaced, sibling scope untouched,
// new cues tagged, collisions only within scope.
func TestApplyScopedOverwrite(t *testing.T) {
	tr := musiclib.Track{
		Title: "t", DurationSec: 300,
		Beatgrid: []musiclib.GridMarker{{PositionMs: 0, BPM: 120}},
		Cues: []musiclib.CuePoint{
			hot(60000, 0, "rekordbox"), // sibling scope: survives + would collide if scope leaked
			mem(90000, "traktor"),      // in scope: cleared by Overwrite
		},
	}
	pats := map[int]Pattern{0: {Name: "p", Cues: []PatternCue{
		{Beats: 0, Kind: musiclib.CueHot, Hotcue: -1},
	}}}
	cues, rep, err := Apply(tr, []float64{60000}, pats, ApplyOptions{Software: "traktor", Overwrite: true, SnapDrop: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Replaced != 1 || rep.Added != 1 || rep.Skipped != 0 {
		t.Fatalf("rep=%+v want replaced=1 added=1 skipped=0", rep)
	}
	var gotNew, gotRB bool
	for _, c := range cues {
		if c.Sw == "traktor" && c.Kind == musiclib.CueHot {
			gotNew = true
		}
		if c.Sw == "rekordbox" && c.Kind == musiclib.CueHot && c.StartMs == 60000 {
			gotRB = true
		}
		if c.Kind == musiclib.CuePlain && c.StartMs == 90000 {
			t.Fatalf("overwrite left the in-scope memory cue: %+v", cues)
		}
	}
	if !gotNew || !gotRB {
		t.Fatalf("new=%v rb=%v cues=%+v", gotNew, gotRB, cues)
	}
}

// RenumberPadsByTime: pads follow track time (pad 0 = earliest), padded loops join the
// pool, other scopes untouched, max demotes, ordered input is a no-op.
func TestRenumberPadsByTime(t *testing.T) {
	cues := []musiclib.CuePoint{
		hot(60000, 0, ""), hot(10000, 5, ""),
		{Kind: musiclib.CueLoop, Hotcue: 7, StartMs: 30000},
		hot(90000, 1, "rekordbox"), // out of scope for traktor
		mem(5000, ""),              // memory cue: never padded
	}
	out, changed := RenumberPadsByTime(cues, "traktor", 0)
	if !changed {
		t.Fatal("expected renumber")
	}
	want := map[float64]int{10000: 0, 30000: 1, 60000: 2, 90000: 1, 5000: -1}
	for _, c := range out {
		if c.Hotcue != want[c.StartMs] {
			t.Fatalf("cue@%v slot=%d want %d (out=%+v)", c.StartMs, c.Hotcue, want[c.StartMs], out)
		}
	}

	// max: pads past the budget demote (hotcue → memory, loop keeps kind)
	three := []musiclib.CuePoint{hot(1000, 0, ""), hot(2000, 1, ""), hot(3000, 2, "")}
	out, _ = RenumberPadsByTime(three, "", 2)
	if out[2].Kind != musiclib.CuePlain || out[2].Hotcue != -1 {
		t.Fatalf("over-budget pad not demoted: %+v", out[2])
	}

	// already ordered = untouched original
	ordered := []musiclib.CuePoint{hot(1000, 0, ""), hot(2000, 1, "")}
	if _, changed := RenumberPadsByTime(ordered, "", 0); changed {
		t.Fatal("ordered input reported changed")
	}
}

// Regression: a pattern cue EARLIER than an existing pad must end up on the LOWER pad -
// pads fill left-to-right, top-to-bottom in track order, never gap-fill order.
func TestApplyPadTimeOrder(t *testing.T) {
	tr := musiclib.Track{
		Title: "t", DurationSec: 300,
		Beatgrid: []musiclib.GridMarker{{PositionMs: 0, BPM: 120}},
		Cues:     []musiclib.CuePoint{hot(60000, 0, "")}, // existing pad 0 late in the track
	}
	pats := map[int]Pattern{0: {Name: "p", Cues: []PatternCue{
		{Beats: 0, Kind: musiclib.CueHot, Hotcue: -1},
	}}}
	cues, rep, err := Apply(tr, []float64{10000}, pats, ApplyOptions{SnapDrop: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Added != 1 {
		t.Fatalf("rep=%+v want added=1", rep)
	}
	slots := map[float64]int{}
	for _, c := range cues {
		if c.Kind == musiclib.CueHot {
			slots[c.StartMs] = c.Hotcue
		}
	}
	if slots[10000] != 0 || slots[60000] != 1 {
		t.Fatalf("pads not in track order: %+v", slots)
	}
}

// Promote: scope + cap respected.
func TestPromoteScopedCap(t *testing.T) {
	cues := []musiclib.CuePoint{
		mem(100, "traktor"), mem(200, "traktor"), mem(300, "traktor"),
		mem(400, "rekordbox"),
		hot(50, 0, "traktor"),
	}
	out, n := PromoteMemoryToHotcues(cues, "traktor", 3)
	if n != 2 { // slot 0 taken → slots 1,2 for the two earliest traktor memory cues
		t.Fatalf("promoted=%d want 2", n)
	}
	for _, c := range out {
		if c.Sw == "rekordbox" && c.Kind != musiclib.CuePlain {
			t.Fatalf("rekordbox memory cue promoted by traktor pass: %+v", c)
		}
		if c.StartMs == 300 && c.Kind != musiclib.CuePlain {
			t.Fatalf("cap 3 exceeded: %+v", c)
		}
	}
}

// Regression: the Breach write - 6 hotcues slotted by creation order (drop cluster got 0-2,
// intro cluster 3-5) + 2 right-click memory cues ON the drop markers. The full write pipeline
// (promote -> cap -> renumber, as ceWriteJobs + ApplyCues run it) must land ALL 8 on pads
// 1..8 in pure track-time order - the drop-marker cues included.
func TestWritePipelinePadsInTimeOrder(t *testing.T) {
	cues := []musiclib.CuePoint{
		{Kind: musiclib.CueGrid, StartMs: 15596, Hotcue: -1},
		hot(59995, 3, ""), hot(65450, 4, ""), mem(70904, ""), hot(76359, 5, ""),
		hot(125450, 0, ""), hot(130904, 1, ""), mem(136359, ""), hot(141813, 2, ""),
	}
	drops := []float64{70904, 136359}
	out := FilterForSoftware(cues, "traktor")
	out, _ = PromoteMemoryToHotcues(out, "", 8) // AutoPromoteOn default for pad-first software
	out, _ = CapPads(out, drops, "", 8, true)
	out, _ = RenumberPadsByTime(out, "", 0)
	slot := 0
	lastMs := -1.0
	for _, c := range out {
		if c.Kind == musiclib.CueGrid {
			continue
		}
		if c.Kind != musiclib.CueHot {
			t.Fatalf("cue @%.0fms left off the pads: %+v", c.StartMs, c)
		}
		if c.StartMs < lastMs {
			t.Fatalf("cue order broken at @%.0fms", c.StartMs)
		}
		lastMs = c.StartMs
		if c.Hotcue != slot {
			t.Fatalf("cue @%.0fms got pad %d, want %d (left-to-right by time)", c.StartMs, c.Hotcue+1, slot+1)
		}
		slot++
	}
	if slot != 8 {
		t.Fatalf("padded cues = %d, want 8", slot)
	}
}
