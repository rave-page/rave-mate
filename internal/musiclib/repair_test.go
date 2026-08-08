package musiclib

import (
	"math"
	"os"
	"strings"
	"testing"
)

// damagedFixture replicates the observed real-library damage:
//
//	dmg-order.flac  the Jul-17 pattern write: pads 3,4,5 early + 0,1,2 late (pad 1 = drop 2)
//	dmg-dupe.flac   the same cue stacked on slots 3 and 7 (Axwell)
//	dmg-pad4.flac   padded TYPE-4 anchor (old single-cue GridAnchor form)
//	dmg-grid.flac   grid marker moved 12.5ms off the reference lattice (gridfix bias)
//	clean.flac      untouched entry - must pass through byte-faithfully
const damagedFixture = `<?xml version="1.0" encoding="UTF-8"?>
<NML VERSION="20">
  <HEAD COMPANY="native" PROGRAM="Traktor"></HEAD>
  <COLLECTION ENTRIES="5">
    <ENTRY TITLE="Order" ARTIST="A">
      <LOCATION DIR="/:Music/:" FILE="dmg-order.flac" VOLUME="C:"></LOCATION>
      <TEMPO BPM="175.000000" BPM_QUALITY="100.000000"></TEMPO>
      <CUE_V2 NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="3.227" LEN="0.000000" REPEATS="-1" HOTCUE="-1"><GRID BPM="175.000000"></GRID></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="56231.803" LEN="0.000000" REPEATS="-1" HOTCUE="3"></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="61717.518" LEN="0.000000" REPEATS="-1" HOTCUE="4"></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="72688.947" LEN="0.000000" REPEATS="-1" HOTCUE="5"></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="187888.957" LEN="0.000000" REPEATS="-1" HOTCUE="0"></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="193374.672" LEN="0.000000" REPEATS="-1" HOTCUE="1"></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="204346.101" LEN="0.000000" REPEATS="-1" HOTCUE="2"></CUE_V2>
    </ENTRY>
    <ENTRY TITLE="Dupe" ARTIST="B">
      <LOCATION DIR="/:Music/:" FILE="dmg-dupe.flac" VOLUME="C:"></LOCATION>
      <TEMPO BPM="174.000000"></TEMPO>
      <CUE_V2 NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="125.0" LEN="0.000000" REPEATS="-1" HOTCUE="-1"><GRID BPM="174.000000"></GRID></CUE_V2>
      <CUE_V2 NAME="Drop" DISPL_ORDER="0" TYPE="0" START="66331.0" LEN="0.000000" REPEATS="-1" HOTCUE="0"></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="131158.0" LEN="0.000000" REPEATS="-1" HOTCUE="1"></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="66331.0" LEN="0.000000" REPEATS="-1" HOTCUE="2"></CUE_V2>
    </ENTRY>
    <ENTRY TITLE="Padded" ARTIST="C">
      <LOCATION DIR="/:Music/:" FILE="dmg-pad4.flac" VOLUME="C:"></LOCATION>
      <TEMPO BPM="172.000000"></TEMPO>
      <CUE_V2 NAME="Intro" DISPL_ORDER="0" TYPE="4" START="8.347" LEN="0.000000" REPEATS="-1" HOTCUE="0"><GRID BPM="172.000000"></GRID></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="55809.093" LEN="0.000000" REPEATS="-1" HOTCUE="1"></CUE_V2>
    </ENTRY>
    <ENTRY TITLE="Grid" ARTIST="D">
      <LOCATION DIR="/:Music/:" FILE="dmg-grid.flac" VOLUME="C:"></LOCATION>
      <TEMPO BPM="174.000000" BPM_QUALITY="100.000000"></TEMPO>
      <CUE_V2 NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="1371.542" LEN="0.000000" REPEATS="-1" HOTCUE="-1"><GRID BPM="174.000000"></GRID></CUE_V2>
      <CUE_V2 NAME="n.n." DISPL_ORDER="0" TYPE="0" START="93138.051" LEN="0.000000" REPEATS="-1" HOTCUE="0" COLOR="#FF0000"></CUE_V2>
    </ENTRY>
    <ENTRY TITLE="Clean" ARTIST="E">
      <LOCATION DIR="/:Music/:" FILE="clean.flac" VOLUME="C:"></LOCATION>
      <TEMPO BPM="128.000000"></TEMPO>
      <CUE_V2 NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="10.0" LEN="0.000000" REPEATS="-1" HOTCUE="-1"><GRID BPM="128.000000"></GRID></CUE_V2>
      <CUE_V2 NAME="One" DISPL_ORDER="0" TYPE="0" START="1000.0" LEN="0.000000" REPEATS="-1" HOTCUE="0"></CUE_V2>
      <CUE_V2 NAME="Two" DISPL_ORDER="0" TYPE="0" START="2000.0" LEN="0.000000" REPEATS="-1" HOTCUE="1"></CUE_V2>
    </ENTRY>
  </COLLECTION>
</NML>`

// reference: dmg-grid.flac's marker before the bias shift (1384.047, on-lattice with
// the live 1371.542 being 12.5ms off it: 1384.047 - 1371.542 = 12.505).
func repairRef() map[string]RefGrid {
	return map[string]RefGrid{
		resolveLocation("C:", "/:Music/:", "dmg-grid.flac"): {StartMs: 1384.047, BPM: 174.0},
	}
}

func TestRepairCollectionFile(t *testing.T) {
	path := writeFixture(t, damagedFixture)
	rep, err := RepairCollectionFile(path, RepairOptions{Ref: repairRef()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Entries != 5 || rep.Changed != 4 {
		t.Fatalf("report %+v: want 5 entries, 4 changed", rep)
	}
	if rep.PadsReordered < 2 || rep.DupesRemoved != 1 || rep.PadsSplit != 1 || rep.GridsRestored != 1 {
		t.Fatalf("report %+v", rep)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Track{}
	_, perr := ParseCollection(f, func(tr Track) { byPath[tr.Path] = tr })
	_ = f.Close() // release before the idempotency pass below (Windows rename-over-open)
	if perr != nil {
		t.Fatal(perr)
	}

	// pads follow track time everywhere
	for file, tr := range byPath {
		var padded []CuePoint
		for _, c := range tr.Cues {
			if c.Hotcue >= 0 {
				if c.Type == 4 {
					t.Errorf("%s: padded TYPE-4 survived: %+v", file, c)
				}
				padded = append(padded, c)
			}
		}
		for i := 1; i < len(padded); i++ {
			a, b := padded[i-1], padded[i]
			if (a.Hotcue < b.Hotcue) != (a.StartMs < b.StartMs) {
				t.Errorf("%s: pads not in time order: %+v", file, padded)
			}
		}
	}

	order := byPath[resolveLocation("C:", "/:Music/:", "dmg-order.flac")]
	slots := map[int]float64{}
	for _, c := range order.Cues {
		if c.Hotcue >= 0 {
			slots[c.Hotcue] = c.StartMs
		}
	}
	if slots[0] != 56231.803 || slots[3] != 187888.957 || slots[5] != 204346.101 {
		t.Errorf("order repair wrong: %+v", slots)
	}

	dupe := byPath[resolveLocation("C:", "/:Music/:", "dmg-dupe.flac")]
	hot := 0
	for _, c := range dupe.Cues {
		if c.Hotcue >= 0 {
			hot++
		}
	}
	if hot != 2 {
		t.Errorf("dupe repair: %d pads, want 2 (stacked dupe dropped)", hot)
	}

	// padded TYPE-4 split into grid cue + plain pad cue, grid position kept
	pad4 := byPath[resolveLocation("C:", "/:Music/:", "dmg-pad4.flac")]
	if len(pad4.Beatgrid) != 1 || pad4.Beatgrid[0].PositionMs != 8.347 {
		t.Errorf("split moved the grid: %+v", pad4.Beatgrid)
	}
	var splitPad *CuePoint
	for i, c := range pad4.Cues {
		if c.Type == 0 && c.StartMs == 8.347 {
			splitPad = &pad4.Cues[i]
		}
	}
	if splitPad == nil || splitPad.Hotcue != 0 || splitPad.Name != "Intro" {
		t.Errorf("split pad clone wrong: %+v", pad4.Cues)
	}

	// grid restored onto the reference lattice; COLOR attr survives token surgery
	grid := byPath[resolveLocation("C:", "/:Music/:", "dmg-grid.flac")]
	if len(grid.Beatgrid) != 1 || math.Abs(grid.Beatgrid[0].PositionMs-1384.047) > 0.01 {
		t.Errorf("grid not restored: %+v", grid.Beatgrid)
	}
	if !strings.Contains(readFileStr(t, path), `COLOR="#FF0000"`) {
		t.Error("COLOR attr lost in repair")
	}

	// idempotent: a second pass changes nothing
	rep2, err := RepairCollectionFile(path, RepairOptions{Ref: repairRef()})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Changed != 0 {
		t.Fatalf("second pass not clean: %+v", rep2)
	}
}

func TestRepairDryRunWritesNothing(t *testing.T) {
	path := writeFixture(t, damagedFixture)
	before := readFileStr(t, path)
	rep, err := RepairCollectionFile(path, RepairOptions{Ref: repairRef(), DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changed != 4 || rep.GridsRestored != 1 {
		t.Fatalf("dry report %+v", rep)
	}
	if readFileStr(t, path) != before {
		t.Error("dry run modified the file")
	}
}

// Large deliberate regrids and sub-noise offsets stay untouched.
func TestRepairGridRestoreWindow(t *testing.T) {
	path := writeFixture(t, damagedFixture)
	ref := map[string]RefGrid{
		// 100ms off the reference lattice: outside the window - manual regrid, leave alone
		resolveLocation("C:", "/:Music/:", "dmg-grid.flac"): {StartMs: 1371.542 + 100, BPM: 174.0},
	}
	rep, err := RepairCollectionFile(path, RepairOptions{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	if rep.GridsRestored != 0 {
		t.Fatalf("out-of-window grid restored: %+v", rep)
	}
}
